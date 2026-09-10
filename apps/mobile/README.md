# `@poweur/mobile` — the Capacitor shell (EPIC-019)

The iOS and Android apps are the **web client in a native container**. There is no mobile
build of the UI and no forked screen: `pnpm stage` copies `apps/web` into `www/`, and
`cap sync` carries it into the platform projects.

```bash
pnpm --filter @poweur/mobile run sync     # stage apps/web, then cap sync
pnpm --filter @poweur/mobile run ios      # …and open Xcode
pnpm --filter @poweur/mobile test         # staging invariants (no native toolchain needed)
```

`pnpm stage` is a pnpm 10+ builtin (publish staging). Always `pnpm run stage` / `pnpm run sync`
so the package scripts run, not pnpm's own command.

## Why a shell rather than native or React Native

A Capacitor app runs on **one fixed origin** and talks to relays cross-origin, which the
relay already permits. That is the property a browser app cannot have: `localStorage` is
per-origin, so `alice.r1.com/app/` structurally cannot see an identity stored by
`alice.r2.com`. Many identities across many relays in one app is the reason to install
anything at all, and it falls out of the shell's origin rather than being built.

## What the shell adds, and what it must not

It adds capabilities a browser lacks — hardware-backed key custody, push, background sync,
the share sheet — through the seam in [`apps/web/js/native.js`](../web/js/native.js), which
answers "is this available?" and otherwise leaves the app unchanged. Everything there has a
web fallback and runs untouched in a plain browser.

It does **not** fix layout. Mobile-first is EPIC-015's job, and a responsive bug found on a
phone belongs in `apps/web`.

## Native key custody

`kdf: "native"` is the load-bearing piece. A browser wraps identity keys with a passkey's
PRF output, and a passkey brings WebAuthn's origin model with it — associated domains,
`rp.id`, a relying party. A platform keystore has none of that, which is what lets **one
store build work against any relay on any domain**, self-hosted included. Associated
domains (E19-T3) are then an optional nicety for hosted identities rather than a gate.

Both halves now exist. The contract — four methods, and what each has to guarantee — is
documented at the bottom of `native.js`; the platforms implement it in
[`ios/App/App/PoweurKeystorePlugin.swift`](ios/App/App/PoweurKeystorePlugin.swift) and
[`android/app/src/main/java/net/poweur/app/PoweurKeystorePlugin.java`](android/app/src/main/java/net/poweur/app/PoweurKeystorePlugin.java).

Neither does anything cryptographic. The web layer generates 32 random bytes, derives an
AES key from them and wraps the identity keys exactly as the `prf` path does;
the plugin only holds those bytes somewhere the app cannot read without the user. That is
why the platform halves are small, and why the shapes differ without the app noticing:

| | iOS | Android |
|---|---|---|
| Where the secret lives | Keychain item, `kSecClassGenericPassword` | ciphertext in `SharedPreferences`, opened by a per-identity AES key in the TEE |
| Biometric gate | `SecAccessControl(.biometryCurrentSet)` over `WhenPasscodeSetThisDeviceOnly` | `setUserAuthenticationRequired(true)` + `setInvalidatedByBiometricEnrollment(true)`, unlocked by `BiometricPrompt` |
| Prompts on **store** | no | **yes** — the Keystore gates the key, not the direction |
| Enrolment changed | OS destroys the item; reads as absent | `KeyPermanentlyInvalidatedException`; deleted, then reads as absent |

Both report an invalidated key as an **absence**, never as a failed unlock: that difference
is what sends the user to re-enrolment (EPIC-011) instead of to a retry that can never work.

Registration is not symmetric either. Android is told about the plugin in
`MainActivity.onCreate`; iOS needs a `CAPBridgeViewController` subclass, because
`SceneDelegate` builds the root controller itself and Capacitor only scans plugins that
ship as Swift packages — hence `MainViewController.swift`, whose entire job is one
`registerPluginInstance` call.

## State of the platform projects

`ios/` is generated (`cap add ios`, Capacitor 8, SwiftPM — no CocoaPods) and **verified on
an iOS 26.5 simulator**: claim → keystore custody with no PIN → relaunch → unlock from the
Keychain. Requires the iOS platform (`xcodebuild -downloadPlatform iOS`, several GB).

One simulator caveat: it has no device passcode, and `.biometryCurrentSet` items are
accepted and read back without ever drawing the Face ID sheet. The gate itself is therefore
only proven on real hardware; what the simulator proves is that the plugin is registered,
the item round-trips, and the app takes the native path over the passkey path.

`android/` is generated (`cap add android`) and builds (`./gradlew :app:assembleDebug`).
See **Testing on Android** below.

## Testing on Android

What is needed, and why:

| Piece | Why |
|---|---|
| **Android Studio** | the only supported way to install the SDK, and the AVD Manager that creates emulators |
| **SDK Platform 36 + Build-Tools 36** | `compileSdk`/`targetSdk` in `android/variables.gradle` |
| **Platform-Tools** (`adb`) | installing and log-reading |
| **JDK 21** | what Capacitor 8's Gradle build expects (`brew install openjdk@21`) |
| **A system image with biometrics** | Google APIs, API 34+; a bare AOSP image has no fingerprint sensor to enrol |

Everything but the last is installed by Android Studio's first-run wizard. Then,
from the **repo root** (not `apps/mobile/android`):

```bash
pnpm android          # stage, cap sync, assembleDebug, adb install -r, launch
```

That is the usual loop against a USB phone or a running emulator. `pnpm mobile:android`
stops after the APK; `ANDROID_SERIAL` picks a device when more than one is connected.

```bash
pnpm mobile:android   # stage, cap sync, assembleDebug only
adb install -r apps/mobile/android/app/build/outputs/apk/debug/app-debug.apk
```

A fresh install talks to **https://poweur.net**. The landing picker can switch to
the local `go run` relay (`http://10.0.2.2:8080` on the emulator, `127.0.0.1:8080`
on iOS) or a typed URL. A physical phone cannot use `10.0.2.2`; use **Other…**
with the Mac's LAN address, and add that host to `network_security_config.xml`
if it is plain HTTP.

A USB phone that `adb devices` does not list is almost always USB debugging, not
the cable: on a Pixel, Developer options → USB debugging, then the USB
notification → **File transfer / Android Auto**, and accept **Allow USB debugging**.

The emulator has no fingerprint enrolled out of the box, and without one the app cannot
use native custody — it will try a PRF passkey or refuse. Enrol one under **Settings → Security → Fingerprint**, and when
the emulator asks for a finger, touch it from the host:

```bash
adb -e emu finger touch 1
```

A relay on the host is reachable from the emulator at `http://10.0.2.2:8080`, not
`127.0.0.1` — that address belongs to the emulated device.
