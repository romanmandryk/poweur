/**
 * Last-known-good identity store (EPIC-029 E29-T1). The cases come from Go
 * (`packages/identity/store_test.go` writes `resolve-store.json` and runs the
 * same list), so both resolvers agree on when a stored document may stand in.
 */

import { mkdtempSync, readdirSync, readFileSync, rmSync, statSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { IndexedDbDocumentStore } from "../src/browser/document-store.js";
import { MemoryDocumentStore, readOnlyDocumentStore } from "../src/document-store.js";
import { parseDocument } from "../src/document.js";
import { PoweurError } from "../src/errors.js";
import { FileDocumentStore } from "../src/node/document-store.js";
import { ResolveCache, resolveIdentity } from "../src/resolve.js";
import type { IdentityDocument } from "../src/types.js";
import { loadVectors } from "./vectors.js";

interface StoreCase {
  name: string;
  stored?: { document: IdentityDocument; fetched_at: string };
  web: { network_error?: boolean; status?: number; document?: IdentityDocument };
  dns_public_key?: string;
  max_stale_seconds: number;
  expect: {
    outcome: "fresh" | "stale" | "error";
    source?: string;
    error?: "rollback" | "key_mismatch" | "failed";
    updated_at?: string;
    stored_updated_at?: string;
  };
}

interface StoreVectors {
  identity: string;
  now: string;
  cases: StoreCase[];
}

const vectors = loadVectors<StoreVectors>("resolve-store");

function webFetch(web: StoreCase["web"]): typeof globalThis.fetch {
  return (async () => {
    if (web.network_error) throw new TypeError("fetch failed");
    return new Response(web.document ? JSON.stringify(web.document) : "", { status: web.status });
  }) as unknown as typeof globalThis.fetch;
}

const down = webFetch({ network_error: true });

function errorClass(error: unknown): string {
  if (error instanceof PoweurError && error.code === "document_rollback") return "rollback";
  if (error instanceof PoweurError && error.code === "key_mismatch") return "key_mismatch";
  return "failed";
}

describe("shared store vectors", () => {
  for (const tc of vectors.cases) {
    it(tc.name, async () => {
      const store = new MemoryDocumentStore();
      if (tc.stored) {
        await store.put(
          vectors.identity,
          JSON.stringify(tc.stored.document),
          Date.parse(tc.stored.fetched_at),
        );
      }
      const dnsKey = tc.dns_public_key;
      const attempt = resolveIdentity(vectors.identity, {
        fetch: webFetch(tc.web),
        store,
        maxStaleMs: tc.max_stale_seconds * 1000,
        now: () => Date.parse(vectors.now),
        txt: dnsKey
          ? {
              async lookupTxt(name: string) {
                if (name === `_poweur.${vectors.identity}`) return [`poweur-pubkey=${dnsKey}`];
                throw new Error("no such host");
              },
            }
          : null,
      });

      if (tc.expect.outcome === "error") {
        const error = await attempt.then(
          () => null,
          (e: unknown) => e,
        );
        expect(error).not.toBeNull();
        expect(errorClass(error)).toBe(tc.expect.error);
      } else {
        const result = await attempt;
        expect(result.stale === true).toBe(tc.expect.outcome === "stale");
        expect(result.source).toBe(tc.expect.source);
        if (tc.expect.updated_at) expect(result.document.updated_at).toBe(tc.expect.updated_at);
        if (result.stale) expect(result.fetchedAt).toBe(Date.parse(tc.stored!.fetched_at));
      }

      const after = await store.get(vectors.identity);
      const storedUpdatedAt = after ? parseDocument(after.raw, true).updated_at : undefined;
      expect(storedUpdatedAt).toBe(tc.expect.stored_updated_at);
    });
  }
});

const goodCase = vectors.cases.find((c) => c.name === "first fetch is stored")!;
const goodDocument = goodCase.web.document!;
const DAY_MS = 24 * 3600 * 1000;

describe("resolver and store", () => {
  it("does not trust a stored document that no longer verifies", async () => {
    const store = new MemoryDocumentStore();
    await store.put(
      vectors.identity,
      JSON.stringify({ ...goodDocument, relay: "evil.example.org" }),
      Date.now(),
    );
    await expect(
      resolveIdentity(vectors.identity, { fetch: down, store, maxStaleMs: DAY_MS }),
    ).rejects.toMatchObject({ code: "resolve_failed" });
  });

  it("caches a stale result briefly instead of refetching a dead host", async () => {
    const store = new MemoryDocumentStore();
    await store.put(vectors.identity, JSON.stringify(goodDocument), Date.now());
    let calls = 0;
    const fetch = (async () => {
      calls += 1;
      throw new TypeError("fetch failed");
    }) as unknown as typeof globalThis.fetch;
    const options = { fetch, store, maxStaleMs: DAY_MS, cache: new ResolveCache() };
    for (let i = 0; i < 3; i += 1) {
      expect((await resolveIdentity(vectors.identity, options)).stale).toBe(true);
    }
    expect(calls).toBe(1);
  });

  it("a read-only store falls back but is never written", async () => {
    const store = new MemoryDocumentStore();
    const options = { store: readOnlyDocumentStore(store), maxStaleMs: DAY_MS };
    const fresh = await resolveIdentity(vectors.identity, { ...options, fetch: webFetch(goodCase.web) });
    expect(await store.get(vectors.identity)).toBeNull();
    await store.put(vectors.identity, fresh.raw!, fresh.fetchedAt!);
    expect((await resolveIdentity(vectors.identity, { ...options, fetch: down })).stale).toBe(true);
  });

  it("a store that throws never fails a good answer", async () => {
    const broken = {
      get: async () => {
        throw new Error("store unavailable");
      },
      put: async () => {
        throw new Error("store unavailable");
      },
    };
    const result = await resolveIdentity(vectors.identity, {
      fetch: webFetch(goodCase.web),
      store: broken,
      maxStaleMs: DAY_MS,
    });
    expect(result.source).toBe("web");
  });

  describe("with a relay candidate (browser)", () => {
    const relayUrl = "https://relay.example.net";
    function routed(own: () => Response, relay: () => Response): typeof globalThis.fetch {
      return (async (url: string) =>
        String(url).startsWith(relayUrl) ? relay() : own()) as unknown as typeof globalThis.fetch;
    }
    const unreachable = (): Response => {
      throw new TypeError("fetch failed");
    };
    async function seeded() {
      const store = new MemoryDocumentStore();
      await store.put(vectors.identity, JSON.stringify(goodDocument), Date.now());
      return store;
    }

    it("own host down and the relay doesn't host the identity: stale", async () => {
      const result = await resolveIdentity(vectors.identity, {
        fetch: routed(unreachable, () => new Response("", { status: 404 })),
        relayUrl,
        store: await seeded(),
        maxStaleMs: DAY_MS,
      });
      expect(result.stale).toBe(true);
    });

    it("own host answers 404 and the relay is down: not stale", async () => {
      await expect(
        resolveIdentity(vectors.identity, {
          fetch: routed(() => new Response("", { status: 404 }), unreachable),
          relayUrl,
          store: await seeded(),
          maxStaleMs: DAY_MS,
        }),
      ).rejects.toMatchObject({ code: "resolve_failed" });
    });

    it("own host down and the relay serves a bad signature: not stale", async () => {
      const tampered = JSON.stringify({ ...goodDocument, relay: "evil.example.org" });
      await expect(
        resolveIdentity(vectors.identity, {
          fetch: routed(unreachable, () => new Response(tampered, { status: 200 })),
          relayUrl,
          store: await seeded(),
          maxStaleMs: DAY_MS,
        }),
      ).rejects.toMatchObject({ code: "resolve_failed" });
    });
  });
});

describe("FileDocumentStore", () => {
  let dir: string;
  beforeEach(() => {
    dir = mkdtempSync(join(tmpdir(), "poweur-docstore-"));
  });
  afterEach(() => {
    rmSync(dir, { recursive: true, force: true });
  });

  it("round-trips in the format the Go store writes, keeping unknown fields", async () => {
    const store = new FileDocumentStore(join(dir, "identities"));
    const raw = JSON.stringify({ ...goodDocument, relay_set: { seq: 3 } });
    const at = Date.parse("2026-01-15T09:30:00Z");
    await store.put("Alice.Example.org", raw, at);

    const path = join(dir, "identities", "alice.example.org.json");
    const file = JSON.parse(readFileSync(path, "utf8")) as Record<string, unknown>;
    expect(file["identity"]).toBe("alice.example.org");
    expect(file["fetched_at"]).toBe("2026-01-15T09:30:00Z");
    expect(statSync(path).mode & 0o777).toBe(0o600);
    expect(readdirSync(join(dir, "identities"))).toEqual(["alice.example.org.json"]);

    const got = await store.get("alice.example.org");
    expect(got?.fetchedAt).toBe(at);
    expect(JSON.parse(got!.raw)).toMatchObject({ relay_set: { seq: 3 } });
  });

  it("returns nothing for missing, corrupt or unsafe names", async () => {
    const store = new FileDocumentStore(dir);
    expect(await store.get("alice.example.org")).toBeNull();
    expect(await store.get("../etc/passwd")).toBeNull();
    await expect(store.put("../etc/passwd", "{}", Date.now())).rejects.toThrow();
  });
});

describe("IndexedDbDocumentStore", () => {
  it("keeps documents in memory when IndexedDB is unavailable", async () => {
    // Node has no indexedDB: opening it throws, as it does in a browser that
    // blocks site data.
    const store = new IndexedDbDocumentStore();
    await store.put("alice.example.org", "{}", 42);
    expect(await store.get("Alice.example.org")).toEqual({
      identity: "alice.example.org",
      raw: "{}",
      fetchedAt: 42,
    });
    expect(await store.get("bob.example.org")).toBeNull();
  });
});
