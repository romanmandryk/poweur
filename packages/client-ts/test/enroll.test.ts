/**
 * Pairing v2 primitives, pinned to Go (packages/identity/pairing.go) by the
 * `pairing.json` vectors. No network: these are what both devices compute.
 */

import { describe, expect, it } from "vitest";

import {
  formatShortCode,
  normalizeShortCode,
  pairingCommitment,
  pairingAppLink,
  pairingLink,
  pairingSas,
  parsePairingLink,
  verifyPairingReveal,
} from "../src/enroll.js";
import { loadVectors } from "./vectors.js";

interface PairingVectors {
  pairings: { ephemeral_public_key: string; commit_nonce: string; approver_nonce: string; commitment: string; sas: string }[];
  short_codes: { input: string; code: string; valid: boolean }[];
}

const vectors = loadVectors<PairingVectors>("pairing");

describe("pairing v2 ↔ Go vectors", () => {
  it("computes the same commitment and digits as Go", () => {
    expect(vectors.pairings.length).toBeGreaterThan(0);
    for (const v of vectors.pairings) {
      expect(pairingCommitment(v.ephemeral_public_key, v.commit_nonce)).toBe(v.commitment);
      expect(pairingSas(v.commitment, v.ephemeral_public_key, v.commit_nonce, v.approver_nonce)).toBe(v.sas);
      expect(() => verifyPairingReveal(v.commitment, v.ephemeral_public_key, v.commit_nonce)).not.toThrow();
    }
  });

  it("refuses a reveal that does not open the commitment", () => {
    const [a, b] = vectors.pairings;
    expect(() => verifyPairingReveal(a!.commitment, b!.ephemeral_public_key, a!.commit_nonce)).toThrow(/does not match/);
  });

  it("reads short codes the way Go does", () => {
    for (const v of vectors.short_codes) {
      expect(normalizeShortCode(v.input)).toBe(v.valid ? v.code : null);
    }
    expect(formatShortCode("K7QM4XP2")).toBe("K7QM-4XP2");
  });

  it("round-trips pairing links", () => {
    const c = vectors.pairings[0]!.commitment;
    const link = pairingLink("https://alice.poweur.net/app/", "Alice.Poweur.net", "K7QM4XP2", c);
    expect(link).toBe(`https://alice.poweur.net/app/#pair=K7QM4XP2.${c}&id=alice.poweur.net`);
    expect(parsePairingLink(link)).toEqual({ code: "K7QM4XP2", commitment: c, identity: "alice.poweur.net" });
    const app = pairingAppLink("Alice.Poweur.net", "K7QM4XP2", c);
    expect(app).toBe(`poweur://pair?pair=K7QM4XP2.${c}&id=alice.poweur.net`);
    expect(parsePairingLink(app)).toEqual({ code: "K7QM4XP2", commitment: c, identity: "alice.poweur.net" });
    for (const input of [`#pair=K7QM4XP2.${c}`, `k7qm-4xp2.${c}`]) {
      expect(parsePairingLink(input)).toEqual({ code: "K7QM4XP2", commitment: c, identity: "" });
    }
    for (const input of ["https://alice.poweur.net/app/", "K7QM4XP2", "K7QM4XP2.short"]) {
      expect(parsePairingLink(input)).toBeNull();
    }
  });
});
