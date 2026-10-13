import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";
import { registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";
import { authorizeURL, pkce, redeem, startBridge, startRP } from "../helpers/bridge.mjs";

/**
 * EPIC-022: an OIDC application signs in a Poweur ID through the bridge, with
 * the real web client as the signer. Nothing signed ever travels in a URL:
 * the signer POSTs, then follows the bridge's resume link (same device), or
 * the starting page continues by itself once another device approved — with
 * nothing to type after a scan, and a number to pick after a push (EPIC-028).
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

  async function unlockAndApprove(page) {
    await expect(page.locator("#btn-auth-unlock, #btn-auth-approve")).toBeVisible({ timeout: 45_000 });
    if (await page.locator("#btn-auth-unlock").count()) {
      await page.click("#btn-auth-unlock");
    }
    await expect(page.locator("#btn-auth-approve")).toBeVisible({ timeout: 45_000 });
    await page.click("#btn-auth-approve");
  }

  test("same device: the signer returns this browser to the bridge", async ({ page }) => {
    await stubPasskeys(page);
    const identity = await registerIdentity(page, relay, "oauthweb");
    const { verifier, challenge } = pkce();

    await identifyAt(page, authorizeURL(bridge, rp, { challenge }), identity);
    // The browser holds its keys in the relay's web app, the operator default.
    await page.click(`a:has-text("Continue at ${relay.addr}")`);
    await expect(page).toHaveURL(/\/id\/\?auth=/);
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

  test("another device: the QR opens the identity's approve page, nothing to type", async ({ browser }) => {
    const phone = await browser.newContext();
    const desktop = await browser.newContext();
    try {
      const signer = await phone.newPage();
      await stubPasskeys(signer);
      const identity = await registerIdentity(signer, relay, "oauthphone");

      const screen = await desktop.newPage();
      const { verifier, challenge } = pkce();
      await identifyAt(screen, authorizeURL(bridge, rp, { challenge, state: "desk" }), identity);
      // A desktop leads with the phone: a QR, and no number before any push.
      await expect(screen.locator("h1")).toHaveText("Approve on your phone");
      await expect(screen.locator("#match-code")).toHaveCount(0);
      // The QR is the identity's own approve page, the request's short link in its fragment.
      const qrLink = (await screen.locator("#request-code").textContent()).trim();
      expect(qrLink).toMatch(new RegExp(`^http://${identity.replace(/\./g, "\\.")}/id/approve#auth=`));
      const fragment = qrLink.slice(qrLink.indexOf("#"));
      expect(decodeURIComponent(fragment.slice("#auth=".length))).toMatch(new RegExp(`^${bridge.issuer}/r/[0-9A-Z]{8}$`));

      // The phone's camera opens it. This browser cannot resolve the identity's host, and
      // here the same app with the phone's keys lives at the relay's own origin.
      await signer.goto(`${relay.baseUrl}/id/approve${fragment}`);
      await expect(signer.locator("#auth-context")).toContainText("to sign in to E2E application", { timeout: 45_000 });
      await expect(signer.locator("#auth-warning")).toBeVisible();
      expect(new URL(signer.url()).hash).toBe(""); // the request leaves the address bar
      await unlockAndApprove(signer);
      await expect(signer.locator("#auth-result-note")).toContainText("Go back to the screen");
      await expect(signer).toHaveURL(/\/id\/approve/);

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

  test("pushed to the app: a trusted bridge's request, approved by picking the desktop's number", async ({ browser }) => {
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
      // From a push: three numbers in place of Approve, and the desktop's is one of them.
      if (await app.locator("#btn-auth-unlock").count()) {
        await app.click("#btn-auth-unlock");
        await app.click("#btn-do-unlock");
      }
      await expect(app.locator("#auth-pick")).toBeVisible({ timeout: 45_000 });
      await expect(app.locator("#btn-auth-approve")).toHaveCount(0);
      await expect(app.locator(".auth-pick-choice")).toHaveCount(3);
      await app.locator(".auth-pick-choice", { hasText: match }).click();
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
