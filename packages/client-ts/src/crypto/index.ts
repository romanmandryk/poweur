/**
 * Cryptographic primitives — the TypeScript twin of
 * `apps/cli/internal/crypto/encryption.go`.
 *
 * Ed25519, X25519, HKDF-SHA256 and ChaCha20-Poly1305 come from @noble rather
 * than WebCrypto: WebCrypto's Ed25519/X25519 support is absent on Node 18 and
 * Safari < 17, and this package promises the same behaviour on every runtime.
 * SHA-256 for proof-of-work uses WebCrypto where it exists (it is the hot
 * loop) and falls back to noble.
 */

import { chacha20poly1305 } from "@noble/ciphers/chacha.js";
import { ed25519, x25519 } from "@noble/curves/ed25519.js";
import { hkdf } from "@noble/hashes/hkdf.js";
import { sha256 } from "@noble/hashes/sha2.js";

import {
  concatBytes,
  fromBase64,
  fromUtf8,
  randomBytes,
  toBase64url,
  utf8,
} from "../encoding.js";
import { PoweurError } from "../errors.js";
import { ENCRYPTION_ALG, type EncryptionMeta } from "../types.js";

export { ENCRYPTION_ALG };

/** HKDF info and AAD prefix — must match Go byte for byte. */
const KDF_INFO = "poweur/msg/v1";
export type SealDomain = "poweur/msg/v1" | "poweur/drive/seal/v1" | "poweur/drive/name/v1" | "poweur/drive/record/v1" | "poweur/group/key/v1";

function validateSealDomain(domain: SealDomain, context: Uint8Array): void {
  if (domain === "poweur/msg/v1") {
    if (context.length !== 0) throw new PoweurError("invalid_argument", "message seals do not accept extra context");
  } else if (["poweur/drive/seal/v1", "poweur/drive/name/v1", "poweur/drive/record/v1", "poweur/group/key/v1"].includes(domain)) {
    if (context.length === 0) throw new PoweurError("invalid_argument", "drive seals require context");
  } else {
    throw new PoweurError("invalid_argument", "unsupported seal domain");
  }
}

// ── Ed25519 ──────────────────────────────────────────────────────────────────

export interface SigningKeypair {
  /** 32-byte Ed25519 seed (Go calls the 64-byte seed||public form the "private key"). */
  privateKey: Uint8Array;
  publicKey: Uint8Array;
}

export function generateSigningKeypair(): SigningKeypair {
  const privateKey = ed25519.utils.randomSecretKey();
  return { privateKey, publicKey: ed25519.getPublicKey(privateKey) };
}

/**
 * Accept either the 32-byte seed or Go's 64-byte `seed||public` encoding and
 * return the 32-byte seed noble expects. `~/.poweur/keys/<id>.key` holds the
 * 64-byte form, so CLI parity depends on this.
 */
export function normalizeEd25519Seed(privateKey: Uint8Array): Uint8Array {
  if (privateKey.length === 32) return privateKey;
  if (privateKey.length === 64) return privateKey.subarray(0, 32);
  throw new PoweurError("invalid_argument", `invalid ed25519 private key size ${privateKey.length}`);
}

/** Go's ed25519.PrivateKey wire form: 64 bytes of seed||public. */
export function expandEd25519PrivateKey(privateKey: Uint8Array): Uint8Array {
  const seed = normalizeEd25519Seed(privateKey);
  return concatBytes(seed, ed25519.getPublicKey(seed));
}

export function ed25519PublicKey(privateKey: Uint8Array): Uint8Array {
  return ed25519.getPublicKey(normalizeEd25519Seed(privateKey));
}

export function signBytes(privateKey: Uint8Array, message: Uint8Array): Uint8Array {
  return ed25519.sign(message, normalizeEd25519Seed(privateKey));
}

export function verifyBytes(
  publicKey: Uint8Array,
  message: Uint8Array,
  signature: Uint8Array,
): boolean {
  try {
    return ed25519.verify(signature, message, publicKey);
  } catch {
    return false;
  }
}

/** Sign a canonical string, returning base64url (no padding). */
export function signCanonical(privateKey: Uint8Array, canonical: string): string {
  return toBase64url(signBytes(privateKey, utf8(canonical)));
}

export function verifyCanonical(
  publicKey: Uint8Array,
  canonical: string,
  signature: string,
): boolean {
  let sig: Uint8Array;
  try {
    sig = fromBase64(signature);
  } catch {
    return false;
  }
  if (sig.length !== 64) return false;
  return verifyBytes(publicKey, utf8(canonical), sig);
}

/** Parse `ed25519:<b64>` or a bare base64 key into 32 raw bytes. */
export function parseEd25519PublicKey(value: string): Uint8Array {
  const raw = fromBase64(value.trim().replace(/^ed25519:/, ""));
  if (raw.length !== 32) {
    throw new PoweurError("invalid_argument", "invalid ed25519 public key length");
  }
  return raw;
}

/** Parse `x25519:<b64>` or a bare base64 key into 32 raw bytes. */
export function parseX25519PublicKey(value: string): Uint8Array {
  const raw = fromBase64(value.trim().replace(/^x25519:/, ""));
  if (raw.length !== 32) {
    throw new PoweurError("invalid_argument", "invalid x25519 public key length");
  }
  return raw;
}

// ── X25519 ───────────────────────────────────────────────────────────────────

export interface EncryptionKeypair {
  privateKey: Uint8Array;
  publicKey: Uint8Array;
}

export function generateEncryptionKeypair(): EncryptionKeypair {
  const privateKey = randomBytes(32);
  return { privateKey, publicKey: x25519.getPublicKey(privateKey) };
}

export function x25519PublicKey(privateKey: Uint8Array): Uint8Array {
  if (privateKey.length !== 32) {
    throw new PoweurError("invalid_argument", "x25519 private key must be 32 bytes");
  }
  return x25519.getPublicKey(privateKey);
}

// ── Sealed message payloads ──────────────────────────────────────────────────

export interface SealedPayload {
  ciphertext: string;
  ephemeralPublicKey: string;
  nonce: string;
}

function deriveKey(shared: Uint8Array, ephemeralPub: Uint8Array, recipientPub: Uint8Array, domain: SealDomain) {
  const salt = concatBytes(ephemeralPub, recipientPub);
  return hkdf(sha256, shared, salt, utf8(domain), 32);
}

function buildAad(ephemeralPub: Uint8Array, recipientPub: Uint8Array, domain: SealDomain, context: Uint8Array) {
  return concatBytes(utf8(domain + "\n"), ephemeralPub, recipientPub, context);
}

/**
 * Seal plaintext for a recipient's X25519 public key: ephemeral ECDH →
 * HKDF-SHA256 → ChaCha20-Poly1305. All outputs are base64url.
 */
export function seal(
  recipientPublicKey: Uint8Array | string,
  plaintext: Uint8Array | string,
): SealedPayload {
  return sealWithDomain(recipientPublicKey, plaintext, KDF_INFO, new Uint8Array());
}

/** Shared construction; drive callers must supply a canonical, nonempty context. */
export function sealWithDomain(
  recipientPublicKey: Uint8Array | string,
  plaintext: Uint8Array | string,
  domain: SealDomain,
  context: Uint8Array,
): SealedPayload {
  validateSealDomain(domain, context);
  const recipientPub =
    typeof recipientPublicKey === "string"
      ? parseX25519PublicKey(recipientPublicKey)
      : recipientPublicKey;
  if (recipientPub.length !== 32) {
    throw new PoweurError("invalid_argument", "recipient public key must be 32 bytes");
  }
  const ephemeralPriv = randomBytes(32);
  const ephemeralPub = x25519.getPublicKey(ephemeralPriv);
  const shared = x25519.getSharedSecret(ephemeralPriv, recipientPub);
  const key = deriveKey(shared, ephemeralPub, recipientPub, domain);
  const nonce = randomBytes(12);
  const aead = chacha20poly1305(key, nonce, buildAad(ephemeralPub, recipientPub, domain, context));
  const message = typeof plaintext === "string" ? utf8(plaintext) : plaintext;
  return {
    ciphertext: toBase64url(aead.encrypt(message)),
    ephemeralPublicKey: toBase64url(ephemeralPub),
    nonce: toBase64url(nonce),
  };
}

/** Open a payload sealed by `seal`, returning raw bytes. */
export function open(recipientPrivateKey: Uint8Array, payload: SealedPayload): Uint8Array {
  return openWithDomain(recipientPrivateKey, payload, KDF_INFO, new Uint8Array());
}

export function openWithDomain(recipientPrivateKey: Uint8Array, payload: SealedPayload, domain: SealDomain, context: Uint8Array): Uint8Array {
  validateSealDomain(domain, context);
  if (recipientPrivateKey.length !== 32) {
    throw new PoweurError("invalid_argument", "recipient private key must be 32 bytes");
  }
  let ephemeralPub: Uint8Array;
  let nonce: Uint8Array;
  let ciphertext: Uint8Array;
  try {
    ephemeralPub = fromBase64(payload.ephemeralPublicKey);
    nonce = fromBase64(payload.nonce);
    ciphertext = fromBase64(payload.ciphertext);
  } catch (cause) {
    throw new PoweurError("decrypt_failed", "malformed encrypted payload", { cause });
  }
  if (ephemeralPub.length !== 32) {
    throw new PoweurError("decrypt_failed", "ephemeral public key must be 32 bytes");
  }
  if (nonce.length !== 12) throw new PoweurError("decrypt_failed", "nonce must be 12 bytes");
  const recipientPub = x25519.getPublicKey(recipientPrivateKey);
  const shared = x25519.getSharedSecret(recipientPrivateKey, ephemeralPub);
  const key = deriveKey(shared, ephemeralPub, recipientPub, domain);
  const aead = chacha20poly1305(key, nonce, buildAad(ephemeralPub, recipientPub, domain, context));
  try {
    return aead.decrypt(ciphertext);
  } catch (cause) {
    throw new PoweurError("decrypt_failed", "message authentication failed", { cause });
  }
}

/** Convenience: seal a UTF-8 string and return the wire-shaped envelope. */
export function encryptMessage(
  recipientPublicKey: Uint8Array | string,
  plaintext: string,
): { payload: string; encryption: EncryptionMeta } {
  const sealed = seal(recipientPublicKey, plaintext);
  return {
    payload: sealed.ciphertext,
    encryption: {
      alg: ENCRYPTION_ALG,
      ephemeral_public_key: sealed.ephemeralPublicKey,
      nonce: sealed.nonce,
    },
  };
}

/** Convenience: open a wire envelope back into a UTF-8 string. */
export function decryptMessage(
  recipientPrivateKey: Uint8Array,
  payload: string,
  encryption: EncryptionMeta,
): string {
  return fromUtf8(
    open(recipientPrivateKey, {
      ciphertext: payload,
      ephemeralPublicKey: encryption.ephemeral_public_key,
      nonce: encryption.nonce,
    }),
  );
}

// ── Hashing ──────────────────────────────────────────────────────────────────

/** SHA-256 over bytes — noble, synchronous, used by the PoW solver. */
export function sha256Bytes(data: Uint8Array): Uint8Array {
  return sha256(data);
}

// ── Master-seed derivation (EPIC-011) ────────────────────────────────────────

export {
  SEED_LEN,
  SEED_INFO_SIGNING,
  SEED_INFO_ENCRYPTION,
  SEED_INFO_VAULT,
  newSeed,
  deriveSeedKey,
  deriveSigningKey,
  deriveEncryptionKey,
  deriveVaultKey,
  type DerivedSigningKey,
  type DerivedEncryptionKey,
} from "./seed.js";
