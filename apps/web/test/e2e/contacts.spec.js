import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";
import { openApp, registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";

/**
 * E15-T2 acceptance: contacts, requests and key pinning, through the UI.
 *
 * Two browser contexts because a contact request is a two-party ceremony —
 * one side's state is only correct if the other side saw what it sent. The
 * relay is real, so the contacts document really does round-trip through DAV.
 */
const MOBILE = { width: 375, height: 812 };

/** The requests tray, once the inbox has actually caught up. */
async function requestFrom(page, sender) {
  await expect
    .poll(async () => {
      await page.click('.nav-tab[data-page="messages"]');
      await page.click('.tray-tab[data-tray="requests"]');
      return page.locator(`[data-accept-contact="${sender}"]`).count();
    }, { timeout: 30_000, message: `no request from ${sender}` })
    .toBeGreaterThan(0);
  return page.locator(`[data-accept-contact="${sender}"]`);
}

test.describe("contacts, requests and key pinning", () => {
  /** @type {Awaited<ReturnType<typeof startRelay>>} */
  let relay;

  test.beforeAll(async () => { relay = await startRelay(); });
  test.afterAll(() => relay?.stop());

  test.use({ viewport: MOBILE });

  test("A requests B by typed name, B accepts, both can message", async ({ browser }) => {
    test.slow();
    const alice = await browser.newContext({ viewport: MOBILE });
    const bob = await browser.newContext({ viewport: MOBILE });
    const alicePage = await alice.newPage();
    const bobPage = await bob.newPage();
    await stubPasskeys(alicePage);
    await stubPasskeys(bobPage);

    const suffix = Date.now().toString(36);
    const aliceId = await registerIdentity(alicePage, relay, `cta${suffix}`);
    const bobId = await registerIdentity(bobPage, relay, `ctb${suffix}`);

    // ── Alice adds Bob by typing his ID ────────────────────────────────────
    await alicePage.click('.nav-tab[data-page="contacts"]');
    await alicePage.click("#btn-add-contact-empty");
    await alicePage.fill(".idin input", bobId);
    // The button unlocks only once the ID actually resolves — a typo cannot
    // become a request.
    await expect(alicePage.locator("#btn-add-contact-go")).toBeEnabled({ timeout: 20_000 });
    await alicePage.fill("#ac-intro", "hi, it's alice");
    await alicePage.fill("#ac-petname", "Bobby");
    await alicePage.click("#btn-add-contact-go");

    // Recorded as outgoing, with the petname, before Bob has done anything.
    await expect(alicePage.locator('[data-contact-menu]')).toHaveCount(1, { timeout: 20_000 });
    await expect(alicePage.locator(".contact-row .chip")).toHaveText("Requested");
    await expect(alicePage.locator(".contact-row .profile-card-name")).toHaveText("Bobby");

    // ── Bob sees it, in the requests tray rather than as a conversation ────
    const accept = await requestFrom(bobPage, aliceId);
    await expect(bobPage.locator(".request-intro")).toHaveText("hi, it's alice");
    // The tray says how many are waiting, from any tray (E07-T3).
    await expect(bobPage.locator('.tray-tab[data-tray="requests"] .tray-badge')).toHaveText("1");

    // The same stranger is one tap from being added in the inbox too.
    await bobPage.click('.tray-tab[data-tray="inbox"]');
    await expect(bobPage.locator(`[data-add-contact="${aliceId}"]`)).toHaveCount(1);

    await bobPage.click('.tray-tab[data-tray="requests"]');
    await accept.click();

    // ── Accepting pins Alice's key and lets her through ────────────────────
    await bobPage.click('.nav-tab[data-page="contacts"]');
    await expect(bobPage.locator(".contact-row .chip")).toHaveText("Contact", { timeout: 20_000 });

    const pinned = await bobPage.evaluate(async (identity) => {
      const { clientFor } = await import("./js/client.js");
      const { getActiveIdentity } = await import("./js/storage.js");
      const contacts = await clientFor(getActiveIdentity()).contacts();
      return (await contacts.load()).contacts.find((c) => c.identity === identity)?.pinned_key ?? null;
    }, aliceId);
    expect(pinned).toMatch(/^ed25519:/);

    // Bob replies through the UI; Alice reads it.
    await bobPage.click("[data-contact-open]");
    await bobPage.fill("#c-body", "hello alice");
    await bobPage.click("#btn-send-msg");
    await expect(bobPage.locator("#c-status")).toHaveText("✓ Sent", { timeout: 20_000 });

    await expect
      .poll(async () => {
        await alicePage.click('.nav-tab[data-page="contacts"]');
        await alicePage.click('.nav-tab[data-page="messages"]');
        await alicePage.click('.tray-tab[data-tray="inbox"]');
        return alicePage.locator(".conv-preview", { hasText: "hello alice" }).count();
      }, { timeout: 30_000, message: "Alice never received Bob's reply" })
      .toBeGreaterThan(0);

    await alice.close();
    await bob.close();
  });

  test("a swapped key blocks the send until it is explicitly trusted", async ({ page }) => {
    test.slow();
    await stubPasskeys(page);
    const suffix = Date.now().toString(36);
    const identity = await registerIdentity(page, relay, `ctk${suffix}`);
    expect(identity).toContain(suffix);

    // A second identity to pin, created head-lessly against the same relay.
    const peer = `ctp${suffix}.poweur.net`;
    await page.evaluate(async ({ target, relayUrl }) => {
      const { createIdentity } = await import("@poweur/client");
      const { identityApiFor } = await import("./js/client.js");
      await createIdentity(identityApiFor(relayUrl), target, { hosted: true });
    }, { target: peer, relayUrl: relay.baseUrl });

    // Pin them, then swap the pinned key for one they never had — what a
    // registrar or relay impersonating a contact looks like from here.
    await page.evaluate(async (target) => {
      const { clientFor } = await import("./js/client.js");
      const { getActiveIdentity } = await import("./js/storage.js");
      const contacts = await clientFor(getActiveIdentity()).contacts();
      await contacts.set(target, "accepted", {});
      const file = await contacts.load();
      const fake = crypto.getRandomValues(new Uint8Array(32));
      const b64url = btoa(String.fromCharCode(...fake))
        .replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
      file.contacts.find((c) => c.identity === target).pinned_key = `ed25519:${b64url}`;
      await contacts.save(file);
    }, peer);

    await page.click('.nav-tab[data-page="messages"]');
    await page.click("#btn-compose");
    await page.fill(".idin input", peer);
    await page.fill("#c-body", "are you still you?");
    await page.click("#btn-send-msg");

    // Blocking dialog, both fingerprints, no send.
    await expect(page.locator("#km-trust")).toBeVisible({ timeout: 20_000 });
    const shownPin = await page.locator("#km-pinned").innerText();
    const shownNow = await page.locator("#km-resolved").innerText();
    expect(shownPin).not.toBe(shownNow);

    await page.click("#km-cancel");
    await expect(page.locator("#c-status")).toHaveText("✕ Not sent — key not trusted");

    // Trusting re-pins and lets the same message go.
    await page.click("#btn-send-msg");
    await expect(page.locator("#km-trust")).toBeVisible({ timeout: 20_000 });
    await page.click("#km-trust");
    await expect(page.locator("#c-status")).toHaveText("✓ Sent", { timeout: 20_000 });
  });
});
