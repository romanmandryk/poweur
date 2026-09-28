/**
 * Key custody for the browser client.
 *
 * This is the half of the old `js/crypto.js` that stays web-owned: the
 * protocol moved to `@poweur/client` (EPIC-015 E15-T6), but *how a browser
 * holds a private key* is a web-app concern — passkey PRF (or a native
 * keystore on the mobile shell), wrapped with AES-GCM in `localStorage`.
 * A user PIN is not a wrapping secret; authenticators without PRF are refused.
 *
 * The wrapped blob format is the shipped one, extended additively: HKDF salt
 * `poweur-key-wrapping-v1`, AES-256-GCM, payload
 * `{"signingJWK":…,"encJWK":…,"seed":…}`. EPIC-011 re-wraps this same payload
 * under each additional authenticator, and blobs written before `seed` existed
 * still open — they simply have no kit (see `keystore.js`).
 *
 * Keys are stored as WebCrypto JWKs and signing happens inside WebCrypto, so
 * the raw Ed25519 private key never crosses into package code. The X25519 key
 * does — decryption is the SDK's job — and that is the one deliberate
 * exception.
 */

import {
  toBase64url, fromBase64, toBase64Std,
  crypto as sdkCrypto,
} from "@poweur/client";

const { newSeed, deriveSigningKey, deriveEncryptionKey } = sdkCrypto;

const enc = new TextEncoder();

const WRAP_SALT = enc.encode("poweur-key-wrapping-v1");

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
    seed: null, // two independent keys: no seed, so no recovery kit
    publicKey: toBase64url(new Uint8Array(await crypto.subtle.exportKey("raw", signing.publicKey))),
    encPublicKey: toBase64url(new Uint8Array(await crypto.subtle.exportKey("raw", encryption.publicKey))),
  };
}

/**
 * Generate a **seed-derived** identity: one 32-byte secret, both keys derived
 * from it (EPIC-011's normative HKDF), and the seed kept so a recovery kit can
 * be produced later.
 *
 * This is what new identities use. The alternative — two independent keys —
 * cannot produce a 24-word kit, which is why EPIC-011 has a migration path for
 * everything registered before it.
 */
export async function generateSeedIdentityJwks() {
  const seed = newSeed();
  return { ...jwksFromSeed(seed), seed: toBase64url(seed) };
}

/** Derive both key JWKs and their public keys from a master seed. */
export function jwksFromSeed(seed) {
  const signing = deriveSigningKey(seed);
  const encryption = deriveEncryptionKey(seed);
  const { signingJWK, encJWK } = jwksFromKeyBytes({
    signingPrivateKey: signing.privateKey,
    signingPublicKey: signing.publicKey,
    encryptionPrivateKey: encryption.privateKey,
    encryptionPublicKey: encryption.publicKey,
  });
  return {
    signingJWK,
    encJWK,
    publicKey: toBase64url(signing.publicKey),
    encPublicKey: toBase64url(encryption.publicKey),
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
    const signature = await this.signBytes(enc.encode(canonical));
    return encoding === "base64std" ? toBase64Std(signature) : toBase64url(signature);
  }

  /** Drive manifests are binary, so they cannot go through the UTF-8 string signer. */
  async signBytes(data) {
    return new Uint8Array(await crypto.subtle.sign({ name: "Ed25519" }, await this.#cryptoKey(), data));
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

async function seal(key, signingJWK, encJWK, seed) {
  const iv = crypto.getRandomValues(new Uint8Array(12));
  const payload = enc.encode(JSON.stringify({ signingJWK, encJWK, ...(seed ? { seed } : {}) }));
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
export async function wrapKeysAES(secret32Bytes, signingJWK, encJWK, seed = null) {
  return seal(await aesKeyFromSecret(secret32Bytes), signingJWK, encJWK, seed);
}

export async function unwrapKeysAES(secret32Bytes, wrapped) {
  return unseal(await aesKeyFromSecret(secret32Bytes), wrapped);
}
