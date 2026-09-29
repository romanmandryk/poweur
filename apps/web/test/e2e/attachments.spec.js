import { test, expect } from "@playwright/test";
import { readFile } from "node:fs/promises";
import { startRelay } from "../helpers/relay.mjs";
import { registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";

/**
 * Encrypted attachments on storage v2 (EPIC-020 E20-T11): the file is sealed
 * on the sender's drive and shared read-only with the recipient, who opens it
 * from the conversation.
 */
const MOBILE = { width: 375, height: 812 };

async function sendFrom(page, to, body) {
  await page.evaluate(async ({ to, body }) => {
    const { clientFor } = await window.__poweurModule("client");
    const { getActiveIdentity } = await window.__poweurModule("storage");
    await clientFor(getActiveIdentity()).sendAndArchive(to, body);
  }, { to, body });
}

async function openConversation(page, peer) {
  const row = page.locator(`.conv-row[data-compose-to="${peer}"]`);
  await expect.poll(async () => {
    await page.click('.nav-tab[data-page="messages"]');
    await page.click('.tray-tab[data-tray="inbox"]');
    return row.count();
  }, { timeout: 40_000 }).toBeGreaterThan(0);
  await row.click();
  await expect(page.locator(".thread-view")).toBeVisible();
}

test.describe("attachments", () => {
  let relay;
  test.beforeAll(async () => { relay = await startRelay(); });
  test.afterAll(() => relay?.stop());
  test.use({ viewport: MOBILE });

  test("a file sent in a conversation opens for its recipient", async ({ browser }) => {
    test.slow();
    const alice = await (await browser.newContext({ viewport: MOBILE })).newPage();
    const bob = await (await browser.newContext({ viewport: MOBILE, acceptDownloads: true })).newPage();
    await stubPasskeys(alice);
    await stubPasskeys(bob);
    const suffix = Date.now().toString(36);
    const aliceId = await registerIdentity(alice, relay, `ata${suffix}`);
    const bobId = await registerIdentity(bob, relay, `atb${suffix}`);

    // A first message starts the conversation on both sides.
    await sendFrom(bob, aliceId, "hi alice");
    await openConversation(alice, bobId);

    const bytes = Buffer.from("%PDF-1.4 a small but real attachment\n".repeat(200));
    await alice.locator("#thread-attach-input").setInputFiles({ name: "report.pdf", mimeType: "application/pdf", buffer: bytes });
    await expect(alice.locator(".bubble-text", { hasText: "report.pdf" })).toBeVisible({ timeout: 30_000 });

    await bob.evaluate(async () => {
      const { loadInbox } = await window.__poweurModule("messages");
      await loadInbox({ force: true });
    });
    await openConversation(bob, aliceId);
    await expect(bob.locator(".bubble-text", { hasText: "report.pdf" })).toBeVisible({ timeout: 30_000 });
    const [download] = await Promise.all([
      bob.waitForEvent("download", { timeout: 30_000 }),
      bob.locator(".bubble-attachment").last().click(),
    ]);
    expect(download.suggestedFilename()).toBe("report.pdf");
    expect((await readFile(await download.path())).equals(bytes)).toBe(true);

    // The sender opens their own copy as well.
    const [own] = await Promise.all([
      alice.waitForEvent("download", { timeout: 30_000 }),
      alice.locator(".bubble-attachment").last().click(),
    ]);
    expect((await readFile(await own.path())).equals(bytes)).toBe(true);
  });
});
