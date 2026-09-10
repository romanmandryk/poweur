import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";
import { openApp, registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";

/**
 * The two-device enrollment ceremony, end to end (EPIC-011 E11-T3).
 *
 * Two browser contexts stand in for two devices, because that is the only way
 * to test what the ceremony is actually for: the new one holds no key at all,
 * and everything it ends up with has to arrive through the relay as ciphertext
 * the relay cannot read.
 *
 * The security property under test is the number comparison. The six digits
 * are derived independently on both sides from the ephemeral public key, so
 * they matching is what proves the two screens are talking to each other
 * rather than to an interloper.
 */

test.use({
  launchOptions: {
    args: ["--host-resolver-rules=MAP *.poweur.net 127.0.0.1,MAP poweur.net 127.0.0.1"],
  },
});

test.describe("new-device enrollment", () => {
  /** @type {Awaited<ReturnType<typeof startRelay>>} */
  let relay;

  test.beforeAll(async () => { relay = await startRelay(); });
  test.afterAll(() => relay?.stop());

  test("a second device joins, and both show the same six digits", async ({ browser }) => {
    const laptop = await browser.newContext({ viewport: { width: 375, height: 812 } });
    const phone = await browser.newContext({ viewport: { width: 375, height: 812 } });
    const laptopPage = await laptop.newPage();
    const phonePage = await phone.newPage();
    await stubPasskeys(laptopPage);
    await stubPasskeys(phonePage);

    const identity = await registerIdentity(laptopPage, relay, `enr${Date.now().toString(36)}`);

    // ── The new device opens a rendezvous and shows its codes ───────────────
    await openApp(phonePage, relay);
    await phonePage.click("#btn-welcome-start");
    await phonePage.click("#opt-join-device");
    await phonePage.fill("#join-identity", identity);
    await phonePage.click("#btn-join-start");

    const requestCode = await phonePage.locator(".rendezvous-code").innerText();
    const phoneSas = await phonePage.locator(".sas-code").innerText();
    expect(requestCode.trim()).not.toBe("");
    expect(phoneSas.trim()).toMatch(/^\d{6}$/);

    // ── The trusted device looks it up and sees the *same* digits ───────────
    await laptopPage.click('.nav-tab[data-page="settings"]');
    await laptopPage.click("#row-keys-devices");
    await laptopPage.click("#btn-enroll-device");
    await laptopPage.fill("#enroll-rendezvous", `  ${requestCode.trim()} \n`);
    await laptopPage.click("#btn-enroll-lookup");

    const laptopSas = await laptopPage.locator(".sas-code").innerText();
    // Derived independently on both sides from the ephemeral key — this
    // equality *is* the authentication step.
    expect(laptopSas.trim()).toBe(phoneSas.trim());

    // ── Approve; the phone claims the seed and sets itself up ───────────────
    await laptopPage.click("#btn-enroll-approve");

    // The seed arrives, then the phone wraps it under its own PRF passkey.
    await expect(phonePage.locator(".dest-title")).toHaveText("Messages", { timeout: 60_000 });

    // The phone now holds the same identity, derived from the same seed.
    const phoneRecord = await phonePage.evaluate((id) =>
      JSON.parse(localStorage.getItem(`poweur:identity:${id}`)), identity);
    const laptopRecord = await laptopPage.evaluate((id) =>
      JSON.parse(localStorage.getItem(`poweur:identity:${id}`)), identity);

    expect(phoneRecord.identity).toBe(identity);
    expect(phoneRecord.publicKey).toBe(laptopRecord.publicKey);
    expect(phoneRecord.encPublicKey).toBe(laptopRecord.encPublicKey);
    expect(phoneRecord.seedDerived).toBe(true);
    // A separate enrollment, because it wrapped the seed under its own passkey.
    expect(phoneRecord.enrollmentId).not.toBe(laptopRecord.enrollmentId);

    // ── The laptop's inventory now lists two devices ────────────────────────
    // No reload: unlocked keys are memory-only by design, so a reload would
    // just send this device back to the unlock screen.
    await laptopPage.click("#row-keys-devices");
    await expect(laptopPage.locator("#panel-root .enrollment-row")).toHaveCount(2);
    await expect(laptopPage.locator("#panel-root")).toContainText("this device");

    await laptop.close();
    await phone.close();
  });

  test("a wrong request code is refused rather than half-approved", async ({ page }) => {
    await stubPasskeys(page);
    await registerIdentity(page, relay, `bad${Date.now().toString(36)}`);

    await page.click('.nav-tab[data-page="settings"]');
    await page.click("#row-keys-devices");
    await page.click("#btn-enroll-device");
    await page.fill("#enroll-rendezvous", "not-a-real-rendezvous-id");
    await page.click("#btn-enroll-lookup");

    await expect(page.locator("#toast-root")).toContainText("No pending device");
    // Nothing to approve — the confirmation step never appears.
    await expect(page.locator("#btn-enroll-approve")).toHaveCount(0);
  });

  test("the identity-host door joins without re-typing the name", async ({ browser }) => {
    // This is the production shape: the new device is at alice.poweur.net/app/.
    // Join prefers that identity host when GET / says it is a relay, so the
    // offer and the approving device (also probing that host) meet.
    const laptop = await browser.newContext({ viewport: { width: 375, height: 812 } });
    const phone = await browser.newContext({ viewport: { width: 375, height: 812 } });
    const laptopPage = await laptop.newPage();
    const phonePage = await phone.newPage();
    await stubPasskeys(laptopPage);
    await stubPasskeys(phonePage);

    const identity = await registerIdentity(laptopPage, relay, `host${Date.now().toString(36)}`);
    const port = new URL(relay.baseUrl).port;

    await phonePage.goto(`http://${identity}:${port}/app/`);
    await expect(phonePage.locator("#opt-join-device")).toBeVisible({ timeout: 30_000 });
    await phonePage.click("#opt-join-device");
    // The host already named the identity — no field, no extra tap.
    await expect(phonePage.locator("#join-identity")).toHaveCount(0);
    await expect(phonePage.locator("#btn-join-start")).toHaveCount(0);
    await expect(phonePage.locator(".rendezvous-code")).toBeVisible({ timeout: 30_000 });

    const requestCode = await phonePage.locator(".rendezvous-code").innerText();
    const phoneSas = await phonePage.locator(".sas-code").innerText();
    expect(requestCode.trim()).not.toBe("");
    expect(phoneSas.trim()).toMatch(/^\d{6}$/);

    await laptopPage.click('.nav-tab[data-page="settings"]');
    await laptopPage.click("#row-keys-devices");
    await laptopPage.click("#btn-enroll-device");
    await laptopPage.fill("#enroll-rendezvous", `  ${requestCode.trim()} \n`);
    await laptopPage.click("#btn-enroll-lookup");

    const laptopSas = await laptopPage.locator(".sas-code").innerText();
    expect(laptopSas.trim()).toBe(phoneSas.trim());

    await laptop.close();
    await phone.close();
  });

  test("a shell posts the join offer to the identity host, not the operator apex", async ({ browser }) => {
    // Capacitor has no origin of its own, so first-run talks to poweur.net
    // (127.0.0.1 here). Once the user names the identity, enroll must call
    // that host — block the home-relay hostname so this cannot pass only
    // because both names share one Go process.
    const laptop = await browser.newContext({ viewport: { width: 375, height: 812 } });
    const phone = await browser.newContext({ viewport: { width: 375, height: 812 } });
    const laptopPage = await laptop.newPage();
    const phonePage = await phone.newPage();
    await stubPasskeys(laptopPage);
    await stubPasskeys(phonePage);

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

    await openApp(phonePage, relay);
    await phonePage.click("#btn-welcome-start");
    await phonePage.click("#opt-join-device");
    await phonePage.fill("#join-identity", identity);
    await phonePage.click("#btn-join-start");

    const requestCode = await phonePage.locator(".rendezvous-code").innerText();
    expect(requestCode.trim()).not.toBe("");

    await laptopPage.click('.nav-tab[data-page="settings"]');
    await laptopPage.click("#row-keys-devices");
    await laptopPage.click("#btn-enroll-device");
    await laptopPage.fill("#enroll-rendezvous", requestCode.trim());
    await laptopPage.click("#btn-enroll-lookup");

    await expect(laptopPage.locator(".sas-code")).toHaveText(/^\d{6}$/, { timeout: 15_000 });

    await laptop.close();
    await phone.close();
  });
});
