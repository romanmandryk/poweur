import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";

/**
 * Stub WebAuthn so createPasskey falls through to PIN wrapping (no PRF).
 * Real passkeys are not available in CI headless Chromium reliably.
 */
async function stubPasskeys(page) {
  await page.addInitScript(() => {
    const fakeId = crypto.getRandomValues(new Uint8Array(32));
    class FakeCredential {
      constructor() {
        this.rawId = fakeId.buffer;
        this.id = btoa(String.fromCharCode(...fakeId));
        this.type = "public-key";
      }
      getClientExtensionResults() {
        return {}; // no PRF → PIN path
      }
    }
    navigator.credentials.create = async () => new FakeCredential();
    navigator.credentials.get = async () => new FakeCredential();
    if (window.PublicKeyCredential) {
      PublicKeyCredential.isUserVerifyingPlatformAuthenticatorAvailable = async () => true;
    }
  });
}

test.describe("hosted web client E2E", () => {
  /** @type {Awaited<ReturnType<typeof startRelay>>} */
  let relay;

  test.beforeAll(async () => {
    relay = await startRelay();
  });

  test.afterAll(() => {
    relay?.stop();
  });

  test("hosted create on this relay (PIN path)", async ({ page }) => {
    await stubPasskeys(page);
    const handle = `e2e${Date.now().toString(36)}`;

    await page.goto(`${relay.baseUrl}/app/`);
    // Persist relay URL for the SPA
    await page.evaluate((url) => {
      const cfg = JSON.parse(localStorage.getItem("poweur:config") || "{}");
      cfg.relayUrl = url;
      cfg.parentDomain = "poweur.net";
      localStorage.setItem("poweur:config", JSON.stringify(cfg));
    }, relay.baseUrl);
    await page.reload();

    // Welcome → add identity → create new
    const start = page.locator("#btn-welcome-start, #opt-create-new, #settings-add-id");
    if (await page.locator("#btn-welcome-start").count()) {
      await page.click("#btn-welcome-start");
    }
    if (await page.locator("#opt-create-new").count()) {
      await page.click("#opt-create-new");
    }

    await page.waitForSelector("#claim-card");

    await page.fill("#ni-handle", handle);
    await expect(page.locator("#btn-claim")).toBeEnabled({ timeout: 20_000 });
    await page.click("#btn-claim");

    // PIN panel (PRF stub returns no PRF)
    await expect(page.locator("#pin-input")).toBeVisible({ timeout: 30_000 });
    await page.fill("#pin-input", "test-pin");
    await page.fill("#pin-confirm", "test-pin");
    await page.click("#btn-pin-ok");

    // Should land on main with identity
    await expect(page.locator("body")).toContainText(handle, { timeout: 45_000 });

    const identity = `${handle}.poweur.net`;
    const docRes = await page.request.get(`${relay.baseUrl}/identities/${identity}`);
    expect(docRes.ok()).toBeTruthy();
    const body = await docRes.json();
    expect(body.identity).toBe(identity);
    expect(body.identity_document || body.public_key).toBeTruthy();
  });
});
