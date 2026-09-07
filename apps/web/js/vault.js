/**
 * Key custody for the browser client.
 *
 * This is the half of the old `js/crypto.js` that stays web-owned: the
 * protocol moved to `@poweur/client` (EPIC-015 E15-T6), but *how a browser
 * holds a private key* is a web-app concern — passkey PRF or a PIN, wrapped
 * with AES-GCM in `localStorage`.
 *
 * The wrapped blob format is unchanged from the shipped client on purpose:
 * EPIC-011's multi-enrollment work re-wraps the same payload under additional
 * authenticators, and existing identities in existing browsers must keep
 * opening. HKDF salt `poweur-key-wrapping-v1`, AES-256-GCM, payload
 * `{"signingJWK":…,"encJWK":…}`.
 *
 * Keys are stored as WebCrypto JWKs and signing happens inside WebCrypto, so
 * the raw Ed25519 private key never crosses into package code. The X25519 key
 * does — decryption is the SDK's job — and that is the one deliberate
 * exception.
 */

import { toBase64url, fromBase64, toBase64Std } from "@poweur/client";

const enc = new TextEncoder();

const WRAP_SALT = enc.encode("poweur-key-wrapping-v1");
const PIN_ITERATIONS = 300_000;

// ─── Encoding ─────────────────────────────────────────────────────────────────

/** `fromBase64` accepts padded/unpadded and both alphabets; re-export as one name. */
export const fromBase64url = fromBase64;
export { toBase64url };

// ─── Key generation & conversion ──────────────────────────────────────────────

/**
 * Generate a fresh identity keypair as WebCrypto JWKs.
 *
 * Deliberately WebCrypto rather than the SDK's `generateIdentityKeys()`: the
 * signing key is then non-extractable-by-default in shape and, more
 * importantly, the stored JWK format matches every identity already in a
 * user's browser.
 */
export async function generateIdentityJwks() {
  const signing = await crypto.subtle.generateKey({ name: "Ed25519" }, true, ["sign", "verify"]);
  const encryption = await crypto.subtle.generateKey({ name: "X25519" }, true, ["deriveBits"]);
  const signingJWK = await crypto.subtle.exportKey("jwk", signing.privateKey);
  const encJWK = await crypto.subtle.exportKey("jwk", encryption.privateKey);
  return {
    signingJWK,
    encJWK,
    publicKey: toBase64url(new Uint8Array(await crypto.subtle.exportKey("raw", signing.publicKey))),
    encPublicKey: toBase64url(new Uint8Array(await crypto.subtle.exportKey("raw", encryption.publicKey))),
  };
}

/** A fresh X25519 keypair on its own — the encryption-key rotation path. */
export async function generateEncryptionJwk() {
  const kp = await crypto.subtle.generateKey({ name: "X25519" }, true, ["deriveBits"]);
  return {
    encJWK: await crypto.subtle.exportKey("jwk", kp.privateKey),
    encPublicKey: toBase64url(new Uint8Array(await crypto.subtle.exportKey("raw", kp.publicKey))),
  };
}

/**
 * Raw key bytes → the JWK pair this client stores.
 *
 * The entry point for anything that starts from bytes rather than from
 * WebCrypto: seed recovery (`identityKeysFromSeed`) and, later, EPIC-011's
 * keystore fetch.
 */
export function jwksFromKeyBytes({ signingPrivateKey, signingPublicKey, encryptionPrivateKey, encryptionPublicKey }) {
  return {
    signingJWK: {
      kty: "OKP",
      crv: "Ed25519",
      // Go stores seed||public; WebCrypto wants the 32-byte seed alone.
      d: toBase64url(signingPrivateKey.slice(0, 32)),
      x: toBase64url(signingPublicKey),
      key_ops: ["sign"],
      ext: true,
    },
    encJWK: encryptionPrivateKey
      ? {
          kty: "OKP",
          crv: "X25519",
          d: toBase64url(encryptionPrivateKey),
          x: toBase64url(encryptionPublicKey),
          key_ops: ["deriveBits"],
          ext: true,
        }
      : null,
  };
}

/**
 * The JWK pair this client stores → the raw bytes `@poweur/client` works in.
 *
 * The inverse of `jwksFromKeyBytes`. Both directions clamp identically
 * (WebCrypto and @noble follow RFC 7748), which `test/vault.test.js` pins.
 */
export function keyBytesFromJwks(identity, signingJWK, encJWK) {
  return {
    identity,
    signingPrivateKey: fromBase64url(signingJWK.d),
    ...(encJWK ? { encryptionPrivateKey: fromBase64url(encJWK.d) } : {}),
  };
}

/** The bare base64url public key a JWK carries (its `x` parameter). */
export function publicKeyFromJwk(jwk) {
  return jwk?.x ?? "";
}

// ─── Signer / Decryptor (the `@poweur/client` seam) ───────────────────────────

/**
 * A `Signer` whose private key lives in WebCrypto, not in package memory.
 *
 * `encoding` mirrors the Go CLI: standard base64 for messages, acks and
 * session proofs, base64url for documents and grants. The SDK asks for the
 * one it needs per call site.
 */
export class WebCryptoSigner {
  #jwk;
  #key = null;

  constructor(identity, signingJWK) {
    this.identity = identity;
    this.publicKey = `ed25519:${publicKeyFromJwk(signingJWK)}`;
    this.#jwk = signingJWK;
  }

  async #cryptoKey() {
    this.#key ??= await crypto.subtle.importKey("jwk", this.#jwk, { name: "Ed25519" }, false, ["sign"]);
    return this.#key;
  }

  async sign(canonical, encoding = "base64url") {
    const signature = new Uint8Array(
      await crypto.subtle.sign({ name: "Ed25519" }, await this.#cryptoKey(), enc.encode(canonical)),
    );
    return encoding === "base64std" ? toBase64Std(signature) : toBase64url(signature);
  }
}

/**
 * A `Decryptor` over a stored X25519 JWK.
 *
 * Unlike signing, this hands raw bytes to the package — message decryption is
 * X25519 + HKDF + ChaCha20-Poly1305 and WebCrypto has no ChaCha. The key is
 * still only reachable while the identity is unlocked.
 */
export class JwkDecryptor {
  #jwk;

  constructor(encJWK) {
    this.encryptionPublicKey = `x25519:${publicKeyFromJwk(encJWK)}`;
    this.#jwk = encJWK;
  }

  async privateKeyBytes() {
    return fromBase64url(this.#jwk.d);
  }
}

// ─── Wrapping ─────────────────────────────────────────────────────────────────

async function aesKeyFromSecret(secret32Bytes) {
  const material = await crypto.subtle.importKey("raw", secret32Bytes, "HKDF", false, ["deriveKey"]);
  return crypto.subtle.deriveKey(
    { name: "HKDF", hash: "SHA-256", salt: WRAP_SALT, info: new Uint8Array() },
    material,
    { name: "AES-GCM", length: 256 },
    false,
    ["encrypt", "decrypt"],
  );
}

async function aesKeyFromPin(pin, salt) {
  const material = await crypto.subtle.importKey("raw", enc.encode(pin), "PBKDF2", false, ["deriveKey"]);
  return crypto.subtle.deriveKey(
    { name: "PBKDF2", hash: "SHA-256", salt, iterations: PIN_ITERATIONS },
    material,
    { name: "AES-GCM", length: 256 },
    false,
    ["encrypt", "decrypt"],
  );
}

async function seal(key, signingJWK, encJWK) {
  const iv = crypto.getRandomValues(new Uint8Array(12));
  const payload = enc.encode(JSON.stringify({ signingJWK, encJWK }));
  const ciphertext = await crypto.subtle.encrypt({ name: "AES-GCM", iv }, key, payload);
  return { iv: toBase64url(iv), ciphertext: toBase64url(new Uint8Array(ciphertext)) };
}

async function unseal(key, { iv, ciphertext }) {
  const plaintext = await crypto.subtle.decrypt(
    { name: "AES-GCM", iv: fromBase64url(iv) },
    key,
    fromBase64url(ciphertext),
  );
  return JSON.parse(new TextDecoder().decode(plaintext));
}

/** Wrap under a 32-byte secret — the passkey PRF output. */
export async function wrapKeysAES(secret32Bytes, signingJWK, encJWK) {
  return seal(await aesKeyFromSecret(secret32Bytes), signingJWK, encJWK);
}

export async function unwrapKeysAES(secret32Bytes, wrapped) {
  return unseal(await aesKeyFromSecret(secret32Bytes), wrapped);
}

/** Wrap under a user PIN (PBKDF2) — the fallback when PRF is unavailable. */
export async function wrapKeysWithPin(pin, signingJWK, encJWK) {
  const salt = crypto.getRandomValues(new Uint8Array(16));
  const sealed = await seal(await aesKeyFromPin(pin, salt), signingJWK, encJWK);
  return { ...sealed, salt: toBase64url(salt), kdf: "pbkdf2" };
}

export async function unwrapKeysWithPin(pin, wrapped) {
  return unseal(await aesKeyFromPin(pin, fromBase64url(wrapped.salt)), wrapped);
}
