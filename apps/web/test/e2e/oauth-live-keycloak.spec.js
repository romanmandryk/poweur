import { execFileSync, spawnSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import { test, expect } from "@playwright/test";
import { freePort, startRelay } from "../helpers/relay.mjs";
import { registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";
import { startBridge } from "../helpers/bridge.mjs";

/**
 * EPIC-022 E22-T8: Keycloak brokers a Poweur ID through the bridge as an
 * ordinary "OpenID Connect v1.0" identity provider. Needs Docker:
 * POWEUR_LIVE_DOCKER=1 pnpm --filter @poweur/web exec playwright test test/e2e/oauth-live-keycloak.spec.js
 */
const IMAGE = "quay.io/keycloak/keycloak:26.3";

test.skip(!process.env.POWEUR_LIVE_DOCKER, "set POWEUR_LIVE_DOCKER=1 to run against a real Keycloak container");
test.setTimeout(240_000);

test.describe("Keycloak brokering through the bridge (live)", () => {
  let relay;
  let bridge;
  let kcPort;
  let kc;
  let container = "";
  const secret = randomBytes(24).toString("base64url");

  async function admin(path, { method = "GET", body } = {}) {
    const tokenRes = await fetch(`${kc}/realms/master/protocol/openid-connect/token`, {
      method: "POST",
      headers: { "content-type": "application/x-www-form-urlencoded" },
      body: new URLSearchParams({ grant_type: "password", client_id: "admin-cli", username: "admin", password: "admin" }),
    });
    const { access_token } = await tokenRes.json();
    const res = await fetch(`${kc}/admin/realms${path}`, {
      method,
      headers: { authorization: `Bearer ${access_token}`, "content-type": "application/json" },
      body: body ? JSON.stringify(body) : undefined,
    });
    if (!res.ok) throw new Error(`${method} ${path}: ${res.status} ${await res.text()}`);
    const text = await res.text();
    return text ? JSON.parse(text) : null;
  }

  test.beforeAll(async () => {
    test.setTimeout(240_000);
    relay = await startRelay();
    kcPort = await freePort();
    kc = `http://127.0.0.1:${kcPort}`;
    const broker = `${kc}/realms/poweur/broker/poweur/endpoint`;
    bridge = await startBridge({
      relay,
      redirectUri: broker,
      listen: "0.0.0.0",
      clients: [{ client_id: "keycloak", client_name: "Company SSO", redirect_uris: [broker], client_secret: secret }],
    });
    const run = spawnSync("docker", [
      "run", "-d", "--rm", "-p", `127.0.0.1:${kcPort}:8080`,
      "--add-host", "oauth.localhost:host-gateway",
      "-e", "KC_BOOTSTRAP_ADMIN_USERNAME=admin", "-e", "KC_BOOTSTRAP_ADMIN_PASSWORD=admin",
      IMAGE, "start-dev",
    ], { encoding: "utf8" });
    if (run.status !== 0) throw new Error(`docker run failed: ${run.stderr}`);
    container = run.stdout.trim();
    const deadline = Date.now() + 180_000;
    for (;;) {
      try {
        if ((await fetch(`${kc}/realms/master`)).ok) break;
      } catch { /* starting */ }
      if (Date.now() > deadline) throw new Error(execFileSync("docker", ["logs", container], { encoding: "utf8" }).slice(-3000));
      await new Promise((r) => setTimeout(r, 1000));
    }

    await admin("", { method: "POST", body: { realm: "poweur", enabled: true } });
    // A Poweur ID has no e-mail or name; the realm must not demand them.
    const profile = await admin("/poweur/users/profile");
    for (const attr of profile.attributes) {
      if (["email", "firstName", "lastName"].includes(attr.name)) delete attr.required;
    }
    await admin("/poweur/users/profile", { method: "PUT", body: profile });
    const iss = bridge.issuer;
    await admin("/poweur/identity-provider/instances", {
      method: "POST",
      body: {
        alias: "poweur", displayName: "Poweur ID", providerId: "oidc", enabled: true, trustEmail: false,
        config: {
          issuer: iss,
          authorizationUrl: `${iss}/authorize`, tokenUrl: `${iss}/token`,
          userInfoUrl: `${iss}/userinfo`, jwksUrl: `${iss}/jwks.json`,
          useJwksUrl: "true", validateSignature: "true",
          clientId: "keycloak", clientSecret: secret, clientAuthMethod: "client_secret_basic",
          pkceEnabled: "true", pkceMethod: "S256",
          defaultScope: "openid poweur_id", syncMode: "IMPORT",
        },
      },
    });
    await admin("/poweur/identity-provider/instances/poweur/mappers", {
      method: "POST",
      body: {
        name: "username", identityProviderAlias: "poweur", identityProviderMapper: "oidc-username-idp-mapper",
        config: { template: "${CLAIM.poweur_id}", syncMode: "INHERIT" },
      },
    });
  });

  test.afterAll(() => {
    if (container) spawnSync("docker", ["rm", "-f", container]);
    bridge?.stop();
    relay?.stop();
  });

  test("a Poweur ID becomes a brokered Keycloak user", async ({ page }) => {
    try {
      await stubPasskeys(page);
      const identity = await registerIdentity(page, relay, "kcuser");

      await page.goto(`${kc}/realms/poweur/account`);
      await page.click("#social-poweur");
      await expect(page).toHaveURL(/oauth\.localhost/, { timeout: 30_000 });
      await page.fill("#identity", identity);
      await page.click("button.primary");
      await page.click(`a:has-text("Continue at ${relay.addr}")`);
      await expect(page.locator("#btn-auth-unlock, #btn-auth-approve")).toBeVisible({ timeout: 45_000 });
      if (await page.locator("#btn-auth-unlock").count()) {
        await page.click("#btn-auth-unlock");
        await page.click("#btn-do-unlock");
      }
      await page.click("#btn-auth-approve");
      await expect(page.locator("h1")).toHaveText("Allow Company SSO?", { timeout: 45_000 });
      await page.click('button[value="allow"]');

      // Keycloak may ask to review the imported profile; nothing in it is required.
      await page.waitForURL(new RegExp(`^${kc}`), { timeout: 30_000 });
      if (await page.locator("#kc-update-profile-form, #kc-idp-review-profile-form").count()) {
        await page.click('input[type="submit"], button[type="submit"]');
      }
      await expect(page).toHaveURL(new RegExp(`${kc}/realms/poweur/account`), { timeout: 30_000 });

      const users = await admin(`/poweur/users?username=${encodeURIComponent(identity)}&exact=true`);
      expect(users).toHaveLength(1);
      const links = await admin(`/poweur/users/${users[0].id}/federated-identity`);
      expect(links[0].identityProvider).toBe("poweur");
      // The link is keyed by the pairwise subject, which says nothing about the ID.
      expect(links[0].userId).toBeTruthy();
      expect(links[0].userId.toLowerCase()).not.toContain("kcuser");
    } catch (error) {
      console.log(execFileSync("docker", ["logs", container], { encoding: "utf8" }).slice(-4000));
      console.log(bridge.stderr().slice(-3000));
      console.log("URL:", page.url());
      throw error;
    }
  });
});
