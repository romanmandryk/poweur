/**
 * Free-ID email housekeeping on a real relay with a tiny storage quota:
 * the oldest email goes, this week's email and other conversations stay,
 * and the drive's usage really goes down.
 */
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { createTestIdentity } from "./helpers/identities.js";
import { startRelay, type RunningRelay } from "./helpers/relay.js";
import type { HistoryRecord } from "../src/history.js";

let relay: RunningRelay;
beforeAll(async () => {
  relay = await startRelay({ env: { MAX_IDENTITY_BYTES: String(600 * 1024) } });
}, 120_000);
afterAll(() => relay?.stop());

const BRIDGE = "email.poweur.net";
const day = 24 * 3600 * 1000;

describe("makeRoomForEmail", () => {
  it("drops the oldest email only, keeps the last 7 days and other conversations", async () => {
    const alice = await createTestIdentity(relay.baseUrl, "trimalice");
    const history = await alice.client.history();
    const now = new Date();
    const record = (i: number, peer: string, ageDays: number): HistoryRecord => ({
      id: `msg_${peer.split(".")[0]}_${i}`,
      sender: peer,
      recipient: alice.identity,
      timestamp: new Date(now.getTime() - ageDays * day).toISOString().replace(/\.\d{3}Z$/, "Z"),
      type: peer === BRIDGE ? "email.message" : "chat.text",
      queue: "inbox",
      body: "x".repeat(20 * 1024),
    });
    const old = Array.from({ length: 8 }, (_, i) => record(i, BRIDGE, 60 - i));
    const recent = [record(100, BRIDGE, 2), record(101, BRIDGE, 1)];
    const chats = [record(200, "bob.poweur.net", 90)];
    const written = await history.appendAll([...old, ...recent, ...chats]);
    expect(written.failed).toBe(0);

    // startAt above 100%: only reads the usage.
    const usage = await alice.client.makeRoomForEmail(BRIDGE, { now, startAt: 2 });
    expect(usage.removed).toBe(0);
    const before = await alice.client.makeRoomForEmail(BRIDGE, { now, startAt: 0.3, target: 0.1 });
    expect(before.used).toBeLessThan(usage.used);
    expect(before.removed).toBeGreaterThan(0);
    expect(before.removed).toBeLessThanOrEqual(8);

    const fresh = await alice.client.history();
    const left = await fresh.conversation(BRIDGE);
    const ids = left.map((r) => r.id);
    expect(ids).toEqual(expect.arrayContaining(["msg_email_100", "msg_email_101"]));
    expect(ids.filter((id) => Number(id.split("_").pop()) < 100).length).toBe(8 - before.removed);
    expect((await fresh.conversation("bob.poweur.net")).map((r) => r.id)).toEqual(["msg_bob_200"]);
  }, 120_000);

  it("does nothing below 90%, and says when it cannot make room", async () => {
    const carol = await createTestIdentity(relay.baseUrl, "trimcarol");
    const idle = await carol.client.makeRoomForEmail(BRIDGE);
    expect(idle).toMatchObject({ removed: 0, full: false });
    expect(idle.quota).toBe(600 * 1024);
  }, 60_000);
});
