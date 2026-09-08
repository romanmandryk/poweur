/**
 * @vitest-environment happy-dom
 *
 * Local persistence — the web twin of the CLI's `~/.poweur` tree, plus the two
 * EPIC-015 E15-T1 rules it enforces: state is keyed by identity, and a relay
 * URL comes from the identity record.
 */
import { describe, it, expect, beforeEach } from "vitest";
import { rfc3339 } from "@poweur/client";
import {
  saveConfig,
  getConfig,
  saveIdentityRecord,
  loadIdentityRecord,
  listIdentities,
  removeIdentity,
  setActiveIdentity,
  getActiveIdentity,
  saveSessionRecord,
  loadSessionRecord,
  BrowserSessionStore,
  defaultRelayUrl,
  relayUrlFor,
  rpIdFor,
  resolveOptionsFor,
} from "../js/storage.js";

const record = (identity, relay) => ({
  identity,
  publicKey: "pub-" + identity,
  encPublicKey: "enc-" + identity,
  credentialId: "cred",
  encryptedKeys: { kdf: "prf", iv: "iv", ciphertext: "ct" },
  relay,
  userId: "uid",
  createdAt: rfc3339(),
});

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
});

describe("storage — identities and config", () => {
  it("round-trips an identity record", () => {
    saveIdentityRecord("alice.poweur.net", record("alice.poweur.net", "https://poweur.net"));
    expect(loadIdentityRecord("alice.poweur.net").publicKey).toBe("pub-alice.poweur.net");
    expect(loadIdentityRecord("nobody.poweur.net")).toBeNull();
  });

  it("lists identities in a stable order and forgets the active one on removal", () => {
    saveIdentityRecord("bob.poweur.net", record("bob.poweur.net", "https://poweur.net"));
    saveIdentityRecord("alice.poweur.net", record("alice.poweur.net", "https://poweur.net"));
    expect(listIdentities()).toEqual(["alice.poweur.net", "bob.poweur.net"]);

    setActiveIdentity("alice.poweur.net");
    expect(getActiveIdentity()).toBe("alice.poweur.net");
    removeIdentity("alice.poweur.net");
    expect(listIdentities()).toEqual(["bob.poweur.net"]);
    expect(getActiveIdentity()).toBeNull();
  });

  it("merges saved config over the defaults", () => {
    expect(getConfig().parentDomain).toBe("poweur.net");
    saveConfig({ ...getConfig(), parentDomain: "example.com" });
    expect(getConfig().parentDomain).toBe("example.com");
  });
});

describe("storage — relay URL comes from the identity record (E15-T1)", () => {
  it("prefers the record's relay over the page origin and over config", () => {
    saveConfig({ relayUrl: "https://config.example" });
    saveIdentityRecord("alice.r1.test", record("alice.r1.test", "https://r1.test"));
    saveIdentityRecord("alice.r2.test", record("alice.r2.test", "https://r2.test"));

    // One client, two identities, two different relays.
    expect(relayUrlFor("alice.r1.test")).toBe("https://r1.test");
    expect(relayUrlFor("alice.r2.test")).toBe("https://r2.test");
    // Neither is the origin the page happens to be served from.
    expect(relayUrlFor("alice.r1.test")).not.toBe(globalThis.location.origin);
  });

  it("falls back to configured relay, then origin, only when no record exists", () => {
    expect(defaultRelayUrl()).toBe(globalThis.location.origin);
    saveConfig({ relayUrl: "https://config.example" });
    expect(defaultRelayUrl()).toBe("https://config.example");
    expect(relayUrlFor("unknown.poweur.net")).toBe("https://config.example");
  });

  it("relaxes the SSRF guard only for local/private relays", () => {
    expect(resolveOptionsFor("https://poweur.net")).toEqual({ relayUrl: "https://poweur.net" });
    expect(resolveOptionsFor("http://127.0.0.1:8080")).toMatchObject({ scheme: "http", allowPrivate: true });
    expect(resolveOptionsFor("http://localhost:3000")).toMatchObject({ scheme: "http", allowPrivate: true });
    expect(resolveOptionsFor("https://192.168.1.9")).toMatchObject({ allowPrivate: true });
    expect(resolveOptionsFor("https://8.8.8.8").allowPrivate).toBeUndefined();
  });
});

describe("storage — BrowserSessionStore is the SDK's SessionStore", () => {
  it("saves, loads and removes by identity", async () => {
    const store = new BrowserSessionStore();
    const session = {
      identity: "alice.poweur.net",
      sessionId: "sess-1",
      sessionPrivateKey: "cHJpdmF0ZQ",
      sessionPublicKey: "cHVibGlj",
      issuedAt: rfc3339(),
      expiresAt: rfc3339(new Date(Date.now() + 3600_000)),
      nonce: "n",
      identitySignature: "sig",
      relayUrl: "https://poweur.net",
    };

    await store.save(session);
    expect(await store.load("alice.poweur.net")).toEqual(session);
    expect(await store.load("bob.poweur.net")).toBeNull();

    await store.remove("alice.poweur.net");
    expect(await store.load("alice.poweur.net")).toBeNull();
  });

  it("keeps sessions in sessionStorage, not localStorage", () => {
    saveSessionRecord("alice.poweur.net", { sessionId: "x" });
    expect(loadSessionRecord("alice.poweur.net")).toEqual({ sessionId: "x" });
    expect(localStorage.getItem("poweur:session:alice.poweur.net")).toBeNull();
  });
});

describe("credential scope (EPIC-018 E18-T4)", () => {
  it("keeps asserting pre-E18 records against the host they were minted on", () => {
    // No rpId on the record: it was created before the field existed, bound to
    // the page host. Reading the registrable domain for it would stop finding
    // the credential at all.
    saveIdentityRecord("legacy.poweur.net", { identity: "legacy.poweur.net", credentialId: "c" });
    expect(rpIdFor("legacy.poweur.net")).toBe(globalThis.location.hostname);
  });

  it("uses the stored scope once a record carries one", () => {
    saveIdentityRecord("scoped.poweur.net", {
      identity: "scoped.poweur.net", credentialId: "c", rpId: "poweur.net",
    });
    expect(rpIdFor("scoped.poweur.net")).toBe("poweur.net");
  });

  it("falls back to the host for an identity it has never seen", () => {
    expect(rpIdFor("unknown.poweur.net")).toBe(globalThis.location.hostname);
  });
});
