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
    await expect(page.locator("#analytics-consent")).not.toBeChecked();
    await page.check("#analytics-consent");
    await page.click("#analytics-save");
    await expect(page.locator("#analytics-save")).toHaveCount(0);
    await page.click("#row-analytics");
    await expect(page.locator("#analytics-consent")).toBeChecked();
    await page.uncheck("#analytics-consent");
    await page.click("#analytics-save");
    await expect(page.locator("#analytics-save")).toHaveCount(0);
    await page.click("#row-analytics");
    await expect(page.locator("#analytics-consent")).not.toBeChecked();
  } finally { relay.stop(); }
});
