import { execFileSync, spawnSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import { test, expect } from "@playwright/test";
import { freePort, startRelay } from "../helpers/relay.mjs";
import { registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";
import { startBridge } from "../helpers/bridge.mjs";

/**
 * EPIC-022 E22-T8: Authentik takes a Poweur ID through an "OpenID Connect"
 * OAuth source. Needs Docker (Authentik is a four-container stack: server,
 * worker, Postgres, Redis):
 * POWEUR_LIVE_DOCKER=1 pnpm --filter @poweur/web exec playwright test test/e2e/oauth-live-authentik.spec.js
 */
const IMAGE = "ghcr.io/goauthentik/server:2026.8.2";
const NET = "poweur-ak-net";
const TOKEN = "ak-bootstrap-token";
const names = ["ak-server", "ak-worker", "ak-db", "ak-redis"];

test.skip(!process.env.POWEUR_LIVE_DOCKER, "set POWEUR_LIVE_DOCKER=1 to run against a real Authentik stack");
test.setTimeout(420_000);

test.describe("Authentik through the bridge (live)", () => {
  let relay;
  let bridge;
  let ak;
  const secret = randomBytes(24).toString("base64url");

  const docker = (...args) => {
    const res = spawnSync("docker", args, { encoding: "utf8" });
    if (res.status !== 0) throw new Error(`docker ${args.join(" ")}: ${res.stderr}`);
    return res.stdout.trim();
  };

  async function api(path, { method = "GET", body } = {}) {
    const res = await fetch(`${ak}/api/v3${path}`, {
      method,
      headers: { authorization: `Bearer ${TOKEN}`, "content-type": "application/json" },
      body: body ? JSON.stringify(body) : undefined,
    });
    if (!res.ok) throw new Error(`${method} ${path}: ${res.status} ${await res.text()}`);
    const text = await res.text();
    return text ? JSON.parse(text) : null;
  }

  // The worker installs the default flows — and the bootstrap token — from
  // blueprints well after the API answers, so readiness means "this flow is
  // there and the token works", not /-/health/ready/.
  async function installed(path, timeoutMs = 60_000) {
    const deadline = Date.now() + timeoutMs;
    for (;;) {
      try {
        const found = (await api(path)).results[0];
        if (found) return found;
      } catch (e) {
        if (Date.now() > deadline) throw e;
      }
      if (Date.now() > deadline) throw new Error(`authentik never installed ${path}`);
      await new Promise((r) => setTimeout(r, 3000));
    }
  }

  const flowPk = async (slug, timeoutMs) => (await installed(`/flows/instances/?slug=${slug}`, timeoutMs)).pk;

  test.beforeAll(async () => {
    relay = await startRelay();
    const port = await freePort();
    ak = `http://127.0.0.1:${port}`;
    const callback = `${ak}/source/oauth/callback/poweur/`;
    bridge = await startBridge({
      relay,
      redirectUri: callback,
      listen: "0.0.0.0",
      clients: [{ client_id: "authentik", client_name: "Authentik", redirect_uris: [callback], client_secret: secret }],
    });

    for (const name of names) spawnSync("docker", ["rm", "-f", name]);
    spawnSync("docker", ["network", "rm", NET]);
    docker("network", "create", NET);
    docker("run", "-d", "--name", "ak-db", "--network", NET,
      "-e", "POSTGRES_PASSWORD=ak", "-e", "POSTGRES_USER=authentik", "-e", "POSTGRES_DB=authentik", "postgres:16-alpine");
    docker("run", "-d", "--name", "ak-redis", "--network", NET, "redis:alpine");
    const env = [
      "-e", "AUTHENTIK_SECRET_KEY=live-check-secret-key",
      "-e", "AUTHENTIK_POSTGRESQL__HOST=ak-db", "-e", "AUTHENTIK_POSTGRESQL__USER=authentik",
      "-e", "AUTHENTIK_POSTGRESQL__NAME=authentik", "-e", "AUTHENTIK_POSTGRESQL__PASSWORD=ak",
      "-e", "AUTHENTIK_REDIS__HOST=ak-redis",
      "-e", "AUTHENTIK_BOOTSTRAP_PASSWORD=akadminpassword", "-e", `AUTHENTIK_BOOTSTRAP_TOKEN=${TOKEN}`,
      "-e", "AUTHENTIK_BOOTSTRAP_EMAIL=admin@example.com",
      "-e", "AUTHENTIK_DISABLE_UPDATE_CHECK=true", "-e", "AUTHENTIK_ERROR_REPORTING__ENABLED=false",
    ];
    // The bridge's issuer is oauth.localhost; inside the containers that is the host.
    const host = ["--add-host", "oauth.localhost:host-gateway"];
    docker("run", "-d", "--name", "ak-server", "--network", NET, "-p", `127.0.0.1:${port}:9000`, ...host, ...env, IMAGE, "server");
    docker("run", "-d", "--name", "ak-worker", "--network", NET, ...host, ...env, IMAGE, "worker");

    try {
      await flowPk("default-source-authentication", 300_000);
    } catch (e) {
      throw new Error(`${e.message}\n${execFileSync("docker", ["logs", "--tail", "50", "ak-server"], { encoding: "utf8" })}`);
    }

    const source = await api("/sources/oauth/", {
      method: "POST",
      body: {
        name: "Poweur ID",
        slug: "poweur",
        enabled: true,
        authentication_flow: await flowPk("default-source-authentication"),
        enrollment_flow: await flowPk("default-source-enrollment"),
        provider_type: "openidconnect",
        consumer_key: "authentik",
        consumer_secret: secret,
        oidc_well_known_url: `${bridge.issuer}/.well-known/openid-configuration`,
        additional_scopes: "poweur_id",
        user_matching_mode: "identifier",
      },
    });
    // A source only appears on the login page once the identification stage
    // offers it.
    const stage = await installed("/stages/identification/?name=default-authentication-identification");
    await api(`/stages/identification/${stage.pk}/`, {
      method: "PATCH",
      body: { sources: [source.pk], show_source_labels: true },
    });
  });

  test.afterAll(() => {
    for (const name of names) spawnSync("docker", ["rm", "-f", name]);
    spawnSync("docker", ["network", "rm", NET]);
    bridge?.stop();
    relay?.stop();
  });

  test("a Poweur ID enrolls and signs in to Authentik", async ({ page }) => {
    try {
      await stubPasskeys(page);
      const identity = await registerIdentity(page, relay, "akuser");

      await page.goto(`${ak}/if/flow/default-authentication-flow/`);
      await page.click("text=Poweur ID");
      await expect(page).toHaveURL(/oauth\.localhost/, { timeout: 60_000 });
      await page.fill("#identity", identity);
      await page.click("#identify-continue");
      await page.click(`a:has-text("Continue at ${relay.addr}")`);
      await expect(page.locator("#btn-auth-unlock, #btn-auth-approve")).toBeVisible({ timeout: 60_000 });
      if (await page.locator("#btn-auth-unlock").count()) {
        await page.click("#btn-auth-unlock");
      }
      await page.click("#btn-auth-approve");
      await expect(page.locator("h1")).toHaveText("Allow Authentik?", { timeout: 60_000 });
      await page.click('button[value="allow"]');

      // Back in Authentik. Enrollment asks for the details a Poweur ID does
      // not carry; the ID itself is the username.
      await expect(page).toHaveURL(new RegExp(`^${ak}`), { timeout: 60_000 });
      await page.waitForTimeout(2000);
      const username = page.locator('input[name="username"]');
      if (await username.count()) {
        await username.fill(identity);
        const email = page.locator('input[name="email"]');
        if (await email.count()) await email.fill(`${identity}@example.invalid`);
        const name = page.locator('input[name="name"]');
        if (await name.count()) await name.fill(identity);
        await page.click('button[type="submit"]');
      }
      await page.waitForTimeout(3000);
      const users = await api(`/core/users/?search=${identity}`);
      expect(users.results.length).toBe(1);
      expect(users.results[0].username).toBe(identity);
      // The account is linked to the source by the bridge's pairwise subject,
      // not by the Poweur ID.
      const links = await api(`/sources/user_connections/oauth/?user=${users.results[0].pk}`);
      expect(links.results.length).toBe(1);
      expect(links.results[0].identifier).not.toContain(identity);
    } catch (e) {
      console.error("bridge stderr:\n" + bridge.stderr());
      console.error("authentik:\n" + execFileSync("docker", ["logs", "--tail", "40", "ak-server"], { encoding: "utf8" }));
      throw e;
    }
  });
});
