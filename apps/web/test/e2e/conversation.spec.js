import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";
import { registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";

/**
 * The conversation view (EPIC-015 E15-T13).
 *
 * A tray row used to open compose, so nothing ever showed an earlier message.
 * This walks the view as a reader meets it: the newest page first, "Load more"
 * only while something older exists, a reply from the bottom of the thread,
 * a message arriving while it is open. Reload coverage returns in EPIC-020.
 */
const MOBILE = { width: 375, height: 812 };

/** Send from the page's own unlocked client — a dozen compose round-trips would be the slow part. */
async function sendFrom(page, to, bodies) {
  await page.evaluate(async ({ to, bodies }) => {
    const { clientFor } = await window.__poweurModule("client");
    const { getActiveIdentity } = await window.__poweurModule("storage");
    const client = clientFor(getActiveIdentity());
    for (const body of bodies) await client.sendAndArchive(to, body);
  }, { to, bodies });
}

async function openConversation(page, peer) {
  const row = page.locator(`.conv-row[data-compose-to="${peer}"]`);
  await expect
    .poll(async () => {
      await page.click('.nav-tab[data-page="messages"]');
      await page.click('.tray-tab[data-tray="inbox"]');
      return row.count();
    }, { timeout: 40_000, message: `no conversation with ${peer}` })
    .toBeGreaterThan(0);
  await row.click();
  await expect(page.locator(".thread-view")).toBeVisible();
}



test.describe("conversation view", () => {
  /** @type {Awaited<ReturnType<typeof startRelay>>} */
  let relay;

  test.beforeAll(async () => { relay = await startRelay(); });
  test.afterAll(() => relay?.stop());

  test.use({ viewport: MOBILE });

  test("shows the latest ten, loads more on demand, replies and updates live", async ({ browser }) => {
    test.slow();
    const aliceCtx = await browser.newContext({ viewport: MOBILE });
    const bobCtx = await browser.newContext({ viewport: MOBILE });
    const alice = await aliceCtx.newPage();
    const bob = await bobCtx.newPage();
    await stubPasskeys(alice);
    await stubPasskeys(bob);

    const suffix = Date.now().toString(36);
    const aliceId = await registerIdentity(alice, relay, `cva${suffix}`);
    const bobId = await registerIdentity(bob, relay, `cvb${suffix}`);

    await sendFrom(bob, aliceId, Array.from({ length: 12 }, (_, i) => `history ${i + 1}`));
    await expect
      .poll(async () => {
        await alice.click('.nav-tab[data-page="messages"]');
        return alice.locator(`.conv-row[data-compose-to="${bobId}"] .conv-preview`).innerText().catch(() => "");
      }, { timeout: 40_000 })
      .toBe("history 12");

    await openConversation(alice, bobId);
    const bubbles = alice.locator(".bubble-row");
    await expect(bubbles).toHaveCount(10);
    await expect(alice.locator(".thread-body")).toContainText("history 12");
    await expect(alice.locator("#btn-thread-more")).toContainText("2 earlier");

    await alice.click("#btn-thread-more");
    await expect(bubbles).toHaveCount(12);
    await expect(alice.locator("#btn-thread-more")).toHaveCount(0);

    // Opening it was reading it.
    await alice.click("#btn-back");
    await expect(alice.locator(`.conv-row[data-compose-to="${bobId}"] .conv-badge`)).toHaveCount(0, { timeout: 20_000 });
    await openConversation(alice, bobId);

    await alice.fill("#thread-input", "reply from the thread");
    await alice.click("#btn-thread-send");
    await expect(alice.locator(".bubble-row.mine").last()).toContainText("reply from the thread", { timeout: 30_000 });
    await expect(alice.locator("#thread-input")).toHaveValue("");

    // Sitting in the thread, touching nothing: the push stream brings it in.
    await sendFrom(bob, aliceId, ["arrived while open"]);
    await expect(alice.locator(".bubble-row.theirs").last()).toContainText("arrived while open", { timeout: 40_000 });

    // Reload/archive assertions return with EPIC-020 Phase 9.

    await aliceCtx.close();
    await bobCtx.close();
  });
});
