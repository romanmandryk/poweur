import { test, expect } from "@playwright/test";
import { appPath } from "../helpers/app-path.mjs";
import { startRelay } from "../helpers/relay.mjs";
import { registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";

/**
 * E18-T3: the hand-off from a launcher host to the identity's own origin.
 *
 * The *receiving* half is what lives here: an arriving fragment is adopted,
 * made active and left locked. Judging a name before a passkey is spent, and
 * the hop itself across real hosts, moved to `modes.spec.js` — which maps the
 * hosts into the test relay with Chromium's resolver rules and can therefore
 * drive both ends (E15-T7).
 */
const MOBILE = { width: 375, height: 812 };

test.describe("claiming a name", () => {
  /** @type {Awaited<ReturnType<typeof startRelay>>} */
  let relay;

  test.beforeAll(async () => { relay = await startRelay(); });
  test.afterAll(() => relay?.stop());

  test.use({ viewport: MOBILE });

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
    await secondPage.goto(`${relay.baseUrl}${appPath()}#claim=${payload}`);

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
