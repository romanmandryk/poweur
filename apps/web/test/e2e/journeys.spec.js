import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";
import { openApp, registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";

/**
 * The CLI journeys, walked in a browser.
 *
 * `apps/integration/journeys_test.go` asserts these through the Go CLI and
 * `packages/client-ts/test/journeys-relay.test.ts` through the SDK. This file
 * is the third client, and the one where the answer is a *screen* rather than
 * a return value: a policy that routes correctly but renders the message in
 * the wrong tray is still a bug the other two suites cannot see.
 *
 * Refreshes are sprinkled through on purpose. The relay's inbox drains, so
 * anything the app cannot redraw after a reload is gone for good.
 */
const MOBILE = { width: 375, height: 812 };

/** Add a second identity to a browser that already holds one. */
async function addIdentity(page, handle) {
  await page.click("#id-pill");
  await page.click("#dd-add-id");
  await page.click("#opt-create-new");
  await page.waitForSelector("#claim-card");
  await page.fill("#ni-handle", handle);
  await expect(page.locator("#btn-claim")).toBeEnabled({ timeout: 20_000 });
  await page.click("#btn-claim");
  await page.waitForSelector("#btn-onboard-skip", { timeout: 45_000 });
  await page.click("#btn-onboard-skip");
  await expect(page.locator(".dest-title")).toHaveText("Messages", { timeout: 45_000 });
  return `${handle}.poweur.net`;
}

/** Switch to an identity already in this browser, unlocking it. */
async function switchTo(page, identity) {
  await page.click("#id-pill");
  await page.click(`[data-switch="${identity}"]`);
  if (await page.locator("#btn-unlock-main").count()) await page.click("#btn-unlock-main");
  const unlock = page.locator("#btn-do-unlock");
  await expect(unlock).toBeVisible({ timeout: 30_000 });
  await unlock.click();
  await expect(page.locator(".dest-title")).toHaveText("Messages", { timeout: 45_000 });
}

async function composeTo(page, recipient, body) {
  await page.click('.nav-tab[data-page="messages"]');
  // At 375px the header action is hidden and the FAB is the one on screen —
  // exactly one of the two is visible, never both (E15-T11).
  await page.click("#btn-compose");
  // Scoped to the picker: a bottom sheet opened earlier in the test can still
  // hold its own identity input, and `.idin-status` would match both.
  const picker = page.locator(".new-chat");
  await picker.locator(".idin input").fill(recipient);
  await expect(picker.locator(".idin-status")).toContainText("Found", { timeout: 20_000 });
  await page.click("#btn-open-chat");
  await expect(page.locator(".thread-view")).toBeVisible({ timeout: 20_000 });
  await page.fill("#thread-input", body);
  await page.click("#btn-thread-send");
  await expect(page.locator(".bubble-row.mine").last()).toContainText(body, { timeout: 30_000 });
  await page.click("#btn-back");
}

/** Set the inbox policy through the SDK — Settings owns the UI for it. */
async function setPolicy(page, mode, anonymous = undefined) {
  await page.evaluate(async ({ mode, anonymous }) => {
    const { clientFor } = await window.__poweurModule("client");
    const { getActiveIdentity } = await window.__poweurModule("storage");
    await clientFor(getActiveIdentity()).setPolicy(mode, anonymous);
  }, { mode, anonymous });
}

test.describe("browser journeys", () => {
  /** @type {Awaited<ReturnType<typeof startRelay>>} */
  let relay;

  test.beforeAll(async () => { relay = await startRelay(); });
  test.afterAll(() => relay?.stop());

  test.use({ viewport: MOBILE });

  test("two identities on one relay, in one browser, keep separate mail", async ({ page }) => {
    test.slow();
    await stubPasskeys(page);
    const suffix = Date.now().toString(36);
    const work = await registerIdentity(page, relay, `work${suffix}`);
    const home = await addIdentity(page, `home${suffix}`);

    // Both are offered by the switcher, and the newest is active.
    await page.click("#id-pill");
    await expect(page.locator("[data-switch]")).toHaveCount(2);
    await page.keyboard.press("Escape");
    expect(await page.evaluate(() => localStorage.getItem("poweur:active"))).toBe(home);

    // Home writes to work. Both ends are in this one browser, which is
    // exactly the case a globally-keyed message store gets wrong.
    await composeTo(page, work, "reminder: dentist");
    await expect(page.locator(".dest-title")).toHaveText("Messages", { timeout: 20_000 });

    // Home's own view shows the sent copy and nothing of work's mail.
    await expect(page.locator(`.conv-row[data-compose-to="${work}"]`))
      .toContainText("reminder: dentist", { timeout: 20_000 });

    await switchTo(page, work);
    await expect
      .poll(async () => {
        await page.click('.nav-tab[data-page="messages"]');
        return page.locator(`.conv-row[data-compose-to="${home}"]`).count();
      }, { timeout: 40_000 })
      .toBe(1);
    await expect(page.locator(`.conv-row[data-compose-to="${home}"]`))
      .toContainText("reminder: dentist");
    // …and only that one conversation: work has no business seeing home's.
    await expect(page.locator(".conv-row")).toHaveCount(1);

    // After a reload each identity still has its own archive, keyed to it.
    await page.reload();
    if (await page.locator("#btn-unlock-main").count()) await page.click("#btn-unlock-main");
    await page.click("#btn-do-unlock");
    await expect(page.locator(".dest-title")).toHaveText("Messages", { timeout: 45_000 });
    await expect(page.locator(`.conv-row[data-compose-to="${home}"]`))
      .toContainText("reminder: dentist", { timeout: 30_000 });
  });

  test("each policy routes a stranger to exactly one tray", async ({ browser }) => {
    test.slow();
    const ownerCtx = await browser.newContext({ viewport: MOBILE });
    const strangerCtx = await browser.newContext({ viewport: MOBILE });
    const ownerPage = await ownerCtx.newPage();
    const strangerPage = await strangerCtx.newPage();
    await stubPasskeys(ownerPage);
    await stubPasskeys(strangerPage);

    const suffix = Date.now().toString(36);
    const ownerId = await registerIdentity(ownerPage, relay, `mtxo${suffix}`);
    const strangerId = await registerIdentity(strangerPage, relay, `mtxs${suffix}`);

    // ── open: a stranger reaches the inbox like anyone else ────────────────
    await setPolicy(ownerPage, "open");
    await composeTo(strangerPage, ownerId, "open policy hello");
    await expect
      .poll(async () => {
        await ownerPage.click('.nav-tab[data-page="messages"]');
        await ownerPage.click('.tray-tab[data-tray="inbox"]');
        return ownerPage.locator(`.conv-row[data-compose-to="${strangerId}"]`).count();
      }, { timeout: 40_000 })
      .toBe(1);
    // A stranger is marked as one, with adding them one tap away.
    await expect(ownerPage.locator(`[data-add-contact="${strangerId}"]`)).toHaveCount(1);

    // ── contacts_only: the same sender is refused, visibly ─────────────────
    await setPolicy(ownerPage, "contacts_only");
    await strangerPage.click('.nav-tab[data-page="messages"]');
    await strangerPage.click("#btn-compose");
    const picker = strangerPage.locator(".new-chat");
    await picker.locator(".idin input").fill(ownerId);
    await expect(picker.locator(".idin-status")).toContainText("Found", { timeout: 20_000 });
    await strangerPage.click("#btn-open-chat");
    await strangerPage.fill("#thread-input", "contacts_only hello");
    await strangerPage.click("#btn-thread-send");
    // The sender is told, rather than left believing it went.
    await expect(strangerPage.locator(".thread-status")).toContainText("✕", { timeout: 30_000 });
    await strangerPage.click("#btn-back");

    // ── contacts_and_requests: a request lands in the requests tray ────────
    await setPolicy(ownerPage, "contacts_and_requests");
    await strangerPage.click('.nav-tab[data-page="contacts"]');
    await strangerPage.click(
      await strangerPage.locator("#btn-add-contact-empty").count() ? "#btn-add-contact-empty" : "#btn-add-contact",
    );
    await strangerPage.locator("#add-contact-input .idin input").fill(ownerId);
    await expect(strangerPage.locator("#add-contact-input .idin-status"))
      .toContainText("Found", { timeout: 20_000 });
    await strangerPage.fill("#ac-intro", "let me in");
    await strangerPage.click("#btn-add-contact-go");

    const accept = ownerPage.locator(`[data-accept-contact="${strangerId}"]`);
    await expect
      .poll(async () => {
        await ownerPage.click('.nav-tab[data-page="messages"]');
        await ownerPage.click('.tray-tab[data-tray="requests"]');
        return accept.count();
      }, { timeout: 40_000, message: "request never reached the requests tray" })
      .toBe(1);
    await expect(ownerPage.locator(".request-intro")).toHaveText("let me in");

    // Accepting completes the handshake and reopens the door.
    await accept.click();
    await expect(ownerPage.locator('.tray-tab[data-tray="requests"] .tray-badge'))
      .toHaveCount(0, { timeout: 20_000 });
    await composeTo(strangerPage, ownerId, "thanks for accepting");
    await expect
      .poll(async () => {
        await ownerPage.click('.nav-tab[data-page="messages"]');
        await ownerPage.click('.tray-tab[data-tray="inbox"]');
        return ownerPage.locator(`.conv-row[data-compose-to="${strangerId}"]`).innerText();
      }, { timeout: 40_000 })
      .toContain("thanks for accepting");
    // Now a contact, so no "add" affordance is offered any more.
    await expect(ownerPage.locator(`[data-add-contact="${strangerId}"]`)).toHaveCount(0);

    await ownerCtx.close();
    await strangerCtx.close();
  });

  test("a stranger's message and a contact's both survive a reload, in their trays", async ({ browser }) => {
    test.slow();
    const ownerCtx = await browser.newContext({ viewport: MOBILE });
    const strangerCtx = await browser.newContext({ viewport: MOBILE });
    const ownerPage = await ownerCtx.newPage();
    const strangerPage = await strangerCtx.newPage();
    await stubPasskeys(ownerPage);
    await stubPasskeys(strangerPage);

    const suffix = Date.now().toString(36);
    const ownerId = await registerIdentity(ownerPage, relay, `mixo${suffix}`);
    const strangerId = await registerIdentity(strangerPage, relay, `mixs${suffix}`);

    await setPolicy(ownerPage, "open", { allow: true, challenge: "none" });
    await composeTo(strangerPage, ownerId, "signed and attributable");
    await strangerPage.evaluate(async ({ identity, relayUrl }) => {
      const { sendAnonymous } = await window.__poweurModule("sdk");
      const { resolveOptionsForRelay } = await window.__poweurModule("client");
      await sendAnonymous(identity, "unsigned and not", {
        resolve: resolveOptionsForRelay(relayUrl),
        targetRelayUrl: relayUrl,
        scheme: "http",
      });
    }, { identity: ownerId, relayUrl: relay.baseUrl });

    // Both arrive, each in its own tray.
    await expect
      .poll(async () => {
        await ownerPage.click('.nav-tab[data-page="messages"]');
        await ownerPage.click('.tray-tab[data-tray="inbox"]');
        return ownerPage.locator(`.conv-row[data-compose-to="${strangerId}"]`).count();
      }, { timeout: 40_000 })
      .toBe(1);
    await ownerPage.click('.tray-tab[data-tray="anonymous"]');
    await expect(ownerPage.locator(".anon-body")).toContainText("unsigned and not", { timeout: 40_000 });

    // Reload: both are still there, and still in the tray they belong to.
    await ownerPage.reload();
    if (await ownerPage.locator("#btn-unlock-main").count()) await ownerPage.click("#btn-unlock-main");
    await ownerPage.click("#btn-do-unlock");
    await expect(ownerPage.locator(".dest-title")).toHaveText("Messages", { timeout: 45_000 });

    await expect(ownerPage.locator(`.conv-row[data-compose-to="${strangerId}"]`))
      .toContainText("signed and attributable", { timeout: 30_000 });
    // The anonymous one did not leak into the signed conversation list.
    await expect(ownerPage.locator(".conv-list")).not.toContainText("unsigned and not");
    await ownerPage.click('.tray-tab[data-tray="anonymous"]');
    await expect(ownerPage.locator(".anon-body")).toContainText("unsigned and not", { timeout: 20_000 });

    await ownerCtx.close();
    await strangerCtx.close();
  });
});
