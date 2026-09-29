import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";
import { registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";

/**
 * Profile and photo on storage v2 (EPIC-020 Phase 9 restore list): saved from
 * the profile editor into `.poweur/public`, back after a reload with this
 * device's avatar cache gone, and served to anyone from the identity host.
 */
const MOBILE = { width: 375, height: 812 };
// A real 2×2 PNG, so the editor's cropper accepts it.
const PNG = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAYAAABytg0kAAAAFklEQVR4nGP8z8DwnwEJMDGgAcICAHwgAwOtNm2nAAAAAElFTkSuQmCC", "base64");

test.describe("profile on v2", () => {
  let relay;
  test.beforeAll(async () => { relay = await startRelay(); });
  test.afterAll(() => relay?.stop());
  test.use({ viewport: MOBILE });

  test("a saved photo and name survive a reload and are public", async ({ browser }) => {
    test.slow();
    const context = await browser.newContext({ viewport: MOBILE });
    const page = await context.newPage();
    await stubPasskeys(page);
    const alice = await registerIdentity(page, relay, `prof${Date.now().toString(36)}`);

    await page.click('.nav-tab[data-page="settings"]');
    await page.click("#row-profile");
    await page.fill("#pe-name", "Alice Durable");
    await page.locator("#pe-avatar").setInputFiles({ name: "me.png", mimeType: "image/png", buffer: PNG });
    await page.click("#pe-save");
    await expect(page.locator('[data-toast-key="success:Profile saved"]')).toBeVisible({ timeout: 30_000 });
    await expect(page.locator("#row-profile")).toContainText("Alice Durable");

    // The profile names a content-addressed avatar in .poweur/public.
    const avatar = await page.evaluate(async () => {
      const { clientFor } = await window.__poweurModule("client");
      const { getActiveIdentity } = await window.__poweurModule("storage");
      return (await clientFor(getActiveIdentity()).profile())?.profile?.avatar ?? "";
    });
    expect(avatar).toMatch(/^avatar-[0-9a-f]+\.(png|webp|jpe?g)$/);

    // A reload is a lock, and this device forgets its copy of the photo:
    // it must come back from the relay.
    await page.evaluate(() => { for (const key of Object.keys(localStorage)) if (key.startsWith("poweur:avatar:")) localStorage.removeItem(key); });
    await page.reload();
    await page.click("#btn-unlock-main");
    await page.locator("#btn-do-unlock").click({ timeout: 30_000 });
    await page.click('.nav-tab[data-page="settings"]');
    await expect(page.locator("#row-profile")).toContainText("Alice Durable", { timeout: 30_000 });
    await page.click("#row-profile");
    await expect(page.locator(".pe-avatar-preview img")).toHaveAttribute("src", /.+/, { timeout: 30_000 });

    // Anyone can fetch the photo from the identity host.
    const visitor = await browser.newContext();
    await visitor.route("**/*", async (route) => {
      const url = new URL(route.request().url());
      if (url.hostname !== alice) return route.continue();
      const target = new URL(`${url.pathname}${url.search}`, relay.baseUrl);
      await route.fulfill({ response: await route.fetch({ url: target.href, headers: { ...route.request().headers(), host: alice } }) });
    });
    const response = await (await visitor.newPage()).goto(`http://${alice}/.well-known/poweur/${avatar}`);
    expect(response.status()).toBe(200);
    expect(response.headers()["content-type"]).toMatch(/^image\//);
    await visitor.close();
    await context.close();
  });
});
