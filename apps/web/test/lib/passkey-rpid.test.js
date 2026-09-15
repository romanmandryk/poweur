/**
 * credentialRpId (EPIC-018 E18-T4). Lived in the legacy components.test.js;
 * it tests `lib/passkey.js`, so it moved here with the module.
 */
import { describe, expect, it } from "vitest";
import { credentialRpId } from "../../src/lib/passkey.js";

describe("credentialRpId (EPIC-018 E18-T4)", () => {
  it("widens to the registrable domain only when the page is under it", () => {
    expect(credentialRpId("alice.poweur.net", "alice.poweur.net")).toBe("poweur.net");
    expect(credentialRpId("alice.poweur.net", "id.poweur.net")).toBe("poweur.net");
  });

  it("falls back to the page host when the identity lives elsewhere", () => {
    // A browser refuses an rp.id that is not a suffix of the current host.
    expect(credentialRpId("alice.poweur.net", "127.0.0.1")).toBe("127.0.0.1");
    expect(credentialRpId("alice.poweur.net", "localhost")).toBe("localhost");
  });

  it("scopes a self-hosted identity to its own registrable domain", () => {
    expect(credentialRpId("bob.example.org", "bob.example.org")).toBe("example.org");
    // …but never to a public suffix.
    expect(credentialRpId("bob.co.uk", "bob.co.uk")).toBe("bob.co.uk");
  });
});
