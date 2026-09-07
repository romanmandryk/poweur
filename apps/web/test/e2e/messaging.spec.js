import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";

import "../helpers/browser-globals.mjs";
import { createWebIdentity, unlock } from "../helpers/identity.mjs";
import { clientFor } from "../../js/client.js";

/**
 * Playwright smoke for relay messaging (same protocol as CLI send/inbox),
 * driven through the web app's own modules rather than the UI: compose is
 * covered once unlock state is injectable, and protocol parity is what the CLI
 * integration suite asserts.
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
    const alice = await createWebIdentity(relay.baseUrl, `e2ealice${suffix}.poweur.net`);
    const bob = await createWebIdentity(relay.baseUrl, `e2ebob${suffix}.poweur.net`);

    unlock(alice);
    const client = clientFor(alice.identity);
    const session = await client.sessions.ensure(client.signer);

    const secret = `e2e-msg-${suffix}`;
    const { message } = await client.send(bob.identity, secret, { signWith: "session" });

    expect(message.payload).not.toBe(secret);
    expect(message.session_id).toBe(session.sessionId);

    unlock(bob);
    const { messages } = await clientFor(bob.identity).inbox();
    expect(messages.some((m) => m.plaintext === secret)).toBeTruthy();

    // SPA still serves under /app/
    const res = await fetch(`${relay.baseUrl}/app/`);
    expect(res.ok).toBeTruthy();
  });

  test("the SPA loads its vendored client, with no CDN fetch", async ({ page }) => {
    const external = [];
    page.on("request", (req) => {
      if (!req.url().startsWith(relay.baseUrl)) external.push(req.url());
    });

    await page.goto(`${relay.baseUrl}/app/`);
    await expect(page.locator("#app")).not.toBeEmpty();

    // The import map resolves @poweur/client out of the served tree.
    const version = await page.evaluate(async () => {
      const mod = await import("@poweur/client");
      return mod.PROTOCOL_VERSION;
    });
    expect(version).toBeTruthy();
    expect(external).toEqual([]);
  });
});
