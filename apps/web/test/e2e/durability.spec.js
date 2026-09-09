import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";
import { openApp, registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";

/**
 * What survives a refresh (EPIC-009 E09-T1, client half).
 *
 * The relay's inbox drains: `GET /messages` hands each message over exactly
 * once and forgets it. A client that renders straight off the last pickup
 * therefore shows a message once and loses it on reload, with nothing left on
 * the relay to re-fetch — which is precisely what people hit. So every
 * assertion here is made *after* a reload, and the reloads are the test.
 *
 * The scenarios mirror `apps/integration/journeys_test.go` and
 * `packages/client-ts/test/journeys-relay.test.ts`: same walks, third client.
 */
const MOBILE = { width: 375, height: 812 };

/**
 * Reload and get back to a usable Messages screen, unlocked.
 *
 * Unwrapped keys are memory-only, so every reload lands on an unlock screen —
 * which is exactly why the refresh is worth testing: everything the app knew
 * is gone, and what comes back has to come back from somewhere durable.
 *
 * `.unlock-name` appears on two screens (the landing card and the `unlock`
 * sub-page), and the card's button only opens the sub-page, so get to
 * `#btn-do-unlock` before pressing anything.
 */
async function reloadAndUnlock(page) {
  await page.reload();
  if (await page.locator("#btn-unlock-main").count()) {
    await page.click("#btn-unlock-main");
  }
  const unlock = page.locator("#btn-do-unlock");
  await expect(unlock).toBeVisible({ timeout: 30_000 });
  await unlock.click();
  // The PIN path (no PRF from the stub authenticator) asks for the PIN.
  if (await page.locator("#pin-input").count()) {
    await page.fill("#pin-input", "test-pin");
    await page.click("#btn-pin-ok");
  }
  await expect(page.locator(".dest-title")).toHaveText("Messages", { timeout: 45_000 });
}

/** Send a message through the compose screen. */
async function composeTo(page, recipient, body) {
  await page.click('.nav-tab[data-page="messages"]');
  // At 375px the header action is hidden and the FAB is the one on screen —
  // exactly one of the two is visible, never both (E15-T11).
  await page.click("#btn-compose");
  await page.fill(".idin input", recipient);
  await expect(page.locator(".idin-status")).toContainText("Found", { timeout: 20_000 });
  await page.fill("#c-body", body);
  await page.click("#btn-send-msg");
  await expect(page.locator(".compose-status")).toContainText("Sent", { timeout: 30_000 });
  await expect(page.locator(".dest-title")).toHaveText("Messages", { timeout: 20_000 });
}

/** Wait for a conversation preview to appear on the Messages destination. */
async function conversationWith(page, peer) {
  await expect
    .poll(async () => {
      await page.click('.nav-tab[data-page="messages"]');
      await page.click('.tray-tab[data-tray="inbox"]');
      return page.locator(`.conv-row[data-compose-to="${peer}"]`).count();
    }, { timeout: 40_000, message: `no conversation with ${peer}` })
    .toBeGreaterThan(0);
  return page.locator(`.conv-row[data-compose-to="${peer}"]`);
}

test.describe("messages survive a refresh", () => {
  /** @type {Awaited<ReturnType<typeof startRelay>>} */
  let relay;

  test.beforeAll(async () => { relay = await startRelay(); });
  test.afterAll(() => relay?.stop());

  test.use({ viewport: MOBILE });

  test("a received message is still there after reload, and after a second one", async ({ browser }) => {
    test.slow();
    const aliceCtx = await browser.newContext({ viewport: MOBILE });
    const bobCtx = await browser.newContext({ viewport: MOBILE });
    const alicePage = await aliceCtx.newPage();
    const bobPage = await bobCtx.newPage();
    await stubPasskeys(alicePage);
    await stubPasskeys(bobPage);

    const suffix = Date.now().toString(36);
    const aliceId = await registerIdentity(alicePage, relay, `dura${suffix}`);
    const bobId = await registerIdentity(bobPage, relay, `durb${suffix}`);

    await composeTo(bobPage, aliceId, "first thing bob said");
    const conversation = await conversationWith(alicePage, bobId);
    await expect(conversation).toContainText("first thing bob said");

    // The reload is the test. Nothing is left on the relay to re-fetch.
    await reloadAndUnlock(alicePage);
    await expect(await conversationWith(alicePage, bobId)).toContainText("first thing bob said");

    // And a message that arrives *after* the archive was loaded joins it
    // rather than replacing it — the merge has to survive a reload too.
    await composeTo(bobPage, aliceId, "second thing bob said");
    await expect
      .poll(async () => {
        await alicePage.click('.nav-tab[data-page="messages"]');
        return alicePage.locator(`.conv-row[data-compose-to="${bobId}"]`).innerText();
      }, { timeout: 40_000 })
      .toContain("second thing");

    await reloadAndUnlock(alicePage);
    await conversationWith(alicePage, bobId);
    // Both are in the archive: open the thread and count what the store holds.
    const bodies = await alicePage.evaluate(async () => {
      const { clientFor } = await import("./js/client.js");
      const { getActiveIdentity } = await import("./js/storage.js");
      const store = await clientFor(getActiveIdentity()).history();
      return (await store.load()).map((r) => r.body);
    });
    expect(bodies).toContain("first thing bob said");
    expect(bodies).toContain("second thing bob said");

    await aliceCtx.close();
    await bobCtx.close();
  });

  test("a sent message is kept too, so a reload shows both sides", async ({ browser }) => {
    test.slow();
    const aliceCtx = await browser.newContext({ viewport: MOBILE });
    const bobCtx = await browser.newContext({ viewport: MOBILE });
    const alicePage = await aliceCtx.newPage();
    const bobPage = await bobCtx.newPage();
    await stubPasskeys(alicePage);
    await stubPasskeys(bobPage);

    const suffix = Date.now().toString(36);
    const aliceId = await registerIdentity(alicePage, relay, `sent${suffix}`);
    const bobId = await registerIdentity(bobPage, relay, `rcvd${suffix}`);

    // Alice writes first. The relay never hands a sender their own message
    // back, so this copy exists only because the client kept it.
    await composeTo(alicePage, bobId, "alice opened the conversation");
    await expect(await conversationWith(alicePage, bobId)).toContainText("alice opened");

    await reloadAndUnlock(alicePage);
    await expect(await conversationWith(alicePage, bobId)).toContainText("alice opened");

    // Bob replies; after a reload Alice has both halves in one thread.
    await composeTo(bobPage, aliceId, "and bob answered");
    await expect
      .poll(async () => {
        await alicePage.click('.nav-tab[data-page="messages"]');
        return alicePage.locator(`.conv-row[data-compose-to="${bobId}"]`).innerText();
      }, { timeout: 40_000 })
      .toContain("and bob answered");

    await reloadAndUnlock(alicePage);
    const queues = await alicePage.evaluate(async () => {
      const { clientFor } = await import("./js/client.js");
      const { getActiveIdentity } = await import("./js/storage.js");
      const store = await clientFor(getActiveIdentity()).history();
      return (await store.load()).map((r) => `${r.queue}:${r.body}`);
    });
    expect(queues).toContain("sent:alice opened the conversation");
    expect(queues).toContain("inbox:and bob answered");

    await aliceCtx.close();
    await bobCtx.close();
  });

  test("the unread badge clears when the conversation is opened, and stays clear", async ({ browser }) => {
    test.slow();
    const aliceCtx = await browser.newContext({ viewport: MOBILE });
    const bobCtx = await browser.newContext({ viewport: MOBILE });
    const alicePage = await aliceCtx.newPage();
    const bobPage = await bobCtx.newPage();
    await stubPasskeys(alicePage);
    await stubPasskeys(bobPage);

    const suffix = Date.now().toString(36);
    const aliceId = await registerIdentity(alicePage, relay, `unra${suffix}`);
    const bobId = await registerIdentity(bobPage, relay, `unrb${suffix}`);

    await composeTo(bobPage, aliceId, "one");
    const conversation = await conversationWith(alicePage, bobId);
    await expect(conversation.locator(".conv-badge")).toHaveText("1", { timeout: 30_000 });

    // Opening it is reading it.
    await conversation.click();
    await expect(alicePage.locator(".compose-body")).toBeVisible();
    await alicePage.click("#btn-back");
    await expect(
      (await conversationWith(alicePage, bobId)).locator(".conv-badge"),
    ).toHaveCount(0, { timeout: 20_000 });

    // The mark is in the tree, not in this tab: it survives the reload that
    // used to reset every count the app had.
    await reloadAndUnlock(alicePage);
    await expect(
      (await conversationWith(alicePage, bobId)).locator(".conv-badge"),
    ).toHaveCount(0, { timeout: 30_000 });

    // A new message makes it count again, which is the other half of a badge
    // being worth anything.
    await composeTo(bobPage, aliceId, "two");
    await expect(
      (await conversationWith(alicePage, bobId)).locator(".conv-badge"),
    ).toHaveText("1", { timeout: 40_000 });

    await aliceCtx.close();
    await bobCtx.close();
  });

  test("anonymous messages are kept, badged as unread, and cleared by looking", async ({ browser }) => {
    test.slow();
    const rcptCtx = await browser.newContext({ viewport: MOBILE });
    const rcptPage = await rcptCtx.newPage();
    await stubPasskeys(rcptPage);

    const suffix = Date.now().toString(36);
    const rcptId = await registerIdentity(rcptPage, relay, `anon${suffix}`);

    // Open the anonymous door, free tier — the policy screen owns this
    // setting; here it is a precondition, not the thing under test.
    await rcptPage.evaluate(async () => {
      const { clientFor } = await import("./js/client.js");
      const { getActiveIdentity } = await import("./js/storage.js");
      await clientFor(getActiveIdentity()).setPolicy("open", { allow: true, challenge: "none" });
    });
    // Written behind the app's back, so let it re-read: the anonymous queue is
    // only polled for the few who turned anonymous on, and the app decides
    // that from the policy it loaded at startup.
    await reloadAndUnlock(rcptPage);

    // An unsigned sender needs no identity at all, which is the point.
    await rcptPage.evaluate(async ({ identity, relayUrl }) => {
      const { sendAnonymous } = await import("@poweur/client");
      const { resolveOptionsForRelay } = await import("./js/client.js");
      await sendAnonymous(identity, "a tip from nobody", {
        resolve: resolveOptionsForRelay(relayUrl),
        targetRelayUrl: relayUrl,
        scheme: "http",
      });
    }, { identity: rcptId, relayUrl: relay.baseUrl });

    // It badges the anonymous tray without being opened…
    await expect
      .poll(async () => {
        await rcptPage.click('.nav-tab[data-page="messages"]');
        return rcptPage.locator('.tray-tab[data-tray="anonymous"] .tray-badge').textContent().catch(() => null);
      }, { timeout: 40_000 })
      .toBe("1");

    // …and looking at the tray clears it, because there is nothing to open:
    // the tray *is* the conversation.
    await rcptPage.click('.tray-tab[data-tray="anonymous"]');
    await expect(rcptPage.locator(".anon-body")).toContainText("a tip from nobody");
    await expect(rcptPage.locator('.tray-tab[data-tray="anonymous"] .tray-badge'))
      .toHaveCount(0, { timeout: 20_000 });

    // The message itself outlives the reload; the drain gave it to us once.
    await reloadAndUnlock(rcptPage);
    await rcptPage.click('.tray-tab[data-tray="anonymous"]');
    await expect(rcptPage.locator(".anon-body")).toContainText("a tip from nobody", { timeout: 30_000 });
    // And it stays read.
    await expect(rcptPage.locator('.tray-tab[data-tray="anonymous"] .tray-badge')).toHaveCount(0);

    await rcptCtx.close();
  });

  test("the archive is sealed on the relay, not stored in the clear", async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: MOBILE });
    const page = await ctx.newPage();
    await stubPasskeys(page);
    const suffix = Date.now().toString(36);
    const me = await registerIdentity(page, relay, `seal${suffix}`);

    const secret = "the passphrase is hunter2";
    await composeTo(page, me, secret); // note to self: one identity, both ends
    await expect
      .poll(async () => {
        await page.click('.nav-tab[data-page="messages"]');
        return page.locator(".conv-row").count();
      }, { timeout: 40_000 })
      .toBeGreaterThan(0);

    const stored = await page.evaluate(async () => {
      const { clientFor } = await import("./js/client.js");
      const { getActiveIdentity } = await import("./js/storage.js");
      const dav = await clientFor(getActiveIdentity()).dav();
      const shards = (await dav.list("poweur-sys/private/messages")).filter((e) => e.dir);
      const files = (await dav.list(shards[0].path)).filter((e) => !e.dir);
      return dav.readText(files[0].path);
    });
    expect(stored).not.toContain(secret);
    const envelope = JSON.parse(stored);
    expect(envelope.alg).toBe("x25519-chacha20-poly1305");
    expect(envelope.ciphertext).toBeTruthy();

    await ctx.close();
  });
});
