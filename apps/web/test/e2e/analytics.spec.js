import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";
import { registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";

test("analytics defaults off and owner preference survives reopening", async ({ page }) => {
  const relay = await startRelay();
  try {
    await stubPasskeys(page);
    await registerIdentity(page, relay, `analytics${Date.now().toString(36)}`);
    await page.click('.nav-tab[data-page="settings"]');
    await page.click("#row-analytics");
    const consent = page.locator("#analytics-consent");
    await expect(consent).toBeVisible({ timeout: 20_000 });
    await expect(consent).not.toBeChecked();
    await page.check("#analytics-consent");
    await page.click("#analytics-save");
    // close() hides the sheet; it does not unmount it, so the button is still
    // in the DOM — asserting count 0 never becomes true.
    await expect(page.locator("#panel-root")).toBeHidden({ timeout: 20_000 });
    await page.click("#row-analytics");
    await expect(consent).toBeVisible({ timeout: 20_000 });
    await expect(consent).toBeChecked();
    await page.uncheck("#analytics-consent");
    await page.click("#analytics-save");
    await expect(page.locator("#panel-root")).toBeHidden({ timeout: 20_000 });
    await page.click("#row-analytics");
    await expect(consent).toBeVisible({ timeout: 20_000 });
    await expect(consent).not.toBeChecked();
  } finally { relay.stop(); }
});
