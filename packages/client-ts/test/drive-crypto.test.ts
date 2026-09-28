import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { chacha20poly1305, xchacha20poly1305 } from "@noble/ciphers/chacha.js";
import { x25519 } from "@noble/curves/ed25519.js";
import { hkdf } from "@noble/hashes/hkdf.js";
import { sha256 } from "@noble/hashes/sha2.js";
import { concatBytes, fromBase64, toBase64url, utf8 } from "../src/encoding.js";
import { openWithDomain, sealWithDomain, generateEncryptionKeypair, type SealDomain } from "../src/crypto/index.js";
import { driveContext, encryptChunk, decryptChunk, chunkID, sealKey, openKey, MAX_PLAINTEXT } from "../src/drive/crypto.js";

function fixture<T>(name: string): T {
  return JSON.parse(readFileSync(new URL(`../../identity/testdata/vectors/${name}.json`, import.meta.url), "utf8")) as T;
}

describe("drive crypto conformance", () => {
  for (const v of fixture<Array<{
    domain: SealDomain; private_key: string; public_key: string; context: string; plaintext: string;
    payload: { ciphertext: string; ephemeral_public_key: string; nonce: string };
  }>>("drive-seals")) {
    it(`opens Go ${v.domain} and reproduces the exact ciphertext`, () => {
      const payload = { ciphertext: v.payload.ciphertext, ephemeralPublicKey: v.payload.ephemeral_public_key, nonce: v.payload.nonce };
      const context = fromBase64(v.context);
      const privateKey = fromBase64(v.private_key);
      const plain = fromBase64(v.plaintext);
      expect(openWithDomain(privateKey, payload, v.domain, context)).toEqual(plain);
      // Fixtures use known, test-only ephemeral entropy. Independently recompute
      // the complete construction, pinning HKDF info, salt and AAD bytes.
      const ephemeral = new Uint8Array(32).fill(3);
      const pub = x25519.getPublicKey(ephemeral);
      const recipient = fromBase64(v.public_key);
      const key = hkdf(sha256, x25519.getSharedSecret(ephemeral, recipient), concatBytes(pub, recipient), utf8(v.domain), 32);
      const aad = concatBytes(utf8(v.domain + "\n"), pub, recipient, context);
      expect(toBase64url(chacha20poly1305(key, fromBase64(payload.nonce), aad).encrypt(plain))).toBe(payload.ciphertext);
      expect(() => openWithDomain(privateKey, { ...payload, nonce: "" }, v.domain, context)).toThrow();
      if (v.domain !== "poweur/msg/v1") {
        expect(() => openWithDomain(privateKey, payload, "poweur/msg/v1", new Uint8Array())).toThrow();
        expect(() => openWithDomain(privateKey, payload, v.domain, utf8("another node"))).toThrow();
      }
    });
  }

  for (const v of fixture<Array<{ size: number; key: string; context: string; chunk: string; hash: string }>>("drive-chunks")) {
    it(`opens and re-creates Go chunk size ${v.size}`, () => {
      const context = driveContext("alice.example", "node1", "content", 1);
      expect(toBase64url(context)).toBe(v.context);
      const key = fromBase64(v.key), chunk = fromBase64(v.chunk);
      const plain = new Uint8Array(v.size).fill(42);
      expect(decryptChunk(key, chunk, context)).toEqual(plain);
      expect(chunkID(chunk)).toBe(v.hash);
      const padded = new Uint8Array(Math.ceil((v.size + 4) / 4096) * 4096);
      new DataView(padded.buffer).setUint32(0, v.size);
      padded.set(plain, 4);
      const nonce = chunk.subarray(0, 24);
      const recreated = concatBytes(nonce, xchacha20poly1305(key, nonce, concatBytes(utf8("poweur/drive/chunk/v1\n"), context)).encrypt(padded));
      expect(recreated).toEqual(chunk);
      expect(() => decryptChunk(key, chunk, driveContext("alice.example", "node2", "content", 1))).toThrow();
      expect(() => decryptChunk(key, chunk.subarray(1), context)).toThrow();
      chunk[chunk.length - 1] = chunk[chunk.length - 1]! ^ 1;
      expect(() => decryptChunk(key, chunk, context)).toThrow();
    });
  }

  it("roundtrips randomly sealed keys and chunks, including maximum-size chunks", () => {
    const { publicKey, privateKey } = generateEncryptionKeypair();
    const context = driveContext("alice.example", "node", "node-key", 1);
    const key = new Uint8Array(32).fill(5);
    expect(openKey(privateKey, sealKey(publicKey, key, context), context)).toEqual(key);
    const content = driveContext("alice.example", "node", "content", 1);
    const plain = new Uint8Array(MAX_PLAINTEXT).fill(8);
    expect(decryptChunk(key, encryptChunk(key, plain, content), content)).toEqual(plain);
    expect(() => sealKey(publicKey, key.subarray(1), context)).toThrow();
    const short = sealWithDomain(publicKey, utf8("short"), "poweur/drive/seal/v1", context);
    expect(() => openKey(privateKey, short, context)).toThrow();
    expect(() => encryptChunk(key, new Uint8Array(MAX_PLAINTEXT + 1), content)).toThrow();
    expect(() => encryptChunk(key, plain, new Uint8Array())).toThrow();
    expect(() => encryptChunk(key.subarray(1), plain, content)).toThrow();
  });

  it("rejects ambiguous and invalid contexts", () => {
    for (const field of ["", "a\0b", "\ud800", "a".repeat(1025)]) expect(() => driveContext(field, "node", "purpose", 1)).toThrow();
    for (const gen of [-1, 0.5, NaN, Infinity, Number.MAX_SAFE_INTEGER + 1]) expect(() => driveContext("owner", "node", "purpose", gen)).toThrow();
    expect(driveContext("a", "bc", "d", 1)).not.toEqual(driveContext("ab", "c", "d", 1));
  });
});
