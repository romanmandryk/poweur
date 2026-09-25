/**
 * Registrable-domain resolution (EPIC-018 E18-T4).
 *
 * This decides a WebAuthn `rp.id`, so both directions are security-relevant:
 * too wide and a credential is offered to origins it should not be, too narrow
 * and the launcher hand-off asks the user to enroll a second time.
 */
import { describe, expect, it } from "vitest";

import { registrableDomain, validateClaimableName, validateHostedHandle, validateIdentityName } from "../src/names.js";

describe("registrableDomain", () => {
  it("scopes a hosted identity to the domain its launcher shares", () => {
    expect(registrableDomain("alice.poweur.net")).toBe("poweur.net");
    expect(registrableDomain("id.poweur.net")).toBe("poweur.net");
    expect(registrableDomain("poweur.net")).toBe("poweur.net");
  });

  it("stops at a public suffix rather than scoping to it", () => {
    // "co.uk" is not registrable: scoping a credential there would offer it to
    // every site in the UK.
    expect(registrableDomain("bob.co.uk")).toBe("bob.co.uk");
    expect(registrableDomain("mail.bob.co.uk")).toBe("bob.co.uk");
    expect(registrableDomain("shop.example.com.au")).toBe("example.com.au");
    expect(registrableDomain("me.github.io")).toBe("me.github.io");
  });

  it("returns hosts that have no registrable domain unchanged", () => {
    expect(registrableDomain("localhost")).toBe("localhost");
    expect(registrableDomain("127.0.0.1")).toBe("127.0.0.1");
    expect(registrableDomain("")).toBe("");
  });

  it("normalizes case and a trailing dot", () => {
    expect(registrableDomain("Alice.Poweur.NET.")).toBe("poweur.net");
  });
});

describe("hosted handle validation matches the relay's fixed rules", () => {
  it("refuses homoglyphs, punycode and double hyphens", () => {
    expect(() => validateIdentityName("аdmin.poweur.net")).toThrow();
    expect(() => validateHostedHandle("xn--80ak6aa92e.poweur.net")).toThrow(/xn--/);
    expect(() => validateHostedHandle("rob--ert.poweur.net")).toThrow(/double hyphen/);
    expect(() => validateHostedHandle("robert.poweur.net")).not.toThrow();
  });

  it("keeps the expanded reserved list in step with Go", () => {
    for (const reserved of ["www", "admin", "support", "verify", "id", "launcher"]) {
      expect(() => validateClaimableName(`${reserved}.poweur.net`), reserved).toThrow(/reserved/);
      expect(() => validateHostedHandle(`${reserved}.poweur.net`), reserved).toThrow(/reserved/);
    }
  });

  it("holds reserved names back from claiming, not from use", () => {
    // An operator-created support.poweur.net must be reachable like any other ID.
    expect(() => validateIdentityName("support.poweur.net")).not.toThrow();
    expect(() => validateClaimableName("alice.poweur.net")).not.toThrow();
  });
});
