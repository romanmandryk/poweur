/**
 * @vitest-environment happy-dom
 *
 * The EPIC-011 stand-in.
 *
 * E11-T1 is mid-flight: seed derivation and the CLI/SDK recovery path have
 * shipped, the relay keystore and multi-passkey enrollment have not. These
 * tests pin the two properties that make a mock safe to ship in a UI — the row
 * for *this* browser is real, and every write refuses loudly instead of
 * pretending — so that swapping in the real implementation is a change of body,
 * not of contract.
 */
import { describe, it, expect, beforeEach } from "vitest";

import {
  listEnrollments, seedMockEnrollments, clearMockEnrollments,
  keystoreAvailable, recoveryKitEligibility, KeystoreUnavailable,
  enrollAuthenticator, removeEnrollment, generateRecoveryKit,
} from "../js/keystore-mock.js";
import { saveIdentityRecord } from "../js/storage.js";

const IDENTITY = "alice.poweur.net";

function record(extra = {}) {
  saveIdentityRecord(IDENTITY, {
    identity: IDENTITY,
    publicKey: "AAA",
    encPublicKey: "BBB",
    credentialId: "Y3JlZGVudGlhbC1pZC1oZXJl",
    encryptedKeys: { kdf: "prf", iv: "iv", ciphertext: "ct" },
    relay: "https://poweur.net",
    createdAt: "2026-01-01T00:00:00Z",
    ...extra,
  });
}

beforeEach(() => {
  localStorage.clear();
});

describe("keystore mock — what is real", () => {
  it("derives this browser's enrollment from the identity record", async () => {
    record();
    const [current, ...rest] = await listEnrollments(IDENTITY);

    expect(rest).toEqual([]);
    expect(current).toMatchObject({
      kind: "passkey",
      wrap: "prf",
      role: "device",
      current: true,
      mock: false,
      created_at: "2026-01-01T00:00:00Z",
    });
  });

  it("reports the PIN fallback as the wrap it actually is", async () => {
    record({ encryptedKeys: { kdf: "pbkdf2", iv: "iv", salt: "s", ciphertext: "ct" } });
    const [current] = await listEnrollments(IDENTITY);
    expect(current.wrap).toBe("pin");
  });

  it("returns nothing for an identity this device does not hold", async () => {
    expect(await listEnrollments("stranger.poweur.net")).toEqual([]);
  });
});

describe("keystore mock — what is mocked, and says so", () => {
  it("flags seeded rows as mock and never as current", async () => {
    record();
    seedMockEnrollments(IDENTITY, [
      { enrollment_id: "e2", kind: "hardware-key", wrap: "prf", role: "recovery-master", label: "YubiKey" },
    ]);

    const enrollments = await listEnrollments(IDENTITY);
    expect(enrollments).toHaveLength(2);

    const seeded = enrollments.find((e) => e.enrollment_id === "e2");
    expect(seeded).toMatchObject({ mock: true, current: false, role: "recovery-master" });
    // The real row stays honest alongside it.
    expect(enrollments.filter((e) => e.mock === false)).toHaveLength(1);

    clearMockEnrollments(IDENTITY);
    expect(await listEnrollments(IDENTITY)).toHaveLength(1);
  });

  it("says the relay keystore is not available yet", () => {
    expect(keystoreAvailable()).toBe(false);
  });

  it("refuses every write rather than faking success", async () => {
    record();
    for (const operation of [enrollAuthenticator, removeEnrollment, generateRecoveryKit]) {
      const error = await operation().catch((e) => e);
      // A mock that silently "removed" a device would be worse than no mock.
      expect(error).toBeInstanceOf(KeystoreUnavailable);
      expect(error.epic).toBe("EPIC-011 E11-T1");
      expect(error.message).toMatch(/EPIC-011/);
    }
  });
});

describe("keystore mock — recovery kit eligibility", () => {
  it("says no for identities whose keys were not derived from a seed", () => {
    record();
    // Everything the web app has ever created has two independent keys, so a
    // 24-word kit cannot exist for them until E11-T1's rotate-to-seed.
    expect(recoveryKitEligibility(IDENTITY)).toEqual({ eligible: false, reason: "legacy-keypair" });
  });

  it("says yes once an identity records that it is seed-derived", () => {
    record({ seedDerived: true });
    expect(recoveryKitEligibility(IDENTITY)).toEqual({ eligible: true, reason: null });
  });

  it("says no for an identity this device does not hold", () => {
    expect(recoveryKitEligibility("stranger.poweur.net")).toEqual({
      eligible: false,
      reason: "unknown-identity",
    });
  });
});
