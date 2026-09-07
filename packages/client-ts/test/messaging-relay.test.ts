/**
 * The TypeScript client against a real Go relay: registration, resolution,
 * both signing paths, inbox, acks and the anonymous/PoW path.
 *
 * These mirror `apps/integration` and the web suite — the same flows, driven
 * from TypeScript, so a protocol change that breaks the SDK fails here rather
 * than in a consumer.
 */

import { afterAll, beforeAll, describe, expect, it } from "vitest";

import { PoweurClient } from "../src/client.js";
import { RelayError } from "../src/errors.js";
import { RelayClient } from "../src/http.js";
import { IdentityApi } from "../src/identity.js";
import { sendAnonymous } from "../src/messages.js";
import { resolveIdentity } from "../src/resolve.js";
import { verifyDocument } from "../src/document.js";
import { INBOX_OPEN, ANON_CHALLENGE_POW, ANON_CHALLENGE_NONE } from "../src/types.js";
import { createTestIdentity, localResolveOptions, type TestIdentity } from "./helpers/identities.js";
import { startRelay, type RunningRelay } from "./helpers/relay.js";

describe("TypeScript client ↔ real relay (messaging)", () => {
  let relay: RunningRelay;
  let alice: TestIdentity;
  let bob: TestIdentity;

  beforeAll(async () => {
    relay = await startRelay();
    alice = await createTestIdentity(relay.baseUrl, "alice");
    bob = await createTestIdentity(relay.baseUrl, "bob");
  }, 180_000);

  afterAll(() => relay?.stop());

  it("registers a hosted identity and resolves it back", async () => {
    const result = await resolveIdentity(alice.identity, localResolveOptions(relay.baseUrl));
    expect(result.document.identity).toBe(alice.identity);
    expect(result.document.public_key).toBe(alice.client.signer.publicKey);
    // The document the relay serves must still verify against its own key —
    // the relay stores it but never signs it.
    expect(() => verifyDocument(result.document)).not.toThrow();
  });

  it("rejects a registration whose signature does not match its key", async () => {
    const api = new IdentityApi(new RelayClient(relay.baseUrl));
    await expect(
      api.register({
        identity: "forged.poweur.net",
        public_key: alice.client.signer.publicKey.replace("ed25519:", ""),
        issued_at: new Date().toISOString().replace(/\.\d{3}Z$/, "Z"),
        nonce: "not-a-real-nonce",
        identity_signature: "AAAA",
      }),
    ).rejects.toBeInstanceOf(RelayError);
  });

  it("session-signed send arrives and decrypts", async () => {
    const sent = await alice.client.send(bob.identity, "hello from typescript");
    expect(sent.message.session_id).toBeTruthy();
    expect(sent.message.session_proof).toBeTruthy();
    expect(sent.message.encryption?.alg).toBe("x25519-chacha20-poly1305");
    // Never plaintext on the wire.
    expect(sent.message.payload).not.toContain("hello from typescript");

    const { messages } = await bob.client.inbox();
    const received = messages.find((m) => m.id === sent.message.id);
    expect(received?.plaintext).toBe("hello from typescript");
    expect(received?.sender).toBe(alice.identity);
  });

  it("identity-signed send works for headless agents", async () => {
    const sent = await alice.client.send(bob.identity, "signed with the identity key", {
      signWith: "identity",
    });
    expect(sent.message.session_id).toBeUndefined();
    const { messages } = await bob.client.inbox();
    expect(messages.find((m) => m.id === sent.message.id)?.plaintext).toBe(
      "signed with the identity key",
    );
  });

  it("carries the envelope type through to the recipient", async () => {
    const sent = await alice.client.send(bob.identity, "hi", { type: "sys.contact.request" });
    expect(sent.message.type).toBe("sys.contact.request");
  });

  it("refuses an invalid sign-with mode before touching the network", async () => {
    await expect(
      alice.client.send(bob.identity, "x", { signWith: "passkey" as never }),
    ).rejects.toThrow(/invalid sign-with/);
  });

  it("refuses to send to an identity that cannot be resolved", async () => {
    await expect(alice.client.send("ghost.poweur.net", "x")).rejects.toThrow();
  });

  it("emits a tick-2 ack that reaches the original sender", async () => {
    const sent = await alice.client.send(bob.identity, "ack me");
    const { acked } = await bob.client.inboxAndAck();
    expect(acked).toContain(sent.message.id);

    const { acks } = await alice.client.inbox();
    const ack = acks.find((a) => a.message_id === sent.message.id);
    expect(ack?.state).toBe("delivered_client");
    expect(ack?.sender).toBe(bob.identity);
  });

  it("reuses one session across sends and refreshes on demand", async () => {
    const first = await alice.client.sessions.ensure(alice.client.signer);
    const second = await alice.client.sessions.ensure(alice.client.signer);
    expect(second.sessionId).toBe(first.sessionId);

    const refreshed = await alice.client.sessions.refresh(alice.client.signer);
    expect(refreshed.sessionId).not.toBe(first.sessionId);
    const { valid } = await alice.client.sessions.status(alice.identity);
    expect(valid).toBe(true);

    // Sending must still work on the new session.
    await expect(alice.client.send(bob.identity, "after refresh")).resolves.toBeTruthy();
  });

  it("revokes a session locally and at the relay", async () => {
    const temp = await createTestIdentity(relay.baseUrl, "revoke");
    await temp.client.sessions.ensure(temp.client.signer);
    const { relayRevoked } = await temp.client.sessions.revoke(temp.client.signer);
    expect(relayRevoked).toBe(true);
    expect((await temp.client.sessions.status(temp.identity)).valid).toBe(false);
  });

  it("reads and writes the inbox policy", async () => {
    const before = await alice.client.policy();
    expect(before.policy.mode).toBe(INBOX_OPEN);
    expect(before.explicit).toBe(false);

    await alice.client.setPolicy("contacts_and_requests");
    const after = await alice.client.policy();
    expect(after.policy.mode).toBe("contacts_and_requests");
    expect(after.explicit).toBe(true);

    await alice.client.setPolicy(INBOX_OPEN);
  });

  it("denies anonymous senders by default", async () => {
    const target = await createTestIdentity(relay.baseUrl, "closed");
    await expect(
      sendAnonymous(target.identity, "let me in", {
        resolve: localResolveOptions(relay.baseUrl),
        targetRelayUrl: relay.baseUrl,
      }),
    ).rejects.toBeTruthy();
  });

  it("accepts an anonymous message when the policy opts in", async () => {
    const target = await createTestIdentity(relay.baseUrl, "openanon");
    await target.client.setPolicy(INBOX_OPEN, {
      allow: true,
      challenge: ANON_CHALLENGE_NONE,
    });
    const result = await sendAnonymous(target.identity, "hello stranger", {
      resolve: localResolveOptions(relay.baseUrl),
      targetRelayUrl: relay.baseUrl,
    });
    expect(result.status).toBeLessThan(300);

    const queue = await target.client.anon();
    expect(queue.some((m) => m.plaintext === "hello stranger")).toBe(true);
  });

  it("solves the proof-of-work an anonymous policy demands", async () => {
    const target = await createTestIdentity(relay.baseUrl, "powanon");
    await target.client.setPolicy(INBOX_OPEN, {
      allow: true,
      challenge: ANON_CHALLENGE_POW,
      // Low but non-trivial: enough to exercise the solver in ~milliseconds.
      pow_bits: 10,
    });

    let challenged = false;
    const result = await sendAnonymous(target.identity, "solved for you", {
      resolve: localResolveOptions(relay.baseUrl),
      targetRelayUrl: relay.baseUrl,
      onChallenge: () => {
        challenged = true;
      },
    });
    expect(challenged).toBe(true);
    expect(result.status).toBeLessThan(300);

    const queue = await target.client.anon();
    expect(queue.some((m) => m.plaintext === "solved for you")).toBe(true);
  }, 120_000);

  it("reports solver progress and can be aborted mid-solve", async () => {
    const target = await createTestIdentity(relay.baseUrl, "powprog");
    await target.client.setPolicy(INBOX_OPEN, {
      allow: true,
      challenge: ANON_CHALLENGE_POW,
      // High enough to take more than one solver chunk, so progress is real.
      pow_bits: 20,
    });

    // Progress is what lets a browser show a spinner instead of freezing.
    const attempts: number[] = [];
    await sendAnonymous(target.identity, "worth the wait", {
      resolve: localResolveOptions(relay.baseUrl),
      targetRelayUrl: relay.baseUrl,
      onSolveProgress: (count) => attempts.push(count),
    });
    expect(attempts.length).toBeGreaterThan(0);
    expect(attempts.at(-1)).toBeGreaterThan(attempts[0] ?? 0);

    // …and abort is what lets them change their mind about paying the cost.
    // 26 bits so the solve cannot finish by luck before the abort lands.
    const costly = await createTestIdentity(relay.baseUrl, "powstop");
    await costly.client.setPolicy(INBOX_OPEN, {
      allow: true,
      challenge: ANON_CHALLENGE_POW,
      pow_bits: 26,
    });
    const controller = new AbortController();
    setTimeout(() => controller.abort(), 50);
    await expect(
      sendAnonymous(costly.identity, "never mind", {
        resolve: localResolveOptions(relay.baseUrl),
        targetRelayUrl: relay.baseUrl,
        signal: controller.signal,
      }),
    ).rejects.toThrow(/aborted/);
  }, 180_000);

  it("reports relay health", async () => {
    const health = await alice.client.identity.health();
    expect(health.status).toBeTruthy();
  });

  it("surfaces a typed error for an unknown identity", async () => {
    const client = new PoweurClient({
      relayUrl: relay.baseUrl,
      signer: alice.client.signer,
      resolve: localResolveOptions(relay.baseUrl),
    });
    await expect(client.identity.get("missing.poweur.net")).rejects.toBeInstanceOf(RelayError);
  });
});
