import { test, expect } from "@playwright/test";
import { appPath } from "../helpers/app-path.mjs";
import { startRelay } from "../helpers/relay.mjs";
import { openApp, registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";

/**
 * Pairing a new device, end to end (EPIC-011 E11-T8).
 *
 * Two browser contexts stand in for two devices, because that is the only way
 * to test what pairing is for: the new one holds no key at all, and everything
 * it ends up with arrives through the relay as ciphertext the relay cannot
 * read. Two ways to carry the code: the new device's QR opened on the other
 * device (no digits — the link carries the commitment), and the typed code
 * (both screens show six digits to compare).
 */

test.use({
  launchOptions: {
    args: ["--host-resolver-rules=MAP *.poweur.net 127.0.0.1,MAP poweur.net 127.0.0.1"],
  },
});

async function twoDevices(browser) {
  const laptop = await browser.newContext({ viewport: { width: 375, height: 812 } });
  const phone = await browser.newContext({ viewport: { width: 375, height: 812 } });
  const laptopPage = await laptop.newPage();
  const phonePage = await phone.newPage();
  await stubPasskeys(laptopPage);
  await stubPasskeys(phonePage);
  return { laptop, phone, laptopPage, phonePage };
}

/** The new device starts: returns the code and the QR's link it shows. */
async function startJoin(phonePage, relay, identity) {
  await openApp(phonePage, relay);
  await phonePage.click("#btn-welcome-start");
  await phonePage.click("#opt-join-device");
  await phonePage.fill("#join-identity", identity);
  await phonePage.click("#btn-join-start");
  return readJoin(phonePage);
}

async function readJoin(phonePage) {
  await expect(phonePage.locator("#join-code")).toBeVisible({ timeout: 30_000 });
  const code = (await phonePage.locator("#join-code").innerText()).trim();
  const link = await phonePage.locator("[data-pair-link]").getAttribute("data-pair-link");
  expect(code).toMatch(/^[0-9A-Z]{4}-[0-9A-Z]{4}$/);
  expect(link).toContain(`#pair=${code.replace("-", "")}.`);
  // The QR on show opens the Poweur app unless another approver was chosen.
  await expect(phonePage.locator("svg[data-qr]")).toHaveAttribute("data-qr", /^poweur:\/\/pair\?pair=/);
  // Nothing to compare until the other device answers.
  await expect(phonePage.locator("#join-sas")).toHaveCount(0);
  return { code, link };
}

async function typeCodeOn(laptopPage, code) {
  await laptopPage.click('.nav-tab[data-page="settings"]');
  await laptopPage.click("#row-keys-devices");
  await laptopPage.click("#btn-enroll-device");
  await laptopPage.fill("#pair-code", code);
  await laptopPage.click("#btn-pair-continue");
}

test.describe("new-device pairing", () => {
  /** @type {Awaited<ReturnType<typeof startRelay>>} */
  let relay;

  test.beforeAll(async () => { relay = await startRelay(); });
  test.afterAll(() => relay?.stop());

  test("typed code: both screens show the same six digits, then the keys move", async ({ browser }) => {
    const { laptop, phone, laptopPage, phonePage } = await twoDevices(browser);
    const identity = await registerIdentity(laptopPage, relay, `enr${Date.now().toString(36)}`);
    const { code } = await startJoin(phonePage, relay, identity);
    await expect(phonePage.locator("#btn-join-check")).toBeVisible();
    await expect(phonePage.locator("#join-expiry")).toContainText("Expires");

    // Typed the way a phone keyboard mangles it.
    await typeCodeOn(laptopPage, `  ${code.toLowerCase()} `);
    const laptopSas = (await laptopPage.locator("#pair-sas").innerText({ timeout: 30_000 })).trim();
    const phoneSas = (await phonePage.locator("#join-sas").innerText({ timeout: 30_000 })).trim();
    expect(laptopSas).toMatch(/^\d{3} \d{3}$/);
    // Computed independently from both sides' contributions: this equality is
    // the authentication step, and the relay could not have arranged it.
    expect(laptopSas).toBe(phoneSas);
    await expect(phonePage.locator("#join-compare")).toContainText("don't type");

    await laptopPage.click("#btn-pair-approve");
    await expect(laptopPage.locator("#pair-done")).toContainText("Keys sent");
    await expect(phonePage.locator(".dest-title")).toHaveText("Messages", { timeout: 60_000 });

    const phoneRecord = await phonePage.evaluate((id) => JSON.parse(localStorage.getItem(`poweur:identity:${id}`)), identity);
    const laptopRecord = await laptopPage.evaluate((id) => JSON.parse(localStorage.getItem(`poweur:identity:${id}`)), identity);
    expect(phoneRecord.publicKey).toBe(laptopRecord.publicKey);
    expect(phoneRecord.encPublicKey).toBe(laptopRecord.encPublicKey);
    expect(phoneRecord.enrollmentId).not.toBe(laptopRecord.enrollmentId);

    await laptop.close();
    await phone.close();
  });

  test("scanned link: the other device opens it, unlocks, and approves with no digits", async ({ browser }) => {
    const { laptop, phone, laptopPage, phonePage } = await twoDevices(browser);
    const identity = await registerIdentity(laptopPage, relay, `scan${Date.now().toString(36)}`);
    const { link } = await startJoin(phonePage, relay, identity);

    // The link points at the identity's own host, where a real laptop keeps
    // its keys (checked in the identity-host test below). This laptop
    // registered on the relay's address instead, so open the same link there.
    // A camera opens a new tab: a fresh page load, so locked.
    const url = new URL(link);
    expect(url.hash).toContain(`&id=${identity}`);
    const scanned = await laptop.newPage();
    await stubPasskeys(scanned);
    await scanned.goto(`${relay.baseUrl}${url.pathname}${url.hash}`);
    await expect(scanned).not.toHaveURL(/#pair=/); // taken out of the address bar
    await scanned.click("#btn-pair-unlock");
    await scanned.click("#btn-do-unlock");
    await expect(scanned.locator("#pair-scan")).toContainText(`to ${identity}?`, { timeout: 30_000 });
    await expect(scanned.locator("#pair-sas")).toHaveCount(0);
    await expect(phonePage.locator("#join-scan")).toContainText("Approve on your other device", { timeout: 30_000 });
    await expect(phonePage.locator("#join-sas")).toHaveCount(0);

    await scanned.click("#btn-pair-approve");
    await expect(phonePage.locator(".dest-title")).toHaveText("Messages", { timeout: 60_000 });
    await laptop.close();
    await phone.close();
  });

  test("a browser that lacks the identity hands the pairing to the Poweur app", async ({ browser }) => {
    const { laptop, phone, laptopPage, phonePage } = await twoDevices(browser);
    const identity = await registerIdentity(laptopPage, relay, `app${Date.now().toString(36)}`);
    const { link } = await startJoin(phonePage, relay, identity);
    // A third browser — say, the phone's own, where the keys live in the app.
    const other = await browser.newContext();
    const page = await other.newPage();
    const url = new URL(link);
    await page.goto(`${relay.baseUrl}${url.pathname}${url.hash}`);
    await expect(page.locator("#pair-error")).toContainText(identity);
    await expect(page.locator("#pair-open-app")).toHaveAttribute("href", new RegExp(`^poweur://pair\\?pair=.+&id=${identity}$`));
    await other.close();
    await laptop.close();
    await phone.close();
  });

  test("a code nobody is waiting with is refused rather than half-approved", async ({ page }) => {
    await stubPasskeys(page);
    await registerIdentity(page, relay, `bad${Date.now().toString(36)}`);
    await typeCodeOn(page, "ZZZZ-ZZZZ");
    await expect(page.locator("#pair-error")).toContainText(/not found|expired/);
    await expect(page.locator("#btn-pair-approve")).toHaveCount(0);
  });

  test("the identity-host door joins without re-typing the name", async ({ browser }) => {
    // The production shape: the new device is at alice.poweur.net/app/, which
    // is also where its pairing link points.
    const { laptop, phone, laptopPage, phonePage } = await twoDevices(browser);
    const identity = await registerIdentity(laptopPage, relay, `host${Date.now().toString(36)}`);
    const port = new URL(relay.baseUrl).port;

    await phonePage.goto(`http://${identity}:${port}${appPath()}`);
    await expect(phonePage.locator("#opt-join-device")).toBeVisible({ timeout: 30_000 });
    await phonePage.click("#opt-join-device");
    await expect(phonePage.locator("#join-identity")).toHaveCount(0);
    await expect(phonePage.locator("#btn-join-start")).toHaveCount(0);
    const { code, link } = await readJoin(phonePage);
    expect(link.startsWith(`http://${identity}:${port}/app/#pair=`)).toBe(true);

    await typeCodeOn(laptopPage, code);
    const laptopSas = (await laptopPage.locator("#pair-sas").innerText({ timeout: 30_000 })).trim();
    await expect(phonePage.locator("#join-sas")).toHaveText(laptopSas, { timeout: 30_000 });
    await laptop.close();
    await phone.close();
  });

  test("a shell posts the join offer to the identity host, not the operator apex", async ({ browser }) => {
    // Capacitor has no origin of its own, so first-run talks to poweur.net
    // (127.0.0.1 here). Once the user names the identity, pairing must call
    // that host — block the home-relay hostname so this cannot pass only
    // because both names share one Go process.
    const { laptop, phone, laptopPage, phonePage } = await twoDevices(browser);
    const identity = await registerIdentity(laptopPage, relay, `host2${Date.now().toString(36)}`);
    const homeHosts = new Set(["127.0.0.1", "localhost", "poweur.net"]);
    const blockHomeEnroll = (route) => {
      const host = new URL(route.request().url()).hostname.toLowerCase();
      if (homeHosts.has(host)) {
        return route.fulfill({
          status: 404,
          contentType: "application/json",
          body: JSON.stringify({ error: "not_found", detail: "home relay" }),
        });
      }
      return route.continue();
    };
    await phonePage.route(`**/${identity}/enroll/**`, blockHomeEnroll);
    await laptopPage.route(`**/${identity}/enroll/**`, blockHomeEnroll);

    const { code } = await startJoin(phonePage, relay, identity);
    await typeCodeOn(laptopPage, code);
    await expect(laptopPage.locator("#pair-sas")).toHaveText(/^\d{3} \d{3}$/, { timeout: 15_000 });
    await laptop.close();
    await phone.close();
  });
});
