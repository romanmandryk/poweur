/**
 * Master-seed key derivation — the TypeScript twin of
 * `packages/identity/seed.go` (EPIC-011 E11-T1).
 *
 * One 32-byte seed derives every long-lived key an identity owns, so recovery
 * has exactly one artifact to protect. Go is the canonical implementation;
 * `test/seed.test.ts` pins this file against the fixtures it emits
 * (`packages/identity/testdata/vectors/seed-derivation.json`).
 */

import { ed25519, x25519 } from "@noble/curves/ed25519.js";
import { hkdf } from "@noble/hashes/hkdf.js";
import { sha256 } from "@noble/hashes/sha2.js";

import { randomBytes, utf8 } from "../encoding.js";
import { PoweurError } from "../errors.js";

/** Length of a master seed in bytes. */
export const SEED_LEN = 32;

/** HKDF info strings. Protocol constants — versioned, never edited in place. */
export const SEED_INFO_SIGNING = "poweur/v1/sign";
export const SEED_INFO_ENCRYPTION = "poweur/v1/enc";
export const SEED_INFO_VAULT = "poweur/v1/vault";

/** A fresh random master seed. */
export function newSeed(): Uint8Array {
  return randomBytes(SEED_LEN);
}

/**
 * Expand a master seed into 32 bytes for one purpose.
 *
 * Salt is omitted, which RFC 5869 defines as HashLen zero bytes; Go passes a
 * nil salt for the same result. The conformance vectors pin this.
 */
export function deriveSeedKey(seed: Uint8Array, info: string): Uint8Array {
  if (seed.length !== SEED_LEN) {
    throw new PoweurError("invalid_argument", `seed must be ${SEED_LEN} bytes`);
  }
  if (info === "") {
    throw new PoweurError("invalid_argument", "derivation info must not be empty");
  }
  return hkdf(sha256, seed, undefined, utf8(info), 32);
}

export interface DerivedSigningKey {
  /** 32-byte Ed25519 seed, matching this package's SigningKeypair shape. */
  privateKey: Uint8Array;
  publicKey: Uint8Array;
}

/** Derive the identity's Ed25519 keypair from the master seed. */
export function deriveSigningKey(seed: Uint8Array): DerivedSigningKey {
  const privateKey = deriveSeedKey(seed, SEED_INFO_SIGNING);
  return { privateKey, publicKey: ed25519.getPublicKey(privateKey) };
}

export interface DerivedEncryptionKey {
  /** Raw 32-byte X25519 scalar, stored unclamped — x25519 clamps internally. */
  privateKey: Uint8Array;
  publicKey: Uint8Array;
}

/** Derive the identity's X25519 keypair from the master seed. */
export function deriveEncryptionKey(seed: Uint8Array): DerivedEncryptionKey {
  const privateKey = deriveSeedKey(seed, SEED_INFO_ENCRYPTION);
  return { privateKey, publicKey: x25519.getPublicKey(privateKey) };
}

/** Derive the symmetric key protecting `poweur-sys/private/vault` (E11-T6). */
export function deriveVaultKey(seed: Uint8Array): Uint8Array {
  return deriveSeedKey(seed, SEED_INFO_VAULT);
}
