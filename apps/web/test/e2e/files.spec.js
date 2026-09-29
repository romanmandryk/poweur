import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";
import { registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";

test.describe("storage-v2 Files", () => {
  let relay;
  test.beforeAll(async () => { relay = await startRelay(); });
  test.afterAll(() => relay?.stop());

  test("upload, share a folder, accept it and revoke access", async ({ browser }) => {
    const aliceContext = await browser.newContext();
    const bobContext = await browser.newContext();
    const alicePage = await aliceContext.newPage();
    const bobPage = await bobContext.newPage();
    await stubPasskeys(alicePage);
    await stubPasskeys(bobPage);
    const suffix = Date.now().toString(36);
    const alice = await registerIdentity(alicePage, relay, `filea${suffix}`);
    const bob = await registerIdentity(bobPage, relay, `fileb${suffix}`);

    await alicePage.click('.nav-tab[data-page="files"]');
    await alicePage.click("#btn-new-folder");
    await alicePage.fill("#dialog-text", "Plans");
    await alicePage.click("#dialog-ok");
    await expect(alicePage.getByText("Plans", { exact: true })).toBeVisible();
    await alicePage.getByText("Plans", { exact: true }).click();
    await expect(alicePage.locator('nav[aria-label="Folder path"]')).toBeVisible();
    await alicePage.locator("#file-upload-input").setInputFiles({ name: "roadmap.txt", mimeType: "text/plain", buffer: Buffer.from("private roadmap") });
    await expect(alicePage.getByText("roadmap.txt", { exact: true })).toBeVisible();

    await alicePage.locator('nav[aria-label="Folder path"] button').first().click();
    await alicePage.getByRole("button", { name: "Share Plans" }).click();
    await alicePage.fill("#link-password", "blue-sky");
    await alicePage.getByRole("button", { name: "Create link" }).click();
    const createdLink = await alicePage.getByRole("textbox", { name: "Share link" }).inputValue();
    const linkContext = await browser.newContext();
    // Keep the identity URL (the viewer parses its drive from the hostname),
    // while routing this test-only hosted name to the ephemeral relay.
    await linkContext.route("**/*", async (route) => {
      const requestUrl = new URL(route.request().url());
      if (requestUrl.hostname !== alice) return route.continue();
      const target = new URL(`${requestUrl.pathname}${requestUrl.search}`, relay.baseUrl);
      const response = await route.fetch({ url: target.href, headers: { ...route.request().headers(), host: alice } });
      await route.fulfill({ response });
    });
    const linkPage = await linkContext.newPage();
    await linkPage.goto(createdLink);
    await expect(linkPage.getByLabel("This link needs a password")).toBeVisible({ timeout: 20_000 });
    await linkPage.fill("#password-input", "blue-sky");
    await linkPage.getByRole("button", { name: "Open" }).click();
    await expect(linkPage.getByText("roadmap.txt", { exact: true })).toBeVisible({ timeout: 20_000 });
    await linkContext.close();
    await alicePage.getByRole("button", { name: "Cancel" }).click();

    await alicePage.getByRole("button", { name: "Share Plans" }).click();
    await alicePage.fill("#share-member", bob);
    await alicePage.getByRole("button", { name: "Share", exact: true }).click();
    await expect(alicePage.locator(`[data-toast-key="success:Shared with ${bob}"]`)).toBeVisible({ timeout: 20_000 });

    // Push is advisory; explicitly exercise its deterministic inbox catch-up.
    await bobPage.evaluate(async () => {
      const { loadInbox } = await window.__poweurModule("messages");
      await loadInbox({ force: true });
    });
    await bobPage.click('.nav-tab[data-page="files"]');
    await bobPage.getByRole("tab", { name: "Shared with me" }).click();
    await expect(bobPage.getByText("Plans", { exact: true })).toBeVisible({ timeout: 20_000 });
    await bobPage.getByRole("button", { name: "Accept" }).click();
    await expect(bobPage.getByText("roadmap.txt", { exact: true })).toBeVisible({ timeout: 20_000 });

    await alicePage.getByRole("button", { name: "Share Plans" }).click();
    const bobAccess = alicePage.getByText(`${bob} · read`);
    await expect(bobAccess).toBeVisible();
    await bobAccess.locator("..").getByRole("button", { name: "Revoke" }).click();
    await expect(alicePage.locator('[data-toast-key="success:Access revoked"]')).toBeVisible();

    await bobPage.getByRole("tab", { name: "My files" }).click();
    await bobPage.getByRole("tab", { name: "Shared with me" }).click();
    await bobPage.getByText("Plans", { exact: true }).click();
    await expect(bobPage.locator(".toast.error")).toBeVisible({ timeout: 20_000 });

    await aliceContext.close();
    await bobContext.close();
    expect(alice).not.toBe(bob);
  });
});
