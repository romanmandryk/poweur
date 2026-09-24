import { beforeEach, describe, expect, it } from "vitest";
import { toBase64url } from "../../src/lib/vault.js";
import {
  clearPendingShareClaim,
  pendingShareClaim,
  takeShareClaimLink,
} from "../../src/lib/share-claim";

describe("share claim handoff", () => {
  beforeEach(() => clearPendingShareClaim());

  it("captures a fragment without leaving the capability in the address bar", () => {
    const claim = {
      share_id: "shr_request", owner: "alice.poweur.net",
      token: "aaaaaaaaaaaaaaaaaaaaaaaaaa", action: "uploaded",
    };
    const encoded = toBase64url(new TextEncoder().encode(JSON.stringify(claim)));
    history.replaceState(null, "", `/app/#share=${encoded}`);
    expect(takeShareClaimLink()).toBe(true);
    expect(pendingShareClaim()).toEqual(claim);
    expect(location.hash).toBe("");
  });

  it("rejects malformed or non-capability fragments", () => {
    expect(takeShareClaimLink("#share=bad")).toBe(false);
    expect(pendingShareClaim()).toBeNull();
  });
});
