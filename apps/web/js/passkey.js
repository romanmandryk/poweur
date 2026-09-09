/**
 * WebAuthn / Passkey integration for Poweur ID.
 *
 * Keys are protected using the PRF (Pseudo-Random Function) WebAuthn extension,
 * which allows deriving a deterministic secret from the authenticator — used to
 * wrap the identity's Ed25519 + X25519 private keys stored in localStorage.
 *
 * There is no PIN fallback. An authenticator that cannot emit PRF cannot hold
 * a web identity; the user is told to switch browser/authenticator or use the
 * mobile app (native keystore). A PIN is not a substitute wrapping secret.
 *
 * Since EPIC-011 this module also produces the **assertions** the relay
 * keystore verifies. That is the bootstrap path: after site data is cleared
 * there is no identity key to sign with, so an assertion from an enrolled
 * authenticator is the only thing that can authorize reading the wrapped seed
 * back. Creation therefore has to capture the credential's public key too —
 * the relay stores it and checks signatures against it.
 */

import { registrableDomain } from "@poweur/client";

import { wrapKeysAES, unwrapKeysAES, toBase64url, fromBase64url } from "./vault.js";

const PRF_SALT = new TextEncoder().encode("poweur-prf-v1");

/** Shown whenever this browser cannot wrap keys with passkey PRF. */
export const PRF_UNAVAILABLE_MESSAGE =
  "This browser or authenticator does not support passkeys with PRF. Use Safari or Chrome with Apple or Google passkeys, or the Poweur mobile app.";

// ─── Passkey Creation ─────────────────────────────────────────────────────────

/**
 * The relying-party id the relay must be told to verify assertions against.
 *
 * The page host, which is what a credential minted before EPIC-018 E18-T4 was
 * bound to. Prefer `rpIdFor(identity)` (storage.js), which returns the value
 * the credential was actually created with; this is the fallback for records
 * that predate storing it, and for calls with no identity in hand.
 */
export function rpId() {
  return globalThis.location?.hostname ?? "";
}

/**
 * The rp.id a credential for `identity` should be created with (E18-T4).
 *
 * The registrable domain of the identity's home, so one credential works on
 * both the launcher host and the identity's own origin — `id.poweur.net`
 * mints it, `alice.poweur.net` asserts it, and the user is never asked to
 * enroll twice for the hop the launcher introduces.
 *
 * A browser only accepts an rp.id that is the current host or a registrable
 * suffix of it, so this falls back to the page host whenever the identity
 * lives somewhere else — which is exactly the dev case, where the app is on
 * 127.0.0.1 and the identity is alice.poweur.net. `host` defaults to the page
 * and is a parameter so the rule is testable without a fake `location`.
 *
 * The tradeoff, stated because it is a decision: hosted credentials become
 * scoped per *domain* rather than per identity, so any `*.poweur.net` origin
 * can ask for an assertion from any hosted credential. Every one of those
 * origins is the same relay under the same operator, so this adds no trust
 * boundary that did not already exist, and the WebAuthn user handle keeps the
 * identities apart in the authenticator's picker.
 */
export function credentialRpId(identity, host = globalThis.location?.hostname ?? "") {
  const candidate = registrableDomain(String(identity || ""));
  if (!candidate) return host;
  if (host === candidate || host.endsWith("." + candidate)) return candidate;
  return host;
}

/**
 * Create a passkey for an identity, requesting PRF output.
 *
 * @param {string} identity  — Full FQDN (alice.poweur.net)
 * @param {string} userId    — Stable user ID (base64url-encoded random bytes)
 * @returns {{ credentialId: string, prfOutput: Uint8Array|null, supportsPRF: boolean,
 *            credentialPublicKey: string|null, credentialAlg: number|null }}
 */
export async function createPasskey(identity, userId, options = {}) {
  if (!window.PublicKeyCredential) throw new Error("WebAuthn is not supported in this browser.");

  const challenge = crypto.getRandomValues(new Uint8Array(32));
  const userIdBytes = fromBase64url(userId);
  const rpId = options.rpId || credentialRpId(identity);

  const createOptions = {
    challenge,
    rp: {
      name: "Poweur ID",
      id: rpId,
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
    // Firefox on macOS can fail when locked to a platform authenticator.
    // Retry without that constraint, but keep requesting PRF — a credential
    // that cannot emit one is not usable here.
    try {
      const fallbackOptions = {
        ...createOptions,
        authenticatorSelection: {
          residentKey: "required",
          userVerification: "required",
        },
      };
      credential = await navigator.credentials.create({ publicKey: fallbackOptions });
    } catch (err2) {
      throw new Error(`Passkey creation failed: ${err2.message}`);
    }
  }

  const credentialId = toBase64url(new Uint8Array(credential.rawId));
  const extResults = credential.getClientExtensionResults();
  const prfFirst = extResults?.prf?.results?.first;
  const prfOutput = prfFirst ? new Uint8Array(prfFirst) : null;
  if (!prfOutput) throw new Error(PRF_UNAVAILABLE_MESSAGE);

  // SPKI DER + COSE algorithm id, which is what the relay verifies assertions
  // against. `getPublicKey()` is unavailable on older Safari; without it the
  // credential still unlocks this browser, it just cannot be enrolled in the
  // relay keystore — callers check for null rather than failing registration.
  let credentialPublicKey = null;
  let credentialAlg = null;
  try {
    const spki = credential.response.getPublicKey?.();
    if (spki) {
      credentialPublicKey = toBase64url(new Uint8Array(spki));
      credentialAlg = credential.response.getPublicKeyAlgorithm?.() ?? null;
    }
  } catch {
    /* leave null — see above */
  }

  // rpId travels back so the caller can store what this credential was
  // actually bound to; every later assertion has to ask for the same scope.
  return { credentialId, prfOutput, supportsPRF: prfOutput !== null, credentialPublicKey, credentialAlg, rpId };
}

// ─── Passkey Authentication ───────────────────────────────────────────────────

/**
 * Authenticate with a stored passkey, retrieving the PRF output for key unwrapping.
 *
 * @param {string} credentialId — base64url credential ID stored alongside identity
 * @returns {{ assertion: PublicKeyCredential, prfOutput: Uint8Array|null, supportsPRF: boolean }}
 */
export async function authenticatePasskey(credentialId, { rpId: scope = null } = {}) {
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
    // Named explicitly: a credential created with rp.id "poweur.net" is not
    // found from alice.poweur.net unless the assertion asks for that scope.
    ...(scope ? { rpId: scope } : {}),
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
export async function wrapKeysWithPRF(prfOutput, signingJWK, encJWK, seed = null) {
  const wrapped = await wrapKeysAES(prfOutput, signingJWK, encJWK, seed);
  return { ...wrapped, kdf: "prf" };
}

/**
 * Decrypt previously PRF-wrapped keys.
 * Returns { signingJWK, encJWK, seed? }
 */
export async function unwrapKeysWithPRF(prfOutput, encryptedData) {
  return unwrapKeysAES(prfOutput, encryptedData);
}

// ─── Assertions (the relay keystore's auth) ──────────────────────────────────

/**
 * Sign a relay challenge with an enrolled authenticator.
 *
 * Shaped exactly as `@poweur/client`'s `WebAuthnAssertion`. Pass no
 * `credentialId` to let the platform offer any discoverable credential — which
 * is the recovery case, where this browser has no record of which one to ask
 * for. `residentKey: "required"` at creation is what makes that work.
 *
 * @returns {{ assertion: object, prfOutput: Uint8Array|null }}
 */
export async function assertChallenge(challenge, { credentialId = null, rpId: scope = null } = {}) {
  if (!window.PublicKeyCredential) throw new Error("WebAuthn is not supported in this browser.");

  const options = {
    // The relay hashes the challenge string it issued; the browser signs over
    // clientDataJSON, which carries it base64url-encoded.
    challenge: new TextEncoder().encode(challenge),
    userVerification: "required",
    timeout: 120000,
    extensions: { prf: { eval: { first: PRF_SALT } } },
    ...(scope ? { rpId: scope } : {}),
  };
  if (credentialId) {
    options.allowCredentials = [{ type: "public-key", id: fromBase64url(credentialId) }];
  }

  let credential;
  try {
    credential = await navigator.credentials.get({ publicKey: options });
  } catch (err) {
    throw new Error(`Passkey authentication failed: ${err.message}`);
  }

  const prfFirst = credential.getClientExtensionResults()?.prf?.results?.first;
  return {
    assertion: {
      credential_id: toBase64url(new Uint8Array(credential.rawId)),
      client_data_json: toBase64url(new Uint8Array(credential.response.clientDataJSON)),
      authenticator_data: toBase64url(new Uint8Array(credential.response.authenticatorData)),
      signature: toBase64url(new Uint8Array(credential.response.signature)),
    },
    prfOutput: prfFirst ? new Uint8Array(prfFirst) : null,
  };
}

// ─── Platform Support Detection ───────────────────────────────────────────────

export async function checkPasskeySupport() {
  if (!window.PublicKeyCredential) {
    return { available: false, prf: false, reason: PRF_UNAVAILABLE_MESSAGE };
  }
  let available = false;
  try {
    available = await PublicKeyCredential.isUserVerifyingPlatformAuthenticatorAvailable();
  } catch {
    return { available: false, prf: false, reason: PRF_UNAVAILABLE_MESSAGE };
  }
  if (!available) {
    return { available: false, prf: false, reason: PRF_UNAVAILABLE_MESSAGE };
  }

  let prf = null;
  try {
    if (typeof PublicKeyCredential.getClientCapabilities === "function") {
      const caps = await PublicKeyCredential.getClientCapabilities();
      if (caps && Object.prototype.hasOwnProperty.call(caps, "extension:prf")) {
        prf = Boolean(caps["extension:prf"]);
      }
    }
  } catch { /* capabilities are advisory; create() is the authority */ }
  if (prf === false) {
    return { available: true, prf: false, reason: PRF_UNAVAILABLE_MESSAGE };
  }
  return { available: true, prf, reason: null };
}
