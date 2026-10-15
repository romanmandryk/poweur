import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";
import { registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";

/**
 * Subjects (EPIC-009 E09-T13): a subject conversation started from the
 * By-subject view, read and answered by the other side, a changed subject
 * becoming a second row, and By ID keeping one row with dividers.
 */
const MOBILE = { width: 375, height: 812 };

async function useView(page, view) {
  await page.click('.nav-tab[data-page="messages"]');
  await page.click('.tray-tab[data-tray="inbox"]');
  await page.click(`[data-view="${view}"]`);
}

async function send(page, body) {
  await page.fill("#thread-input", body);
  await page.click("#btn-thread-send");
  await expect(page.locator(".bubble-row.mine").last()).toContainText(body, { timeout: 30_000 });
}

test.describe("subjects", () => {
  let relay;
  test.beforeAll(async () => { relay = await startRelay(); });
  test.afterAll(() => relay?.stop());
  test.use({ viewport: MOBILE });

  test("start, answer and change a subject; both views agree", async ({ browser }) => {
    test.slow();
    const aliceCtx = await browser.newContext({ viewport: MOBILE });
    const bobCtx = await browser.newContext({ viewport: MOBILE });
    const alice = await aliceCtx.newPage();
    const bob = await bobCtx.newPage();
    await stubPasskeys(alice);
    await stubPasskeys(bob);
    const suffix = Date.now().toString(36);
    await registerIdentity(alice, relay, `sja${suffix}`);
    const bobId = await registerIdentity(bob, relay, `sjb${suffix}`);

    // By ID is the default; a new message's subject starts collapsed.
    await alice.click('.nav-tab[data-page="messages"]');
    await expect(alice.locator('[data-view="id"]')).toHaveAttribute("aria-checked", "true");
    await useView(alice, "subject");
    await alice.click("#btn-compose");
    const picker = alice.locator(".new-chat");
    await picker.locator(".idin input").fill(bobId);
    await expect(picker.locator(".idin-status")).toContainText("Found", { timeout: 20_000 });
    await alice.click("#btn-open-chat");
    // From By subject the field is open.
    await alice.fill("#thread-subject", "Q3 budget");
    await send(alice, "numbers attached");
    await alice.click("#btn-back");

    // Bob sees a Q3 budget row, opens it, and the reply field is prefilled.
    await useView(bob, "subject");
    const q3 = bob.locator(".conv-row", { has: bob.locator(".conv-subject", { hasText: "Q3 budget" }) });
    await expect(q3).toHaveCount(1, { timeout: 40_000 });
    await q3.click();
    await expect(bob.locator("#thread-subject")).toHaveValue("Q3 budget");
    await send(bob, "looks good");

    // Changing the subject warns, and makes a second row.
    await bob.fill("#thread-subject", "Offsite");
    await expect(bob.locator("#subject-starts-new")).toBeVisible();
    await send(bob, "and the offsite?");
    await bob.click("#btn-back");
    await expect(bob.locator(".conv-subject")).toHaveText(["Offsite", "Q3 budget"], { timeout: 20_000 });

    // By ID: one row for the contact, both subjects as dividers.
    await useView(bob, "id");
    await expect(bob.locator(".conv-row")).toHaveCount(1);
    await bob.locator(".conv-row").click();
    await expect(bob.locator(".subject-divider")).toHaveText(["Q3 budget", "Offsite"]);
    await bob.click("#btn-back");

    // The mode survives a reload.
    await useView(bob, "subject");
    await bob.reload();
    await bob.click("#btn-unlock-main");
    await bob.locator("#btn-do-unlock").click({ timeout: 30_000 });
    await expect(bob.locator(".dest-title")).toHaveText("Messages", { timeout: 45_000 });
    await expect(bob.locator('[data-view="subject"]')).toHaveAttribute("aria-checked", "true", { timeout: 30_000 });
    // …and so does the grouping, rebuilt from the archive.
    await expect(bob.locator(".conv-subject")).toHaveText(["Offsite", "Q3 budget"], { timeout: 30_000 });
  });
});
