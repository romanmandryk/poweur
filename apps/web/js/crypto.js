/**
 * Cryptographic primitives for the Poweur ID web client.
 *
 * Uses Web Crypto API (Ed25519, X25519, HKDF, AES-GCM) plus
 * @noble/ciphers for ChaCha20-Poly1305 (not in Web Crypto).
 *
 * Key encoding: base64url without padding throughout, matching the relay.
 */

import { chacha20poly1305 } from "https://esm.sh/@noble/ciphers@1.1.3/chacha";
import { randomBytes as nobleRandomBytes } from "https://esm.sh/@noble/ciphers@1.1.3/webcrypto";

// ─── Helpers ─────────────────────────────────────────────────────────────────

export function toBase64url(bytes) {
  return btoa(String.fromCharCode(...bytes))
    .replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

export function fromBase64url(str) {
  // Accept padded or unpadded, standard or URL-safe base64.
  const s = str.replace(/-/g, "+").replace(/_/g, "/");
  const padded = s.padEnd(s.length + (4 - (s.length % 4)) % 4, "=");
  return Uint8Array.from(atob(padded), c => c.charCodeAt(0));
}

function toBase64(bytes) {
  return btoa(String.fromCharCode(...bytes));
}

function concatBytes(...arrays) {
  const total = arrays.reduce((n, a) => n + a.length, 0);
  const out = new Uint8Array(total);
  let offset = 0;
  for (const a of arrays) { out.set(a, offset); offset += a.length; }
  return out;
}

const enc = new TextEncoder();

// ─── Ed25519 Signing ──────────────────────────────────────────────────────────

/**
 * Generate an Ed25519 signing keypair.
 * Returns { publicKeyBytes: Uint8Array, privateKeyJWK: object }
 */
export async function generateSigningKeypair() {
  const kp = await crypto.subtle.generateKey({ name: "Ed25519" }, true, ["sign", "verify"]);
  const publicKeyBytes = new Uint8Array(await crypto.subtle.exportKey("raw", kp.publicKey));
  const privateKeyJWK = await crypto.subtle.exportKey("jwk", kp.privateKey);
  return { publicKeyBytes, privateKeyJWK };
}

/**
 * Sign a canonical string with an Ed25519 private key.
 * privateKeyJWK: JWK object (from generateSigningKeypair or unwrapped storage)
 * Returns base64url signature string.
 */
export async function sign(privateKeyJWK, canonicalString) {
  const key = await crypto.subtle.importKey(
    "jwk", privateKeyJWK, { name: "Ed25519" }, false, ["sign"]
  );
  const sig = await crypto.subtle.sign({ name: "Ed25519" }, key, enc.encode(canonicalString));
  return toBase64url(new Uint8Array(sig));
}

/**
 * Import a raw Ed25519 public key (32 bytes or base64url string).
 */
async function importEd25519Public(keyBytesOrStr) {
  const bytes = typeof keyBytesOrStr === "string" ? fromBase64url(keyBytesOrStr) : keyBytesOrStr;
  return crypto.subtle.importKey("raw", bytes, { name: "Ed25519" }, false, ["verify"]);
}

// ─── X25519 Encryption Keys ───────────────────────────────────────────────────

/**
 * Generate an X25519 keypair for encryption.
 * Returns { publicKeyBytes: Uint8Array, privateKeyJWK: object }
 */
export async function generateEncryptionKeypair() {
  const kp = await crypto.subtle.generateKey(
    { name: "X25519" }, true, ["deriveBits"]
  );
  const publicKeyBytes = new Uint8Array(await crypto.subtle.exportKey("raw", kp.publicKey));
  const privateKeyJWK = await crypto.subtle.exportKey("jwk", kp.privateKey);
  return { publicKeyBytes, privateKeyJWK };
}

// ─── HKDF ─────────────────────────────────────────────────────────────────────

async function hkdf(ikm, salt, info) {
  const keyMaterial = await crypto.subtle.importKey("raw", ikm, "HKDF", false, ["deriveBits"]);
  const bits = await crypto.subtle.deriveBits(
    { name: "HKDF", hash: "SHA-256", salt, info: enc.encode(info) },
    keyMaterial, 256
  );
  return new Uint8Array(bits);
}

// ─── X25519 + HKDF + ChaCha20-Poly1305 ───────────────────────────────────────

/**
 * Encrypt plaintext (string) for a recipient's X25519 public key.
 * recipientEncPubKey: base64url string or Uint8Array (32 bytes)
 * Returns { ciphertext, ephemeralPublicKey, nonce } — all base64url strings.
 */
export async function encryptMessage(plaintext, recipientEncPubKey) {
  const recipientPubBytes = typeof recipientEncPubKey === "string"
    ? fromBase64url(recipientEncPubKey) : recipientEncPubKey;

  // Generate ephemeral X25519 keypair
  const ephKP = await crypto.subtle.generateKey(
    { name: "X25519" }, true, ["deriveBits"]
  );
  const ephPubBytes = new Uint8Array(await crypto.subtle.exportKey("raw", ephKP.publicKey));

  // Import recipient's public key
  const recipientKey = await crypto.subtle.importKey(
    "raw", recipientPubBytes, { name: "X25519" }, false, []
  );

  // X25519 shared secret
  const sharedBits = new Uint8Array(await crypto.subtle.deriveBits(
    { name: "X25519", public: recipientKey }, ephKP.privateKey, 256
  ));

  // HKDF-SHA256: salt = ephPub || recipientPub, info = "poweur/msg/v1"
  const salt = concatBytes(ephPubBytes, recipientPubBytes);
  const key = await hkdf(sharedBits, salt, "poweur/msg/v1");

  // ChaCha20-Poly1305 AEAD
  const nonce = nobleRandomBytes(12);
  const aad = concatBytes(enc.encode("poweur/msg/v1\n"), ephPubBytes, recipientPubBytes);
  const chacha = chacha20poly1305(key, nonce, aad);
  const ciphertext = chacha.encrypt(enc.encode(plaintext));

  return {
    ciphertext: toBase64url(ciphertext),
    ephemeralPublicKey: toBase64url(ephPubBytes),
    nonce: toBase64url(nonce),
  };
}

/**
 * Decrypt a message encrypted with encryptMessage().
 * myEncPrivKeyJWK: JWK of my X25519 private key (must have `x` public key field).
 * Returns plaintext string.
 */
export async function decryptMessage(myEncPrivKeyJWK, { ciphertext, ephemeralPublicKey, nonce }) {
  const ephPubBytes = fromBase64url(ephemeralPublicKey);
  const nonceBytes  = fromBase64url(nonce);
  const ciphertextBytes = fromBase64url(ciphertext);

  // The JWK `x` field IS the X25519 public key (base64url, 32 bytes).
  const myPubRaw = fromBase64url(myEncPrivKeyJWK.x);

  // Import private key for X25519
  const myPrivKey = await crypto.subtle.importKey(
    "jwk", myEncPrivKeyJWK, { name: "X25519" }, false, ["deriveBits"]
  );

  // Import ephemeral public key (public-only, no key_ops needed)
  const ephKey = await crypto.subtle.importKey(
    "raw", ephPubBytes, { name: "X25519" }, false, []
  );

  // X25519 shared secret
  const sharedBits = new Uint8Array(
    await crypto.subtle.deriveBits({ name: "X25519", public: ephKey }, myPrivKey, 256)
  );

  // HKDF-SHA256: salt = ephPub || myPub, info = "poweur/msg/v1"
  const salt = concatBytes(ephPubBytes, myPubRaw);
  const key  = await hkdf(sharedBits, salt, "poweur/msg/v1");

  // ChaCha20-Poly1305 decrypt — AAD matches encryption side exactly
  const aad = concatBytes(enc.encode("poweur/msg/v1\n"), ephPubBytes, myPubRaw);
  const chacha = chacha20poly1305(key, nonceBytes, aad);
  const plaintext = chacha.decrypt(ciphertextBytes);
  return new TextDecoder().decode(plaintext);
}

// ─── AES-GCM Key Wrapping (for passkey PRF) ───────────────────────────────────

/**
 * Wrap signing + encryption private key JWKs using an AES-GCM key derived
 * from the PRF output (or any 32-byte secret).
 * Returns { iv: base64url, ciphertext: base64url }
 */
export async function wrapKeysAES(secret32Bytes, signingJWK, encJWK) {
  const keyMaterial = await crypto.subtle.importKey("raw", secret32Bytes, "HKDF", false, ["deriveKey"]);
  const wrappingKey = await crypto.subtle.deriveKey(
    { name: "HKDF", hash: "SHA-256", salt: enc.encode("poweur-key-wrapping-v1"), info: new Uint8Array() },
    keyMaterial,
    { name: "AES-GCM", length: 256 },
    false, ["encrypt", "decrypt"]
  );
  const iv = crypto.getRandomValues(new Uint8Array(12));
  const payload = enc.encode(JSON.stringify({ signingJWK, encJWK }));
  const ciphertext = await crypto.subtle.encrypt({ name: "AES-GCM", iv }, wrappingKey, payload);
  return { iv: toBase64url(iv), ciphertext: toBase64url(new Uint8Array(ciphertext)) };
}

/**
 * Unwrap keys previously wrapped by wrapKeysAES.
 * Returns { signingJWK, encJWK }
 */
export async function unwrapKeysAES(secret32Bytes, { iv, ciphertext }) {
  const keyMaterial = await crypto.subtle.importKey("raw", secret32Bytes, "HKDF", false, ["deriveKey"]);
  const wrappingKey = await crypto.subtle.deriveKey(
    { name: "HKDF", hash: "SHA-256", salt: enc.encode("poweur-key-wrapping-v1"), info: new Uint8Array() },
    keyMaterial,
    { name: "AES-GCM", length: 256 },
    false, ["encrypt", "decrypt"]
  );
  const plaintext = await crypto.subtle.decrypt(
    { name: "AES-GCM", iv: fromBase64url(iv) },
    wrappingKey,
    fromBase64url(ciphertext)
  );
  return JSON.parse(new TextDecoder().decode(plaintext));
}

/**
 * Derive an AES wrapping key from a PIN using PBKDF2.
 */
export async function wrapKeysWithPin(pin, signingJWK, encJWK) {
  const pinMaterial = await crypto.subtle.importKey("raw", enc.encode(pin), "PBKDF2", false, ["deriveKey"]);
  const salt = crypto.getRandomValues(new Uint8Array(16));
  const wrappingKey = await crypto.subtle.deriveKey(
    { name: "PBKDF2", hash: "SHA-256", salt, iterations: 300000 },
    pinMaterial,
    { name: "AES-GCM", length: 256 },
    false, ["encrypt", "decrypt"]
  );
  const iv = crypto.getRandomValues(new Uint8Array(12));
  const payload = enc.encode(JSON.stringify({ signingJWK, encJWK }));
  const ciphertext = await crypto.subtle.encrypt({ name: "AES-GCM", iv }, wrappingKey, payload);
  return {
    iv: toBase64url(iv),
    salt: toBase64url(salt),
    ciphertext: toBase64url(new Uint8Array(ciphertext)),
    kdf: "pbkdf2",
  };
}

export async function unwrapKeysWithPin(pin, { iv, salt, ciphertext }) {
  const pinMaterial = await crypto.subtle.importKey("raw", enc.encode(pin), "PBKDF2", false, ["deriveKey"]);
  const wrappingKey = await crypto.subtle.deriveKey(
    { name: "PBKDF2", hash: "SHA-256", salt: fromBase64url(salt), iterations: 300000 },
    pinMaterial,
    { name: "AES-GCM", length: 256 },
    false, ["encrypt", "decrypt"]
  );
  const plaintext = await crypto.subtle.decrypt(
    { name: "AES-GCM", iv: fromBase64url(iv) },
    wrappingKey,
    fromBase64url(ciphertext)
  );
  return JSON.parse(new TextDecoder().decode(plaintext));
}

// ─── Canonical String Builders ────────────────────────────────────────────────

export function canonicalMessage(sender, recipient, timestamp, payload, id, sessionID, enc_) {
  const parts = [sender, recipient, timestamp, payload];
  if (id) parts.push("id:" + id);
  if (sessionID) parts.push("session:" + sessionID);
  if (enc_ && enc_.alg) parts.push(`enc:${enc_.alg}:${enc_.ephemeralPublicKey}:${enc_.nonce}`);
  return parts.join("\n");
}

export function canonicalAck(id, messageID, state, sender, recipient, timestamp, sessionID) {
  const parts = ["ack", id, messageID, state, sender, recipient, timestamp];
  if (sessionID) parts.push("session:" + sessionID);
  return parts.join("\n");
}

export function canonicalIdentityRegistration(identity, publicKey, encPublicKey, relayAddress, issuedAt, nonce) {
  return ["identity-registration", identity, publicKey, encPublicKey, relayAddress, issuedAt, nonce].join("\n");
}

/** Build and sign a v1 identity document (EPIC-001). */
export async function buildSignedIdentityDocument(signingJWK, { identity, publicKey, encPublicKey, relay, updatedAt }) {
  const doc = {
    version: 1,
    identity,
    public_key: publicKey.startsWith("ed25519:") ? publicKey : `ed25519:${publicKey}`,
    encryption_public_key: encPublicKey
      ? (encPublicKey.startsWith("x25519:") ? encPublicKey : `x25519:${encPublicKey}`)
      : undefined,
    relay,
    capabilities: ["messaging"],
    updated_at: updatedAt,
  };
  // Canonical JSON: sorted keys, no signature, no undefined
  const canonObj = {};
  for (const k of Object.keys(doc).filter(k => doc[k] !== undefined).sort()) {
    canonObj[k] = doc[k];
  }
  const canon = JSON.stringify(canonObj);
  const signature = await sign(signingJWK, canon);
  return { ...doc, signature };
}

export function canonicalSessionRegistration(identity, sessionPublicKey, issuedAt, expiresAt, nonce) {
  return ["session-registration", identity, sessionPublicKey, issuedAt, expiresAt, nonce].join("\n");
}

export function canonicalSessionRevocation(identity, sessionID, issuedAt, nonce) {
  return ["session-revocation", identity, sessionID, issuedAt, nonce].join("\n");
}

// ─── Misc ─────────────────────────────────────────────────────────────────────

/** Generate a ULID-style message ID (timestamp + random). */
export function generateMessageId() {
  const ts = Date.now().toString(36).toUpperCase().padStart(10, "0");
  const rnd = toBase64url(crypto.getRandomValues(new Uint8Array(10)))
    .toUpperCase().replace(/[^A-Z0-9]/g, "").substring(0, 16);
  return ts + rnd;
}

/** Current UTC timestamp in RFC 3339 format. */
export function now() {
  return new Date().toISOString().replace(/\.\d{3}Z$/, "Z");
}

/** Random base64url nonce (32 bytes). */
export function randomNonce() {
  return toBase64url(crypto.getRandomValues(new Uint8Array(32)));
}
