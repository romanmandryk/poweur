/**
 * The contact lifecycle against a real Go relay: request → requests queue →
 * accept → mutual messaging, plus block.
 *
 * `PoweurClient.requestContact/acceptContact/blockContact` compose two things
 * that must stay in step — the contacts document in the owner's tree and a
 * typed message the recipient's relay routes on. A unit test with a fake relay
 * would assert the composition but not the routing, and the routing is the
 * half EPIC-007 actually specifies.
 */

import { afterAll, beforeAll, describe, expect, it } from "vitest";

import { PoweurError } from "../src/errors.js";
import {
  CONTACT_ACCEPTED,
  CONTACT_BLOCKED,
  CONTACT_REQUESTED,
  INBOX_OPEN,
  MSG_TYPE_CONTACT_ACCEPT,
  MSG_TYPE_CONTACT_REQUEST,
} from "../src/types.js";
import { createTestIdentity, type TestIdentity } from "./helpers/identities.js";
import { startRelay, type RunningRelay } from "./helpers/relay.js";

describe("contact requests ↔ real relay", () => {
  let relay: RunningRelay;
  let alice: TestIdentity;
  let bob: TestIdentity;

  beforeAll(async () => {
    relay = await startRelay();
    alice = await createTestIdentity(relay.baseUrl, "creqalice");
    bob = await createTestIdentity(relay.baseUrl, "creqbob");
  }, 180_000);

  afterAll(() => relay?.stop());

  it("requests, accepts, and pins both directions", async () => {
    const { contact, result } = await alice.client.requestContact(bob.identity, {
      intro: "hi, it's alice",
    });

    // Recorded locally as requested, pinned to the key we just addressed —
    // the TOFU moment is the request, not the reply.
    expect(contact.state).toBe(CONTACT_REQUESTED);
    expect(contact.pinned_key).toBe(bob.client.signer.publicKey);
    expect(result.message.type).toBe(MSG_TYPE_CONTACT_REQUEST);

    // Bob's policy is open (the default), so the request arrives as a typed
    // message in the inbox rather than in the requests queue.
    const inbox = await bob.client.inbox();
    const request = inbox.messages.find((m) => m.sender === alice.identity);
    expect(request?.type).toBe(MSG_TYPE_CONTACT_REQUEST);
    expect(request?.plaintext).toBe("hi, it's alice");

    const { contact: accepted, notified } = await bob.client.acceptContact(alice.identity, {
      petname: "Alice",
    });
    expect(accepted.state).toBe(CONTACT_ACCEPTED);
    expect(accepted.petname).toBe("Alice");
    expect(accepted.pinned_key).toBe(alice.client.signer.publicKey);
    expect(notified).toBe(true);

    const aliceInbox = await alice.client.inbox();
    expect(
      aliceInbox.messages.some(
        (m) => m.sender === bob.identity && m.type === MSG_TYPE_CONTACT_ACCEPT,
      ),
    ).toBe(true);

    // Both pins agree with what resolves now.
    expect((await (await bob.client.contacts()).checkPin(alice.identity)).status).toBe("ok");
    expect((await (await alice.client.contacts()).checkPin(bob.identity)).status).toBe("ok");
  });

  it("refuses to re-request someone already accepted", async () => {
    const carol = await createTestIdentity(relay.baseUrl, "creqcarol");
    await carol.client.acceptContact(bob.identity);
    await expect(carol.client.requestContact(bob.identity)).rejects.toBeInstanceOf(PoweurError);
    await expect(carol.client.requestContact(bob.identity)).rejects.toThrow(
      /already an accepted contact/,
    );
  });

  it("routes a stranger's request into the requests queue under contacts_and_requests", async () => {
    const dave = await createTestIdentity(relay.baseUrl, "creqdave");
    const erin = await createTestIdentity(relay.baseUrl, "creqerin");
    await dave.client.setPolicy("contacts_and_requests");

    await erin.client.requestContact(dave.identity, { intro: "let me in" });

    const queued = await dave.client.requests();
    const entry = queued.find((r) => r.sender === erin.identity);
    expect(entry?.type).toBe(MSG_TYPE_CONTACT_REQUEST);

    // …and nowhere else: the queue is not the inbox.
    const inbox = await dave.client.inbox();
    expect(inbox.messages.some((m) => m.sender === erin.identity)).toBe(false);

    // A plain message from the same stranger is still refused.
    await expect(erin.client.send(dave.identity, "and again")).rejects.toThrow();

    // Accepting lets them through.
    await dave.client.acceptContact(erin.identity);
    await expect(erin.client.send(dave.identity, "thanks")).resolves.toBeTruthy();

    await dave.client.setPolicy(INBOX_OPEN);
  });

  it("blocks without telling the blocked sender", async () => {
    const frank = await createTestIdentity(relay.baseUrl, "creqfrank");
    const grace = await createTestIdentity(relay.baseUrl, "creqgrace");

    const blocked = await frank.client.blockContact(grace.identity);
    expect(blocked.state).toBe(CONTACT_BLOCKED);
    // Blocking sends nothing — Grace's inbox stays empty.
    expect((await grace.client.inbox()).messages).toHaveLength(0);

    await frank.client.setPolicy("contacts_only");
    await expect(grace.client.send(frank.identity, "hello?")).rejects.toThrow();
    await frank.client.setPolicy(INBOX_OPEN);
  });
});
