import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";
import { registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";

/**
 * E15-T3 acceptance: inbox policy, anonymous ingress and proof-of-work.
 *
 * The proof-of-work is solved by the actual browser under test — that is the
 * point of the feature and the part no Node test covers, since the cost being
 * bearable in a browser is the whole design constraint (EPIC-014).
 */
const MOBILE = { width: 375, height: 812 };

test.describe("inbox policy, anonymous and PoW", () => {
  /** @type {Awaited<ReturnType<typeof startRelay>>} */
  let relay;

  test.beforeAll(async () => { relay = await startRelay(); });
  test.afterAll(() => relay?.stop());

  test.use({ viewport: MOBILE });

  test("policy round-trips, and a browser-solved anonymous message lands only in its tray", async ({ browser }) => {
    test.slow();
    const owner = await browser.newContext({ viewport: MOBILE });
    const stranger = await browser.newContext({ viewport: MOBILE });
    const ownerPage = await owner.newPage();
    const strangerPage = await stranger.newPage();
    await stubPasskeys(ownerPage);
    await stubPasskeys(strangerPage);

    const suffix = Date.now().toString(36);
    const ownerId = await registerIdentity(ownerPage, relay, `pola${suffix}`);
    await registerIdentity(strangerPage, relay, `polb${suffix}`);

    // ── Set a policy from Settings ─────────────────────────────────────────
    await ownerPage.click('.nav-tab[data-page="settings"]');
    await ownerPage.click("#row-policy");
    await ownerPage.waitForSelector("#policy-save");
    await ownerPage.click('[data-mode="contacts_and_requests"]');
    await ownerPage.click("#policy-anon-allow");
    await ownerPage.click('[data-challenge="pow"]');

    // 8 bits: the relay's floor, and enough to prove the challenge round-trip
    // without spending the test's budget mining.
    await ownerPage.fill("#policy-pow-bits", "8");
    await expect(ownerPage.locator("#policy-bits-readout")).toContainText("8 bits");
    await expect(ownerPage.locator("#policy-bits-warning")).toBeHidden();
    await ownerPage.click("#policy-save");

    // The rows report the saved state, not what was typed into the panel.
    await expect(ownerPage.locator("#row-policy .settings-row-value"))
      .toHaveText("Contacts, and requests from others", { timeout: 20_000 });
    await expect(ownerPage.locator("#row-policy-anon .settings-row-value")).toHaveText("On · 8 bits");

    // …and the document on the relay is what the CLI would have written.
    const stored = await ownerPage.evaluate(async () => {
      const { clientFor } = await import("./js/client.js");
      const { getActiveIdentity } = await import("./js/storage.js");
      return (await clientFor(getActiveIdentity()).policy()).policy;
    });
    expect(stored).toMatchObject({
      mode: "contacts_and_requests",
      anonymous: { allow: true, challenge: "pow", pow_bits: 8, max_bytes: 4096, max_per_day: 20 },
    });

    // ── A stranger sends anonymously, solving the PoW in their browser ─────
    // A signed-in app offers no anonymous toggle (E15-T13): someone signed in
    // talks as themselves. The send path itself is the SDK's, run in-page.
    await strangerPage.click('.nav-tab[data-page="messages"]');
    await strangerPage.click("#btn-compose");
    await expect(strangerPage.locator(".new-chat")).toBeVisible();
    await expect(strangerPage.locator("#c-anon")).toHaveCount(0);
    await strangerPage.click("#btn-back");
    await sendAnonymouslyFrom(strangerPage, ownerId, "from nobody in particular");

    // ── It lands in the anonymous tray, and nowhere else ───────────────────
    await expect
      .poll(async () => {
        await ownerPage.click('.nav-tab[data-page="messages"]');
        await ownerPage.click('.tray-tab[data-tray="anonymous"]');
        return ownerPage.locator(".anon-body", { hasText: "from nobody in particular" }).count();
      }, { timeout: 30_000, message: "anonymous message never arrived" })
      .toBe(1);

    // No sender, so nothing to reply to and nobody to add.
    await expect(ownerPage.locator(".anon-row .chip")).toHaveText("Anonymous");
    await expect(ownerPage.locator(".anon-row [data-compose-to]")).toHaveCount(0);
    await expect(ownerPage.locator(".anon-row [data-add-contact]")).toHaveCount(0);

    // The signed inbox never saw it.
    await ownerPage.click('.tray-tab[data-tray="inbox"]');
    await expect(ownerPage.locator(".conv-list")).toHaveCount(0);

    await owner.close();
    await stranger.close();
  });

  test("a message arrives without the reader touching anything", async ({ browser }) => {
    test.slow();
    // EPIC-009 E09-T2: the app holds a push stream open, so a message shows up
    // with no click, no navigation and no poll interval to wait out.
    const readerCtx = await browser.newContext({ viewport: MOBILE });
    const senderCtx = await browser.newContext({ viewport: MOBILE });
    const reader = await readerCtx.newPage();
    const sender = await senderCtx.newPage();
    await stubPasskeys(reader);
    await stubPasskeys(sender);

    const suffix = Date.now().toString(36);
    const readerId = await registerIdentity(reader, relay, `pshr${suffix}`);
    await registerIdentity(sender, relay, `pshs${suffix}`);

    // The reader sits on Messages and does nothing at all from here on.
    await reader.click('.nav-tab[data-page="messages"]');
    await expect(reader.locator(".empty-state-title")).toHaveText("No messages yet");

    await sender.click("#btn-compose");
    await sender.locator(".new-chat .idin input").fill(readerId);
    await expect(sender.locator(".new-chat .idin-status")).toContainText("Found", { timeout: 20_000 });
    await sender.click("#btn-open-chat");
    await sender.fill("#thread-input", "pushed, not polled");
    await sender.click("#btn-thread-send");
    await expect(sender.locator(".bubble-row.mine").last()).toContainText("pushed, not polled", { timeout: 20_000 });

    await expect(reader.locator(".conv-preview")).toHaveText("pushed, not polled", { timeout: 30_000 });

    await readerCtx.close();
    await senderCtx.close();
  });

  test("an anonymous message is refused while the policy denies it", async ({ page }) => {
    test.slow();
    await stubPasskeys(page);
    const suffix = Date.now().toString(36);
    const sender = await registerIdentity(page, relay, `pols${suffix}`);
    expect(sender).toContain(suffix);

    // A fresh identity that has never opted in: the default everywhere is deny.
    const closed = `polc${suffix}.poweur.net`;
    await page.evaluate(async ({ target, relayUrl }) => {
      const { createIdentity } = await import("@poweur/client");
      const { identityApiFor } = await import("./js/client.js");
      await createIdentity(identityApiFor(relayUrl), target, { hosted: true });
    }, { target: closed, relayUrl: relay.baseUrl });

    // Refused by the relay, and reported as an error rather than silently dropped.
    await expect(sendAnonymouslyFrom(page, closed, "let me in")).rejects.toThrow();
  });
});

/**
 * Send with no identity attached, from inside the page: the SDK call the app
 * used to make from compose, with the same relay resolution, so the proof of
 * work is still mined in the browser.
 */
async function sendAnonymouslyFrom(page, to, body) {
  await page.evaluate(async ({ to, body }) => {
    const { sendAnonymous } = await import("@poweur/client");
    const { resolveOptionsForRelay } = await import("./js/client.js");
    const { getActiveIdentity, relayUrlFor } = await import("./js/storage.js");
    const resolve = resolveOptionsForRelay(relayUrlFor(getActiveIdentity()));
    await sendAnonymous(to, body, { resolve, ...(resolve.scheme ? { scheme: resolve.scheme } : {}) });
  }, { to, body });
}
