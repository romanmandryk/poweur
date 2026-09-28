import { describe, expect, it } from "vitest";
import { DriveClient } from "../src/drive/client.js";
import { RelayClient, type FetchLike } from "../src/http.js";
import { chunkID } from "../src/drive/crypto.js";

const signer = { identity: "alice.poweur.net", publicKey: "unused", sign: async (value: string) => value };
function fixture(handler: (path: string, init: RequestInit) => Response | Promise<Response>) {
  let challenges = 0;
  const fetch: FetchLike = async (url, init) => {
    const path = new URL(String(url)).pathname;
    if (path === "/auth/challenge") return Response.json({ challenge: String(++challenges) });
    return handler(path, init ?? {});
  };
  return { client: new DriveClient(new RelayClient("https://relay.example", { fetch }), signer), challenges: () => challenges };
}

describe("drive transport", () => {
  it("retries an ambiguous commit with identical bytes and fresh authentication", async () => {
    const bodies: string[] = [], challenges: string[] = [];
    const { client } = fixture((_path, init) => {
      bodies.push(String(init.body));
      challenges.push(new Headers(init.headers).get("X-Poweur-Challenge")!);
      if (bodies.length === 1) throw new TypeError("connection lost after commit");
      return Response.json({ seq: 1, head: "head", positions: null });
    });
    expect(await client.commit({ unshare: { id: "share" } })).toEqual({ seq: 1, head: "head", positions: null });
    expect(bodies[0]).toBe(bodies[1]);
    expect(JSON.parse(bodies[0]!).id).toMatch(/^[0-9a-f]{32}$/);
    expect(challenges).toEqual(["1", "2"]);
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
    const { client } = fixture((path, init) => {
      if (init.method === "PUT") { expect(path).toContain(chunkID(data)); expect(init.body).toEqual(data); return Response.json({}); }
      if (path.endsWith("children")) return Response.json({ children: [], cursor: "" });
      if (path.endsWith("changes")) return Response.json({ changes: [], cursor: "8" });
      if (path.endsWith("records")) return Response.json({ records: [], next: 9 });
      return Response.json({ drive: signer.identity, root: "", used: 0, quota: 1024 });
    });
    expect(await client.upload(data)).toEqual({ id: chunkID(data), size: 1 });
    expect((await client.info()).quota).toBe(1024);
    expect((await client.children("node")).children).toEqual([]);
    expect((await client.changes("7")).cursor).toBe("8");
    expect((await client.records("node", 8)).next).toBe(9);
  });
  it("rejects a page belonging to another node", async () => {
    const { client } = fixture(() => Response.json({ drive: signer.identity, node: "wrong" }));
    await expect(client.page("node", "version", "hash")).rejects.toThrow("invalid chunk page");
  });
});
