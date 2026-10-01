import { test, expect, devices } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";
import { registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";
import { authorizeURL, pkce, startBridge, startRP } from "../helpers/bridge.mjs";

/**
 * EPIC-022 E22-T10: the bridge's own pages. Signed out, the landing offers one
 * thing — sign in; signed in, what to do next. Developers register an app from
 * the console. A phone is offered this device first; a desktop never is.
 */
test.describe("OAuth bridge pages", () => {
  let relay;
  let bridge;
  let rp;

  test.beforeAll(async () => {
    relay = await startRelay();
    rp = await startRP();
    bridge = await startBridge({ relay, redirectUri: rp.redirectUri });
  });

  test.afterAll(() => {
    bridge?.stop();
    rp?.stop();
    relay?.stop();
  });

  test("sign in to the bridge, then register an application", async ({ page }) => {
    await stubPasskeys(page);
    const identity = await registerIdentity(page, relay, "bridgedev");

    await page.goto(bridge.issuer);
    await expect(page.locator("h1")).toHaveText("Sign in to apps with your Poweur ID");
    await expect(page.locator("#home-developers")).toHaveCount(0);
    await page.click("#home-signin");
    await expect(page.locator("#identify-title")).toHaveText("Sign in");
    await page.fill("#identity", identity);
    await page.click("#identify-continue");
    await expect(page.locator("#approve-identity")).toHaveText(identity);
    await page.click(`a:has-text("Continue at ${relay.addr}")`);
    await expect(page.locator("#btn-auth-unlock, #btn-auth-approve")).toBeVisible({ timeout: 45_000 });
    if (await page.locator("#btn-auth-unlock").count()) {
      await page.click("#btn-auth-unlock");
    }
    await page.click("#btn-auth-approve");

    // Back on the landing, signed in: actions, not an explanation.
    await expect(page.locator("#nav-identity")).toHaveText(identity, { timeout: 45_000 });
    await expect(page.locator("h1")).toHaveText(identity);
    await expect(page.locator("#home-apps")).toContainText("Your apps");
    await page.click("#home-developers");

    await expect(page.locator("h1")).toHaveText("Developer console");
    await page.click("#dev-new");
    await page.fill("#name", "Team wiki");
    await page.fill("#redirect_uris", "https://wiki.example/callback");
    await page.click("#client-register");
    await expect(page.locator("h1")).toHaveText("Team wiki");
    await expect(page.locator("#client-secret")).toHaveText(/^pws_/);
    const clientID = (await page.locator("#client-id").textContent()).trim();
    expect(clientID).toMatch(/^pwc_/);

    // The secret is shown once; the console lists the app.
    await page.goto(`${bridge.issuer}/developers`);
    await expect(page.locator("#dev-clients")).toContainText("Team wiki");
    await page.click("text=Team wiki");
    await expect(page.locator("#client-secret")).toHaveCount(0);
    await expect(page.locator("#client-id")).toHaveText(clientID);

    // Signing out ends the session.
    await page.click("#nav-signout");
    await expect(page.locator("#home-signin")).toBeVisible();
  });

  test("a phone leads with this device; another device is one tap away", async ({ browser }) => {
    const phone = await browser.newContext({ ...devices["iPhone 13"] });
    try {
      const page = await phone.newPage();
      const { challenge } = pkce();
      // Any resolvable ID reaches the approval step; nobody approves here.
      const helper = await browser.newContext();
      const signer = await helper.newPage();
      await stubPasskeys(signer);
      const identity = await registerIdentity(signer, relay, "bridgephone");
      await helper.close();

      await page.goto(authorizeURL(bridge, rp, { challenge }));
      await expect(page.locator("#identify-title")).toHaveText("Sign in to E2E application");
      await page.fill("#identity", identity);
      await page.click("#identify-continue");
      await expect(page.locator("h1")).toHaveText("Approve this sign-in");
      await expect(page.locator("#open-app")).toBeVisible();
      await expect(page.locator(".signer-link").first()).toBeVisible();
      // The QR and code are there, folded away.
      await expect(page.locator("#other-device")).not.toHaveAttribute("open", "");
      await expect(page.locator("#match-code")).toBeHidden();
      await page.click("#other-device summary");
      await expect(page.locator("#match-code")).toBeVisible();
    } finally {
      await phone.close();
    }
  });

  test("a desktop never offers to open the app on this device", async ({ page }) => {
    const { challenge } = pkce();
    const helper = await page.context().browser().newContext();
    const signer = await helper.newPage();
    await stubPasskeys(signer);
    const identity = await registerIdentity(signer, relay, "bridgedesk");
    await helper.close();

    await page.goto(authorizeURL(bridge, rp, { challenge }));
    await page.fill("#identity", identity);
    await page.click("#identify-continue");
    await expect(page.locator("h1")).toHaveText("Approve on your phone");
    await expect(page.locator("#match-code")).toBeVisible();
    await expect(page.locator("#open-app")).toHaveCount(0);
    await expect(page.locator("#this-browser")).toContainText("Or approve in this browser");
  });
});
