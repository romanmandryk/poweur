import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";
import { registerIdentity, stubPasskeys, stubPasskeysWithoutPrf, openApp } from "../helpers/app-ui.mjs";

test.describe("hosted web client E2E", () => {
  /** @type {Awaited<ReturnType<typeof startRelay>>} */
  let relay;

  test.beforeAll(async () => {
    relay = await startRelay();
  });

  test.afterAll(() => {
    relay?.stop();
  });

  test("hosted create on this relay (PRF path)", async ({ page }) => {
    await stubPasskeys(page);
    const handle = `e2e${Date.now().toString(36)}`;
    const identity = await registerIdentity(page, relay, handle);

    const docRes = await page.request.get(`${relay.baseUrl}/identities/${identity}`);
    expect(docRes.ok()).toBeTruthy();
    const body = await docRes.json();
    expect(body.identity).toBe(identity);
    expect(body.identity_document || body.public_key).toBeTruthy();
  });

  test("refuses an authenticator without PRF before a name is spent", async ({ page }) => {
    await stubPasskeysWithoutPrf(page);
    await openApp(page, relay);
    if (await page.locator("#btn-welcome-start").count()) await page.click("#btn-welcome-start");
    if (await page.locator("#opt-create-new").count()) await page.click("#opt-create-new");
    await page.waitForSelector("#claim-card");
    await expect(page.locator("#claim-prf-required")).toContainText("does not support passkeys with PRF", { timeout: 20_000 });
    await expect(page.locator("#btn-claim")).toBeDisabled();
    await expect(page.locator("#pin-input")).toHaveCount(0);
  });
});
