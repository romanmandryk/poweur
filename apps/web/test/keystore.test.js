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

import "./helpers/browser-globals.mjs";
import {
  buildRecoveryKit, verifyRecoveryKit, recoveryKitEligibility,
  keysFromMnemonic, deviceLabel, rewrap,
} from "../js/keystore.js";
import { saveIdentityRecord } from "../js/storage.js";
import { jwksFromSeed, unwrapKeysAES, unwrapKeysWithPin } from "../js/vault.js";

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
    record({ seedDerived: true });
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

describe("recovery kit eligibility", () => {
  it("is available for seed-derived identities", () => {
    record({ seedDerived: true });
    expect(recoveryKitEligibility(IDENTITY)).toEqual({ eligible: true, reason: null });
  });

  it("is refused for identities created from two independent keys", () => {
    record({ seedDerived: false });
    expect(recoveryKitEligibility(IDENTITY)).toEqual({ eligible: false, reason: "legacy-keypair" });
  });

  it("is refused for an identity this device does not hold", () => {
    expect(recoveryKitEligibility("stranger.poweur.net"))
      .toEqual({ eligible: false, reason: "unknown-identity" });
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

  it("does the same under a PIN", async () => {
    const seed = sdk.newSeed();
    const { signingJWK, encJWK } = jwksFromSeed(seed);
    const wrapped = await rewrap({ pin: "hunter2" }, { signingJWK, encJWK, seed: toBase64url(seed) });
    expect((await unwrapKeysWithPin("hunter2", wrapped)).seed).toBe(toBase64url(seed));
  });

  it("refuses to store keys with neither a PRF secret nor a PIN", async () => {
    await expect(rewrap({}, { signingJWK: {}, encJWK: {}, seed: null }))
      .rejects.toThrow(/PRF secret or a PIN/);
  });
});

describe("deviceLabel", () => {
  it("names something a person would recognise in a device list", () => {
    expect(deviceLabel("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Chrome/120.0 Safari/537.36"))
      .toBe("Chrome on Mac");
    expect(deviceLabel("Mozilla/5.0 (iPhone; CPU iPhone OS 17_0) Version/17.0 Safari/605.1"))
      .toBe("Safari on iOS");
    expect(deviceLabel("Mozilla/5.0 (Windows NT 10.0) Firefox/121.0")).toBe("Firefox on Windows");
    expect(deviceLabel("")).toBe("browser on Device");
  });
});
