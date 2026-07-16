import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";
import {
  createHostedIdentity,
  createSession,
  sendEncryptedMessage,
  pullInbox,
  decryptInboxMessages,
} from "../../js/messaging.js";

/**
 * Playwright smoke for relay messaging (same protocol as CLI send/inbox).
 * Uses page.request + messaging helpers — UI compose is covered once unlock
 * state is injectable; protocol parity is what CLI integration asserts.
 */
test.describe("web ↔ relay messaging E2E", () => {
  /** @type {Awaited<ReturnType<typeof startRelay>>} */
  let relay;

  test.beforeAll(async () => {
    relay = await startRelay();
  });

  test.afterAll(() => {
    relay?.stop();
  });

  test("hosted register, session send, inbox decrypt", async () => {
    const suffix = Date.now().toString(36);
    const alice = await createHostedIdentity(relay.baseUrl, `e2ealice${suffix}.poweur.net`);
    const bob = await createHostedIdentity(relay.baseUrl, `e2ebob${suffix}.poweur.net`);

    const session = await createSession(relay.baseUrl, alice.identity, alice.signingJWK);
    const secret = `e2e-msg-${suffix}`;
    const wire = await sendEncryptedMessage({
      relayUrl: relay.baseUrl,
      sender: alice.identity,
      recipient: bob.identity,
      plaintext: secret,
      identitySigningJWK: alice.signingJWK,
      signWith: "session",
      session,
    });

    expect(wire.payload).not.toBe(secret);
    expect(wire.session_id).toBe(session.sessionId);

    const inbox = await pullInbox(relay.baseUrl, bob.identity, bob.signingJWK);
    const decrypted = await decryptInboxMessages(bob.encJWK, inbox.messages || []);
    expect(decrypted.some((m) => m.plaintext === secret)).toBeTruthy();

    // SPA still serves under /app/
    const res = await fetch(`${relay.baseUrl}/app/`);
    expect(res.ok).toBeTruthy();
  });
});
