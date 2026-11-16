/**
 * Sending to a contact whose identity can't be resolved (EPIC-029 E29-T4):
 * the message is encrypted to their stored document and posted to the relay
 * that document names. Without a stored document nothing is sent.
 */

import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { PoweurClient } from "../src/client.js";
import { Contacts } from "../src/contacts.js";
import { generateSigningKeypair } from "../src/crypto/index.js";
import { generateIdentityKeys, signerFor } from "../src/crypto/keys.js";
import { MemoryDocumentStore } from "../src/document-store.js";
import { signDocumentWithKey } from "../src/document.js";
import { toBase64url } from "../src/encoding.js";
import { RelayClient } from "../src/http.js";
import { Messaging } from "../src/messages.js";
import { configuredMaxStaleMs, loadConfig, parseGoDuration, saveConfig } from "../src/node/config.js";
import type { SystemFiles } from "../src/systemfiles.js";
import type { IdentityDocument } from "../src/types.js";

const BOB = "bob.example.org";
const DAY_MS = 24 * 3600 * 1000;

function bobDocument() {
  const { privateKey, publicKey } = generateSigningKeypair();
  const document = signDocumentWithKey(
    {
      version: 1,
      identity: BOB,
      public_key: `ed25519:${toBase64url(publicKey)}`,
      encryption_public_key: `x25519:${toBase64url(new Uint8Array(32).fill(7))}`,
      relay: "relay.example.org",
      capabilities: ["messaging"],
      updated_at: "2026-01-15T09:30:00Z",
    } as IdentityDocument,
    privateKey,
  );
  return document;
}

const down = (async () => {
  throw new TypeError("fetch failed");
}) as unknown as typeof globalThis.fetch;

/** A relay that accepts every message and remembers where it was posted. */
function acceptingRelay() {
  const posts: { url: string; body: Record<string, unknown> }[] = [];
  const fetch = (async (url: string, init: RequestInit) => {
    posts.push({ url: String(url), body: JSON.parse(String(init.body)) as Record<string, unknown> });
    return new Response("{}", { status: 202 });
  }) as unknown as typeof globalThis.fetch;
  return { posts, fetch };
}

async function seededStore(document: IdentityDocument, fetchedAt = Date.now() - DAY_MS) {
  const store = new MemoryDocumentStore();
  await store.put(BOB, JSON.stringify(document), fetchedAt);
  return store;
}

function messagingFor(store: MemoryDocumentStore, relayFetch: typeof globalThis.fetch, maxStaleMs: number) {
  return new Messaging({
    client: new RelayClient("https://home.example.net", { fetch: relayFetch }),
    resolve: { fetch: down, store },
    maxStaleMs,
    clientFor: (url) => new RelayClient(url, { fetch: relayFetch }),
  });
}

const alice = signerFor(generateIdentityKeys("alice.example.net")).signer;

describe("Messaging.send with a stored identity", () => {
  it("encrypts to the stored document and posts to its relay", async () => {
    const relay = acceptingRelay();
    const fetchedAt = Date.now() - DAY_MS;
    const messaging = messagingFor(await seededStore(bobDocument(), fetchedAt), relay.fetch, 30 * DAY_MS);

    const result = await messaging.send(alice, BOB, "hello during the outage", { signWith: "identity" });

    expect(result.stale).toBe(true);
    expect(result.identityFetchedAt).toBe(fetchedAt);
    expect(result.targetRelay).toBe("https://relay.example.org");
    expect(relay.posts).toHaveLength(1);
    expect(relay.posts[0]!.url).toBe("https://relay.example.org/messages");
    expect(relay.posts[0]!.body["recipient"]).toBe(BOB);
    // Encrypted, never the plaintext.
    expect(JSON.stringify(relay.posts[0]!.body)).not.toContain("hello during the outage");
    expect(relay.posts[0]!.body["encryption"]).toBeTruthy();
  });

  it("is not marked stale when the identity resolves", async () => {
    const relay = acceptingRelay();
    const document = bobDocument();
    const messaging = new Messaging({
      client: new RelayClient("https://home.example.net", { fetch: relay.fetch }),
      resolve: {
        fetch: (async () => new Response(JSON.stringify(document), { status: 200 })) as unknown as typeof globalThis.fetch,
        store: new MemoryDocumentStore(),
      },
      maxStaleMs: 30 * DAY_MS,
      clientFor: (url) => new RelayClient(url, { fetch: relay.fetch }),
    });
    const result = await messaging.send(alice, BOB, "hello", { signWith: "identity" });
    expect(result.stale).toBeUndefined();
    expect(relay.posts).toHaveLength(1);
  });

  it("sends nothing when the stored document is too old", async () => {
    const relay = acceptingRelay();
    const store = await seededStore(bobDocument(), Date.now() - 31 * DAY_MS);
    await expect(
      messagingFor(store, relay.fetch, 30 * DAY_MS).send(alice, BOB, "hello", { signWith: "identity" }),
    ).rejects.toMatchObject({ code: "resolve_failed", message: expect.stringContaining(`no cached identity for ${BOB}`) });
    expect(relay.posts).toHaveLength(0);
  });

  it("sends nothing with maxStaleMs 0, even with a stored document", async () => {
    const relay = acceptingRelay();
    await expect(
      messagingFor(await seededStore(bobDocument()), relay.fetch, 0).send(alice, BOB, "hello", { signWith: "identity" }),
    ).rejects.toMatchObject({ code: "resolve_failed" });
    expect(relay.posts).toHaveLength(0);
  });
});

describe("PoweurClient with a documentStore", () => {
  it("sends from the store by default and keeps other resolves fresh", async () => {
    const relay = acceptingRelay();
    const store = await seededStore(bobDocument());
    const client = new PoweurClient({
      relayUrl: "https://home.example.net",
      signer: alice,
      fetch: relay.fetch,
      resolve: { fetch: down },
      documentStore: store,
    });
    // The store rides on every resolve, the stale limit does not.
    expect(client.resolveOptions.store).toBe(store);
    expect(client.resolveOptions.maxStaleMs).toBeUndefined();
    // Pinning a key must be a fresh answer.
    await expect(client.contacts().then((c) => c.resolvePin(BOB))).rejects.toMatchObject({
      code: "resolve_failed",
    });
  });
});

describe("Contacts.checkPin with a stored identity", () => {
  function contactsWith(pinnedKey: string, store: MemoryDocumentStore, maxStaleMs: number) {
    const files = {
      readOptional: async () =>
        JSON.stringify({ version: 1, contacts: [{ identity: BOB, state: "accepted", pinned_key: pinnedKey }] }),
    } as unknown as SystemFiles;
    return new Contacts(files, { fetch: down, store }, maxStaleMs);
  }

  it("compares the pin against the stored document during an outage", async () => {
    const document = bobDocument();
    const store = await seededStore(document);
    expect((await contactsWith(document.public_key, store, 30 * DAY_MS).checkPin(BOB)).status).toBe("ok");
    const other = bobDocument();
    expect((await contactsWith(other.public_key, store, 30 * DAY_MS).checkPin(BOB)).status).toBe("mismatch");
  });

  it("still fails without a stale limit", async () => {
    const document = bobDocument();
    await expect(
      contactsWith(document.public_key, await seededStore(document), 0).checkPin(BOB),
    ).rejects.toMatchObject({ code: "resolve_failed" });
  });
});

describe("max_stale in ~/.poweur/config.toml", () => {
  let home: string;
  const saved = { home: process.env["POWEUR_HOME"], env: process.env["POWEUR_MAX_STALE"] };
  beforeEach(() => {
    home = mkdtempSync(join(tmpdir(), "poweur-maxstale-"));
    process.env["POWEUR_HOME"] = home;
    delete process.env["POWEUR_MAX_STALE"];
  });
  afterEach(() => {
    rmSync(home, { recursive: true, force: true });
    if (saved.home === undefined) delete process.env["POWEUR_HOME"];
    else process.env["POWEUR_HOME"] = saved.home;
    if (saved.env === undefined) delete process.env["POWEUR_MAX_STALE"];
    else process.env["POWEUR_MAX_STALE"] = saved.env;
  });

  it("parses Go durations", () => {
    expect(parseGoDuration("0")).toBe(0);
    expect(parseGoDuration("720h")).toBe(720 * 3600 * 1000);
    expect(parseGoDuration("1h30m")).toBe(90 * 60 * 1000);
    expect(parseGoDuration("45s")).toBe(45_000);
    for (const bad of ["", "soon", "30", "-5h", "5 h", "5d"]) expect(parseGoDuration(bad)).toBeNull();
  });

  it("survives a save and gives way to POWEUR_MAX_STALE", () => {
    const config = loadConfig();
    expect(configuredMaxStaleMs(config)).toBeUndefined();
    saveConfig({ ...config, identity: "alice.example.net", max_stale: "48h" });
    const reloaded = loadConfig();
    expect(reloaded.max_stale).toBe("48h");
    expect(configuredMaxStaleMs(reloaded)).toBe(48 * 3600 * 1000);
    // A save by code that knows nothing of the setting keeps it.
    saveConfig(reloaded);
    expect(loadConfig().max_stale).toBe("48h");

    process.env["POWEUR_MAX_STALE"] = "0";
    expect(configuredMaxStaleMs(reloaded)).toBe(0);
    process.env["POWEUR_MAX_STALE"] = "whenever";
    expect(() => configuredMaxStaleMs(reloaded)).toThrow(/POWEUR_MAX_STALE/);
  });
});
