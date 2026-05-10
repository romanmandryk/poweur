/**
 * WebAuthn / Passkey integration for Poweur ID.
 *
 * Keys are protected using the PRF (Pseudo-Random Function) WebAuthn extension,
 * which allows deriving a deterministic secret from the authenticator — used to
 * wrap the identity's Ed25519 + X25519 private keys stored in localStorage.
 *
 * Fallback: if the authenticator or browser does not support PRF, keys are
 * wrapped with a user-supplied PIN via PBKDF2 (see crypto.js).
 */

import { wrapKeysAES, unwrapKeysAES, toBase64url, fromBase64url } from "./crypto.js";

const PRF_SALT = new TextEncoder().encode("poweur-prf-v1");

// ─── Passkey Creation ─────────────────────────────────────────────────────────

/**
 * Create a passkey for an identity, requesting PRF output.
 *
 * @param {string} identity  — Full FQDN (alice.poweur.net)
 * @param {string} userId    — Stable user ID (base64url-encoded random bytes)
 * @returns {{ credentialId: string, prfOutput: Uint8Array|null, supportsPRF: boolean }}
 */
export async function createPasskey(identity, userId) {
  if (!window.PublicKeyCredential) throw new Error("WebAuthn is not supported in this browser.");

  const challenge = crypto.getRandomValues(new Uint8Array(32));
  const userIdBytes = fromBase64url(userId);

  const createOptions = {
    challenge,
    rp: {
      name: "Poweur ID",
      id: window.location.hostname === "localhost" ? "localhost" : window.location.hostname,
    },
    user: {
      id: userIdBytes,
      name: identity,
      displayName: identity,
    },
    pubKeyCredParams: [
      { type: "public-key", alg: -8 },   // Ed25519
      { type: "public-key", alg: -7 },   // ES256 (fallback)
      { type: "public-key", alg: -257 }, // RS256 (broad compat fallback)
    ],
    authenticatorSelection: {
      authenticatorAttachment: "platform",
      requireResidentKey: true,
      residentKey: "required",
      userVerification: "required",
    },
    timeout: 120000,
    extensions: {
      prf: { eval: { first: PRF_SALT } },
    },
  };

  let credential;
  try {
    credential = await navigator.credentials.create({ publicKey: createOptions });
  } catch (err) {
    throw new Error(`Passkey creation failed: ${err.message}`);
  }

  const credentialId = toBase64url(new Uint8Array(credential.rawId));
  const extResults = credential.getClientExtensionResults();
  const prfFirst = extResults?.prf?.results?.first;
  const prfOutput = prfFirst ? new Uint8Array(prfFirst) : null;

  return { credentialId, prfOutput, supportsPRF: prfOutput !== null };
}

// ─── Passkey Authentication ───────────────────────────────────────────────────

/**
 * Authenticate with a stored passkey, retrieving the PRF output for key unwrapping.
 *
 * @param {string} credentialId — base64url credential ID stored alongside identity
 * @returns {{ assertion: PublicKeyCredential, prfOutput: Uint8Array|null, supportsPRF: boolean }}
 */
export async function authenticatePasskey(credentialId) {
  if (!window.PublicKeyCredential) throw new Error("WebAuthn is not supported in this browser.");

  const challenge = crypto.getRandomValues(new Uint8Array(32));
  const credIdBytes = fromBase64url(credentialId);

  const getOptions = {
    challenge,
    allowCredentials: [{ type: "public-key", id: credIdBytes }],
    userVerification: "required",
    timeout: 120000,
    extensions: {
      prf: { eval: { first: PRF_SALT } },
    },
  };

  let assertion;
  try {
    assertion = await navigator.credentials.get({ publicKey: getOptions });
  } catch (err) {
    throw new Error(`Passkey authentication failed: ${err.message}`);
  }

  const extResults = assertion.getClientExtensionResults();
  const prfFirst = extResults?.prf?.results?.first;
  const prfOutput = prfFirst ? new Uint8Array(prfFirst) : null;

  return { assertion, prfOutput, supportsPRF: prfOutput !== null };
}

// ─── Key Wrapping with PRF ────────────────────────────────────────────────────

/**
 * Encrypt the identity's private key JWKs using the PRF output as key material.
 * Returns an object suitable for storage.
 */
export async function wrapKeysWithPRF(prfOutput, signingJWK, encJWK) {
  const wrapped = await wrapKeysAES(prfOutput, signingJWK, encJWK);
  return { ...wrapped, kdf: "prf" };
}

/**
 * Decrypt previously PRF-wrapped keys.
 * Returns { signingJWK, encJWK }
 */
export async function unwrapKeysWithPRF(prfOutput, encryptedData) {
  return unwrapKeysAES(prfOutput, encryptedData);
}

// ─── Challenge Signing (for inbox auth) ──────────────────────────────────────

/**
 * Sign a relay challenge string with the identity's signing key JWK.
 * Returns base64url signature.
 */
export async function signChallenge(signingJWK, challenge) {
  const key = await crypto.subtle.importKey(
    "jwk", signingJWK, { name: "Ed25519" }, false, ["sign"]
  );
  const sig = await crypto.subtle.sign({ name: "Ed25519" }, key, new TextEncoder().encode(challenge));
  const bytes = new Uint8Array(sig);
  return btoa(String.fromCharCode(...bytes)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

// ─── Platform Support Detection ───────────────────────────────────────────────

export async function checkPasskeySupport() {
  if (!window.PublicKeyCredential) return { available: false, reason: "WebAuthn not supported" };
  try {
    const available = await PublicKeyCredential.isUserVerifyingPlatformAuthenticatorAvailable();
    return { available, reason: available ? null : "No platform authenticator available" };
  } catch {
    return { available: false, reason: "Could not query platform authenticator" };
  }
}
