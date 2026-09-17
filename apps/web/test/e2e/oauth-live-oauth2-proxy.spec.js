import { execFileSync, spawnSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import { test, expect } from "@playwright/test";
import { freePort, startRelay } from "../helpers/relay.mjs";
import { registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";
import { startBridge } from "../helpers/bridge.mjs";

/**
 * EPIC-022 E22-T8: an unmodified oauth2-proxy container signs a Poweur ID in
 * through the bridge, with the real web signer. Needs Docker, so it runs only
 * when asked: POWEUR_LIVE_DOCKER=1 pnpm --filter @poweur/web exec playwright
 * test test/e2e/oauth-live-oauth2-proxy.spec.js
 */
const IMAGE = "quay.io/oauth2-proxy/oauth2-proxy:v7.12.0";

test.skip(!process.env.POWEUR_LIVE_DOCKER, "set POWEUR_LIVE_DOCKER=1 to run against a real oauth2-proxy container");

test.describe("oauth2-proxy through the bridge (live)", () => {
  let relay;
  let bridge;
  let proxyPort;
  let container = "";
  const secret = randomBytes(24).toString("base64url");

  test.beforeAll(async () => {
    relay = await startRelay();
    proxyPort = await freePort();
    const redirect = `http://127.0.0.1:${proxyPort}/oauth2/callback`;
    bridge = await startBridge({
      relay,
      redirectUri: redirect,
      // The container reaches the bridge through the host gateway.
      listen: "0.0.0.0",
      clients: [{
        client_id: "oauth2-proxy", client_name: "Team dashboard",
        redirect_uris: [redirect], client_secret: secret,
      }],
    });
    const run = spawnSync("docker", [
      "run", "-d", "--rm",
      "-p", `127.0.0.1:${proxyPort}:4180`,
      "--add-host", "oauth.localhost:host-gateway",
      IMAGE,
      "--http-address=0.0.0.0:4180",
      "--provider=oidc",
      `--oidc-issuer-url=${bridge.issuer}`,
      "--client-id=oauth2-proxy",
      `--client-secret=${secret}`,
      `--redirect-url=${redirect}`,
      "--code-challenge-method=S256",
      "--scope=openid poweur_id",
      "--oidc-email-claim=poweur_id",
      "--email-domain=*",
      "--insecure-oidc-allow-unverified-email",
      "--skip-provider-button",
      "--upstream=static://200",
      `--cookie-secret=${randomBytes(16).toString("hex")}`,
      "--cookie-secure=false",
    ], { encoding: "utf8" });
    if (run.status !== 0) throw new Error(`docker run failed: ${run.stderr}`);
    container = run.stdout.trim();
    const deadline = Date.now() + 60_000;
    for (;;) {
      try {
        const res = await fetch(`http://127.0.0.1:${proxyPort}/ping`);
        if (res.ok) break;
      } catch { /* starting */ }
      if (Date.now() > deadline) {
        throw new Error(`oauth2-proxy did not start:\n${execFileSync("docker", ["logs", container], { encoding: "utf8" })}`);
      }
      await new Promise((r) => setTimeout(r, 500));
    }
  });

  test.afterAll(() => {
    if (container) spawnSync("docker", ["rm", "-f", container]);
    bridge?.stop();
    relay?.stop();
  });

  test("a protected app lets a Poweur ID in", async ({ page }) => {
    try {
      await stubPasskeys(page);
      const identity = await registerIdentity(page, relay, "proxyuser");

      await page.goto(`http://127.0.0.1:${proxyPort}/`);
      await expect(page).toHaveURL(new RegExp(`^${bridge.issuer.replace(/\./g, "\\.")}/t/`));
      await page.fill("#identity", identity);
      await page.click("button.primary");
      await page.click(`a:has-text("Continue at ${relay.addr}")`);
      await expect(page.locator("#btn-auth-unlock, #btn-auth-approve")).toBeVisible({ timeout: 45_000 });
      if (await page.locator("#btn-auth-unlock").count()) {
        await page.click("#btn-auth-unlock");
        await page.click("#btn-do-unlock");
      }
      await page.click("#btn-auth-approve");
      await expect(page.locator("h1")).toHaveText("Allow Team dashboard?", { timeout: 45_000 });
      await page.click('button[value="allow"]');

      // oauth2-proxy redeemed the code, verified the ID token and let us through.
      await expect(page).toHaveURL(`http://127.0.0.1:${proxyPort}/`, { timeout: 30_000 });
      await expect(page.locator("body")).toContainText("Authenticated");
      const me = await page.evaluate(async () => (await fetch("/oauth2/userinfo")).json());
      expect(me.email).toBe(identity);
    } catch (error) {
      console.log(execFileSync("docker", ["logs", container], { encoding: "utf8" }).slice(-4000));
      console.log(bridge.stderr().slice(-4000));
      throw error;
    }
  });
});
