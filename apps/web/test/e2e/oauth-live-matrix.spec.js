import { execFileSync, spawnSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import { chmodSync, mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test, expect } from "@playwright/test";
import { freePort, startRelay } from "../helpers/relay.mjs";
import { registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";
import { startBridge } from "../helpers/bridge.mjs";

/**
 * Matrix (Synapse) signs people in with their Poweur ID through the bridge,
 * using Synapse's built-in OpenID Connect login: the recipe in
 * apps/docs/docs/auth/matrix.md. Drives Synapse's own login API (no Element):
 * SSO redirect → bridge → login token → a Matrix user named after the ID.
 * Needs Docker:
 * POWEUR_LIVE_DOCKER=1 pnpm --filter @poweur/web exec playwright test test/e2e/oauth-live-matrix.spec.js
 */
const IMAGE = "matrixdotorg/synapse:v1.162.0";

test.skip(!process.env.POWEUR_LIVE_DOCKER, "set POWEUR_LIVE_DOCKER=1 to run against a real Synapse container");
test.setTimeout(240_000);

test.describe("Matrix (Synapse) sign-in through the bridge (live)", () => {
  let relay;
  let bridge;
  let syn;
  let container = "";
  const secret = randomBytes(24).toString("base64url");
  const doneUrl = "http://127.0.0.1:1/matrix-login-done";

  test.beforeAll(async () => {
    test.setTimeout(240_000);
    relay = await startRelay();
    const port = await freePort();
    syn = `http://127.0.0.1:${port}`;
    const callback = `${syn}/_synapse/client/oidc/callback`;
    bridge = await startBridge({
      relay,
      redirectUri: callback,
      listen: "0.0.0.0",
      clients: [{ client_id: "synapse", client_name: "Poweur chat", redirect_uris: [callback], client_secret: secret }],
    });

    // Synapse generates its own base config and signing key; the OpenID
    // Connect part is a second config file, exactly as the recipe says.
    const data = mkdtempSync(join(tmpdir(), "poweur-synapse-"));
    const extra = join(data, "extra");
    mkdirSync(join(data, "data"));
    mkdirSync(extra);
    chmodSync(join(data, "data"), 0o777);
    const gen = spawnSync("docker", [
      "run", "--rm", "-v", `${join(data, "data")}:/data`,
      "-e", "SYNAPSE_SERVER_NAME=localhost", "-e", "SYNAPSE_REPORT_STATS=no", IMAGE, "generate",
    ], { encoding: "utf8" });
    if (gen.status !== 0) throw new Error(`synapse generate failed: ${gen.stderr}`);
    writeFileSync(join(extra, "poweur.yaml"), `
public_baseurl: "${syn}/"
enable_registration: false
password_config:
  enabled: false
sso:
  client_whitelist: ["http://127.0.0.1:1/"]
  update_profile_information: true
oidc_providers:
  - idp_id: oidc-poweur
    idp_name: "Poweur ID"
    issuer: "${bridge.issuer}"
    client_id: "synapse"
    client_secret: "${secret}"
    client_auth_method: client_secret_basic
    pkce_method: always
    scopes: ["openid", "poweur_id", "profile"]
    skip_verification: true
    user_mapping_provider:
      config:
        localpart_template: "{{ user.poweur_id }}"
        display_name_template: "{{ user.name or user.poweur_id }}"
`);
    const run = spawnSync("docker", [
      "run", "-d", "--rm", "-p", `127.0.0.1:${port}:8008`,
      "--add-host", "oauth.localhost:host-gateway",
      "-v", `${join(data, "data")}:/data`, "-v", `${extra}:/extra:ro`,
      "--entrypoint", "python", IMAGE,
      "-m", "synapse.app.homeserver", "--config-path", "/data/homeserver.yaml", "--config-path", "/extra/poweur.yaml",
    ], { encoding: "utf8" });
    if (run.status !== 0) throw new Error(`docker run failed: ${run.stderr}`);
    container = run.stdout.trim();
    const deadline = Date.now() + 180_000;
    for (;;) {
      try {
        if ((await fetch(`${syn}/health`)).ok) break;
      } catch { /* starting */ }
      if (Date.now() > deadline) throw new Error(execFileSync("docker", ["logs", container], { encoding: "utf8" }).slice(-3000));
      await new Promise((r) => setTimeout(r, 1000));
    }
  });

  test.afterAll(() => {
    if (container) spawnSync("docker", ["rm", "-f", container]);
    bridge?.stop();
    relay?.stop();
  });

  test("a Poweur ID signs in to Matrix and becomes @<id>:localhost", async ({ page, request }) => {
    try {
      // Only Poweur sign-in is offered: no passwords, no open registration.
      const flows = await (await request.get(`${syn}/_matrix/client/v3/login`)).json();
      const types = flows.flows.map((f) => f.type);
      expect(types).toContain("m.login.sso");
      expect(types).not.toContain("m.login.password");
      const sso = flows.flows.find((f) => f.type === "m.login.sso");
      expect(sso.identity_providers.map((p) => p.id)).toContain("oidc-poweur");

      await stubPasskeys(page);
      const identity = await registerIdentity(page, relay, "mxuser");
      let loginToken = "";
      await page.route(`${doneUrl}**`, (route) => {
        loginToken = new URL(route.request().url()).searchParams.get("loginToken") ?? "";
        return route.fulfill({ status: 200, contentType: "text/plain", body: "done" });
      });

      await page.goto(`${syn}/_matrix/client/v3/login/sso/redirect/oidc-poweur?redirectUrl=${encodeURIComponent(doneUrl)}`);
      await expect(page).toHaveURL(/oauth\.localhost/, { timeout: 30_000 });
      await page.fill("#identity", identity);
      await page.click("#identify-continue");
      await page.click(`a:has-text("Continue at ${relay.addr}")`);
      await expect(page.locator("#btn-auth-unlock, #btn-auth-approve")).toBeVisible({ timeout: 45_000 });
      if (await page.locator("#btn-auth-unlock").count()) {
        await page.click("#btn-auth-unlock");
      }
      await page.click("#btn-auth-approve");
      await expect(page.locator("h1")).toHaveText("Allow Poweur chat?", { timeout: 45_000 });
      await page.click('button[value="allow"]');
      await expect.poll(() => loginToken, { timeout: 30_000 }).not.toBe("");

      const login = await request.post(`${syn}/_matrix/client/v3/login`, { data: { type: "m.login.token", token: loginToken } });
      expect(login.ok()).toBe(true);
      const session = await login.json();
      expect(session.user_id).toBe(`@${identity}:localhost`);
      const profile = await (await request.get(`${syn}/_matrix/client/v3/profile/${encodeURIComponent(session.user_id)}`, {
        headers: { authorization: `Bearer ${session.access_token}` },
      })).json();
      expect(profile.displayname).toBeTruthy();
    } catch (error) {
      if (container) console.log(execFileSync("docker", ["logs", container], { encoding: "utf8" }).slice(-4000));
      console.log(bridge.stderr().slice(-3000));
      console.log("URL:", page.url());
      throw error;
    }
  });
});
