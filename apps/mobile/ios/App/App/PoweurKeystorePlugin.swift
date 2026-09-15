import Foundation
import Capacitor
import LocalAuthentication
import Security

/**
 * The iOS half of the `PoweurKeystore` contract (EPIC-019 E19-T2).
 *
 * The contract itself — four methods, and what each has to guarantee — is
 * documented at the bottom of `apps/web/src/lib/native.js`, which is the only
 * caller. Nothing cryptographic happens here: the web layer generates the 32
 * random bytes and does the AES wrapping, and this plugin's whole job is to
 * hold those bytes somewhere the app cannot read them without the user.
 *
 * That division is deliberate. It is what lets one native build work against
 * any relay on any domain: a passkey drags WebAuthn's origin model along with
 * it (an `rp.id`, associated domains, a relying party), and the Keychain has
 * none of that.
 */
@objc(PoweurKeystorePlugin)
public class PoweurKeystorePlugin: CAPPlugin, CAPBridgedPlugin {
    public let identifier = "PoweurKeystorePlugin"
    public let jsName = "PoweurKeystore"
    public let pluginMethods: [CAPPluginMethod] = [
        CAPPluginMethod(name: "setSecret", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "getSecret", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "deleteSecret", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "canUseBiometrics", returnType: CAPPluginReturnPromise)
    ]

    /// One service for every entry; the identity is the account.
    private static let service = "net.poweur.app.keystore"

    /// Keychain work blocks on a biometric prompt, so it never runs on the
    /// main thread — the prompt is drawn by the same run loop we would block.
    private let queue = DispatchQueue(label: "net.poweur.app.keystore", qos: .userInitiated)

    // MARK: - setSecret

    @objc func setSecret(_ call: CAPPluginCall) {
        guard let key = call.getString("key"), !key.isEmpty else {
            call.reject("key is required", "bad_request"); return
        }
        guard let value = call.getString("value"), !value.isEmpty else {
            call.reject("value is required", "bad_request"); return
        }
        let gate = call.getString("gate") ?? "biometric"
        guard gate == "biometric" || gate == "device" else {
            call.reject("unknown gate \(gate)", "bad_request"); return
        }

        queue.async {
            var attributes = Self.query(for: key)
            attributes[kSecValueData as String] = Data(value.utf8)

            if gate == "biometric" {
                // `.biometryCurrentSet` is the point of the biometric gate: the
                // entry dies when the enrolled set changes, so someone who adds
                // their own face to an unlocked phone does not inherit the
                // identity. `WhenPasscodeSetThisDeviceOnly` refuses to exist on
                // a device with no passcode, and never reaches a backup.
                var accessError: Unmanaged<CFError>?
                guard let access = SecAccessControlCreateWithFlags(
                    nil,
                    kSecAttrAccessibleWhenPasscodeSetThisDeviceOnly,
                    .biometryCurrentSet,
                    &accessError
                ) else {
                    let message = (accessError?.takeRetainedValue()).map { String(describing: $0) }
                        ?? "this device cannot gate on biometrics"
                    call.reject(message, "biometrics_unavailable"); return
                }
                attributes[kSecAttrAccessControl as String] = access
            } else {
                // The background receive path has no user to ask at 3am. Still
                // this device only, still never synced.
                attributes[kSecAttrAccessible as String] =
                    kSecAttrAccessibleWhenUnlockedThisDeviceOnly
            }

            // Only now, with the new item fully described, is the old one
            // dropped: deleting first would destroy a working secret whenever
            // building its replacement failed. A stale entry must not survive
            // either — under a *weaker* gate it would be a silent downgrade.
            SecItemDelete(Self.query(for: key) as CFDictionary)

            let status = SecItemAdd(attributes as CFDictionary, nil)
            guard status == errSecSuccess else {
                call.reject(Self.message(for: status), Self.code(for: status)); return
            }
            call.resolve()
        }
    }

    // MARK: - getSecret

    @objc func getSecret(_ call: CAPPluginCall) {
        guard let key = call.getString("key"), !key.isEmpty else {
            call.reject("key is required", "bad_request"); return
        }
        let reason = call.getString("reason") ?? "Unlock your identity"

        queue.async {
            let context = LAContext()
            context.localizedReason = reason

            var query = Self.query(for: key)
            query[kSecReturnData as String] = true
            query[kSecMatchLimit as String] = kSecMatchLimitOne
            query[kSecUseAuthenticationContext as String] = context

            var item: CFTypeRef?
            let status = SecItemCopyMatching(query as CFDictionary, &item)

            switch status {
            case errSecSuccess:
                guard let data = item as? Data, let value = String(data: data, encoding: .utf8) else {
                    call.reject("stored secret is unreadable", "corrupt"); return
                }
                call.resolve(["value": value])
            case errSecItemNotFound:
                // Either never stored, or the OS destroyed it when the enrolled
                // biometrics changed. Both mean this device's copy is gone for
                // good, which the web layer turns into the re-enrolment path —
                // so it is reported as an absence, not as a failure to retry.
                call.resolve(["value": NSNull()])
            default:
                // Everything else — a cancelled prompt, a failed match, a
                // locked device — is transient and must NOT read as absence.
                call.reject(Self.message(for: status), Self.code(for: status))
            }
        }
    }

    // MARK: - deleteSecret

    @objc func deleteSecret(_ call: CAPPluginCall) {
        guard let key = call.getString("key"), !key.isEmpty else {
            call.reject("key is required", "bad_request"); return
        }
        queue.async {
            let status = SecItemDelete(Self.query(for: key) as CFDictionary)
            guard status == errSecSuccess || status == errSecItemNotFound else {
                call.reject(Self.message(for: status), Self.code(for: status)); return
            }
            call.resolve()
        }
    }

    // MARK: - canUseBiometrics

    @objc func canUseBiometrics(_ call: CAPPluginCall) {
        let context = LAContext()
        var error: NSError?

        // The passcode comes first, and not as a formality: the entry is stored
        // under `WhenPasscodeSetThisDeviceOnly`, which simply refuses to exist
        // on a device with no passcode. Reporting biometrics as usable there
        // would send the caller down a path that fails at the write — which is
        // exactly what a simulator with Face ID enrolled and no passcode does.
        var passcodeError: NSError?
        let hasPasscode = context.canEvaluatePolicy(
            .deviceOwnerAuthentication, error: &passcodeError)
        let available = hasPasscode && context.canEvaluatePolicy(
            .deviceOwnerAuthenticationWithBiometrics, error: &error)

        var result: [String: Any] = ["available": available]
        if !available {
            result["reason"] = hasPasscode
                ? Self.biometricReason(error)
                : Self.biometricReason(passcodeError)
        }
        // The kind matters to the copy the app shows: "Use Face ID" is wrong on
        // a device that only has Touch ID.
        switch context.biometryType {
        case .faceID: result["kind"] = "face"
        case .touchID: result["kind"] = "touch"
        case .opticID: result["kind"] = "optic"
        default: result["kind"] = "none"
        }
        call.resolve(result)
    }

    // MARK: - helpers

    private static func query(for key: String) -> [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: key
        ]
    }

    private static func code(for status: OSStatus) -> String {
        switch status {
        case errSecUserCanceled: return "cancelled"
        case errSecAuthFailed: return "auth_failed"
        case errSecInteractionNotAllowed: return "locked"
        case errSecDuplicateItem: return "conflict"
        default: return "keychain_error"
        }
    }

    private static func message(for status: OSStatus) -> String {
        switch status {
        case errSecUserCanceled: return "Authentication was cancelled."
        case errSecAuthFailed: return "Authentication failed."
        case errSecInteractionNotAllowed: return "The device is locked."
        default:
            return SecCopyErrorMessageString(status, nil) as String?
                ?? "Keychain error \(status)."
        }
    }

    private static func biometricReason(_ error: NSError?) -> String {
        guard let code = error.map({ LAError.Code(rawValue: $0.code) }) ?? nil else {
            return "unavailable"
        }
        switch code {
        case .biometryNotEnrolled: return "not_enrolled"
        case .biometryNotAvailable: return "no_hardware"
        case .biometryLockout: return "locked_out"
        case .passcodeNotSet: return "no_passcode"
        default: return "unavailable"
        }
    }
}
