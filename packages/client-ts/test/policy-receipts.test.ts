import { describe, expect, it } from "vitest";

import { sendsReadReceiptsTo, validateInboxPolicy } from "../src/policy.js";
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
