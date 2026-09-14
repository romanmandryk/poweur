import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";
import { registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";

/**
 * Nav badges (EPIC-015 E15-T13).
 *
 * Messages carries the number of unread messages and Contacts the number of
 * requests waiting for an answer — on every destination, not only the one the
 * news belongs to. So the reader here sits on Files and touches nothing while
 * mail and a request arrive by push.
 */
const MOBILE = { width: 375, height: 812 };

async function sendFrom(page, to, bodies) {
  await page.evaluate(async ({ to, bodies }) => {
    const { clientFor } = await import("./js/client.js");
    const { getActiveIdentity } = await import("./js/storage.js");
    const client = clientFor(getActiveIdentity());
    for (const body of bodies) await client.sendAndArchive(to, body);
  }, { to, bodies });
}

async function requestContactFrom(page, to) {
  await page.evaluate(async (to) => {
    const { clientFor } = await import("./js/client.js");
    const { getActiveIdentity } = await import("./js/storage.js");
    const client = clientFor(getActiveIdentity());
    await client.sessions.ensure(client.signer);
    await client.requestContact(to, { intro: "hello from carol" });
  }, to);
}

test.describe("nav badges", () => {
  /** @type {Awaited<ReturnType<typeof startRelay>>} */
  let relay;

  test.beforeAll(async () => { relay = await startRelay(); });
  test.afterAll(() => relay?.stop());

  test.use({ viewport: MOBILE });

  test("count unread messages and pending requests from any destination, and clear", async ({ browser }) => {
    test.slow();
    const contexts = await Promise.all([0, 1, 2].map(() => browser.newContext({ viewport: MOBILE })));
    const [alice, bob, carol] = await Promise.all(contexts.map((ctx) => ctx.newPage()));
    for (const page of [alice, bob, carol]) await stubPasskeys(page);

    const suffix = Date.now().toString(36);
    const aliceId = await registerIdentity(alice, relay, `nba${suffix}`);
    const bobId = await registerIdentity(bob, relay, `nbb${suffix}`);
    await registerIdentity(carol, relay, `nbc${suffix}`);

    const messagesBadge = alice.locator('.nav-tab[data-page="messages"] .nav-badge');
    const contactsBadge = alice.locator('.nav-tab[data-page="contacts"] .nav-badge');

    // Nothing waiting, nothing shown.
    await alice.click('.nav-tab[data-page="files"]');
    await expect(messagesBadge).toHaveCount(0);
    await expect(contactsBadge).toHaveCount(0);

    // Alice stays on Files from here and does not touch anything.
    await sendFrom(bob, aliceId, ["one", "two"]);
    await expect(messagesBadge).toHaveText("2", { timeout: 40_000 });
    await expect(alice.locator('.nav-tab[data-page="messages"]')).toHaveAttribute("aria-label", "Messages, 2 new");

    // A request is counted on Contacts, and not again as a message.
    await requestContactFrom(carol, aliceId);
    await expect(contactsBadge).toHaveText("1", { timeout: 40_000 });
    await expect(messagesBadge).toHaveText("2");

    // Reading the conversation clears the Messages badge; the request is still waiting.
    await alice.click('.nav-tab[data-page="messages"]');
    await alice.click(`.conv-row[data-compose-to="${bobId}"]`);
    await expect(alice.locator(".thread-view")).toBeVisible();
    await expect(messagesBadge).toHaveCount(0, { timeout: 20_000 });
    await expect(contactsBadge).toHaveText("1");

    await Promise.all(contexts.map((ctx) => ctx.close()));
  });

  test("unlocking pulls what arrived while locked, on any screen, without the push stream", async ({ browser }) => {
    test.slow();
    const contexts = await Promise.all([0, 1, 2].map(() => browser.newContext({ viewport: MOBILE })));
    const [alice, bob, carol] = await Promise.all(contexts.map((ctx) => ctx.newPage()));
    for (const page of [alice, bob, carol]) await stubPasskeys(page);

    const suffix = Date.now().toString(36);
    const aliceId = await registerIdentity(alice, relay, `nua${suffix}`);
    await registerIdentity(bob, relay, `nub${suffix}`);
    await registerIdentity(carol, relay, `nuc${suffix}`);

    // No push stream at all: whatever shows up after unlocking was fetched
    // because unlocking fetched it, not because an event said to.
    await alice.route("**/events/**", (route) => route.abort());

    // Keys are memory-only, so a reload is a lock.
    await alice.reload();
    await sendFrom(bob, aliceId, ["while you were away", "and again"]);
    await requestContactFrom(carol, aliceId);

    // Unlock from Contacts, not Messages.
    await alice.click('.nav-tab[data-page="contacts"]');
    await alice.click("#btn-unlock-main");
    const unlock = alice.locator("#btn-do-unlock");
    await expect(unlock).toBeVisible({ timeout: 30_000 });
    await unlock.click();
    await expect(alice.locator(".dest-title")).toHaveText("Contacts", { timeout: 45_000 });

    await expect(alice.locator('.nav-tab[data-page="messages"] .nav-badge')).toHaveText("2", { timeout: 30_000 });
    await expect(alice.locator('.nav-tab[data-page="contacts"] .nav-badge')).toHaveText("1", { timeout: 30_000 });

    await Promise.all(contexts.map((ctx) => ctx.close()));
  });
});
