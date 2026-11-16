import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";
import { registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";

/**
 * EPIC-029 E29-T4: a contact's identity can't be looked up, and messaging
 * them still works from the identity document this device kept.
 *
 * Every request that could tell Alice's browser who Bob is — his own host's
 * well-known document and the relay's record of him — is blocked, after a
 * reload has emptied the in-memory resolver cache. What is left is the copy
 * in IndexedDB from the first message.
 */
const MOBILE = { width: 375, height: 812 };

async function sendFrom(page, to, body) {
  await page.evaluate(async ({ to, body }) => {
    const { clientFor } = await window.__poweurModule("client");
    const { getActiveIdentity } = await window.__poweurModule("storage");
    await clientFor(getActiveIdentity()).sendAndArchive(to, body);
  }, { to, body });
}

async function reloadAndUnlock(page) {
  await page.reload();
  await page.click("#btn-unlock-main");
  await page.locator("#btn-do-unlock").click({ timeout: 30_000 });
  await expect(page.locator(".dest-title")).toHaveText("Messages", { timeout: 45_000 });
}

/** Is this a request that would resolve `identity`? */
function resolves(url, identity) {
  return url.hostname === identity || decodeURIComponent(url.pathname) === `/identities/${identity}`;
}

/** Recipients waiting in this browser's outbox (`lib/outbox.js`). */
function queuedFor(page, identity) {
  return page.evaluate((key) => JSON.parse(localStorage.getItem(key) || "[]").map((entry) => entry.recipient),
    `poweur.outbox.v1.${identity.toLowerCase()}`);
}

async function openConversation(page, peer) {
  const row = page.locator(`.conv-row[data-compose-to="${peer}"]`);
  await expect.poll(async () => {
    await page.click('.nav-tab[data-page="messages"]');
    return row.count();
  }, { timeout: 40_000 }).toBeGreaterThan(0);
  await row.click();
}

test.describe("messaging a contact whose identity can't be resolved", () => {
  let relay;
  test.beforeAll(async () => { relay = await startRelay(); });
  test.afterAll(() => relay?.stop());
  test.use({ viewport: MOBILE });

  test("sends from the saved identity, and queues then delivers when the relay is blocked too", async ({ browser }) => {
    test.slow();
    const alice = await (await browser.newContext({ viewport: MOBILE })).newPage();
    const bob = await (await browser.newContext({ viewport: MOBILE })).newPage();
    await stubPasskeys(alice);
    await stubPasskeys(bob);
    const suffix = Date.now().toString(36);
    const aliceId = await registerIdentity(alice, relay, `oua${suffix}`);
    const bobId = await registerIdentity(bob, relay, `oub${suffix}`);

    // The first message resolves Bob; his document stays on this device.
    await sendFrom(alice, bobId, "before the outage");
    const kept = await alice.evaluate((identity) => new Promise((resolve) => {
      const open = indexedDB.open("poweur-identities");
      open.onerror = () => resolve(null);
      open.onsuccess = () => {
        const get = open.result.transaction("documents").objectStore("documents").get(identity);
        get.onsuccess = () => resolve(get.result ?? null);
        get.onerror = () => resolve(null);
      };
    }), bobId);
    expect(kept?.identity).toBe(bobId);
    expect(JSON.parse(kept.raw).identity).toBe(bobId);

    // A reload empties the five-minute in-memory cache. From here on, nothing
    // on the network will say who Bob is.
    await reloadAndUnlock(alice);
    let blockedLookups = 0;
    await alice.route((url) => resolves(url, bobId), (route) => {
      blockedLookups += 1;
      return route.abort("namenotresolved");
    });

    await openConversation(alice, bobId);
    await alice.fill("#thread-input", "during the outage");
    await alice.click("#btn-thread-send");
    await expect(alice.locator(".thread-body")).toContainText("during the outage");

    // It was delivered, not queued: Bob's relay is up, only his identity
    // lookup is not.
    await openConversation(bob, aliceId);
    await expect(bob.locator(".thread-body")).toContainText("during the outage", { timeout: 40_000 });
    expect(blockedLookups).toBeGreaterThan(0);
    expect(await queuedFor(alice, aliceId)).toEqual([]);

    // Now the relay can't take the message either: it waits in the outbox.
    await alice.route((url) => url.pathname === "/messages", (route) =>
      route.request().method() === "POST" ? route.abort("connectionrefused") : route.fallback());
    await alice.fill("#thread-input", "written while offline");
    await alice.click("#btn-thread-send");
    await expect(alice.getByText("Message queued until the relay is reachable")).toBeVisible({ timeout: 20_000 });
    expect(await queuedFor(alice, aliceId)).toEqual([bobId]);

    // The block lifts. Unlocking retries the outbox, and the message arrives.
    await alice.unrouteAll({ behavior: "ignoreErrors" });
    await reloadAndUnlock(alice);
    await expect(bob.locator(".thread-body")).toContainText("written while offline", { timeout: 60_000 });
    await expect.poll(() => queuedFor(alice, aliceId), { timeout: 30_000 }).toEqual([]);
  });
});
