import { test, expect } from "@playwright/test";
import { mkdirSync } from "node:fs";
import { startRelay } from "../helpers/relay.mjs";
import { openApp, stubPasskeys } from "../helpers/app-ui.mjs";

/**
 * Marketing screenshots for apps/site: a real relay, real identities, real
 * messages and a real shared folder, captured through the real screens.
 *
 *   pnpm --filter @poweur/web build
 *   cd apps/web && SHOTS_DIR=../site/assets/shots/raw npx playwright test -c playwright.shots.config.js
 */
const OUT = process.env.SHOTS_DIR || "test-results/shots";
const DESKTOP = { width: 1440, height: 900 };
const PHONE = { width: 390, height: 844 };

async function register(page, relay, handle, domain) {
  await openApp(page, relay);
  if (await page.locator("#btn-welcome-start").count()) await page.click("#btn-welcome-start");
  if (await page.locator("#opt-create-new").count()) await page.click("#opt-create-new");
  await page.waitForSelector("#claim-card");
  if (await page.locator("#ni-domain").count()) await page.selectOption("#ni-domain", domain);
  await page.fill("#ni-handle", handle);
  await expect(page.locator("#btn-claim")).toBeEnabled({ timeout: 20_000 });
  await page.click("#btn-claim");
  await page.waitForSelector("#btn-onboard-skip", { timeout: 45_000 });
  await page.click("#btn-onboard-skip");
  await expect(page.locator(".dest-title")).toHaveText("Messages", { timeout: 45_000 });
  return `${handle}.${domain}`;
}

/** Mutual contacts, set through the SDK so every chat lands in the inbox. */
async function befriend(page, others) {
  await page.evaluate(async (others) => {
    const { clientFor } = await window.__poweurModule("client");
    const { getActiveIdentity } = await window.__poweurModule("storage");
    const contacts = await clientFor(getActiveIdentity()).contacts();
    for (const [identity, petname] of others) await contacts.set(identity, "accepted", { petname });
  }, others);
}

async function openThread(page, peer) {
  await page.click('.nav-tab[data-page="messages"]');
  const row = page.locator(`.conv-row:has-text("${peer.split(".")[0]}")`).first();
  if (await row.count()) {
    await row.click();
  } else {
    await page.locator("#btn-compose:visible").first().click();
    const picker = page.locator(".new-chat");
    await picker.locator(".idin input").fill(peer);
    await expect(picker.locator(".idin-status")).toContainText("Found", { timeout: 20_000 });
    await page.click("#btn-open-chat");
  }
  await expect(page.locator(".thread-view")).toBeVisible({ timeout: 20_000 });
}

async function say(page, peer, body) {
  await openThread(page, peer);
  await page.fill("#thread-input", body);
  await page.click("#btn-thread-send");
  await expect(page.locator(".bubble-row.mine").last()).toContainText(body, { timeout: 30_000 });
  if (await page.locator("#btn-back:visible").count()) await page.click("#btn-back");
}

async function waitFor(page, peer, text) {
  await expect
    .poll(async () => {
      await openThread(page, peer);
      return page.locator(".bubble-row:not(.mine)").filter({ hasText: text }).count();
    }, { timeout: 45_000 })
    .toBeGreaterThan(0);
  if (await page.locator("#btn-back:visible").count()) await page.click("#btn-back");
}

async function unlockAfterReload(page) {
  await page.reload();
  if (await page.locator("#btn-unlock-main").count()) await page.click("#btn-unlock-main");
  const unlock = page.locator("#btn-do-unlock");
  if (await unlock.isVisible({ timeout: 5_000 }).catch(() => false)) await unlock.click();
  await expect(page.locator(".dest-title")).toHaveText("Messages", { timeout: 45_000 });
}

async function shot(page, name) {
  // Let toasts from the previous step fade out.
  await expect(page.locator("#toast-root > *")).toHaveCount(0, { timeout: 10_000 }).catch(() => {});
  await page.waitForTimeout(600);
  await page.screenshot({ path: `${OUT}/${name}.png` });
}

test("product shots", async ({ browser }) => {
  test.setTimeout(600_000);
  mkdirSync(OUT, { recursive: true });
  const relay = await startRelay({ hostedDomains: "poweur.net,example.com" });
  try {
    const ctx = async (viewport, scale) => {
      const c = await browser.newContext({ viewport, deviceScaleFactor: scale, colorScheme: "dark" });
      // The real alice/bob.poweur.net publish DNS keys; resolve against the local relay only.
      await c.route(/cloudflare-dns\.com|dns\.google/, (route) => route.abort());
      const p = await c.newPage();
      await stubPasskeys(p);
      return p;
    };
    const alice = await ctx(DESKTOP, 2);
    const bob = await ctx(PHONE, 3);
    const carl = await ctx(PHONE, 3);

    const A = await register(alice, relay, "alice", "poweur.net");
    const B = await register(bob, relay, "bob", "poweur.net");
    const C = await register(carl, relay, "carl", "example.com");
    await befriend(alice, [[B, "Bob"], [C, "Carl"]]);
    await befriend(bob, [[A, "Alice"], [C, "Carl"]]);
    await befriend(carl, [[A, "Alice"], [B, "Bob"]]);
    // The app read its contact list at unlock; reload so it sees the new ones.
    for (const page of [alice, bob, carl]) await unlockAfterReload(page);

    // ── The trip (Lisbon is named once, in chat) ────────────────────────────────────────────────────
    await say(bob, A, "Hey! Ready for Lisbon? ✈️");
    await waitFor(alice, B, "Lisbon");
    await say(alice, B, "Almost! I've shared the trip folder with you and Carl.");
    await say(carl, A, "Dinner on Friday is booked — table for 3 at 20:00 🍕");
    await waitFor(alice, C, "Dinner");
    await say(alice, C, "Amazing, thank you! Menu is in the shared folder?");
    await say(carl, A, "Yep, dropped it in /shared/trip-2026 👍");
    await waitFor(bob, A, "trip folder");
    await say(bob, A, "Got it — just added the flight PDFs.");
    await say(bob, A, "Can you check the hotel dates? I think we arrive on the 2nd.");
    await waitFor(alice, B, "hotel dates");
    await say(alice, B, "Checked: Oct 2 → Oct 6, 4 nights. All good 👌");

    // ── The shared folder ─────────────────────────────────────────────────
    await alice.evaluate(async () => {
      const { clientFor } = await window.__poweurModule("client");
      const { getActiveIdentity } = await window.__poweurModule("storage");
      const dav = await clientFor(getActiveIdentity()).dav();
      const base = "shared/trip-2026";
      await dav.mkdir(base);
      await dav.mkdir(`${base}/photos`);
      await dav.write(`${base}/itinerary.md`, "# Trip, Oct 2–6\n\n- Fri: dinner at 20:00\n- Sat: day trip\n");
      await dav.write(`${base}/budget.csv`, "item,eur\nflights,420\nhotel,640\nfood,300\n");
      await dav.write(`${base}/flights.pdf`, "%PDF-1.4\n" + "x".repeat(180_000));
      await dav.write(`${base}/hotel-booking.pdf`, "%PDF-1.4\n" + "x".repeat(96_000));
      await dav.write(`${base}/menu-carl.pdf`, "%PDF-1.4\n" + "x".repeat(64_000));
      await dav.write(`${base}/photos/sunset.jpg`, "x".repeat(2_400_000));
      await dav.write(`shared/recipes.md`, "# Recipes\n");
      await dav.mkdir(`shared/design-reviews`);
    });

    await alice.click('.nav-tab[data-page="files"]');
    await alice.click('[data-open-dir="shared"]');
    await alice.click('[data-share="shared/trip-2026"]');
    for (const who of [B, C]) {
      await alice.fill(".audience-picker .idin input", who);
      await alice.press(".audience-picker .idin input", "Enter");
      await expect(alice.locator(".audience-chips")).toContainText(who.split(".")[0], { timeout: 20_000 });
    }
    await alice.click('#share-perms [data-perm="rw"]');
    await shot(alice, "desktop-share-panel");
    await alice.click("#btn-share-go");
    await expect(alice.locator('.conv-row:has-text("trip-2026") .chip-accent')).toHaveText("Shared", { timeout: 20_000 });

    // ── Captures ──────────────────────────────────────────────────────────
    await alice.click('[data-nav-path=""]');
    await alice.click('[data-open-dir="shared"]');
    await shot(alice, "desktop-files-shared");
    await alice.click('[data-open-dir="shared/trip-2026"]');
    await shot(alice, "desktop-files-trip");

    await openThread(alice, C); // read Carl's messages so no unread badge remains
    await shot(alice, "desktop-thread-carl");
    await openThread(alice, B);
    await shot(alice, "desktop-thread-bob");
    if (await alice.locator("#btn-back:visible").count()) await alice.click("#btn-back");
    await alice.click('.nav-tab[data-page="messages"]');
    await shot(alice, "desktop-inbox");

    await openThread(bob, A);
    await shot(bob, "phone-bob-thread");
    await bob.click("#btn-back");
    await bob.click('.nav-tab[data-page="messages"]');
    await shot(bob, "phone-bob-inbox");
    await bob.click('.tray-tab[data-tray="requests"]');
    await shot(bob, "phone-bob-offer");

    await openThread(carl, A);
    await shot(carl, "phone-carl-thread");

    // ── Use-case card grabs (Alice at phone size) ──────────────────────────
    await alice.setViewportSize(PHONE);
    await alice.click('.nav-tab[data-page="settings"]');
    await alice.click("#row-keys-devices");
    await alice.waitForTimeout(1500);
    await shot(alice, "card-devices");
  } finally {
    relay.stop();
  }
});
