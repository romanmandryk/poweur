/**
 * The recovery-kit and device-label helpers (EPIC-011, web side).
 *
 * The relay-backed half — enroll, list, remove, bootstrap fetch — is in
 * `test/keystore-relay.test.js`, because none of it is meaningful against a
 * stub. What is unit-testable is the encoding and eligibility logic, and that
 * is where a silent bug would be worst: a kit that "looks right" but decodes
 * to the wrong seed is only discovered when someone needs it.
 */
import { describe, it, expect, beforeEach } from "vitest";
import { crypto as sdk, toBase64url } from "@poweur/client";

import "../helpers/browser-globals.mjs";
import {
  buildRecoveryKit, verifyRecoveryKit, 
  keysFromMnemonic, deviceLabel, rewrap, restoreLocalRecord,
} from "../../src/lib/keystore.js";
import { saveIdentityRecord, loadIdentityRecord } from "../../src/lib/storage.js";
import { jwksFromSeed, unwrapKeysAES, wrapKeysAES } from "../../src/lib/vault.js";

const IDENTITY = "alice.poweur.net";

function record(extra = {}) {
  saveIdentityRecord(IDENTITY, {
    identity: IDENTITY,
    publicKey: "AAA",
    encPublicKey: "BBB",
    relay: "https://poweur.net",
    encryptedKeys: { kdf: "prf", iv: "iv", ciphertext: "ct" },
    createdAt: "2026-01-01T00:00:00Z",
    ...extra,
  });
}

beforeEach(() => localStorage.clear());

describe("recovery kit", () => {
  it("renders the seed as 24 words that decode back to it", () => {
    record({});
    const seed = sdk.newSeed();
    const kit = buildRecoveryKit(IDENTITY, toBase64url(seed));

    expect(kit.identity).toBe(IDENTITY);
    expect(kit.mnemonic.split(" ")).toHaveLength(24);
    // The kit is an *encoding* of the seed, not a second secret.
    expect(kit.seed).toBe(toBase64url(seed));
    expect(verifyRecoveryKit(kit.mnemonic, kit.seed)).toBe(true);
  });

  it("accepts a kit typed back with messy spacing and casing", () => {
    const seed = sdk.newSeed();
    const kit = buildRecoveryKit(IDENTITY, toBase64url(seed));
    const messy = `  ${kit.mnemonic.toUpperCase().split(" ").join("   ")}  `;
    expect(verifyRecoveryKit(messy, kit.seed)).toBe(true);
  });

  it("rejects a kit for a different seed, and a mistyped one", () => {
    const kit = buildRecoveryKit(IDENTITY, toBase64url(sdk.newSeed()));
    const other = buildRecoveryKit(IDENTITY, toBase64url(sdk.newSeed()));

    expect(verifyRecoveryKit(other.mnemonic, kit.seed)).toBe(false);
    // One word swapped: the BIP39 checksum is what catches transcription slips.
    const words = kit.mnemonic.split(" ");
    words[3] = words[3] === "abandon" ? "ability" : "abandon";
    expect(verifyRecoveryKit(words.join(" "), kit.seed)).toBe(false);
    expect(verifyRecoveryKit("not a mnemonic at all", kit.seed)).toBe(false);
    expect(verifyRecoveryKit("", kit.seed)).toBe(false);
  });

  it("rebuilds the identity's keys from the words alone", async () => {
    const seed = sdk.newSeed();
    const direct = jwksFromSeed(seed);
    const kit = buildRecoveryKit(IDENTITY, toBase64url(seed));

    const restored = keysFromMnemonic(kit.mnemonic);
    // This is the whole promise of a kit: paper in, same identity out.
    expect(restored.publicKey).toBe(direct.publicKey);
    expect(restored.encPublicKey).toBe(direct.encPublicKey);
    expect(restored.signingJWK).toEqual(direct.signingJWK);
  });

  it("refuses to build a kit with no seed", () => {
    expect(() => buildRecoveryKit(IDENTITY, null)).toThrow(/no master seed/i);
  });
});

describe("rewrap", () => {
  it("stores the seed alongside the keys under a PRF secret", async () => {
    const seed = sdk.newSeed();
    const { signingJWK, encJWK } = jwksFromSeed(seed);
    const secret = crypto.getRandomValues(new Uint8Array(32));

    const wrapped = await rewrap({ prfOutput: secret }, { signingJWK, encJWK, seed: toBase64url(seed) });
    expect(wrapped.kdf).toBe("prf");

    const opened = await unwrapKeysAES(secret, wrapped);
    expect(opened.signingJWK).toEqual(signingJWK);
    // Without the seed in the blob there is no kit after a restore.
    expect(opened.seed).toBe(toBase64url(seed));
  });

  it("refuses to store keys with no PRF secret", async () => {
    await expect(rewrap({}, { signingJWK: {}, encJWK: {}, seed: null }))
      .rejects.toThrow(/PRF secret/);
  });
});

describe("restoreLocalRecord", () => {
  it("rebuilds the local record from the authenticator that opened it", async () => {
    const seed = sdk.newSeed();
    const { signingJWK, encJWK } = jwksFromSeed(seed);
    const prf = crypto.getRandomValues(new Uint8Array(32));
    const wrapped = await wrapKeysAES(prf, signingJWK, encJWK, toBase64url(seed));

    const record = restoreLocalRecord(IDENTITY, {
      signingJWK, encJWK, seed: toBase64url(seed),
      assertion: { credential_id: "cred-abc" },
      entry: {
        enrollment_id: "enr-1",
        credential_id: "cred-abc",
        credential_public_key: "spki",
        credential_alg: -8,
        wrapped,
        payload: "seed",
        created_at: "2026-03-01T00:00:00Z",
      },
    }, { relayUrl: "https://poweur.net" });

    expect(record).toMatchObject({
      enrollmentId: "enr-1",
      credentialId: "cred-abc",
      credentialPublicKey: "spki",
      credentialAlg: -8,
      supportsPRF: true,
    });
    expect(record.encryptedKeys.kdf).toBe("prf");
    expect(record.encryptedKeys.ciphertext).toBe(wrapped.ciphertext);
    expect(loadIdentityRecord(IDENTITY).enrollmentId).toBe("enr-1");

    // The blob the relay held still opens under the same PRF — we did not re-wrap.
    const opened = await unwrapKeysAES(prf, record.encryptedKeys);
    expect(opened.signingJWK).toEqual(signingJWK);
    expect(opened.seed).toBe(toBase64url(seed));
  });

  it("refuses a fetch that named no credential", () => {
    expect(() => restoreLocalRecord(IDENTITY, {
      entry: { wrapped: { iv: "a", ciphertext: "b" } },
      assertion: {},
    }, { relayUrl: "https://poweur.net" })).toThrow(/nothing to restore/i);
  });
});

describe("deviceLabel", () => {
  it("names something a person would recognise in a device list", () => {
    expect(deviceLabel("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Chrome/120.0 Safari/537.36", { native: false }))
      .toBe("Chrome on Mac");
    expect(deviceLabel("Mozilla/5.0 (iPhone; CPU iPhone OS 17_0) Version/17.0 Safari/605.1", { native: false }))
      .toBe("Safari on iOS");
    expect(deviceLabel("Mozilla/5.0 (Windows NT 10.0) Firefox/121.0", { native: false })).toBe("Firefox on Windows");
    expect(deviceLabel("", { native: false })).toBe("browser on Device");
  });

  it("does not name the WebView as a browser inside the native shell", () => {
    const iphone = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0) Version/17.0 Safari/605.1";
    const android = "Mozilla/5.0 (Linux; Android 14) Chrome/120.0 Mobile Safari/537.36";
    const desktop = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Chrome/120.0 Safari/537.36";
    expect(deviceLabel(iphone, { native: true })).toBe("iPhone");
    expect(deviceLabel(android, { native: true })).toBe("Android");
    expect(deviceLabel(desktop, { native: true })).toBe("This device");
  });
});
