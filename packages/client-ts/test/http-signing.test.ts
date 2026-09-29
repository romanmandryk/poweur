import { describe, expect, it } from "vitest";
import { RelayClient, type FetchLike } from "../src/http.js";

const signer = { identity: "alice.poweur.net", publicKey: "unused", sign: async (value: string) => Buffer.from(value).toString("base64") };
const signed = (init?: RequestInit) => {
  const headers = new Headers(init?.headers);
  return { timestamp: Number(headers.get("X-Poweur-Timestamp")), message: Buffer.from(headers.get("X-Poweur-Signature") ?? "", "base64").toString() };
};

describe("signed requests", () => {
  it("sign method, path, query, time, nonce and body", async () => {
    let seen: RequestInit | undefined;
    const fetch: FetchLike = async (_url, init) => { seen = init; return Response.json({ ok: true }); };
    await new RelayClient("https://relay.example", { fetch }).request({ method: "POST", path: "/drive/alice.poweur.net/commit?x=1", body: { a: 1 }, sign: { signer, sessionId: "s1" } });
    const lines = signed(seen).message.split("\n");
    expect(lines.slice(0, 4)).toEqual(["poweur-request/v1", "alice.poweur.net", "POST", "/drive/alice.poweur.net/commit?x=1"]);
    expect(lines[5]).toMatch(/^[0-9a-f]{32}$/);
    // SHA-256 of the exact bytes sent.
    expect(lines[6]).toBe("015abd7f5cc57a2dd94b7590f04ad8084273905ee33ec5cebeae62276a97f862");
    expect(new Headers(seen!.headers).get("X-Poweur-Session-Id")).toBe("s1");
  });

  it("re-signs once with the relay's clock when ours is off", async () => {
    const relayNow = Date.now() + 10 * 60_000;
    const stamps: number[] = [];
    const fetch: FetchLike = async (_url, init) => {
      const { timestamp } = signed(init);
      stamps.push(timestamp);
      const fresh = Math.abs(timestamp * 1000 - relayNow) < 60_000;
      return Response.json(fresh ? { ok: true } : { error: "unauthorized" }, { status: fresh ? 200 : 401, headers: { Date: new Date(relayNow).toUTCString() } });
    };
    const relay = new RelayClient("https://relay.example", { fetch });
    expect(await relay.request({ method: "GET", path: "/drive/alice.poweur.net", sign: { signer } })).toEqual({ ok: true });
    expect(stamps.length).toBe(2);
    // Later requests use the learned offset straight away.
    await relay.request({ method: "GET", path: "/drive/alice.poweur.net", sign: { signer } });
    expect(stamps.length).toBe(3);
  });
});
