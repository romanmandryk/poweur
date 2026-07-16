/**
 * Unit parity with apps/cli TestEncryptDecryptRoundtrip (+ key wrap).
 */
import { describe, it, expect } from "vitest";
import {
  generateEncryptionKeypair,
  encryptMessage,
  decryptMessage,
  wrapKeysWithPin,
  unwrapKeysWithPin,
  generateSigningKeypair,
  toBase64url,
} from "../js/crypto.js";
import { assertSignWith } from "../js/messaging.js";

describe("crypto unit (CLI parity)", () => {
  it("encrypt/decrypt roundtrip", async () => {
    const { publicKeyBytes, privateKeyJWK } = await generateEncryptionKeypair();
    const plaintext = "hello poweur";
    const sealed = await encryptMessage(plaintext, toBase64url(publicKeyBytes));
    expect(sealed.ciphertext).toBeTruthy();
    expect(sealed.ephemeralPublicKey).toBeTruthy();
    expect(sealed.nonce).toBeTruthy();
    expect(sealed.ciphertext).not.toBe(plaintext);

    const opened = await decryptMessage(privateKeyJWK, sealed);
    expect(opened).toBe(plaintext);
  });

  it("PIN wrap/unwrap roundtrip for identity keys", async () => {
    const { privateKeyJWK: sig } = await generateSigningKeypair();
    const { privateKeyJWK: enc } = await generateEncryptionKeypair();
    const wrapped = await wrapKeysWithPin("test-pin", sig, enc);
    expect(wrapped.kdf).toBe("pbkdf2");
    const unwrapped = await unwrapKeysWithPin("test-pin", wrapped);
    expect(unwrapped.signingJWK.d).toBe(sig.d);
    expect(unwrapped.encJWK.d).toBe(enc.d);
  });

  it("rejects invalid sign-with (CLI TestSendMessageRejectsInvalidSignWith)", () => {
    expect(() => assertSignWith("device")).toThrow(/invalid --sign-with/);
    expect(assertSignWith("session")).toBe("session");
    expect(assertSignWith("identity")).toBe("identity");
  });
});
