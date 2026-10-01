import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";
import { stubPasskeys } from "../helpers/app-ui.mjs";
import { authorizeURL, pkce, redeem, startBridge, startRP } from "../helpers/bridge.mjs";

/**
 * EPIC-022: someone without a Poweur ID reaches the bridge. The page checks
 * a name at the launcher, opens the launcher in a new tab, notices when the
 * name has been created there, and the sign-in carries on with it.
 */
test.describe("OAuth bridge: create an ID mid-sign-in", () => {
  let relay;
  let bridge;
  let rp;

  test.beforeAll(async () => {
    relay = await startRelay();
    rp = await startRP();
    bridge = await startBridge({ relay, redirectUri: rp.redirectUri, launcher: true });
  });

  test.afterAll(() => {
    bridge?.stop();
    rp?.stop();
    relay?.stop();
  });

  test("a first-time visitor creates an ID in a new tab and signs in with it", async ({ page, context }) => {
    await stubPasskeys(page);
    const { verifier, challenge } = pkce();
    await page.goto(authorizeURL(bridge, rp, { challenge }));
    await expect(page.locator("#identify-title")).toHaveText("Sign in to E2E application");
    await page.click("#create-open");
    await expect(page.locator("#create-id-section h2")).toHaveText("Create a Poweur ID");

    // A taken-looking name is refused; a fresh one is offered.
    await page.fill("#new-handle", "admin");
    await expect(page.locator("#new-handle-status")).not.toHaveText(/available|Checking/, { timeout: 10_000 });
    await page.fill("#new-handle", "newcomer");
    await expect(page.locator("#new-handle-status")).toHaveText("newcomer.poweur.net is available", { timeout: 10_000 });
    await expect(page.locator("#create-id")).toHaveAttribute("href", `${relay.baseUrl}/app/?from=signin&handle=newcomer`);

    const [launcher] = await Promise.all([context.waitForEvent("page"), page.click("#create-id")]);
    await stubPasskeys(launcher);
    await launcher.waitForLoadState();
    await launcher.evaluate((url) => {
      localStorage.setItem("poweur:config", JSON.stringify({ relayUrl: url, parentDomain: "poweur.net" }));
    }, relay.baseUrl);
    await launcher.reload();
    if (await launcher.locator("#btn-welcome-start").count()) await launcher.click("#btn-welcome-start");
    if (await launcher.locator("#opt-create-new").count()) await launcher.click("#opt-create-new");
    await launcher.waitForSelector("#claim-card");
    await expect(launcher.locator("#claim-from-signin")).toBeVisible();
    await expect(launcher.locator("#ni-handle")).toHaveValue("newcomer");
    await expect(launcher.locator("#btn-claim")).toBeEnabled({ timeout: 20_000 });
    await launcher.click("#btn-claim");
    await launcher.waitForSelector("#btn-onboard-skip", { timeout: 45_000 });
    await launcher.click("#btn-onboard-skip");

    // Back in the sign-in tab: it noticed, and the ID is filled in.
    await page.bringToFront();
    await page.evaluate(() => window.dispatchEvent(new Event("focus")));
    await expect(page.locator("#created-ready")).toHaveText("newcomer.poweur.net is ready — continue to sign in.", {
      timeout: 30_000,
    });
    await expect(page.locator("#identity")).toHaveValue("newcomer.poweur.net");
    await page.click("#identify-continue");
    await expect(page.locator("#approve-identity")).toHaveText("newcomer.poweur.net");

    // The new ID approves in the launcher tab, where its keys are.
    await page.click(`a:has-text("Continue at ${relay.addr}")`);
    await expect(page).toHaveURL(/\/app\/\?auth=/);
    await expect(page.locator("#btn-auth-unlock, #btn-auth-approve")).toBeVisible({ timeout: 45_000 });
    if (await page.locator("#btn-auth-unlock").count()) {
      await page.click("#btn-auth-unlock");
    }
    await page.click("#btn-auth-approve");
    await expect(page.locator("h1")).toHaveText("Allow E2E application?", { timeout: 45_000 });
    await page.click('button[value="allow"]');
    await expect(page.locator("h1")).toHaveText("Back at the application");
    const claims = await redeem(bridge, rp, rp.hits.at(-1).searchParams.get("code"), verifier);
    expect(claims.poweur_id).toBe("newcomer.poweur.net");
  });
});
