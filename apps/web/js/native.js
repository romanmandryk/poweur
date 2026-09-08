/**
 * The native capability seam (EPIC-019 E19-T2).
 *
 * The shell is the *same* web app in a native container, so nothing here forks
 * a screen. What it does is answer one question — "is there a hardware-backed
 * secret store on this device?" — and give the rest of the app a single way to
 * use it when the answer is yes.
 *
 * Why this exists at all: a browser wraps identity keys with a passkey's PRF
 * output or a PIN, and a passkey drags WebAuthn's origin model along with it —
 * associated domains, `rp.id`, a relying party. A native keystore has none of
 * that, which is what lets one store build work against *any* relay on *any*
 * domain. That is why `kdf: "native"` is the load-bearing task of EPIC-019 and
 * associated domains are an optional nicety.
 *
 * The contract a plugin must satisfy is at the bottom of this file. Everything
 * above it works unchanged in a plain browser, where `hasNativeKeystore()` is
 * false and the app takes the passkey or PIN path exactly as before.
 */

import { wrapKeysAES, unwrapKeysAES, fromBase64url, toBase64url } from "./vault.js";

/** The plugin, when the shell injected one. */
function plugin() {
  return globalThis.Capacitor?.Plugins?.PoweurKeystore ?? null;
}

/** True inside a Capacitor shell that exposes secure storage. */
export function hasNativeKeystore() {
  return Boolean(plugin());
}

/** True inside the shell at all — used for shell-only affordances, not custody. */
export function isNativeShell() {
  return Boolean(globalThis.Capacitor?.isNativePlatform?.());
}

/**
 * Gates on the secret, mirroring the tiered custody EPIC-019 specifies.
 *
 * `biometric` is the default because the identity's signing key authorises
 * everything anyone can do as you. `device` exists only for the background
 * receive path, which cannot prompt: a notification arriving at 3am has no
 * user to ask.
 */
export const GATE_BIOMETRIC = "biometric";
export const GATE_DEVICE = "device";

/**
 * Wrap identity keys under a hardware-held secret.
 *
 * The secret never leaves the platform's keystore in a form the app keeps: it
 * is fetched, used to derive the AES key, and dropped. `wrapKeysAES` is reused
 * unchanged — only where the 32 bytes come from differs, which is the whole
 * point of the `kdf` field.
 */
export async function wrapKeysNative(identity, signingJWK, encJWK, seed = null, options = {}) {
  const store = requirePlugin();
  const gate = options.gate ?? GATE_BIOMETRIC;
  const secret = randomSecret();
  await store.setSecret({ key: secretKeyFor(identity), value: toBase64url(secret), gate });
  const wrapped = await wrapKeysAES(secret, signingJWK, encJWK, seed);
  secret.fill(0);
  return { ...wrapped, kdf: "native", gate };
}

/**
 * Unwrap, prompting for biometrics if the secret was stored behind them.
 *
 * `reason` is shown in the platform's own prompt, so it has to say what the
 * user is authorising rather than that the app wants something.
 */
export async function unwrapKeysNative(identity, wrapped, { reason = "Unlock your identity" } = {}) {
  const store = requirePlugin();
  const { value } = await store.getSecret({ key: secretKeyFor(identity), reason });
  if (!value) {
    throw new Error(
      "This device no longer holds the key for this identity. " +
      "Add the device again from one you still use, or restore from your recovery kit.",
    );
  }
  const secret = fromBase64url(value);
  try {
    return await unwrapKeysAES(secret, wrapped);
  } finally {
    secret.fill(0);
  }
}

/** Forget the hardware secret — removing an identity from this device. */
export async function forgetNativeSecret(identity) {
  const store = plugin();
  if (!store) return;
  await store.deleteSecret({ key: secretKeyFor(identity) }).catch(() => {});
}

/**
 * Whether this device can gate on biometrics right now.
 *
 * Distinguishes "no hardware" from "enrolled but currently unavailable", which
 * matters: the first means fall back to a PIN forever, the second means try
 * again after the user fixes it.
 */
export async function biometricAvailability() {
  const store = plugin();
  if (!store) return { available: false, reason: "no_native_keystore" };
  try {
    return await store.canUseBiometrics();
  } catch (error) {
    return { available: false, reason: String(error?.message ?? error) };
  }
}

function requirePlugin() {
  const store = plugin();
  if (!store) {
    throw new Error("native key custody is not available in this build");
  }
  return store;
}

function randomSecret() {
  return crypto.getRandomValues(new Uint8Array(32));
}

/**
 * One secret per identity, so removing one identity cannot lock the others out
 * and a compromised prompt for one is not consent for all.
 */
function secretKeyFor(identity) {
  return `poweur.identity.${String(identity).trim().toLowerCase()}`;
}

/**
 * ---------------------------------------------------------------------------
 * The plugin contract
 * ---------------------------------------------------------------------------
 *
 * A shell provides `Capacitor.Plugins.PoweurKeystore` with four methods. The
 * JavaScript above is complete; what remains is the platform half, and it is
 * deliberately tiny — everything cryptographic already happens here.
 *
 *   setSecret({ key, value, gate })       → void
 *     Store `value` (base64url of 32 random bytes) under `key`.
 *     gate "biometric": iOS `SecAccessControl` with `.biometryCurrentSet`,
 *       Android `setUserAuthenticationRequired(true)` + `BiometricPrompt`.
 *     gate "device": iOS `kSecAttrAccessibleWhenUnlockedThisDeviceOnly`,
 *       Android device-credential-bound. Never synced to a cloud backup: a
 *       key restored onto another device is a key the user cannot revoke.
 *
 *   getSecret({ key, reason })            → { value: string | null }
 *     Prompts when the entry is biometric-gated. Returns `{ value: null }` when
 *     the entry is gone — including the case that matters most, an OS
 *     invalidation after biometric enrolment changed. That is not an error
 *     condition to swallow: it means this device's copy is unrecoverable and
 *     the user needs the re-enrolment path (EPIC-011), which is why the message
 *     above says so instead of "unlock failed".
 *
 *   deleteSecret({ key })                 → void
 *
 *   canUseBiometrics()                    → { available: boolean, reason?: string }
 *
 * The app stores only the *wrapped* keys, exactly as it does for `prf` and
 * `pbkdf2`; the plugin holds one 32-byte secret and knows nothing about
 * identities, relays or messages.
 */
