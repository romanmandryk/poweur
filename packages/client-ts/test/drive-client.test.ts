import { describe, expect, it } from "vitest";
import { linkSecret } from "../src/drive/files.js";
import { IndexedDBChunkCache } from "../src/browser/drive-cache.js";
import { DriveClient } from "../src/drive/client.js";
import { RelayClient, type FetchLike } from "../src/http.js";
import { chunkID } from "../src/drive/crypto.js";

const signer = { identity: "alice.poweur.net", publicKey: "unused", sign: async (value: string) => Buffer.from(value).toString("base64") };
/** Counts authenticated requests: each is signed with a fresh nonce, and
 * none needs a challenge round trip. */
function fixture(handler: (path: string, init: RequestInit) => Response | Promise<Response>) {
  const nonces: string[] = [];
  const fetch: FetchLike = async (url, init) => {
    const path = new URL(String(url)).pathname;
    if (path === "/auth/challenge") throw new Error("signed requests need no challenge");
    const nonce = new Headers(init?.headers).get("X-Poweur-Nonce");
    if (nonce) nonces.push(nonce);
    return handler(path, init ?? {});
  };
  return { client: new DriveClient(new RelayClient("https://relay.example", { fetch }), signer), challenges: () => nonces.length, nonces };
}

describe("drive helpers", () => {
  it("mixes a link fragment with a password half and does not reuse the buffer", () => {
    const fragment = new Uint8Array(32).fill(1);
    const mixed = linkSecret(fragment, new Uint8Array(32).fill(2));
    expect(mixed[0]).toBe(3);
    expect(fragment[0]).toBe(1);
    expect(linkSecret(fragment)).toEqual(fragment);
  });
  it("rejects chunk ids before touching IndexedDB", async () => {
    const cache = new IndexedDBChunkCache();
    await expect(cache.get("nope")).rejects.toThrow("invalid chunk");
    await expect(cache.put("ab", new Uint8Array())).rejects.toThrow("invalid cached chunk");
  });
});

describe("drive transport", () => {
  it("retries an ambiguous commit with identical bytes and fresh authentication", async () => {
    const bodies: string[] = [];
    const { client, nonces } = fixture((_path, init) => {
      bodies.push(String(init.body));
      if (bodies.length === 1) throw new TypeError("connection lost after commit");
      return Response.json({ seq: 1, head: "head", positions: null });
    });
    expect(await client.commit({ unshare: { id: "share" } })).toEqual({ seq: 1, head: "head", positions: null });
    expect(bodies[0]).toBe(bodies[1]);
    expect(JSON.parse(bodies[0]!).id).toMatch(/^[0-9a-f]{32}$/);
    expect(nonces.length).toBe(2);
    expect(nonces[0]).not.toBe(nonces[1]);
  });
  it("surfaces head conflicts without retry or overwrite", async () => {
    const { client, challenges } = fixture(() => Response.json({ error: "conflict" }, { status: 409 }));
    await expect(client.commit({})).rejects.toMatchObject({ status: 409 });
    expect(challenges()).toBe(1);
  });
  it("bounds retries of unavailable relays", async () => {
    const { client, challenges } = fixture(() => Response.json({ error: "retry" }, { status: 503 }));
    await expect(client.commit({})).rejects.toMatchObject({ status: 503 });
    expect(challenges()).toBe(3);
  });
  it("validates downloaded ciphertext and rejects substitutions", async () => {
    const data = new Uint8Array([1, 2, 3]);
    const { client } = fixture(() => new Response(data));
    expect(await client.chunk("node", { id: chunkID(data), size: 3 }, "version")).toEqual(data);
    await expect(client.chunk("node", { id: "0".repeat(64), size: 3 })).rejects.toThrow("invalid chunk");
    await expect(client.chunk("node", { id: chunkID(data), size: 4 })).rejects.toThrow("invalid chunk");
  });
  it("does not trust corrupt cached chunks", async () => {
    const data = new Uint8Array([4, 5]);
    const { client, challenges } = fixture(() => new Response(data));
    let stored: Uint8Array | undefined;
    const cached = new DriveClient(client.relay, signer, signer.identity, {
      get: async () => new Uint8Array([8, 9]), put: async (_id, bytes) => { stored = bytes; },
    });
    expect(await cached.chunk("node", { id: chunkID(data), size: 2 })).toEqual(data);
    expect(stored).toEqual(data);
    expect(challenges()).toBe(1);
  });
  it("uploads content-addressed bytes and reads paginated APIs", async () => {
    const data = new Uint8Array([1]);
    let puts = 0;
    const { client } = fixture((path, init) => {
      if (path.endsWith("/chunks/missing")) return Response.json({ missing: [{ id: chunkID(data), size: 1, upload: { method: "PUT", url: `/drive/${signer.identity}/chunks/${chunkID(data)}` } }] });
      if (init.method === "PUT") { puts++; expect(path).toContain(chunkID(data)); expect(init.body).toEqual(data); return Response.json({}); }
      if (path.endsWith("children")) return Response.json({ children: [], cursor: "" });
      if (path.endsWith("changes")) return Response.json({ changes: [], cursor: "8" });
      if (path.endsWith("records")) return Response.json({ records: [], next: 9 });
      return Response.json({ drive: signer.identity, root: "", used: 0, quota: 1024 });
    });
    expect(await client.upload(data)).toEqual({ id: chunkID(data), size: 1 });
    expect(puts).toBe(1);
    expect((await client.info()).quota).toBe(1024);
    expect((await client.children("node")).children).toEqual([]);
    expect((await client.changes("7")).cursor).toBe("8");
    expect((await client.records("node", 8)).next).toBe(9);
  });
  it("does not upload a chunk the relay already stored", async () => {
    const data = new Uint8Array([1]);
    const { client } = fixture((_path, init) => {
      if (init.method === "PUT") throw new Error("uploaded a present chunk");
      return Response.json({ missing: [] });
    });
    expect(await client.store([data])).toEqual([{ id: chunkID(data), size: 1 }]);
  });
  it("sends a presigned upload without Poweur credentials", async () => {
    const data = new Uint8Array([7]);
    const { client } = fixture(async (path, init) => {
      if (path.endsWith("/chunks/missing")) {
        return Response.json({ missing: [{ id: chunkID(data), size: 1, upload: { method: "PUT", url: "https://bucket.example/object", headers: { "x-amz-checksum": "abc" } } }] });
      }
      const headers = new Headers(init.headers);
      expect(headers.get("X-Poweur-Identity")).toBeNull();
      expect(headers.get("x-amz-checksum")).toBe("abc");
      expect(path).toBe("/object");
      return new Response(null, { status: 200 });
    });
    expect((await client.upload(data)).id).toBe(chunkID(data));
  });
  it("reads drive events and ignores keepalive frames", async () => {
    const { client } = fixture(() => new Response(": padding\n\ndata: {\"type\":\"ready\",\"identity\":\"alice.poweur.net\",\"timestamp\":\"t\"}\n\ndata: {\"type\":\"drive.changed\",\"identity\":\"alice.poweur.net\",\"timestamp\":\"t\",\"drive\":{\"operation\":\"append\"}}\n\n"));
    const types: string[] = [];
    await client.subscribe(event => { types.push(event.type); });
    expect(types).toEqual(["ready", "drive.changed"]);
  });
  it("rejects a page belonging to another node", async () => {
    const { client } = fixture(() => Response.json({ drive: signer.identity, node: "wrong" }));
    await expect(client.page("node", "version", "hash")).rejects.toThrow("invalid chunk page");
  });
});
