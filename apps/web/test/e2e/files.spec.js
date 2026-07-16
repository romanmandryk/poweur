import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";
import { createHostedIdentity } from "../../js/messaging.js";
import {
  mintDavToken,
  listDir,
  uploadFile,
  downloadFile,
  makeDir,
} from "../../js/files.js";

/**
 * Playwright smoke for WebDAV files (EPIC-003) — same protocol the SPA file
 * browser and `poweur dav` use: token mint, mkdir/put/propfind/get, and
 * public web serving via /pub with the .poweur-web-public marker.
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

  test("dav token, upload, list, download", async () => {
    const suffix = Date.now().toString(36);
    const owner = await createHostedIdentity(relay.baseUrl, `e2efiles${suffix}.poweur.net`);

    const tok = await mintDavToken(relay.baseUrl, owner.identity, owner.signingJWK);
    expect(tok.scope).toBe("dav:full");

    await makeDir(relay.baseUrl, owner.identity, tok.token, "private/e2e");
    await uploadFile(relay.baseUrl, owner.identity, tok.token, "private/e2e/hello.txt", "hi from e2e");

    const entries = await listDir(relay.baseUrl, owner.identity, tok.token, "private/e2e");
    expect(entries.map((e) => e.name)).toContain("hello.txt");

    const res = await downloadFile(relay.baseUrl, owner.identity, tok.token, "private/e2e/hello.txt");
    expect(await res.text()).toBe("hi from e2e");
  });

  test("public web serving via /pub with marker", async ({ request }) => {
    const suffix = Date.now().toString(36);
    const owner = await createHostedIdentity(relay.baseUrl, `e2epub${suffix}.poweur.net`);
    const tok = await mintDavToken(relay.baseUrl, owner.identity, owner.signingJWK);

    await makeDir(relay.baseUrl, owner.identity, tok.token, "public/site");
    await uploadFile(relay.baseUrl, owner.identity, tok.token, "public/site/index.html", "<h1>e2e pub</h1>");

    // /pub routes by Host header (vanity host): https://<identity>/pub/<path>.
    // Node fetch strips Host, so use Playwright's request fixture.
    const pubGet = (path) =>
      request.get(`${relay.baseUrl}/pub/${path}`, { headers: { Host: owner.identity } });

    // Not served before the marker exists
    const before = await pubGet("site/index.html");
    expect(before.status()).toBe(404);

    await uploadFile(relay.baseUrl, owner.identity, tok.token, "public/site/.poweur-web-public", "");

    const after = await pubGet("site/index.html");
    expect(after.ok()).toBeTruthy();
    expect(await after.text()).toBe("<h1>e2e pub</h1>");
  });
});
