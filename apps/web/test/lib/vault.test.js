/**
 * The key-custody adapter (`js/vault.js`) — the only crypto the web app still
 * owns after E15-T6.
 *
 * Two things must hold, and both are load-bearing:
 *  1. the wrapped-blob format is unchanged, so identities created by the
 *     shipped client keep opening (and EPIC-011 can re-wrap the same payload);
 *  2. WebCrypto JWKs and `@poweur/client`'s raw key bytes describe the same
 *     key — otherwise a signature made here would not verify there.
 */
import { describe, it, expect } from "vitest";
import { crypto as sdk, toBase64url, fromBase64 } from "@poweur/client";
import {
  generateIdentityJwks, generateEncryptionJwk,
  jwksFromKeyBytes, keyBytesFromJwks,
  WebCryptoSigner, JwkDecryptor,
  wrapKeysAES, unwrapKeysAES,
} from "../../src/lib/vault.js";

describe("vault — JWK ↔ @poweur/client key bytes", () => {
  it("derives the same public keys as the SDK", async () => {
    const { signingJWK, encJWK, publicKey, encPublicKey } = await generateIdentityJwks();
    const bytes = keyBytesFromJwks("alice.poweur.net", signingJWK, encJWK);

    expect(toBase64url(sdk.ed25519PublicKey(bytes.signingPrivateKey))).toBe(publicKey);
    expect(toBase64url(sdk.x25519PublicKey(bytes.encryptionPrivateKey))).toBe(encPublicKey);
    // ...and the JWK's own `x` is that same key, which is what the Signer reports.
    expect(signingJWK.x).toBe(publicKey);
    expect(encJWK.x).toBe(encPublicKey);
  });

  it("round-trips SDK-generated keys back into WebCrypto (the EPIC-011 seed path)", async () => {
    const seed = sdk.newSeed();
    const signing = sdk.deriveSigningKey(seed);
    const encryption = sdk.deriveEncryptionKey(seed);

    const { signingJWK, encJWK } = jwksFromKeyBytes({
      signingPrivateKey: signing.privateKey,
      signingPublicKey: signing.publicKey,
      encryptionPrivateKey: encryption.privateKey,
      encryptionPublicKey: encryption.publicKey,
    });

    // WebCrypto rejects a JWK whose `x` disagrees with `d`, so importing at all
    // is the assertion that the two derivations agree.
    const signer = new WebCryptoSigner("alice.poweur.net", signingJWK);
    const signature = await signer.sign("hello");
    expect(sdk.verifyBytes(signing.publicKey, new TextEncoder().encode("hello"), fromBase64(signature))).toBe(true);
    expect(new JwkDecryptor(encJWK).encryptionPublicKey).toBe(`x25519:${toBase64url(encryption.publicKey)}`);
  });
});

describe("vault — WebCryptoSigner", () => {
  it("produces signatures the SDK verifies, in both encodings", async () => {
    const { signingJWK, publicKey } = await generateIdentityJwks();
    const signer = new WebCryptoSigner("alice.poweur.net", signingJWK);
    const canonical = "identity-registration\nalice.poweur.net";
    const message = new TextEncoder().encode(canonical);
    const pub = fromBase64(publicKey);

    expect(signer.publicKey).toBe(`ed25519:${publicKey}`);
    for (const encoding of ["base64url", "base64std"]) {
      const signature = await signer.sign(canonical, encoding);
      expect(sdk.verifyBytes(pub, message, fromBase64(signature))).toBe(true);
    }
    // base64std is padded standard base64; base64url is neither.
    expect(await signer.sign(canonical, "base64std")).toMatch(/[+/=]|^[A-Za-z0-9+/]+$/);
  });

  it("matches the SDK's own LocalSigner byte for byte (Ed25519 is deterministic)", async () => {
    const { signingJWK } = await generateIdentityJwks();
    const bytes = keyBytesFromJwks("alice.poweur.net", signingJWK, null);
    const mine = await new WebCryptoSigner("alice.poweur.net", signingJWK).sign("canonical\nstring");
    const theirs = toBase64url(sdk.signBytes(bytes.signingPrivateKey, new TextEncoder().encode("canonical\nstring")));
    expect(mine).toBe(theirs);
  });
});

describe("vault — JwkDecryptor", () => {
  it("opens a message the SDK sealed to its public key", async () => {
    const { encJWK, encPublicKey } = await generateEncryptionJwk();
    const decryptor = new JwkDecryptor(encJWK);
    expect(decryptor.encryptionPublicKey).toBe(`x25519:${encPublicKey}`);
    const sealed = sdk.seal(fromBase64(encPublicKey), "hello poweur");
    const opened = sdk.open(await decryptor.privateKeyBytes(), sealed);
    expect(new TextDecoder().decode(opened)).toBe("hello poweur");
  });
});

describe("vault — key wrapping (format is frozen for EPIC-011)", () => {
  it("round-trips under a 32-byte secret (the passkey PRF path)", async () => {
    const { signingJWK, encJWK } = await generateIdentityJwks();
    const secret = crypto.getRandomValues(new Uint8Array(32));
    const wrapped = await wrapKeysAES(secret, signingJWK, encJWK);

    expect(wrapped).toHaveProperty("iv");
    expect(wrapped).toHaveProperty("ciphertext");
    expect(JSON.stringify(wrapped)).not.toContain(signingJWK.d);

    const opened = await unwrapKeysAES(secret, wrapped);
    expect(opened.signingJWK).toEqual(signingJWK);
    expect(opened.encJWK).toEqual(encJWK);
  });

  it("rejects the wrong secret", async () => {
    const { signingJWK, encJWK } = await generateIdentityJwks();
    const wrapped = await wrapKeysAES(crypto.getRandomValues(new Uint8Array(32)), signingJWK, encJWK);
    await expect(unwrapKeysAES(crypto.getRandomValues(new Uint8Array(32)), wrapped)).rejects.toThrow();
  });
});
