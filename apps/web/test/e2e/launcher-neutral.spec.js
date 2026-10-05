import { test, expect } from "@playwright/test";
import { appPath } from "../helpers/app-path.mjs";
import { startRelay } from "../helpers/relay.mjs";
import { startGuestbook } from "../helpers/guestbook.mjs";

/**
 * The launcher (poweur.net) is neutral ground: it remembers no identity, because keys live on
 * each ID's own host. Whatever an older visit left in its storage is dropped, a sign-in request
 * opened there asks for the exact ID and hands the request over to that ID's own page, and on a
 * wide screen the page uses the width.
 */
let relay;
let guestbook;
let port;

test.beforeAll(async () => {
  relay = await startRelay();
  guestbook = await startGuestbook({ relay });
  port = new URL(relay.baseUrl).port;
});
test.afterAll(() => {
  guestbook?.stop();
  relay?.stop();
});

test.use({
  launchOptions: { args: ["--host-resolver-rules=MAP *.poweur.net 127.0.0.1,MAP poweur.net 127.0.0.1"] },
});

const at = (host, path = appPath()) => `http://${host}:${port}${path}`;

test("the launcher forgets an identity an older visit left behind", async ({ page }) => {
  await page.addInitScript(() => {
    if (sessionStorage.getItem("seeded")) return;
    sessionStorage.setItem("seeded", "1");
    localStorage.setItem("poweur:active", "cccccc.poweur.net");
    localStorage.setItem("poweur:identity:cccccc.poweur.net", JSON.stringify({ identity: "cccccc.poweur.net" }));
    localStorage.setItem("poweur:avatar:cccccc.poweur.net", "{}");
    localStorage.setItem("poweur:theme", "dark");
  });
  await page.goto(at("id.poweur.net"));
  await page.waitForSelector("#claim-card");
  await expect.poll(() => page.evaluate(() => Object.keys(localStorage).filter((k) => /^poweur:(active|identity|avatar)/.test(k)))).toEqual([]);
  // Preferences are not identity state.
  expect(await page.evaluate(() => localStorage.getItem("poweur:theme"))).toBe("dark");
});

test("a sign-in opened on the launcher asks for the exact ID and hands the request over", async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem("poweur:active", "cccccc.poweur.net");
    localStorage.setItem("poweur:identity:cccccc.poweur.net", JSON.stringify({ identity: "cccccc.poweur.net" }));
  });
  const book = await page.context().newPage();
  await book.goto(guestbook.origin);
  await book.click("#signin");
  await expect(book.locator("#pending")).toBeVisible();
  const requestLink = (await book.locator("#approve-url").textContent()).trim();

  await page.goto(at("id.poweur.net", `${appPath()}?auth=${encodeURIComponent(requestLink)}`));
  await expect(page.locator("text=Poweur Guestbook").first()).toBeVisible({ timeout: 45_000 });
  // No remembered identity, no picker, no unlock: only the question.
  await expect(page.locator("#auth-other-input")).toBeVisible();
  await expect(page.locator("#auth-identity")).toHaveCount(0);
  await expect(page.locator("#btn-auth-unlock, #btn-auth-approve")).toHaveCount(0);

  // A bare name is completed with the launcher's domain.
  await page.fill("#auth-other-input", "bobbob");
  await page.click("#btn-auth-other");
  await expect(page).toHaveURL(new RegExp(`^http://bobbob\\.poweur\\.net:${port}/`), { timeout: 20_000 });
  expect(new URL(page.url()).searchParams.get("auth")).toBe(requestLink);
});

test("on a wide screen the launcher sets the pitch beside the claim", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const page = await ctx.newPage();
  await page.goto(at("id.poweur.net"));
  await page.waitForSelector("#claim-card");
  const hero = await page.locator(".landing-hero").boundingBox();
  const claim = await page.locator("#claim-card").boundingBox();
  expect(claim.x).toBeGreaterThan(hero.x + hero.width - 4);
  expect(Math.abs(hero.y + hero.height / 2 - (claim.y + claim.height / 2))).toBeLessThan(300);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await ctx.close();
});
