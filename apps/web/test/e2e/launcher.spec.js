import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";
import { registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";

/**
 * E18-T3: claiming a name before spending a passkey on it, and the hand-off
 * from the launcher host to the identity's own origin.
 *
 * The cross-origin hop itself cannot run here — a test relay is one host on
 * 127.0.0.1 and `alice.poweur.net` is in no DNS — so this covers the two
 * halves that *are* testable: the launcher computes the right hand-off, and an
 * arriving fragment is adopted. The relay side of the same flow is
 * `TestINT_NAME_01`.
 */
const MOBILE = { width: 375, height: 812 };

test.describe("claiming a name", () => {
  /** @type {Awaited<ReturnType<typeof startRelay>>} */
  let relay;

  test.beforeAll(async () => { relay = await startRelay(); });
  test.afterAll(() => relay?.stop());

  test.use({ viewport: MOBILE });

  test("the handle is checked before any passkey is created", async ({ page }) => {
    test.slow();
    await stubPasskeys(page);
    // A registered identity to collide with.
    const taken = await registerIdentity(page, relay, `clm${Date.now().toString(36)}`);

    await page.goto(`${relay.baseUrl}/app/`);
    await page.click('.nav-tab[data-page="launcher"]');

    // Reserved: the relay's own vocabulary, not a guess made in the browser.
    await page.fill("#ni-handle", "admin");
    await expect(page.locator("#ni-availability")).toHaveText(
      "This name is reserved by the operator.", { timeout: 20_000 });
    await expect(page.locator("#btn-next-id")).toBeDisabled();

    // Already claimed.
    await page.fill("#ni-handle", taken.split(".")[0]);
    await expect(page.locator("#ni-availability")).toHaveText("That name is already taken.", { timeout: 20_000 });
    await expect(page.locator("#btn-next-id")).toBeDisabled();

    // Non-ASCII: the homoglyph gate, reported as a character-set problem.
    await page.fill("#ni-handle", "аdmin");
    await expect(page.locator("#ni-availability")).toHaveText("Use only a-z, 0-9 and hyphen.", { timeout: 20_000 });

    // …and a free one unlocks the step.
    const free = `free${Date.now().toString(36)}`;
    await page.fill("#ni-handle", free);
    await expect(page.locator("#ni-availability")).toContainText("is available", { timeout: 20_000 });
    await expect(page.locator("#btn-next-id")).toBeEnabled();
  });

  test("an identity handed over by the launcher is adopted and locked", async ({ browser }) => {
    test.slow();
    // Claim on one context, then hand the record to a *fresh* one, which is
    // what crossing an origin does to localStorage.
    const first = await browser.newContext({ viewport: MOBILE });
    const firstPage = await first.newPage();
    await stubPasskeys(firstPage);
    const identity = await registerIdentity(firstPage, relay, `hnd${Date.now().toString(36)}`);

    const payload = await firstPage.evaluate((id) => {
      const record = JSON.parse(localStorage.getItem(`poweur:identity:${id}`));
      const json = new TextEncoder().encode(JSON.stringify({ identity: id, record }));
      return btoa(String.fromCharCode(...json))
        .replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
    }, identity);

    const second = await browser.newContext({ viewport: MOBILE });
    const secondPage = await second.newPage();
    await stubPasskeys(secondPage);
    // Arrive with the fragment on the *first* load, the way a redirect from
    // another origin does — the config is seeded before any script runs, since
    // a real identity origin would already know its own relay.
    await secondPage.addInitScript((url) => {
      localStorage.setItem("poweur:config", JSON.stringify({ relayUrl: url, parentDomain: "poweur.net" }));
    }, relay.baseUrl);
    await secondPage.goto(`${relay.baseUrl}/app/#claim=${payload}`);

    // Adopted: the identity is active and the app asks to unlock it, rather
    // than offering to create another one.
    await expect(secondPage.locator(".unlock-name")).toHaveText(identity.split(".")[0], { timeout: 20_000 });
    expect(await secondPage.evaluate(() => localStorage.getItem("poweur:active"))).toBe(identity);

    // The wrapped blob does not stay in the address bar or in history.
    expect(secondPage.url()).not.toContain("#claim=");

    await first.close();
    await second.close();
  });
});
