import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";
import { registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";

/**
 * EPIC-021's reason to exist: the legacy shell rebuilt the whole page from an
 * HTML string on every change, so buttons flickered, focus jumped and a half-
 * typed reply was re-created under the cursor. The rewrite must keep the same
 * DOM nodes across navigation and live updates.
 */
const MOBILE = { width: 375, height: 812 };

test.describe("no full-page re-render", () => {
  /** @type {Awaited<ReturnType<typeof startRelay>>} */
  let relay;
  test.beforeAll(async () => { relay = await startRelay(); });
  test.afterAll(() => relay?.stop());
  test.use({ viewport: MOBILE });

  test("navigation and a live message keep the shell nodes, the draft and focus", async ({ browser }) => {
    test.slow();
    const readerCtx = await browser.newContext({ viewport: MOBILE });
    const senderCtx = await browser.newContext({ viewport: MOBILE });
    const reader = await readerCtx.newPage();
    const sender = await senderCtx.newPage();
    await stubPasskeys(reader);
    await stubPasskeys(sender);

    const suffix = Date.now().toString(36);
    const readerId = await registerIdentity(reader, relay, `nfr${suffix}`);
    const senderId = await registerIdentity(sender, relay, `nfs${suffix}`);

    // Tag the header and nav; a re-render from strings would drop the tags.
    await reader.evaluate(() => {
      document.querySelector(".app-header").dataset.probe = "header";
      document.querySelector(".bottom-nav").dataset.probe = "nav";
    });
    for (const page of ["contacts", "files", "settings", "messages"]) {
      await reader.click(`.nav-tab[data-page="${page}"]`);
    }
    await expect(reader.locator('.app-header[data-probe="header"]')).toHaveCount(1);
    await expect(reader.locator('.bottom-nav[data-probe="nav"]')).toHaveCount(1);

    // Open a conversation with the sender, start typing, and leave focus there.
    await reader.click("#btn-compose");
    await reader.locator(".new-chat .idin input").fill(senderId);
    await expect(reader.locator(".new-chat .idin-status")).toContainText("Found", { timeout: 20_000 });
    await reader.click("#btn-open-chat");
    await expect(reader.locator(".thread-view")).toBeVisible({ timeout: 20_000 });
    await reader.fill("#thread-input", "half-typed reply");
    await reader.evaluate(() => { document.querySelector("#thread-input").dataset.probe = "input"; });

    // The sender writes while the reader is mid-sentence.
    await sender.evaluate(async (to) => {
      const { clientFor } = await window.__poweurModule("client");
      const { getActiveIdentity } = await window.__poweurModule("storage");
      await clientFor(getActiveIdentity()).sendAndArchive(to, "arrived mid-sentence");
    }, readerId);
    await expect(reader.locator(".bubble-row.theirs").last()).toContainText("arrived mid-sentence", { timeout: 40_000 });

    // Same input node, same text, still focused.
    await expect(reader.locator('#thread-input[data-probe="input"]')).toHaveValue("half-typed reply");
    expect(await reader.evaluate(() => document.activeElement?.id)).toBe("thread-input");

    await readerCtx.close();
    await senderCtx.close();
  });
});
