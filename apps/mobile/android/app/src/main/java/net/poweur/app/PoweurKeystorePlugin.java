package net.poweur.app;

import android.os.Build;
import android.content.SharedPreferences;
import android.security.keystore.KeyGenParameterSpec;
import android.security.keystore.KeyPermanentlyInvalidatedException;
import android.security.keystore.KeyProperties;
import android.util.Base64;

import androidx.biometric.BiometricManager;
import androidx.biometric.BiometricPrompt;
import androidx.fragment.app.FragmentActivity;

import com.getcapacitor.JSObject;
import com.getcapacitor.Plugin;
import com.getcapacitor.PluginCall;
import com.getcapacitor.PluginMethod;
import com.getcapacitor.annotation.CapacitorPlugin;

import java.security.KeyStore;
import java.security.UnrecoverableKeyException;

import javax.crypto.Cipher;
import javax.crypto.KeyGenerator;
import javax.crypto.SecretKey;
import javax.crypto.spec.GCMParameterSpec;

/**
 * The Android half of the `PoweurKeystore` contract (EPIC-019 E19-T2).
 *
 * The contract — four methods and what each has to guarantee — is documented at
 * the bottom of `apps/web/src/lib/native.js`, the only caller. Nothing about
 * identities, relays or messages is known here: the web layer generates 32
 * random bytes and does the AES wrapping of the keys themselves, and this class
 * only keeps those bytes somewhere the app cannot read without the user.
 *
 * Android has no equivalent of an ACL'd Keychain item, so the shape differs
 * from iOS: a hardware-held AES key per identity encrypts the secret, and the
 * ciphertext lives in ordinary preferences. That is safe precisely because the
 * key never leaves the TEE — copying the preferences file off the device yields
 * bytes nothing can open.
 */
@CapacitorPlugin(name = "PoweurKeystore")
public class PoweurKeystorePlugin extends Plugin {

    private static final String KEYSTORE = "AndroidKeyStore";
    private static final String PREFS = "poweur.keystore";
    private static final String TRANSFORM =
        KeyProperties.KEY_ALGORITHM_AES + "/" + KeyProperties.BLOCK_MODE_GCM + "/"
            + KeyProperties.ENCRYPTION_PADDING_NONE;
    private static final int TAG_BITS = 128;
    private static final String GATE_BIOMETRIC = "biometric";
    private static final String GATE_DEVICE = "device";

    // ── setSecret ──────────────────────────────────────────────────────────

    @PluginMethod
    public void setSecret(PluginCall call) {
        final String key = call.getString("key");
        final String value = call.getString("value");
        final String gate = call.getString("gate", GATE_BIOMETRIC);
        if (key == null || key.isEmpty()) { call.reject("key is required", "bad_request"); return; }
        if (value == null || value.isEmpty()) { call.reject("value is required", "bad_request"); return; }
        if (!GATE_BIOMETRIC.equals(gate) && !GATE_DEVICE.equals(gate)) {
            call.reject("unknown gate " + gate, "bad_request"); return;
        }

        final boolean biometric = GATE_BIOMETRIC.equals(gate);
        final SecretKey secretKey;
        final Cipher cipher;
        try {
            // Generating under the same alias replaces the old key outright, so
            // there is nothing to delete first — and deleting first would
            // destroy a working secret whenever generating its replacement
            // failed, which is exactly what a device with no enrolled
            // biometrics does.
            secretKey = generateKey(alias(key), biometric);
            cipher = Cipher.getInstance(TRANSFORM);
            cipher.init(Cipher.ENCRYPT_MODE, secretKey);
        } catch (Exception error) {
            call.reject(biometricSetupMessage(error), errorCode(error), error);
            return;
        }

        if (!biometric) {
            finishSet(call, key, gate, cipher, value);
            return;
        }
        // A key bound to biometrics needs the user for *encryption* too — the
        // Keystore gates the key, not the direction. One prompt when the
        // identity is created is the price of never prompting-free afterwards.
        prompt(call, cipher, call.getString("reason", "Protect your Poweur identity"),
            authorized -> finishSet(call, key, gate, authorized, value));
    }

    private void finishSet(PluginCall call, String key, String gate, Cipher cipher, String value) {
        try {
            byte[] sealed = cipher.doFinal(value.getBytes("UTF-8"));
            byte[] iv = cipher.getIV();
            prefs().edit()
                .putString(key, encode(iv) + ":" + encode(sealed))
                .putString(key + ".gate", gate)
                .apply();
            call.resolve();
        } catch (Exception error) {
            forget(key);
            call.reject("Could not store the secret.", "keystore_error", error);
        }
    }

    // ── getSecret ──────────────────────────────────────────────────────────

    @PluginMethod
    public void getSecret(PluginCall call) {
        final String key = call.getString("key");
        if (key == null || key.isEmpty()) { call.reject("key is required", "bad_request"); return; }

        final String stored = prefs().getString(key, null);
        if (stored == null || !stored.contains(":")) { resolveMissing(call); return; }

        final String[] parts = stored.split(":", 2);
        final byte[] iv = decode(parts[0]);
        final byte[] sealed = decode(parts[1]);

        final Cipher cipher;
        try {
            KeyStore store = KeyStore.getInstance(KEYSTORE);
            store.load(null);
            SecretKey secretKey = (SecretKey) store.getKey(alias(key), null);
            if (secretKey == null) { forget(key); resolveMissing(call); return; }
            cipher = Cipher.getInstance(TRANSFORM);
            cipher.init(Cipher.DECRYPT_MODE, secretKey, new GCMParameterSpec(TAG_BITS, iv));
        } catch (KeyPermanentlyInvalidatedException | UnrecoverableKeyException invalidated) {
            // The enrolled biometrics changed, so the OS destroyed the key. This
            // device's copy is unrecoverable — an absence, not a retryable
            // failure, which is what sends the web layer to re-enrolment.
            forget(key);
            resolveMissing(call);
            return;
        } catch (Exception error) {
            call.reject("Could not open the secret.", "keystore_error", error);
            return;
        }

        if (!GATE_BIOMETRIC.equals(prefs().getString(key + ".gate", GATE_BIOMETRIC))) {
            finishGet(call, key, cipher, sealed);
            return;
        }
        prompt(call, cipher, call.getString("reason", "Unlock your identity"),
            authorized -> finishGet(call, key, authorized, sealed));
    }

    private void finishGet(PluginCall call, String key, Cipher cipher, byte[] sealed) {
        try {
            JSObject result = new JSObject();
            result.put("value", new String(cipher.doFinal(sealed), "UTF-8"));
            call.resolve(result);
        } catch (Exception error) {
            call.reject("Could not open the secret.", "keystore_error", error);
        }
    }

    private void resolveMissing(PluginCall call) {
        JSObject result = new JSObject();
        result.put("value", (String) null);
        call.resolve(result);
    }

    // ── deleteSecret ───────────────────────────────────────────────────────

    @PluginMethod
    public void deleteSecret(PluginCall call) {
        String key = call.getString("key");
        if (key == null || key.isEmpty()) { call.reject("key is required", "bad_request"); return; }
        forget(key);
        call.resolve();
    }

    // ── canUseBiometrics ───────────────────────────────────────────────────

    @PluginMethod
    public void canUseBiometrics(PluginCall call) {
        int status = BiometricManager.from(getContext())
            .canAuthenticate(BiometricManager.Authenticators.BIOMETRIC_STRONG);
        JSObject result = new JSObject();
        result.put("available", status == BiometricManager.BIOMETRIC_SUCCESS);
        result.put("kind", status == BiometricManager.BIOMETRIC_SUCCESS ? "biometric" : "none");
        if (status != BiometricManager.BIOMETRIC_SUCCESS) {
            result.put("reason", availabilityReason(status));
        }
        call.resolve(result);
    }

    private static String availabilityReason(int status) {
        switch (status) {
            case BiometricManager.BIOMETRIC_ERROR_NONE_ENROLLED: return "not_enrolled";
            case BiometricManager.BIOMETRIC_ERROR_NO_HARDWARE: return "no_hardware";
            case BiometricManager.BIOMETRIC_ERROR_HW_UNAVAILABLE: return "hardware_busy";
            case BiometricManager.BIOMETRIC_ERROR_SECURITY_UPDATE_REQUIRED: return "update_required";
            default: return "unavailable";
        }
    }

    // ── helpers ────────────────────────────────────────────────────────────

    private interface Authorized { void run(Cipher cipher); }

    /**
     * Run the platform prompt against this cipher, then hand the *authorised*
     * cipher back. A cancel is rejected rather than reported as an absence:
     * losing that distinction would tell the app the identity is gone every
     * time somebody dismissed a dialog.
     */
    private void prompt(PluginCall call, Cipher cipher, String reason, Authorized then) {
        final FragmentActivity activity = getActivity();
        activity.runOnUiThread(() -> {
            BiometricPrompt.PromptInfo info = new BiometricPrompt.PromptInfo.Builder()
                .setTitle("Poweur")
                .setSubtitle(reason)
                .setNegativeButtonText("Cancel")
                .setAllowedAuthenticators(BiometricManager.Authenticators.BIOMETRIC_STRONG)
                .setConfirmationRequired(false)
                .build();

            BiometricPrompt biometricPrompt = new BiometricPrompt(
                activity,
                androidx.core.content.ContextCompat.getMainExecutor(activity),
                new BiometricPrompt.AuthenticationCallback() {
                    @Override
                    public void onAuthenticationSucceeded(BiometricPrompt.AuthenticationResult result) {
                        Cipher authorized = result.getCryptoObject() == null
                            ? cipher : result.getCryptoObject().getCipher();
                        then.run(authorized == null ? cipher : authorized);
                    }

                    @Override
                    public void onAuthenticationError(int code, CharSequence message) {
                        boolean cancelled = code == BiometricPrompt.ERROR_USER_CANCELED
                            || code == BiometricPrompt.ERROR_NEGATIVE_BUTTON
                            || code == BiometricPrompt.ERROR_CANCELED;
                        call.reject(String.valueOf(message), cancelled ? "cancelled" : "auth_failed");
                    }
                });

            biometricPrompt.authenticate(info, new BiometricPrompt.CryptoObject(cipher));
        });
    }

    private SecretKey generateKey(String alias, boolean biometric) throws Exception {
        KeyGenParameterSpec.Builder spec = new KeyGenParameterSpec.Builder(
            alias, KeyProperties.PURPOSE_ENCRYPT | KeyProperties.PURPOSE_DECRYPT)
            .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
            .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
            .setKeySize(256);

        if (biometric) {
            spec.setUserAuthenticationRequired(true);
            // Every use needs a fresh authentication, and adding a fingerprint
            // destroys the key — someone who enrols their own face on an
            // unlocked phone must not inherit the identity.
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
                spec.setUserAuthenticationParameters(
                    0, KeyProperties.AUTH_BIOMETRIC_STRONG);
            } else {
                spec.setUserAuthenticationValidityDurationSeconds(-1);
            }
            spec.setInvalidatedByBiometricEnrollment(true);
        }

        KeyGenerator generator = KeyGenerator.getInstance(
            KeyProperties.KEY_ALGORITHM_AES, KEYSTORE);
        generator.init(spec.build());
        return generator.generateKey();
    }

    /** Drop both halves — the hardware key and the ciphertext it opens. */
    private void forget(String key) {
        prefs().edit().remove(key).remove(key + ".gate").apply();
        try {
            KeyStore store = KeyStore.getInstance(KEYSTORE);
            store.load(null);
            store.deleteEntry(alias(key));
        } catch (Exception ignored) {
            // Nothing to undo: the ciphertext is already gone, so the orphaned
            // key opens nothing.
        }
    }

    private SharedPreferences prefs() {
        return getContext().getSharedPreferences(PREFS, android.content.Context.MODE_PRIVATE);
    }

    private static String alias(String key) {
        return "poweur.kek." + key;
    }

    private static String encode(byte[] raw) {
        return Base64.encodeToString(raw, Base64.NO_WRAP);
    }

    private static byte[] decode(String encoded) {
        return Base64.decode(encoded, Base64.NO_WRAP);
    }

    private static String errorCode(Exception error) {
        return error instanceof java.security.InvalidAlgorithmParameterException
            ? "biometrics_unavailable" : "keystore_error";
    }

    private static String biometricSetupMessage(Exception error) {
        return error instanceof java.security.InvalidAlgorithmParameterException
            ? "This device has no biometrics enrolled, so the key cannot be gated on them."
            : "Could not create a hardware key.";
    }
}
