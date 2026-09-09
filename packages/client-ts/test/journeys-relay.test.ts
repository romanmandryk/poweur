/**
 * The CLI journeys, run through the SDK against a real Go relay.
 *
 * These are deliberately the same scenarios as `apps/integration/journeys_test.go`
 * and `apps/integration/history_test.go`, asserted through `@poweur/client`
 * instead of `poweur`. Parity is the point: the two implementations share a
 * protocol and a set of documents, and the only way to know they still agree
 * about *behaviour* — not just about canonical strings, which the conformance
 * vectors already pin — is to walk the same paths with both.
 */

import { afterAll, beforeAll, describe, expect, it } from "vitest";

import { HISTORY_QUEUE_SENT, historyPeer, unreadCounts } from "../src/history.js";
import {
  INBOX_CONTACTS_AND_REQUESTS,
  INBOX_CONTACTS_ONLY,
  INBOX_OPEN,
  MSG_TYPE_CONTACT_ACCEPT,
  MSG_TYPE_CONTACT_REQUEST,
  CONTACT_ACCEPTED,
  CONTACT_REQUESTED,
} from "../src/types.js";
import { sendAnonymous } from "../src/messages.js";
import { createTestIdentity, localResolveOptions, type TestIdentity } from "./helpers/identities.js";
import { startRelay, type RunningRelay } from "./helpers/relay.js";

let relay: RunningRelay;

beforeAll(async () => {
  relay = await startRelay();
}, 180_000);

afterAll(() => relay?.stop());

/** The state one identity records for another, or null. */
async function contactState(who: TestIdentity, target: string): Promise<string | null> {
  const file = await (await who.client.contacts()).load();
  return file.contacts.find((c) => c.identity === target.toLowerCase())?.state ?? null;
}

describe("journey: two identities on one relay", () => {
  it("registers both independently and keeps their mailboxes apart", async () => {
    const work = await createTestIdentity(relay.baseUrl, "j1work");
    const home = await createTestIdentity(relay.baseUrl, "j1home");
    const outsider = await createTestIdentity(relay.baseUrl, "j1out");

    // Separate principals, not aliases: different signing and encryption keys.
    expect(work.client.signer.publicKey).not.toBe(home.client.signer.publicKey);
    expect(work.client.decryptor?.encryptionPublicKey).not.toBe(
      home.client.decryptor?.encryptionPublicKey,
    );

    // Both resolve on the relay that hosts them.
    for (const id of [work, home]) {
      const doc = await id.client.identity.get(id.identity);
      expect(doc.identity).toBe(id.identity);
      // The relay serves the bare base64url key; a signer carries the
      // `ed25519:` prefix. Same key, two spellings.
      expect(id.client.signer.publicKey).toContain(doc.public_key);
    }

    await outsider.client.send(work.identity, "quarterly numbers");

    // Only the addressee has it.
    expect((await home.client.inbox()).messages).toHaveLength(0);
    const workInbox = await work.client.inbox();
    expect(workInbox.messages.map((m) => m.plaintext)).toEqual(["quarterly numbers"]);

    // And they can message each other.
    await work.client.send(home.identity, "reminder: dentist");
    const homeInbox = await home.client.inbox();
    expect(homeInbox.messages[0]?.sender).toBe(work.identity);
    expect(homeInbox.messages[0]?.plaintext).toBe("reminder: dentist");
  }, 120_000);
});

describe("journey: mutual contact handshake", () => {
  it("leaves both sides calling each other a contact, against closed policies", async () => {
    const alice = await createTestIdentity(relay.baseUrl, "j2alice");
    const bob = await createTestIdentity(relay.baseUrl, "j2bob");

    await alice.client.setPolicy(INBOX_CONTACTS_AND_REQUESTS);
    await bob.client.setPolicy(INBOX_CONTACTS_ONLY);

    expect(await contactState(alice, bob.identity)).toBeNull();

    // Bob asks; his own side records the intent immediately.
    await bob.client.requestContact(alice.identity, { intro: "hi, this is bob" });
    expect(await contactState(bob, alice.identity)).toBe(CONTACT_REQUESTED);

    // Alice's queue has it, and her inbox does not.
    const queued = await alice.client.requests();
    expect(queued.map((r) => r.sender)).toEqual([bob.identity]);
    expect(queued[0]?.type).toBe(MSG_TYPE_CONTACT_REQUEST);
    expect((await alice.client.inbox()).messages).toHaveLength(0);

    const { contact, notified } = await alice.client.acceptContact(bob.identity, { petname: "Bob" });
    expect(contact.state).toBe(CONTACT_ACCEPTED);
    // Her answer reaches him even though his inbox is closed — it is the
    // reply to a request he sent, which is the one thing a closed inbox has
    // to admit or the handshake deadlocks.
    expect(notified).toBe(true);

    // It rides his requests queue, not his message stream.
    expect((await bob.client.inbox()).messages).toHaveLength(0);
    const answers = await bob.client.requests();
    expect(answers.map((r) => r.type)).toContain(MSG_TYPE_CONTACT_ACCEPT);

    // Completing the handshake is the client's job on this side.
    await (await bob.client.contacts()).set(alice.identity, CONTACT_ACCEPTED, {});
    expect(await contactState(bob, alice.identity)).toBe(CONTACT_ACCEPTED);

    // Mutual: both directions carry against two closed policies.
    await alice.client.send(bob.identity, "glad we connected");
    expect((await bob.client.inbox()).messages[0]?.plaintext).toBe("glad we connected");
    await bob.client.send(alice.identity, "likewise");
    expect((await alice.client.inbox()).messages[0]?.plaintext).toBe("likewise");
  }, 120_000);
});

describe("journey: the policy × sender matrix", () => {
  // Every pair, and for each the one queue it is allowed to reach. The
  // negative half is the half that matters: a request that also copies itself
  // into the inbox is a policy that does not hold.
  const cases: Array<{
    name: string;
    policy: typeof INBOX_OPEN | typeof INBOX_CONTACTS_ONLY | typeof INBOX_CONTACTS_AND_REQUESTS;
    kind: "contact" | "stranger" | "request";
    want: "inbox" | "requests" | "rejected";
  }> = [
    { name: "open/contact", policy: INBOX_OPEN, kind: "contact", want: "inbox" },
    { name: "open/stranger", policy: INBOX_OPEN, kind: "stranger", want: "inbox" },
    { name: "open/request", policy: INBOX_OPEN, kind: "request", want: "inbox" },
    { name: "contacts_only/contact", policy: INBOX_CONTACTS_ONLY, kind: "contact", want: "inbox" },
    { name: "contacts_only/stranger", policy: INBOX_CONTACTS_ONLY, kind: "stranger", want: "rejected" },
    { name: "contacts_only/request", policy: INBOX_CONTACTS_ONLY, kind: "request", want: "rejected" },
    { name: "contacts_and_requests/contact", policy: INBOX_CONTACTS_AND_REQUESTS, kind: "contact", want: "inbox" },
    { name: "contacts_and_requests/stranger", policy: INBOX_CONTACTS_AND_REQUESTS, kind: "stranger", want: "rejected" },
    { name: "contacts_and_requests/request", policy: INBOX_CONTACTS_AND_REQUESTS, kind: "request", want: "requests" },
  ];

  for (const tc of cases) {
    it(`routes ${tc.name} to ${tc.want}`, async () => {
      const rcpt = await createTestIdentity(relay.baseUrl, "j3r");
      const sender = await createTestIdentity(relay.baseUrl, "j3s");
      await rcpt.client.setPolicy(tc.policy);

      const body = `matrix probe ${tc.name}`;
      let refused = false;
      try {
        if (tc.kind === "contact") {
          await (await rcpt.client.contacts()).set(sender.identity, CONTACT_ACCEPTED, { pin: true });
          await sender.client.send(rcpt.identity, body);
        } else if (tc.kind === "stranger") {
          await sender.client.send(rcpt.identity, body);
        } else {
          await sender.client.requestContact(rcpt.identity, { intro: body });
        }
      } catch {
        refused = true;
      }

      const inbox = await rcpt.client.inbox();
      const requests = await rcpt.client.requests();
      const anon = await rcpt.client.anon();

      if (tc.want === "rejected") {
        expect(refused).toBe(true);
        expect(inbox.messages.some((m) => m.plaintext === body)).toBe(false);
        expect(requests).toHaveLength(0);
        return;
      }
      expect(refused).toBe(false);
      if (tc.want === "inbox") {
        expect(inbox.messages.some((m) => m.plaintext === body)).toBe(true);
        expect(requests).toHaveLength(0);
      } else {
        expect(requests.map((r) => r.sender)).toEqual([sender.identity]);
        expect(inbox.messages).toHaveLength(0);
      }
      // A signed message never reaches the anonymous queue, whatever the mode.
      expect(anon).toHaveLength(0);

      // Drained means drained: a second read finds nothing.
      expect((await rcpt.client.inbox()).messages).toHaveLength(0);
      expect(await rcpt.client.requests()).toHaveLength(0);
    }, 120_000);
  }
});

/**
 * Anonymous send, pointed at the local relay. `solvedPow` is not something
 * the SDK returns — the only observable is the challenge callback, which is
 * exactly what a UI has to hang a progress bar off, so the test asserts on
 * the same signal a caller would.
 */
async function anonSend(
  relayUrl: string,
  recipient: string,
  body: string,
): Promise<{ solvedPow: boolean }> {
  let solvedPow = false;
  await sendAnonymous(recipient, body, {
    resolve: localResolveOptions(relayUrl),
    targetRelayUrl: relayUrl,
    scheme: "http",
    onChallenge: () => {
      solvedPow = true;
    },
  });
  return { solvedPow };
}

describe("journey: anonymous tiers", () => {
  it("walks closed → free → proof-of-work without ever touching the signed inbox", async () => {
    const rcpt = await createTestIdentity(relay.baseUrl, "j4r");
    const sender = await createTestIdentity(relay.baseUrl, "j4s");

    // The signed inbox stays shut for strangers throughout.
    await rcpt.client.setPolicy(INBOX_CONTACTS_ONLY);
    await expect(sender.client.send(rcpt.identity, "signed stranger")).rejects.toThrow();

    // Tier 1 — anonymous off, the default.
    await expect(anonSend(relay.baseUrl, rcpt.identity, "anon while closed")).rejects.toThrow();
    expect(await rcpt.client.anon()).toHaveLength(0);

    // Tier 2 — allowed, no challenge.
    await rcpt.client.setPolicy(INBOX_CONTACTS_ONLY, { allow: true, challenge: "none" });
    const free = await anonSend(relay.baseUrl, rcpt.identity, "free anonymous tip");
    expect(free.solvedPow).toBe(false);
    let queue = await rcpt.client.anon();
    expect(queue.map((m) => m.plaintext)).toEqual(["free anonymous tip"]);
    expect((await rcpt.client.inbox()).messages).toHaveLength(0);

    // Tier 3 — priced in proof-of-work.
    await rcpt.client.setPolicy(INBOX_CONTACTS_ONLY, {
      allow: true,
      challenge: "pow",
      pow_bits: 8,
    });
    const paid = await anonSend(relay.baseUrl, rcpt.identity, "paid anonymous tip");
    expect(paid.solvedPow).toBe(true);
    queue = await rcpt.client.anon();
    expect(queue.map((m) => m.plaintext)).toEqual(["paid anonymous tip"]);
    // Unsigned means unsigned: the queue entry carries no sender to trust.
    expect((queue[0] as { sender?: string }).sender ?? "").toBe("");
    expect((await rcpt.client.inbox()).messages).toHaveLength(0);

    // Opting into anonymous mail changes nothing about the signed path.
    await (await rcpt.client.contacts()).set(sender.identity, CONTACT_ACCEPTED, { pin: true });
    await sender.client.send(rcpt.identity, "signed and known");
    expect((await rcpt.client.inbox()).messages[0]?.plaintext).toBe("signed and known");
  }, 180_000);
});

describe("journey: message history survives the drain", () => {
  it("keeps both sides of a conversation after the relay has forgotten it", async () => {
    const alice = await createTestIdentity(relay.baseUrl, "j5alice");
    const bob = await createTestIdentity(relay.baseUrl, "j5bob");

    await bob.client.sendAndArchive(alice.identity, "morning");
    const first = await alice.client.inboxAndArchive();
    expect(first.archived).toBe(1);
    expect(first.lost).toBe(0);

    await alice.client.sendAndArchive(bob.identity, "morning yourself");
    await bob.client.inboxAndArchive();
    await bob.client.sendAndArchive(alice.identity, "coffee?");
    await alice.client.inboxAndArchive();

    // The relay has genuinely forgotten them.
    expect((await alice.client.inbox()).messages).toHaveLength(0);

    const history = await alice.client.history();
    const records = await history.load();
    expect(records.map((r) => r.body)).toEqual(["morning", "morning yourself", "coffee?"]);
    // Direction is recorded, not inferred.
    expect(records[1]?.queue).toBe(HISTORY_QUEUE_SENT);
    // Both directions thread under one peer.
    expect(records.every((r) => historyPeer(r, alice.identity) === bob.identity)).toBe(true);
    expect((await history.conversation(bob.identity)).length).toBe(3);

    // A second client holding the same keys reads the same archive — this is
    // what putting it in the tree buys over a per-device store.
    const secondDevice = await alice.client.history();
    expect((await secondDevice.load()).map((r) => r.body)).toEqual([
      "morning",
      "morning yourself",
      "coffee?",
    ]);
  }, 180_000);

  it("counts unread down to zero and back up again", async () => {
    const alice = await createTestIdentity(relay.baseUrl, "j6alice");
    const bob = await createTestIdentity(relay.baseUrl, "j6bob");

    await bob.client.send(alice.identity, "one");
    await bob.client.send(alice.identity, "two");
    await alice.client.inboxAndArchive();

    const history = await alice.client.history();
    expect((await history.unread())[bob.identity]).toBe(2);

    await history.markConversationRead(bob.identity, await history.load());
    expect((await history.unread())[bob.identity] ?? 0).toBe(0);

    // The mark lives in the tree, so a fresh reader sees it too — this is the
    // badge that stays cleared across a reload.
    const reloaded = await alice.client.history();
    expect((await reloaded.unread())[bob.identity] ?? 0).toBe(0);

    // …and a new message makes it non-zero again, which is the other half of
    // a badge being worth anything.
    await bob.client.send(alice.identity, "three");
    await alice.client.inboxAndArchive();
    expect((await (await alice.client.history()).unread())[bob.identity]).toBe(1);
  }, 180_000);

  it("archives anonymous messages without inventing a sender", async () => {
    const rcpt = await createTestIdentity(relay.baseUrl, "j7r");
    const sender = await createTestIdentity(relay.baseUrl, "j7s");
    await rcpt.client.setPolicy(INBOX_OPEN, { allow: true, challenge: "none" });

    await sender.client.send(rcpt.identity, "signed and attributable");
    await anonSend(relay.baseUrl, rcpt.identity, "unsigned and not");
    await rcpt.client.inboxAndArchive();
    await rcpt.client.anonAndArchive();

    const history = await rcpt.client.history();
    const records = await history.load();
    const signed = records.find((r) => r.queue === "inbox");
    const anon = records.find((r) => r.queue === "anonymous");
    expect(signed?.sender).toBe(sender.identity);
    expect(anon?.sender ?? "").toBe("");

    // They are separate conversations, so the counts are separate too.
    const counts = unreadCounts(await history.readState(), rcpt.identity, records);
    expect(counts[sender.identity]).toBe(1);
    expect(counts["anonymous"]).toBe(1);
  }, 180_000);

  it("stores the archive sealed, not in the clear", async () => {
    const alice = await createTestIdentity(relay.baseUrl, "j8alice");
    const bob = await createTestIdentity(relay.baseUrl, "j8bob");
    const secret = "the passphrase is hunter2";

    await bob.client.send(alice.identity, secret);
    await alice.client.inboxAndArchive();

    const dav = await alice.client.dav();
    const shards = (await dav.list("poweur-sys/private/messages")).filter((e) => e.dir);
    const files = (await dav.list(shards[0]!.path)).filter((e) => !e.dir);
    expect(files.length).toBeGreaterThan(0);
    const stored = await dav.readText(files[0]!.path);
    expect(stored).not.toContain(secret);
    const envelope = JSON.parse(stored) as Record<string, string>;
    expect(envelope["alg"]).toBe("x25519-chacha20-poly1305");
    expect(envelope["ciphertext"]).toBeTruthy();

    // Another identity cannot reach the owner-only zone at all.
    const bobDav = await bob.client.dav({ audience: alice.identity, force: true }).catch(() => null);
    if (bobDav) {
      await expect(bobDav.readText(files[0]!.path)).rejects.toThrow();
    }

    // The owner still reads it perfectly.
    expect((await (await alice.client.history()).load()).map((r) => r.body)).toEqual([secret]);
  }, 180_000);
});
