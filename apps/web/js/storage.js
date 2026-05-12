/**
 * Persistence layer for the Poweur ID web client.
 *
 * localStorage  — persists across sessions:  identity records, config
 * sessionStorage — cleared on tab close:      unlocked session keys
 */

const IDENTITY_PREFIX = "poweur:identity:";
const ACTIVE_KEY = "poweur:active";
const CONFIG_KEY = "poweur:config";
const SESSION_PREFIX = "poweur:session:";

// ─── Default Config ───────────────────────────────────────────────────────────

const DEFAULT_CONFIG = {
  relayUrl: window.location.origin,
  parentDomain: "poweur.net",
  dnsProvider: "cloudflare",
  dnsToken: "",
};

// ─── Config ───────────────────────────────────────────────────────────────────

export function getConfig() {
  try {
    const raw = localStorage.getItem(CONFIG_KEY);
    return raw ? { ...DEFAULT_CONFIG, ...JSON.parse(raw) } : { ...DEFAULT_CONFIG };
  } catch {
    return { ...DEFAULT_CONFIG };
  }
}

export function saveConfig(config) {
  localStorage.setItem(CONFIG_KEY, JSON.stringify(config));
}

// ─── Identity Records ─────────────────────────────────────────────────────────

/**
 * Identity record shape:
 * {
 *   identity: string,           // FQDN (alice.poweur.net)
 *   publicKey: string,          // base64url Ed25519 public key
 *   encPublicKey: string,       // base64url X25519 public key
 *   credentialId: string,       // base64url WebAuthn credential ID
 *   encryptedKeys: {            // wrapped private keys
 *     kdf: "prf" | "pbkdf2",
 *     iv: string,
 *     ciphertext: string,
 *     salt?: string,            // only for pbkdf2
 *   },
 *   relay: string,              // relay URL used at registration time
 *   userId: string,             // base64url random bytes (WebAuthn user ID)
 *   createdAt: string,          // ISO timestamp
 * }
 */
export function saveIdentityRecord(identity, record) {
  localStorage.setItem(IDENTITY_PREFIX + identity, JSON.stringify(record));
}

export function loadIdentityRecord(identity) {
  const raw = localStorage.getItem(IDENTITY_PREFIX + identity);
  return raw ? JSON.parse(raw) : null;
}

export function listIdentities() {
  const result = [];
  for (let i = 0; i < localStorage.length; i++) {
    const key = localStorage.key(i);
    if (key && key.startsWith(IDENTITY_PREFIX)) {
      result.push(key.slice(IDENTITY_PREFIX.length));
    }
  }
  return result.sort();
}

export function removeIdentity(identity) {
  localStorage.removeItem(IDENTITY_PREFIX + identity);
  if (getActiveIdentity() === identity) {
    localStorage.removeItem(ACTIVE_KEY);
  }
}

// ─── Active Identity ──────────────────────────────────────────────────────────

export function getActiveIdentity() {
  return localStorage.getItem(ACTIVE_KEY) || null;
}

export function setActiveIdentity(identity) {
  if (identity) {
    localStorage.setItem(ACTIVE_KEY, identity);
  } else {
    localStorage.removeItem(ACTIVE_KEY);
  }
}

// ─── Session (sessionStorage — ephemeral) ────────────────────────────────────

/**
 * Session record shape:
 * {
 *   sessionId: string,
 *   sessionPublicKey: string,      // base64url
 *   sessionSigningJWK: object,     // Ed25519 private key JWK (in-memory only during tab)
 *   issuedAt: string,
 *   expiresAt: string,
 * }
 */
export function saveSessionRecord(identity, record) {
  sessionStorage.setItem(SESSION_PREFIX + identity, JSON.stringify(record));
}

export function loadSessionRecord(identity) {
  const raw = sessionStorage.getItem(SESSION_PREFIX + identity);
  return raw ? JSON.parse(raw) : null;
}

export function removeSessionRecord(identity) {
  sessionStorage.removeItem(SESSION_PREFIX + identity);
}

/** Returns true if session exists and hasn't expired. */
export function isSessionValid(identity) {
  const rec = loadSessionRecord(identity);
  if (!rec || !rec.expiresAt) return false;
  return new Date(rec.expiresAt) > new Date();
}

// ─── Unlocked Keys (in-memory only) ──────────────────────────────────────────
// These live only in JS memory — never persisted to any storage.
// Stored on the module-level singleton below.

let _unlockedKeys = null; // { signingJWK, encJWK, identity }

export function setUnlockedKeys(identity, signingJWK, encJWK) {
  _unlockedKeys = { identity, signingJWK, encJWK };
}

export function getUnlockedKeys() {
  return _unlockedKeys;
}

export function clearUnlockedKeys() {
  _unlockedKeys = null;
}
