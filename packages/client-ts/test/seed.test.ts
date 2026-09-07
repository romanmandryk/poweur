/**
 * Master-seed derivation: TypeScript ↔ Go conformance (EPIC-011 E11-T1).
 *
 * Go is canonical (`packages/identity/seed.go`). Every case here re-derives
 * with the TypeScript implementation and compares against the fixture Go
 * emitted, so an info string or KDF parameter that changes on one side and
 * not the other turns CI red instead of silently splitting the two clients.
 */

import { ed25519, x25519 } from "@noble/curves/ed25519.js";
import { describe, expect, it } from "vitest";

import { fromBase64, toBase64url, utf8 } from "../src/encoding.js";
import {
  SEED_INFO_ENCRYPTION,
  SEED_INFO_SIGNING,
  SEED_INFO_VAULT,
  SEED_LEN,
  deriveEncryptionKey,
  deriveSeedKey,
  deriveSigningKey,
  deriveVaultKey,
  newSeed,
} from "../src/crypto/seed.js";
import { loadVectors, vectorsAvailable } from "./vectors.js";

interface SeedVector {
  name: string;
  seed: string;
  signing_public_key: string;
  signing_private_seed: string;
  encryption_public_key: string;
  encryption_private_key: string;
  vault_key: string;
  signature: string;
}

interface SeedVectorFile {
  message: string;
  info: { signing: string; encryption: string; vault: string };
  vectors: SeedVector[];
}

describe("seed derivation — Go conformance", () => {
  it("has vectors generated", () => {
    expect(vectorsAvailable()).toBe(true);
  });

  const file = loadVectors<SeedVectorFile>("seed-derivation");

  it("agrees with Go on the info strings", () => {
    expect(file.info.signing).toBe(SEED_INFO_SIGNING);
    expect(file.info.encryption).toBe(SEED_INFO_ENCRYPTION);
    expect(file.info.vault).toBe(SEED_INFO_VAULT);
  });

  for (const v of file.vectors) {
    describe(`vector: ${v.name}`, () => {
      const seed = fromBase64(v.seed);

      it("derives Go's signing keypair", () => {
        const derived = deriveSigningKey(seed);
        expect(toBase64url(derived.privateKey)).toBe(v.signing_private_seed);
        expect(`ed25519:${toBase64url(derived.publicKey)}`).toBe(v.signing_public_key);
      });

      it("derives Go's encryption keypair", () => {
        const derived = deriveEncryptionKey(seed);
        expect(toBase64url(derived.privateKey)).toBe(v.encryption_private_key);
        expect(`x25519:${toBase64url(derived.publicKey)}`).toBe(v.encryption_public_key);
      });

      it("derives Go's vault key", () => {
        expect(toBase64url(deriveVaultKey(seed))).toBe(v.vault_key);
      });

      // Byte equality alone would not catch a key that cannot actually sign.
      it("verifies a signature Go produced with the derived key", () => {
        const { publicKey } = deriveSigningKey(seed);
        expect(
          ed25519.verify(fromBase64(v.signature), utf8(file.message), publicKey),
        ).toBe(true);
      });

      it("agrees with Go's encryption public key under X25519", () => {
        const { privateKey } = deriveEncryptionKey(seed);
        const other = new Uint8Array(32).fill(11);
        const mine = x25519.getSharedSecret(privateKey, x25519.getPublicKey(other));
        const theirs = x25519.getSharedSecret(other, fromBase64(v.encryption_public_key.slice("x25519:".length)));
        expect(toBase64url(mine)).toBe(toBase64url(theirs));
      });
    });
  }
});

describe("seed derivation — local behaviour", () => {
  it("rejects seeds that are not 32 bytes", () => {
    expect(() => deriveSeedKey(new Uint8Array(31), SEED_INFO_SIGNING)).toThrow();
    expect(() => deriveSeedKey(new Uint8Array(33), SEED_INFO_SIGNING)).toThrow();
  });

  it("rejects an empty info string", () => {
    expect(() => deriveSeedKey(new Uint8Array(SEED_LEN), "")).toThrow();
  });

  it("separates domains", () => {
    const seed = new Uint8Array(SEED_LEN).fill(9);
    const sign = toBase64url(deriveSeedKey(seed, SEED_INFO_SIGNING));
    const enc = toBase64url(deriveSeedKey(seed, SEED_INFO_ENCRYPTION));
    const vault = toBase64url(deriveSeedKey(seed, SEED_INFO_VAULT));
    expect(new Set([sign, enc, vault]).size).toBe(3);
  });

  it("is deterministic and seed-sensitive", () => {
    const a = new Uint8Array(SEED_LEN).fill(3);
    const b = new Uint8Array(SEED_LEN).fill(3);
    b.set([2], SEED_LEN - 1);
    expect(toBase64url(deriveSeedKey(a, SEED_INFO_SIGNING))).toBe(
      toBase64url(deriveSeedKey(a, SEED_INFO_SIGNING)),
    );
    expect(toBase64url(deriveSeedKey(a, SEED_INFO_SIGNING))).not.toBe(
      toBase64url(deriveSeedKey(b, SEED_INFO_SIGNING)),
    );
  });

  it("generates distinct seeds of the right length", () => {
    const a = newSeed();
    expect(a.length).toBe(SEED_LEN);
    expect(toBase64url(a)).not.toBe(toBase64url(newSeed()));
  });
});
