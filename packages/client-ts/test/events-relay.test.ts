/**
 * Cursor pickup and push notifications against a real relay (EPIC-009 T1/T2).
 *
 * Both halves of the same idea: the relay keeps a message until the recipient
 * says they have it, and tells them the moment it arrives. The stream is only
 * a cue — every assertion about *delivery* here goes through the cursor read,
 * which is exactly how a client is meant to use it.
 */

import { afterAll, beforeAll, describe, expect, it } from "vitest";

import { streamEvents, streamForever, type StreamEvent } from "../src/events.js";
import { createTestIdentity, type TestIdentity } from "./helpers/identities.js";
import { startRelay, type RunningRelay } from "./helpers/relay.js";

describe("push notifications and cursor pickup", () => {
  let relay: RunningRelay;
  let alice: TestIdentity;
  let bob: TestIdentity;

  beforeAll(async () => {
    relay = await startRelay();
    alice = await createTestIdentity(relay.baseUrl, "evalice");
    bob = await createTestIdentity(relay.baseUrl, "evbob");
  }, 180_000);

  afterAll(() => relay?.stop());

  it("keeps messages until the recipient says it has them", async () => {
    await alice.client.send(bob.identity, "one");

    const first = await bob.client.messages.inbox(bob.client.signer, bob.client.decryptor, { since: "" });
    expect(first.messages.map((m) => m.plaintext)).toContain("one");
    expect(first.cursor).toBeTruthy();

    // Read again without acknowledging: still there. A drain-on-read pickup
    // would have lost this message to any dropped connection.
    const again = await bob.client.messages.inbox(bob.client.signer, bob.client.decryptor, { since: "" });
    expect(again.messages).toHaveLength(first.messages.length);

    const consumed = await bob.client.messages.consume(bob.client.signer, { through: first.cursor! });
    expect(consumed.consumed).toBeGreaterThan(0);

    const after = await bob.client.messages.inbox(bob.client.signer, bob.client.decryptor, { since: "" });
    expect(after.messages).toHaveLength(0);
  });

  it("only forgets through the cursor the client actually saw", async () => {
    await alice.client.send(bob.identity, "two");
    const seen = await bob.client.messages.inbox(bob.client.signer, bob.client.decryptor, { since: "" });
    // A message that lands *after* the read must survive the acknowledgement.
    await alice.client.send(bob.identity, "three");
    await bob.client.messages.consume(bob.client.signer, { through: seen.cursor! });

    const left = await bob.client.messages.inbox(bob.client.signer, bob.client.decryptor, { since: "" });
    expect(left.messages.map((m) => m.plaintext)).toEqual(["three"]);
    await bob.client.messages.consume(bob.client.signer, { through: left.cursor! });
  });

  it("notifies an open stream when a message arrives", async () => {
    const events: StreamEvent[] = [];
    const controller = new AbortController();

    const ready = new Promise<void>((resolve) => {
      void streamEvents(bob.client.relay, bob.client.signer, {
        signal: controller.signal,
        onOpen: resolve,
        onEvent: (event) => events.push(event),
      }).catch(() => {});
    });
    await ready;

    await alice.client.send(bob.identity, "pushed");
    await waitFor(() => events.some((e) => e.type === "message"));

    // The notification is a cue, not a delivery: it carries no payload, and
    // the message is still waiting to be picked up.
    const notification = events.find((e) => e.type === "message")!;
    expect(notification.message_id).toBeTruthy();
    expect(JSON.stringify(notification)).not.toContain("pushed");

    const inbox = await bob.client.messages.inbox(bob.client.signer, bob.client.decryptor, { since: "" });
    expect(inbox.messages.map((m) => m.plaintext)).toContain("pushed");
    await bob.client.messages.consume(bob.client.signer, { through: inbox.cursor! });

    controller.abort();
  }, 60_000);

  it("reconnects on its own, and a gap costs nothing", async () => {
    const opens: number[] = [];
    const events: StreamEvent[] = [];
    const controller = new AbortController();

    void streamForever(bob.client.relay, bob.client.signer, {
      signal: controller.signal,
      baseDelayMs: 50,
      onOpen: () => opens.push(Date.now()),
      onEvent: (event) => events.push(event),
    });
    await waitFor(() => opens.length === 1);

    // Everything sent while nobody is listening is still there afterwards:
    // the stream carries no state, the spool does.
    controller.abort();
    await alice.client.send(bob.identity, "while offline");

    const resumed = new AbortController();
    void streamForever(bob.client.relay, bob.client.signer, {
      signal: resumed.signal,
      baseDelayMs: 50,
      onEvent: (event) => events.push(event),
    });
    const inbox = await bob.client.messages.inbox(bob.client.signer, bob.client.decryptor, { since: "" });
    expect(inbox.messages.map((m) => m.plaintext)).toContain("while offline");
    await bob.client.messages.consume(bob.client.signer, { through: inbox.cursor! });
    resumed.abort();
  }, 60_000);
});

async function waitFor(predicate: () => boolean, timeoutMs = 15_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (predicate()) return;
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error("condition never became true");
}
