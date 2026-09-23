import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";
import { registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";
import { authorizeURL, pkce, redeem, startBridge, startRP } from "../helpers/bridge.mjs";

/**
 * EPIC-022: an OIDC application signs in a Poweur ID through the bridge, with
 * the real web client as the signer. Nothing signed ever travels in a URL:
 * the signer POSTs, then follows the bridge's resume link (same device), or
 * the starting page continues by itself once the approving device sent the
 * code it showed (another device).
 */
test.describe("OAuth bridge through the web signer", () => {
  let relay;
  let bridge;
  let rp;

  test.beforeAll(async () => {
    relay = await startRelay();
    rp = await startRP();
    bridge = await startBridge({ relay, redirectUri: rp.redirectUri, pushHandle: "oauthpusher" });
  });

  test.afterAll(() => {
    bridge?.stop();
    rp?.stop();
    relay?.stop();
  });

  async function identifyAt(page, url, identity) {
    await page.goto(url);
    await page.fill("#identity", identity);
    await page.click("#identify-continue");
    await expect(page.locator("#approve-identity")).toHaveText(identity);
  }

  async function unlockAndApprove(page, code = "") {
    await expect(page.locator("#btn-auth-unlock, #btn-auth-approve")).toBeVisible({ timeout: 45_000 });
    if (await page.locator("#btn-auth-unlock").count()) {
      await page.click("#btn-auth-unlock");
      await page.click("#btn-do-unlock");
    }
    await expect(page.locator("#btn-auth-approve")).toBeVisible({ timeout: 45_000 });
    if (code) await page.fill("#auth-match", code);
    await page.click("#btn-auth-approve");
  }

  test("same device: the signer returns this browser to the bridge", async ({ page }) => {
    await stubPasskeys(page);
    const identity = await registerIdentity(page, relay, "oauthweb");
    const { verifier, challenge } = pkce();

    await identifyAt(page, authorizeURL(bridge, rp, { challenge }), identity);
    // The browser holds its keys in the relay's web app, the operator default.
    await page.click(`a:has-text("Continue at ${relay.addr}")`);
    await expect(page).toHaveURL(/\/app\/\?auth=/);
    await expect(page.locator("text=Poweur OAuth bridge").first()).toBeVisible({ timeout: 45_000 });
    await unlockAndApprove(page);

    // Back at the bridge, in this browser, for consent.
    await expect(page.locator("h1")).toHaveText("Allow E2E application?", { timeout: 45_000 });
    const history = await page.evaluate(() => performance.getEntriesByType("navigation").map((n) => n.name));
    expect(history.join(" ")).not.toMatch(/response=/);
    await page.click('button[value="allow"]');
    await expect(page.locator("h1")).toHaveText("Back at the application");

    const back = rp.hits.at(-1);
    expect(back.searchParams.get("state")).toBe("st");
    expect(back.searchParams.get("iss")).toBe(bridge.issuer);
    const claims = await redeem(bridge, rp, back.searchParams.get("code"), verifier);
    expect(claims.poweur_id).toBe(identity);
    expect(claims.nonce).toBe("nn");
    expect(claims.sub).not.toContain("oauthweb");
  });

  test("another device: the code on the starting screen lets it continue", async ({ browser }) => {
    const phone = await browser.newContext();
    const desktop = await browser.newContext();
    try {
      const signer = await phone.newPage();
      await stubPasskeys(signer);
      const identity = await registerIdentity(signer, relay, "oauthphone");

      const screen = await desktop.newPage();
      const { verifier, challenge } = pkce();
      await identifyAt(screen, authorizeURL(bridge, rp, { challenge, state: "desk" }), identity);
      // A desktop leads with the phone: the code to type and the request.
      await expect(screen.locator("h1")).toHaveText("Approve on your phone");
      const match = (await screen.locator("#match-code").textContent()).trim();
      // The QR carries a short link to the request, not the request itself.
      const link = (await screen.locator("#request-code").textContent()).trim();
      expect(link).toMatch(new RegExp(`^${bridge.issuer}/r/[0-9A-Z]{8}$`));

      // The phone's camera opens it: a page offering this phone's signers.
      await signer.goto(link);
      await expect(signer.locator("h1")).toHaveText("Approve on this phone");
      await signer.click(`a.handoff-signer:has-text("${relay.addr}")`);
      await expect(signer.locator("#auth-context")).toContainText("to sign in to E2E application", { timeout: 45_000 });
      await unlockAndApprove(signer, match);
      await expect(signer.locator("#auth-result-note")).toContainText("Go back to the screen");
      await expect(signer).toHaveURL(/\/app\//);

      // The desktop page notices on its own.
      await expect(screen.locator("h1")).toHaveText("Allow E2E application?", { timeout: 30_000 });
      await screen.click('button[value="allow"]');
      await expect(screen.locator("h1")).toHaveText("Back at the application");
      const back = rp.hits.at(-1);
      expect(back.searchParams.get("state")).toBe("desk");
      const claims = await redeem(bridge, rp, back.searchParams.get("code"), verifier);
      expect(claims.poweur_id).toBe(identity);
    } finally {
      await phone.close();
      await desktop.close();
    }
  });

  test("pushed to the app: a trusted bridge's request, approved with the desktop's code", async ({ browser }) => {
    const phone = await browser.newContext();
    const desktop = await browser.newContext();
    try {
      const app = await phone.newPage();
      await stubPasskeys(app);
      const identity = await registerIdentity(app, relay, "oauthpush");

      // The user lets this bridge send sign-in requests — and nothing else.
      await app.click('.nav-tab[data-page="settings"]');
      await app.click("#row-policy-signin");
      await app.fill("#policy-trusted-auth", bridge.pushIdentity);
      await app.click("#policy-save");
      await expect(app.locator("#row-policy-signin .settings-row-value")).toHaveText(bridge.pushIdentity, { timeout: 15_000 });
      await app.click('.nav-tab[data-page="messages"]');

      const screen = await desktop.newPage();
      const { verifier, challenge } = pkce();
      await identifyAt(screen, authorizeURL(bridge, rp, { challenge, state: "push" }), identity);
      await expect(screen.locator("text=" + bridge.pushIdentity)).toBeVisible();
      await screen.click("#push-send");
      await expect(screen.locator("#push-notice")).toContainText("Sent", { timeout: 30_000 });
      const match = (await screen.locator("#match-code").textContent()).trim();
      // The prompt arrives in its own list, not as a conversation.
      await expect(app.locator("#auth-prompts")).toContainText("E2E application", { timeout: 45_000 });
      await expect(app.locator("#auth-prompts")).toContainText(`via ${bridge.pushIdentity}`);
      await app.click(".btn-prompt-review");
      await expect(app.locator("#auth-context")).toContainText("to sign in to E2E application", { timeout: 45_000 });
      // From a push, the code is required.
      if (await app.locator("#btn-auth-unlock").count()) {
        await app.click("#btn-auth-unlock");
        await app.click("#btn-do-unlock");
      }
      await expect(app.locator("#btn-auth-approve")).toBeVisible({ timeout: 45_000 });
      await app.click("#btn-auth-approve");
      await expect(app.locator("text=Enter the 2-digit code")).toBeVisible();
      await app.fill("#auth-match", match);
      await app.click("#btn-auth-approve");
      await expect(app.locator("#auth-result-note")).toContainText("Go back to the screen");

      await expect(screen.locator("h1")).toHaveText("Allow E2E application?", { timeout: 30_000 });
      await screen.click('button[value="allow"]');
      await expect(screen.locator("h1")).toHaveText("Back at the application");
      const back = rp.hits.at(-1);
      expect(back.searchParams.get("state")).toBe("push");
      const claims = await redeem(bridge, rp, back.searchParams.get("code"), verifier);
      expect(claims.poweur_id).toBe(identity);
    } finally {
      await phone.close();
      await desktop.close();
    }
  });
});
