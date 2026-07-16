/**
 * Web client ↔ real relay messaging — parity with CLI unit send tests +
 * apps/integration hosted/same-relay message flows.
 */
import { describe, it, expect, beforeAll, afterAll } from "vitest";
import {
  createHostedIdentity,
  createSession,
  sendEncryptedMessage,
  pullInbox,
  decryptInboxMessages,
  assertSignWith,
} from "../js/messaging.js";
import { startRelay } from "./helpers/relay.mjs";

describe("web client ↔ real relay (messaging)", () => {
  /** @type {Awaited<ReturnType<typeof startRelay>>} */
  let relay;
  let alice;
  let bob;

  beforeAll(async () => {
    relay = await startRelay();
    const suffix = Date.now().toString(36);
    alice = await createHostedIdentity(relay.baseUrl, `aliceweb${suffix}.poweur.net`);
    bob = await createHostedIdentity(relay.baseUrl, `bobweb${suffix}.poweur.net`);
  }, 90_000);

  afterAll(() => {
    relay?.stop();
  });

  it("session-signed send retains ciphertext and session id (CLI TestSendMessageUsesSessionAndRetainsPayload)", async () => {
    const session = await createSession(relay.baseUrl, alice.identity, alice.signingJWK);
    expect(session.sessionId).toBeTruthy();

    const plaintext = "Hello from web";
    const wire = await sendEncryptedMessage({
      relayUrl: relay.baseUrl,
      sender: alice.identity,
      recipient: bob.identity,
      plaintext,
      identitySigningJWK: alice.signingJWK,
      signWith: "session",
      session,
    });

    expect(wire.id).toBeTruthy();
    expect(wire.sender).toBe(alice.identity);
    expect(wire.recipient).toBe(bob.identity);
    expect(wire.signature).toBeTruthy();
    expect(wire.encryption?.alg).toBe("x25519-chacha20-poly1305");
    expect(wire.payload).not.toBe(plaintext);
    expect(wire.session_id).toBe(session.sessionId);
    expect(wire.session_proof?.session_public_key).toBe(session.sessionPublicKey);

    const inbox = await pullInbox(relay.baseUrl, bob.identity, bob.signingJWK);
    const decrypted = await decryptInboxMessages(bob.encJWK, inbox.messages || []);
    const hit = decrypted.find((m) => m.plaintext === plaintext);
    expect(hit).toBeTruthy();
    expect(hit.sender).toBe(alice.identity);
  });

  it("identity-signed send skips session (CLI TestSendMessageWithSignWithIdentity)", async () => {
    const plaintext = "identity-signed hi";
    const wire = await sendEncryptedMessage({
      relayUrl: relay.baseUrl,
      sender: alice.identity,
      recipient: bob.identity,
      plaintext,
      identitySigningJWK: alice.signingJWK,
      signWith: "identity",
      session: null,
    });

    expect(wire.session_id).toBeUndefined();
    expect(wire.session_proof).toBeUndefined();
    expect(wire.signature).toBeTruthy();
    expect(wire.payload).not.toBe(plaintext);

    const inbox = await pullInbox(relay.baseUrl, bob.identity, bob.signingJWK);
    const decrypted = await decryptInboxMessages(bob.encJWK, inbox.messages || []);
    expect(decrypted.some((m) => m.plaintext === plaintext)).toBe(true);
  });

  it("assertSignWith rejects unknown modes before send", () => {
    expect(() => assertSignWith("device")).toThrow(/invalid --sign-with/);
  });
});
