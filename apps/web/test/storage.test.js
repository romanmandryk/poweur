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
  hasRelayUrl,
  relayUrlFor,
  identityOriginUrl,
  apiBaseForIdentity,
  ROOT_SESSION_KEY,
  PRODUCTION_RELAY_URL,
  localDevRelayUrl,
  relayPresetFor,
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

  it("builds an identity origin from the fallback's scheme and port", () => {
    expect(identityOriginUrl("johnjohn.poweur.net", "https://poweur.net"))
      .toBe("https://johnjohn.poweur.net");
    expect(identityOriginUrl("alice.poweur.net", "http://127.0.0.1:8080"))
      .toBe("http://alice.poweur.net:8080");
    expect(identityOriginUrl("127.0.0.1", "http://127.0.0.1:8080")).toBe("");
  });

  it("prefers the identity host for hosted names stored on the operator apex", () => {
    sessionStorage.setItem(ROOT_SESSION_KEY, JSON.stringify({
      relay_address: "poweur.net",
      hosted_domains: ["poweur.net"],
    }));
    saveIdentityRecord("johnjohn.poweur.net", record("johnjohn.poweur.net", "https://poweur.net"));
    expect(apiBaseForIdentity("johnjohn.poweur.net", "https://poweur.net"))
      .toBe("https://johnjohn.poweur.net");
    expect(relayUrlFor("johnjohn.poweur.net")).toBe("https://johnjohn.poweur.net");
    // Already on the identity host — do not bounce to the apex.
    saveIdentityRecord("johnjohn.poweur.net", record("johnjohn.poweur.net", "https://johnjohn.poweur.net"));
    expect(relayUrlFor("johnjohn.poweur.net")).toBe("https://johnjohn.poweur.net");
  });

  it("leaves a loopback stored relay alone so local tests keep talking to 127.0.0.1", () => {
    sessionStorage.setItem(ROOT_SESSION_KEY, JSON.stringify({
      hosted_domains: ["poweur.net"],
    }));
    saveIdentityRecord("alice.poweur.net", record("alice.poweur.net", "http://127.0.0.1:8080"));
    expect(relayUrlFor("alice.poweur.net")).toBe("http://127.0.0.1:8080");
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

describe("relay URL on a shell origin (EPIC-019 E19-T1)", () => {
  it("seeds a non-web origin from the production relay, never from itself", () => {
    // A Capacitor shell runs on capacitor://localhost. Returning that as the
    // relay produced an app that registered against itself. Production is the
    // seed; the picker can still switch to a local or custom relay.
    const original = Object.getOwnPropertyDescriptor(globalThis, "location");
    Object.defineProperty(globalThis, "location", {
      value: { origin: "capacitor://localhost", protocol: "capacitor:" },
      configurable: true,
    });
    try {
      expect(defaultRelayUrl()).toBe(PRODUCTION_RELAY_URL);
      expect(hasRelayUrl()).toBe(true);

      saveConfig({ ...getConfig(), relayUrl: "https://selfhost.example" });
      expect(defaultRelayUrl()).toBe("https://selfhost.example");
    } finally {
      if (original) Object.defineProperty(globalThis, "location", original);
    }
  });

  it("still falls back to the origin when the relay serves the app", () => {
    expect(defaultRelayUrl()).toBe(globalThis.location.origin);
    expect(hasRelayUrl()).toBe(true);
  });

  it("does not treat Android's https://localhost shell origin as a relay", () => {
    // The protocol test above is an iOS answer. **Android serves the same
    // bundle from `https://localhost`** — an ordinary web origin by every
    // syntactic test — so Capacitor is the signal, and production is the seed.
    globalThis.Capacitor = { isNativePlatform: () => true };
    try {
      expect(defaultRelayUrl()).toBe(PRODUCTION_RELAY_URL);
      expect(defaultRelayUrl()).not.toBe(globalThis.location.origin);
      expect(hasRelayUrl()).toBe(true);

      saveConfig({ ...getConfig(), relayUrl: "http://10.0.2.2:8080" });
      expect(defaultRelayUrl()).toBe("http://10.0.2.2:8080");
    } finally {
      delete globalThis.Capacitor;
    }
  });
});

describe("relay presets (shell picker)", () => {
  it("maps known URLs onto the three picker choices", () => {
    expect(relayPresetFor("")).toBe("production");
    expect(relayPresetFor(PRODUCTION_RELAY_URL)).toBe("production");
    expect(relayPresetFor("https://poweur.net/")).toBe("production");
    expect(relayPresetFor("http://127.0.0.1:8080")).toBe("local");
    expect(relayPresetFor("http://10.0.2.2:8080")).toBe("local");
    expect(relayPresetFor("https://selfhost.example")).toBe("custom");
  });

  it("points the local preset at the emulator host on Android", () => {
    expect(localDevRelayUrl()).toBe("http://127.0.0.1:8080");
    globalThis.Capacitor = { getPlatform: () => "android" };
    try {
      expect(localDevRelayUrl()).toBe("http://10.0.2.2:8080");
    } finally {
      delete globalThis.Capacitor;
    }
  });
});
