import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";
import { registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";

/**
 * The way back when everything is gone: delete the identity from the browser, then restore it
 * from the 24 words alone. The restored identity must be the same one — it can still unlock
 * and show the same kit — and a wrong kit must change nothing.
 */
test.describe("Restore from recovery kit", () => {
  let relay;

  test.beforeAll(async () => {
    relay = await startRelay();
  });

  test.afterAll(() => relay?.stop());

  test("an identity removed from the browser comes back from its 24 words", async ({ page }) => {
    await stubPasskeys(page);
    const identity = await registerIdentity(page, relay, "phraseback");

    await page.click('.nav-tab[data-page="settings"]');
    await page.click("#row-recovery-kit");
    const panel = page.locator("#panel-root");
    await expect(panel.locator(".mnemonic-word")).toHaveCount(24);
    const words = await panel.locator(".mnemonic-word").allInnerTexts();
    await page.keyboard.press("Escape");

    await page.click("#row-remove-id");
    await page.click("#panel-confirm-remove");
    await expect(page.locator("#settings-identity #row-recovery-kit")).toHaveCount(0);

    // Nothing is left here but the way to add an identity.
    await page.goto(`${relay.baseUrl}/app/`);
    if (await page.locator("#btn-welcome-start").count()) await page.click("#btn-welcome-start");
    await page.click("#opt-restore-phrase");

    // A kit that belongs to no one is refused and nothing is stored.
    await page.fill("#restore-identity", identity);
    await page.fill("#restore-words", ["abandon", ...words.slice(1)].join(" "));
    await page.click("#btn-restore-phrase");
    await expect(page.locator(".toast, [role=alert], [role=status]").filter({ hasText: /recovery kit/i }).first()).toBeVisible({ timeout: 20_000 });
    expect(await page.evaluate(() => Object.keys(localStorage).filter((k) => k.startsWith("poweur:identity:")))).toEqual([]);

    // The right words restore the same identity on this browser.
    await page.fill("#restore-words", words.join(" "));
    await page.click("#btn-restore-phrase");
    await expect(page.locator(".dest-title")).toHaveText("Messages", { timeout: 45_000 });

    await page.click('.nav-tab[data-page="settings"]');
    await expect(page.locator("#settings-identity")).toContainText("phraseback");
    await page.click("#row-recovery-kit");
    await expect(page.locator("#panel-root .mnemonic-word")).toHaveText(words);
  });
});
