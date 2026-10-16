import { describe, expect, it, vi } from "vitest";

const stream = vi.hoisted(() => ({ handlers: null as any }));
vi.mock("@poweur/client", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  streamForever: vi.fn(async (_relay: unknown, _signer: unknown, handlers: any) => {
    stream.handlers = handlers;
    await new Promise<void>((resolve) => handlers.signal?.addEventListener("abort", () => resolve()));
  }),
}));

import { isRateLimited, sendWithRetry, startPoweurBus, toInbound } from "../src/bus.js";
import { looksLikePoweurId, normalizePoweurId, resolvePoweurAccount } from "../src/config.js";

const msg = (id: string, extra: Record<string, unknown> = {}) => ({
  id, sender: "Alice.Poweur.net", recipient: "agent.poweur.net", timestamp: "2026-10-16T10:00:00Z",
  payload: "", signature: "", plaintext: `hello ${id}`, ...extra,
});

function fakeClient(batches: any[][]) {
  return {
    identityName: "agent.poweur.net",
    relay: {}, signer: { identity: "agent.poweur.net" }, decryptor: {},
    messages: {
      inbox: vi.fn(async () => ({ messages: batches.shift() ?? [], cursor: "c1" })),
      consume: vi.fn(async () => ({ consumed: 1, acks_consumed: 0, pending: 0 })),
    },
    send: vi.fn(async () => ({ message: { id: "out-1" } })),
  } as any;
}

const settle = () => new Promise((r) => setTimeout(r, 10));

describe("toInbound", () => {
  it("passes chat text and skips typed, system and empty messages", () => {
    expect(toInbound(msg("1") as any)).toMatchObject({ sender: "alice.poweur.net", text: "hello 1", messageId: "1" });
    expect(toInbound(msg("2", { type: "chat.text", thread_id: "t1", subject: "Plan" }) as any)).toMatchObject({ threadId: "t1", subject: "Plan" });
    expect(toInbound(msg("3", { type: "sys.contact.request" }) as any)).toBeNull();
    expect(toInbound(msg("4", { type: "email.message" }) as any)).toBeNull();
    expect(toInbound(msg("5", { plaintext: null }) as any)).toBeNull();
  });
});

describe("startPoweurBus", () => {
  it("hands each message over, then consumes; a failure leaves the inbox for the next pickup", async () => {
    const client = fakeClient([[msg("a"), msg("b", { type: "sys.share.offer" })], [msg("c")]]);
    const seen: string[] = [];
    const controller = new AbortController();
    let fail = false;
    const bus = startPoweurBus({
      client, signal: controller.signal,
      onMessage: async (m) => {
        if (fail) throw new Error("agent crashed");
        seen.push(m.messageId);
      },
    });
    stream.handlers.onOpen();
    await settle();
    expect(seen).toEqual(["a"]);
    expect(client.messages.consume).toHaveBeenCalledTimes(1);

    fail = true;
    stream.handlers.onEvent({ type: "message" });
    await settle();
    expect(client.messages.consume).toHaveBeenCalledTimes(1);

    expect(await bus.sendText("alice.poweur.net", "hi", { threadId: "t1" })).toBe("out-1");
    expect(client.send).toHaveBeenCalledWith("alice.poweur.net", "hi", { threadId: "t1" });
    controller.abort();
    await bus.done;
  });
});

describe("config", () => {
  it("normalizes and recognizes Poweur IDs", () => {
    expect(normalizePoweurId(" poweur:Alice.Poweur.NET. ")).toBe("alice.poweur.net");
    expect(normalizePoweurId("https://alice.poweur.net/")).toBe("alice.poweur.net");
    expect(looksLikePoweurId("alice.poweur.net")).toBe(true);
    expect(looksLikePoweurId("alice")).toBe(false);
    expect(looksLikePoweurId("*")).toBe(false);
  });

  it("is configured only with a valid identity", () => {
    expect(resolvePoweurAccount({ channels: {} }).configured).toBe(false);
    expect(resolvePoweurAccount({ channels: { poweur: { identity: "Agent.Poweur.net" } } })).toMatchObject({ configured: true, identity: "agent.poweur.net", enabled: true });
    expect(resolvePoweurAccount({ channels: { poweur: { identity: "agent.poweur.net", enabled: false } } }).enabled).toBe(false);
  });
});

describe("sendWithRetry", () => {
  const limited = () => Object.assign(new Error("rate_limit_exceeded"), { status: 429, relayCode: "rate_limit_exceeded" });

  it("waits out a burst with growing delays, then sends", async () => {
    const waits: number[] = [];
    const send = vi.fn().mockRejectedValueOnce(limited()).mockRejectedValueOnce(limited()).mockResolvedValue("ok");
    await expect(sendWithRetry(send, { sleep: async (ms) => void waits.push(ms) })).resolves.toBe("ok");
    expect(waits).toEqual([3000, 6000]);
  });

  it("gives up when the limit outlasts the budget, and never retries other errors", async () => {
    const waits: number[] = [];
    await expect(sendWithRetry(async () => { throw limited(); }, { maxWaitMs: 20_000, sleep: async (ms) => void waits.push(ms) })).rejects.toThrow("rate_limit");
    expect(waits).toEqual([3000, 6000]);
    const send = vi.fn(async () => { throw Object.assign(new Error("boom"), { status: 500 }); });
    await expect(sendWithRetry(send, { sleep: async () => {} })).rejects.toThrow("boom");
    expect(send).toHaveBeenCalledTimes(1);
    expect(isRateLimited({ status: 429 })).toBe(true);
    expect(isRateLimited(new Error("x"))).toBe(false);
  });
});
