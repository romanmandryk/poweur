import { generateKeyPairSync } from "node:crypto";
import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";
import { registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";

/**
 * Notifications and install on the web (EPIC-009 E09-T12).
 *
 * Headless Chromium has no push service to subscribe to, so the browser half
 * stops at what it can prove: the service worker installs at /id/, the
 * relay's push key reaches the Settings row, and the enable button is offered.
 * Delivery itself is covered by apps/integration TestINT_WEBPUSH_01 and the
 * relay's push tests, which decrypt what a device would receive.
 */

const vapidKey = () => generateKeyPairSync("ec", { namedCurve: "P-256" }).privateKey.export({ format: "jwk" }).d;

test.describe("notifications on this device", () => {
  /** @type {Awaited<ReturnType<typeof startRelay>>} */
  let relay;

  test.beforeAll(async () => {
    relay = await startRelay({ env: { PUSH_VAPID_PRIVATE_KEY: vapidKey(), PUSH_VAPID_SUBJECT: "mailto:ops@poweur.test" } });
  });
  test.afterAll(() => relay?.stop());

  test("registers the service worker and offers notifications", async ({ page }) => {
    // Headless Chromium answers "denied" whatever is granted; a real browser
    // starts at "default", which is what the Settings row is written for.
    await page.addInitScript(() => {
      Object.defineProperty(Notification, "permission", { get: () => "default" });
    });
    await stubPasskeys(page);
    await registerIdentity(page, relay, `push${Date.now().toString(36)}`);

    const scope = await page.evaluate(async () => (await navigator.serviceWorker.ready).scope);
    expect(new URL(scope).pathname).toBe("/id/");
    const sw = await page.request.get(`${relay.baseUrl}/id/sw.js`);
    expect(sw.ok()).toBeTruthy();
    expect(sw.headers()["cache-control"]).toBe("no-cache");

    await page.click('[data-page="settings"]');
    await expect(page.locator("#row-notifications")).toContainText("Off", { timeout: 20_000 });
    await page.click("#row-notifications");
    await expect(page.locator("#notifications-panel")).toHaveAttribute("data-state", "off");
    await expect(page.locator("#push-enable")).toBeVisible();
  });
});

test.describe("relay without push", () => {
  /** @type {Awaited<ReturnType<typeof startRelay>>} */
  let relay;

  test.beforeAll(async () => {
    relay = await startRelay();
  });
  test.afterAll(() => relay?.stop());

  test("says notifications are not offered instead of failing", async ({ page }) => {
    await stubPasskeys(page);
    await registerIdentity(page, relay, `nopush${Date.now().toString(36)}`);
    await page.click('[data-page="settings"]');
    await expect(page.locator("#row-notifications")).toContainText("Not offered", { timeout: 20_000 });
  });
});
