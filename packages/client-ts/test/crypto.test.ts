/** Unit tests for the crypto layer: happy path plus the failure modes. */

import { describe, expect, it } from "vitest";

import {
  decryptMessage,
  ed25519PublicKey,
  encryptMessage,
  expandEd25519PrivateKey,
  generateEncryptionKeypair,
  generateSigningKeypair,
  normalizeEd25519Seed,
  open,
  parseEd25519PublicKey,
  parseX25519PublicKey,
  seal,
  signCanonical,
  verifyCanonical,
  x25519PublicKey,
} from "../src/crypto/index.js";
import {
  LocalDecryptor,
  LocalSigner,
  MemoryKeyStore,
  generateIdentityKeys,
  signerFor,
} from "../src/crypto/keys.js";
import { fromBase64, toBase64Std, toBase64url } from "../src/encoding.js";

describe("ed25519", () => {
  it("signs and verifies a canonical string", () => {
    const { privateKey, publicKey } = generateSigningKeypair();
    const signature = signCanonical(privateKey, "hello\nworld");
    expect(verifyCanonical(publicKey, "hello\nworld", signature)).toBe(true);
    expect(verifyCanonical(publicKey, "hello\nworld!", signature)).toBe(false);
  });

  it("rejects a malformed or wrong-length signature", () => {
    const { publicKey } = generateSigningKeypair();
    expect(verifyCanonical(publicKey, "x", "not base64!!!")).toBe(false);
    expect(verifyCanonical(publicKey, "x", toBase64url(new Uint8Array(10)))).toBe(false);
  });

  it("accepts Go's 64-byte private key form as well as the 32-byte seed", () => {
    const { privateKey } = generateSigningKeypair();
    const expanded = expandEd25519PrivateKey(privateKey);
    expect(expanded).toHaveLength(64);
    expect(normalizeEd25519Seed(expanded)).toEqual(privateKey);
    expect(signCanonical(expanded, "same")).toBe(signCanonical(privateKey, "same"));
  });

  it("refuses a key of any other length", () => {
    expect(() => normalizeEd25519Seed(new Uint8Array(48))).toThrow(/invalid ed25519 private key size/);
  });

  it("parses prefixed and bare public keys alike", () => {
    const { publicKey } = generateSigningKeypair();
    const bare = toBase64url(publicKey);
    expect(parseEd25519PublicKey(bare)).toEqual(publicKey);
    expect(parseEd25519PublicKey(`ed25519:${bare}`)).toEqual(publicKey);
    // Go emits standard base64 on some paths; both must decode.
    expect(parseEd25519PublicKey(toBase64Std(publicKey))).toEqual(publicKey);
  });

  it("rejects a public key of the wrong length", () => {
    expect(() => parseEd25519PublicKey(toBase64url(new Uint8Array(31)))).toThrow(/length/);
  });
});

describe("message encryption", () => {
  it("round-trips through the wire envelope", () => {
    const recipient = generateEncryptionKeypair();
    const { payload, encryption } = encryptMessage(recipient.publicKey, "hello 🔒");
    expect(encryption.alg).toBe("x25519-chacha20-poly1305");
    expect(decryptMessage(recipient.privateKey, payload, encryption)).toBe("hello 🔒");
  });

  it("round-trips an empty payload", () => {
    const recipient = generateEncryptionKeypair();
    const sealed = seal(recipient.publicKey, "");
    expect(open(recipient.privateKey, sealed)).toHaveLength(0);
  });

  it("produces a fresh ephemeral key and nonce per message", () => {
    const recipient = generateEncryptionKeypair();
    const a = seal(recipient.publicKey, "same text");
    const b = seal(recipient.publicKey, "same text");
    expect(a.ephemeralPublicKey).not.toBe(b.ephemeralPublicKey);
    expect(a.nonce).not.toBe(b.nonce);
    expect(a.ciphertext).not.toBe(b.ciphertext);
  });

  it("fails closed when the wrong key opens it", () => {
    const recipient = generateEncryptionKeypair();
    const stranger = generateEncryptionKeypair();
    const sealed = seal(recipient.publicKey, "secret");
    expect(() => open(stranger.privateKey, sealed)).toThrow(/authentication failed/);
  });

  it("fails closed when the ciphertext is tampered with", () => {
    const recipient = generateEncryptionKeypair();
    const sealed = seal(recipient.publicKey, "secret");
    const bytes = fromBase64(sealed.ciphertext);
    bytes[0] = (bytes[0] as number) ^ 0xff;
    expect(() =>
      open(recipient.privateKey, { ...sealed, ciphertext: toBase64url(bytes) }),
    ).toThrow(/authentication failed/);
  });

  it("fails closed when the ephemeral key is swapped", () => {
    const recipient = generateEncryptionKeypair();
    const other = generateEncryptionKeypair();
    const sealed = seal(recipient.publicKey, "secret");
    expect(() =>
      open(recipient.privateKey, {
        ...sealed,
        ephemeralPublicKey: toBase64url(other.publicKey),
      }),
    ).toThrow(/authentication failed/);
  });

  it("rejects a recipient key of the wrong size", () => {
    expect(() => seal(new Uint8Array(16), "x")).toThrow(/32 bytes/);
    expect(() => parseX25519PublicKey(toBase64url(new Uint8Array(16)))).toThrow(/length/);
  });
});

describe("signers and key stores", () => {
  it("exposes the identity's public key in prefixed form", () => {
    const { privateKey, publicKey } = generateSigningKeypair();
    const signer = new LocalSigner("alice.poweur.net", privateKey);
    expect(signer.publicKey).toBe(`ed25519:${toBase64url(publicKey)}`);
    expect(signer.identity).toBe("alice.poweur.net");
  });

  it("signs in both base64 flavours the relay accepts", async () => {
    const { privateKey, publicKey } = generateSigningKeypair();
    const signer = new LocalSigner("alice.poweur.net", privateKey);
    const url = await signer.sign("canonical", "base64url");
    const std = await signer.sign("canonical", "base64std");
    expect(url).not.toBe(std);
    // Same signature bytes either way — only the encoding differs.
    expect(fromBase64(url)).toEqual(fromBase64(std));
    expect(verifyCanonical(publicKey, "canonical", std)).toBe(true);
  });

  it("derives the encryption public key for a decryptor", async () => {
    const { privateKey, publicKey } = generateEncryptionKeypair();
    const decryptor = new LocalDecryptor(privateKey);
    expect(decryptor.encryptionPublicKey).toBe(`x25519:${toBase64url(publicKey)}`);
    expect(await decryptor.privateKeyBytes()).toEqual(privateKey);
  });

  it("round-trips identity keys through the memory store", async () => {
    const store = new MemoryKeyStore();
    const keys = generateIdentityKeys("alice.poweur.net");
    await store.save(keys);
    expect(await store.list()).toEqual(["alice.poweur.net"]);
    const loaded = await store.load("alice.poweur.net");
    expect(loaded?.signingPrivateKey).toEqual(keys.signingPrivateKey);
    await store.remove("alice.poweur.net");
    expect(await store.load("alice.poweur.net")).toBeNull();
  });

  it("omits the decryptor for an identity with no encryption key", () => {
    const { privateKey } = generateSigningKeypair();
    const { signer, decryptor } = signerFor({
      identity: "alice.poweur.net",
      signingPrivateKey: privateKey,
    });
    expect(signer).toBeInstanceOf(LocalSigner);
    expect(decryptor).toBeNull();
  });

  it("derives the same public keys as the raw helpers", () => {
    const signing = generateSigningKeypair();
    const encryption = generateEncryptionKeypair();
    expect(ed25519PublicKey(signing.privateKey)).toEqual(signing.publicKey);
    expect(x25519PublicKey(encryption.privateKey)).toEqual(encryption.publicKey);
  });
});
