import { describe, expect, it } from "vitest";
import {
  appBasePath,
  claimInvite,
  identityAppUrl,
  joinIdentityFor,
  launcherAppUrl,
  normalizeHandleInput,
  policyHint,
  relayUrlForPreset,
  webCustodyBlocked,
} from "../../src/lib/claim";

const hosted = { domain: "poweur.net", hostedDomains: ["poweur.net", "example.org"] };

describe("normalizeHandleInput (E15-T12)", () => {
  it.each([
    ["alice", "alice"],
    ["  Alice ", "alice"],
    ["@alice", "alice"],
    ["@@alice", "alice"],
    ["alice.poweur.net", "alice"],
    ["Melissa.poweur.net", "melissa"],
    ["bob.example.org", "bob"],
    ["bob.elsewhere.com", "bob.elsewhere.com"],
  ])("%s → %s", (raw, want) => {
    expect(normalizeHandleInput(raw, hosted)).toBe(want);
  });
});

describe("relayUrlForPreset", () => {
  const presets = { production: "https://poweur.net", local: "http://127.0.0.1:8080" };
  it("names production and local, and cleans a typed URL", () => {
    expect(relayUrlForPreset("production", "", presets)).toBe("https://poweur.net");
    expect(relayUrlForPreset("local", "", presets)).toBe("http://127.0.0.1:8080");
    expect(relayUrlForPreset("custom", "relay.example.com//", presets)).toBe("https://relay.example.com");
    expect(relayUrlForPreset("custom", "http://10.0.0.2:8080/", presets)).toBe("http://10.0.0.2:8080");
    expect(relayUrlForPreset("custom", "  ", presets)).toBe("");
  });
});

describe("policyHint", () => {
  it("says nothing until the relay has answered with a policy", () => {
    expect(policyHint({ reachable: true, resolved: true }, null)).toBe("");
    expect(policyHint({ reachable: false, resolved: false }, { min_len: 6, max_len: 24 })).toBe("");
  });

  it("renders the relay's own rule", () => {
    expect(policyHint({ reachable: true, resolved: true }, { min_len: 6, max_len: 24, charset: "a-z, 0-9 and hyphen" }))
      .toBe("6–24 characters, a-z, 0-9 and hyphen");
  });
});

describe("webCustodyBlocked", () => {
  it("never blocks a native keystore, nor while still probing", () => {
    expect(webCustodyBlocked({ kind: "native" }, { available: false })).toBe(false);
    expect(webCustodyBlocked(null, null)).toBe(false);
  });

  it("blocks a browser without passkeys or without PRF", () => {
    expect(webCustodyBlocked({ kind: "passkey" }, { available: false })).toBe(true);
    expect(webCustodyBlocked({ kind: "passkey" }, { available: true, prf: false })).toBe(true);
    expect(webCustodyBlocked({ kind: "passkey" }, { available: true, prf: true })).toBe(false);
  });
});

describe("links stay in the app they were opened from (EPIC-021)", () => {
  it("finds the mount directory", () => {
    expect(appBasePath("/newapp/")).toBe("/newapp/");
    expect(appBasePath("/app/")).toBe("/app/");
    expect(appBasePath("/newapp/index.html")).toBe("/newapp/");
    expect(appBasePath("/")).toBe("/");
  });

  it("hands off to the identity's origin in the same app, keeping the port", () => {
    expect(identityAppUrl("alice.poweur.net", { protocol: "https:", port: "", pathname: "/newapp/" })).toBe(
      "https://alice.poweur.net/newapp/",
    );
    expect(identityAppUrl("alice.poweur.net", { protocol: "http:", port: "8088", pathname: "/app/" })).toBe(
      "http://alice.poweur.net:8088/app/",
    );
  });

  it("points back to the launcher in the same app", () => {
    expect(launcherAppUrl("id.poweur.net", { protocol: "https:", port: "", pathname: "/newapp/" })).toBe("https://id.poweur.net/newapp/");
  });
});

describe("joinIdentityFor", () => {
  it("uses the identity host's subject, whatever was typed", () => {
    expect(joinIdentityFor("someone", { mode: "identity", subject: "bob.poweur.net", domain: "poweur.net" })).toBe("bob.poweur.net");
  });

  it("completes a bare handle with the host's or configured domain", () => {
    expect(joinIdentityFor(" Alice ", { mode: "unknown", domain: "poweur.net" })).toBe("alice.poweur.net");
    expect(joinIdentityFor("alice", { mode: "unknown", domain: "" }, "example.org")).toBe("alice.example.org");
    expect(joinIdentityFor("alice.other.net", { mode: "unknown", domain: "poweur.net" })).toBe("alice.other.net");
    expect(joinIdentityFor("", { mode: "unknown", domain: "poweur.net" })).toBe("");
  });
});

describe("claimInvite", () => {
  it("takes a plausible handle and the sign-in marker, nothing else", () => {
    expect(claimInvite("?handle=Alice&from=signin")).toEqual({ handle: "alice", fromSignIn: true });
    expect(claimInvite("?handle=a-b-1")).toEqual({ handle: "a-b-1", fromSignIn: false });
    for (const bad of ["-x", "a.b", "<script>", "a b", "x".repeat(64), ""]) {
      expect(claimInvite(`?handle=${encodeURIComponent(bad)}`).handle).toBe("");
    }
    expect(claimInvite("?from=elsewhere").fromSignIn).toBe(false);
  });
});
