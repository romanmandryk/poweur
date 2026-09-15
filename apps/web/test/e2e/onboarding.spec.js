import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";
import { openApp, stubPasskeys } from "../helpers/app-ui.mjs";

/**
 * E15-T5 acceptance: a brand-new identity, created in the browser, comes out
 * of onboarding configured and can do the whole product without the CLI —
 * message, add a contact, set a policy, upload and share a file.
 *
 * This is the one test that refuses to take a shortcut anywhere: everything
 * below is clicked, because the claim being made is about what a person can
 * do with the app, not about what the modules can do when driven directly.
 */
const MOBILE = { width: 375, height: 812 };

/** Register through the real screens and stop in the setup flow. */
async function register(page, relay, handle) {
  await openApp(page, relay);
  if (await page.locator("#btn-welcome-start").count()) await page.click("#btn-welcome-start");
  if (await page.locator("#opt-create-new").count()) await page.click("#opt-create-new");
  await page.waitForSelector("#claim-card");
  await page.fill("#ni-handle", handle);
  await expect(page.locator("#btn-claim")).toBeEnabled({ timeout: 20_000 });
  await page.click("#btn-claim");
  await expect(page.locator(".onboard-title")).toBeVisible({ timeout: 45_000 });
  return `${handle}.poweur.net`;
}

test.describe("first run", () => {
  /** @type {Awaited<ReturnType<typeof startRelay>>} */
  let relay;

  test.beforeAll(async () => { relay = await startRelay(); });
  test.afterAll(() => relay?.stop());

  test.use({ viewport: MOBILE });

  test("a new identity finishes setup and uses the whole app", async ({ browser }) => {
    test.slow();
    const aliceCtx = await browser.newContext({ viewport: MOBILE });
    const bobCtx = await browser.newContext({ viewport: MOBILE });
    const alice = await aliceCtx.newPage();
    const bob = await bobCtx.newPage();
    await stubPasskeys(alice);
    await stubPasskeys(bob);

    const suffix = Date.now().toString(36);
    const aliceId = await register(alice, relay, `oba${suffix}`);
    const bobId = await register(bob, relay, `obb${suffix}`);
    await bob.click("#btn-onboard-skip");
    await expect(bob.locator(".dest-title")).toHaveText("Messages", { timeout: 20_000 });

    // ── Step 1: the inbox policy, which is why the flow exists ─────────────
    await expect(alice.locator(".onboard-title")).toHaveText("Who can message you?");
    await expect(alice.locator(".policy-mode.selected")).toHaveAttribute(
      "data-mode", "contacts_and_requests",
    );
    await alice.click("#btn-onboard-next");

    // ── Step 2: the profile, optional ──────────────────────────────────────
    await expect(alice.locator(".onboard-title")).toHaveText("How should people see you?", { timeout: 20_000 });
    await alice.fill("#pe-name", "Alice Example");
    await alice.fill("#pe-bio", "testing the front door");
    await alice.click("#btn-onboard-next");

    // ── Step 3: done ───────────────────────────────────────────────────────
    await expect(alice.locator(".onboard-title")).toHaveText("You're set", { timeout: 20_000 });
    await alice.click("#btn-onboard-next");
    await expect(alice.locator(".dest-title")).toHaveText("Messages");

    // Both choices stuck, and Settings reports them.
    await alice.click('.nav-tab[data-page="settings"]');
    await expect(alice.locator("#row-policy .settings-row-value"))
      .toHaveText("Contacts, and requests from others", { timeout: 20_000 });
    await expect(alice.locator("#row-profile .settings-row-value")).toHaveText("Alice Example");

    // ── …and the app works, all of it, from the UI ─────────────────────────
    // A contact request, accepted on the other side.
    await alice.click('.nav-tab[data-page="contacts"]');
    await alice.click("#btn-add-contact-empty");
    await alice.fill(".idin input", bobId);
    await expect(alice.locator("#btn-add-contact-go")).toBeEnabled({ timeout: 20_000 });
    await alice.click("#btn-add-contact-go");
    // The send is async behind a loading overlay; polling Bob before this
    // lands is why CI timed out on an empty requests tray.
    await expect(alice.locator(".contact-row .chip")).toHaveText("Requested", { timeout: 20_000 });

    await expect
      .poll(async () => {
        await bob.click('.nav-tab[data-page="messages"]');
        await bob.click('.tray-tab[data-tray="requests"]');
        return bob.locator(`[data-accept-contact="${aliceId}"]`).count();
      }, { timeout: 30_000, message: "request never arrived" })
      .toBeGreaterThan(0);
    await bob.click(`[data-accept-contact="${aliceId}"]`);

    // Alice's client has to *see* the acceptance before her own policy lets
    // bob through — the accept rides her requests queue, and reading it is
    // what promotes bob from `requested` to a contact on her side too.
    await expect
      .poll(async () => {
        await alice.click('.nav-tab[data-page="settings"]');
        await alice.click('.nav-tab[data-page="messages"]');
        await alice.click('.nav-tab[data-page="contacts"]');
        return alice.locator('.contact-row .chip:text-is("Contact")').count();
      }, { timeout: 30_000, message: "alice never saw the acceptance" })
      .toBe(1);

    // A message, now that alice's policy allows a contact through.
    await bob.click('.nav-tab[data-page="contacts"]');
    await expect(bob.locator(".contact-row .chip")).toHaveText("Contact", { timeout: 20_000 });
    await bob.click("[data-contact-open]");
    await expect(bob.locator(".thread-view")).toBeVisible();
    await bob.fill("#thread-input", "hello from bob");
    await bob.click("#btn-thread-send");
    await expect(bob.locator(".bubble-row.mine").last()).toContainText("hello from bob", { timeout: 20_000 });
    // A conversation stays open after sending, as in any chat app; on a phone
    // it covers the tab bar, so leave it to go anywhere else.
    await bob.click("#btn-back");

    await expect
      .poll(async () => {
        await alice.click('.nav-tab[data-page="settings"]');
        await alice.click('.nav-tab[data-page="messages"]');
        return alice.locator(".conv-preview", { hasText: "hello from bob" }).count();
      }, { timeout: 30_000, message: "message never arrived" })
      .toBeGreaterThan(0);

    // A file, uploaded and shared with that contact.
    await alice.click('.nav-tab[data-page="files"]');
    await alice.click('[data-open-dir="shared"]');
    await alice.setInputFiles("#ff-upload", {
      name: "hello.txt",
      mimeType: "text/plain",
      buffer: Buffer.from("written from the browser"),
    });
    await expect(alice.locator(".conv-list")).toContainText("hello.txt", { timeout: 30_000 });
    await alice.click('[data-share="shared/hello.txt"]');
    await alice.fill(".audience-picker .idin input", bobId);
    await alice.press(".audience-picker .idin input", "Enter");
    await expect(alice.locator(".audience-chips")).toContainText(bobId.split(".")[0], { timeout: 20_000 });
    await alice.click("#btn-share-go");
    await expect(alice.locator('.conv-row:has-text("hello.txt") .chip-accent'))
      .toHaveText("Shared", { timeout: 20_000 });

    // The grantee sees exactly that file, and nothing else of alice's.
    await bob.click('.nav-tab[data-page="files"]');
    await bob.click("#btn-files-shared");
    await bob.fill(".owner-picker .idin input", aliceId);
    await bob.press(".owner-picker .idin input", "Enter");
    await expect(bob.locator(".visitor-banner")).toContainText(aliceId, { timeout: 20_000 });
    await bob.click('[data-open-dir="shared"]');
    await expect(bob.locator(".conv-name")).toHaveText("hello.txt", { timeout: 20_000 });

    await aliceCtx.close();
    await bobCtx.close();
  });

  test("dialogs are keyboard-usable and rows answer Enter", async ({ page }) => {
    test.slow();
    await stubPasskeys(page);
    const suffix = Date.now().toString(36);
    await register(page, relay, `oba11y${suffix}`.slice(0, 20));
    await page.click("#btn-onboard-skip");

    await page.click('.nav-tab[data-page="settings"]');
    await page.click("#row-policy");
    await page.waitForSelector("#policy-save");

    // Focus lands inside the dialog, not behind it.
    const focusedInside = await page.evaluate(() =>
      document.getElementById("panel-root").contains(document.activeElement));
    expect(focusedInside).toBe(true);

    // Tab wraps rather than walking out into the page behind the backdrop.
    for (let i = 0; i < 40; i++) await page.keyboard.press("Tab");
    expect(await page.evaluate(() =>
      document.getElementById("panel-root").contains(document.activeElement))).toBe(true);

    // Escape closes it, and focus returns to the row that opened it.
    await page.keyboard.press("Escape");
    await expect(page.locator("#panel-root")).toBeHidden();
    expect(await page.evaluate(() => document.activeElement?.id)).toBe("row-policy");

    // A row with role="button" is operable from the keyboard.
    await page.click('.nav-tab[data-page="files"]');
    const folder = page.locator('[data-open-dir="shared"]').first();
    await expect(folder).toBeVisible({ timeout: 20_000 });
    await folder.press("Enter");
    await expect(page.locator(".breadcrumbs")).toContainText("shared", { timeout: 20_000 });
  });

  test("every step can be skipped, and nothing is written when they are", async ({ page }) => {
    test.slow();
    await stubPasskeys(page);
    const suffix = Date.now().toString(36);
    await register(page, relay, `obs${suffix}`);

    await page.click("#btn-onboard-skip");
    await expect(page.locator(".dest-title")).toHaveText("Messages");

    // Skipping writes nothing: the relay default stands, and the panel says so
    // rather than pretending a policy was chosen.
    const policy = await page.evaluate(async () => {
      const { clientFor } = await window.__poweurModule("client");
      const { getActiveIdentity } = await window.__poweurModule("storage");
      const client = clientFor(getActiveIdentity());
      const [inbox, profile] = await Promise.all([client.policy(), client.profile()]);
      return { explicit: inbox.explicit, mode: inbox.policy.mode, profileWritten: profile.explicit };
    });
    expect(policy).toEqual({ explicit: false, mode: "open", profileWritten: false });

    await page.click('.nav-tab[data-page="settings"]');
    await expect(page.locator("#row-profile .settings-row-value")).toHaveText("Not set", { timeout: 20_000 });
  });
});
