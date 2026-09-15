import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";

import "../helpers/browser-globals.mjs";
import { createWebIdentity, unlock } from "../helpers/identity.mjs";
import { clientFor } from "../../src/lib/client.js";

/**
 * Playwright smoke for WebDAV files (EPIC-003) — same protocol the SPA file
 * browser and `poweur dav` use: token mint, mkdir/put/propfind/get, and
 * public web serving via /pub with the .poweur-web-public marker.
 *
 * The /pub case lives here rather than in Vitest because it routes on the Host
 * header, which Node's fetch strips and Playwright's request fixture does not.
 */
test.describe("web ↔ relay files E2E", () => {
  /** @type {Awaited<ReturnType<typeof startRelay>>} */
  let relay;

  test.beforeAll(async () => {
    relay = await startRelay();
  });

  test.afterAll(() => {
    relay?.stop();
  });

  async function davFor(identity) {
    unlock(identity);
    return clientFor(identity.identity).dav({ force: true });
  }

  test("dav token, upload, list, download", async () => {
    const suffix = Date.now().toString(36);
    const owner = await createWebIdentity(relay.baseUrl, `e2efiles${suffix}.poweur.net`);

    unlock(owner);
    const client = clientFor(owner.identity);
    const token = await client.davToken();
    expect(token.scope).toBe("dav:full");

    const dav = await client.dav();
    await dav.mkdir("private/e2e");
    await dav.write("private/e2e/hello.txt", "hi from e2e");

    const entries = await dav.list("private/e2e");
    expect(entries.map((e) => e.name)).toContain("hello.txt");

    expect(await dav.readText("private/e2e/hello.txt")).toBe("hi from e2e");
  });

  test("public web serving via /pub with marker", async ({ request }) => {
    const suffix = Date.now().toString(36);
    const owner = await createWebIdentity(relay.baseUrl, `e2epub${suffix}.poweur.net`);
    const dav = await davFor(owner);

    await dav.mkdir("public/site");
    await dav.write("public/site/index.html", "<h1>e2e pub</h1>");

    // /pub routes by Host header (vanity host): https://<identity>/pub/<path>.
    const pubGet = (path) =>
      request.get(`${relay.baseUrl}/pub/${path}`, { headers: { Host: owner.identity } });

    // Not served before the marker exists
    const before = await pubGet("site/index.html");
    expect(before.status()).toBe(404);

    await dav.write("public/site/.poweur-web-public", "");

    const after = await pubGet("site/index.html");
    expect(after.ok()).toBeTruthy();
    expect(await after.text()).toBe("<h1>e2e pub</h1>");
  });
});
