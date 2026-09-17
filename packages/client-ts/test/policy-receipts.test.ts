import { describe, expect, it } from "vitest";

import { sendsReadReceiptsTo, trustsAuthService, validateInboxPolicy, writeInboxPolicy } from "../src/policy.js";
import { MSG_TYPE_AUTH_REQUEST, SYSTEM_MESSAGE_TYPES } from "../src/msgtypes.js";
import type { InboxPolicy } from "../src/types.js";

describe("read-receipt policy", () => {
  it("defaults to enabled and honors global and per-contact opt-outs", () => {
    expect(sendsReadReceiptsTo({ version: 1, mode: "open" }, "bob.example.org")).toBe(true);
    expect(sendsReadReceiptsTo({
      version: 1, mode: "open", read_receipts: { enabled: false },
    }, "bob.example.org")).toBe(false);
    expect(sendsReadReceiptsTo({
      version: 1, mode: "open",
      read_receipts: { enabled: true, disabled_for: ["Bob.Example.ORG"] },
    }, "bob.example.org")).toBe(false);
  });

  it("validates the canonical policy shape", () => {
    const valid: InboxPolicy = {
      version: 1, mode: "open",
      read_receipts: { enabled: true, disabled_for: ["bob.example.org"] },
    };
    expect(() => validateInboxPolicy(valid)).not.toThrow();
    expect(() => validateInboxPolicy({
      ...valid, read_receipts: { enabled: true, disabled_for: ["bad"] },
    })).toThrow(/invalid read-receipt identity/);
    expect(() => validateInboxPolicy({
      ...valid, read_receipts: { enabled: true, disabled_for: ["bob.example.org", "BOB.EXAMPLE.ORG"] },
    })).toThrow(/duplicate/);
  });
});

describe("trusted sign-in services (EPIC-022 E22-T7)", () => {
  it("validates and matches like Go", () => {
    const policy = { version: 1, mode: "contacts_only" as const, trusted_auth_services: ["Bridge.Poweur.org"] };
    expect(() => validateInboxPolicy(policy)).not.toThrow();
    expect(trustsAuthService(policy, "bridge.poweur.org")).toBe(true);
    expect(trustsAuthService(policy, "evil.poweur.org")).toBe(false);
    expect(trustsAuthService(policy, "")).toBe(false);
    expect(trustsAuthService({ version: 1, mode: "open" }, "bridge.poweur.org")).toBe(false);
    for (const list of [["not a name"], ["bridge.poweur.org", "BRIDGE.poweur.org"], Array.from({ length: 17 }, (_, i) => `b${i}.example.org`)]) {
      expect(() => validateInboxPolicy({ version: 1, mode: "open", trusted_auth_services: list })).toThrow();
    }
  });

  it("writes the list only when it has entries", async () => {
    const written: Record<string, unknown> = {};
    const dav = { writeJson: async (path: string, value: unknown) => { written[path] = value; } };
    const with_ = await writeInboxPolicy(dav as never, "open", undefined, undefined, [" Bridge.Poweur.org "]);
    expect(with_.trusted_auth_services).toEqual(["bridge.poweur.org"]);
    const without = await writeInboxPolicy(dav as never, "open", undefined, undefined, []);
    expect(without.trusted_auth_services).toBeUndefined();
  });

  it("is a registered system type", () => {
    expect(SYSTEM_MESSAGE_TYPES).toContain(MSG_TYPE_AUTH_REQUEST);
    expect([...SYSTEM_MESSAGE_TYPES]).toEqual([...SYSTEM_MESSAGE_TYPES].sort());
  });
});
