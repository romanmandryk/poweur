/**
 * The OAuth bridge (apps/oauth) beside a test relay, plus a relying party
 * that records its callback — for journeys through the real web signer.
 */
import { execFileSync, spawn } from "node:child_process";
import { createServer } from "node:http";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { randomBytes, createHash } from "node:crypto";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { freePort, waitForHealth } from "./relay.mjs";

const __dirname = dirname(fileURLToPath(import.meta.url));
const OAUTH_DIR = join(__dirname, "../../../oauth");
const CLI_DIR = join(__dirname, "../../../cli");

/**
 * Give the bridge its own Poweur ID for sign-in prompts: build the CLI, and
 * create the identity on the relay under a home that holds nothing else.
 */
function preparePusher(dir, relay, handle) {
  const bin = join(dir, "poweur");
  execFileSync("go", ["build", "-o", bin, "."], { cwd: CLI_DIR, stdio: "pipe" });
  const home = join(dir, "push-home");
  const identity = `${handle}.poweur.net`;
  execFileSync(bin, ["identity", "create", identity, "--hosted", "--relay", relay.baseUrl, "--json"], {
    env: { ...process.env, HOME: home, POWEUR_RESOLVER_SCHEME: "http", RESOLVER_ALLOW_PRIVATE: "1", POWEUR_RESOLVER_DIAL: relay.addr },
    stdio: "pipe",
  });
  return { bin, home, identity };
}

/** A relying party that only needs to be navigated to. */
export async function startRP() {
  const port = await freePort();
  const hits = [];
  const server = createServer((req, res) => {
    hits.push(new URL(req.url, `http://127.0.0.1:${port}`));
    res.writeHead(200, { "content-type": "text/html" });
    res.end("<title>RP callback</title><h1>Back at the application</h1>");
  });
  await new Promise((r) => server.listen(port, "127.0.0.1", r));
  return {
    redirectUri: `http://127.0.0.1:${port}/cb`,
    hits,
    stop: () => server.close(),
  };
}

/**
 * Start the bridge. Its issuer is `oauth.localhost` (the sign-in audience needs
 * a dotted host, and Chromium resolves *.localhost to loopback); identity
 * lookups all go to the test relay.
 */
export async function startBridge({ relay, redirectUri, pushHandle = "", clients = null, listen = "127.0.0.1", launcher = false }) {
  const port = await freePort();
  const dir = mkdtempSync(join(tmpdir(), "poweur-oauth-e2e-"));
  const push = pushHandle ? preparePusher(dir, relay, pushHandle) : null;
  const clientsFile = join(dir, "clients.json");
  writeFileSync(clientsFile, JSON.stringify({
    clients: clients ?? [{
      client_id: "e2e", client_name: "E2E application",
      redirect_uris: [redirectUri], token_endpoint_auth_method: "none",
    }],
  }));
  const issuer = `http://oauth.localhost:${port}`;
  const child = spawn("go", ["run", "./cmd/poweur-oauth"], {
    cwd: OAUTH_DIR,
    env: {
      ...process.env,
      OAUTH_ISSUER: issuer,
      OAUTH_ADDR: `${listen}:${port}`,
      OAUTH_DATABASE: join(dir, "oauth.db"),
      OAUTH_KEY_ENCRYPTION_KEY: randomBytes(32).toString("base64"),
      OAUTH_DEFAULT_SIGNER: `${relay.baseUrl}/app/`,
      OAUTH_STATIC_CLIENTS: clientsFile,
      // The create-an-ID offer points at the test relay, or nowhere.
      OAUTH_LAUNCHER_URL: launcher ? relay.baseUrl : "none",
      OAUTH_LAUNCHER_DOMAIN: "poweur.net",
      RESOLVER_ALLOW_PRIVATE: "1",
      POWEUR_RESOLVER_SCHEME: "http",
      OAUTH_RESOLVER_DIAL: relay.addr,
      OAUTH_RATE_AUTHORIZE: "-1",
      OAUTH_RATE_IDENTIFY: "-1",
      OAUTH_RATE_CALLBACK: "-1",
      OAUTH_RATE_TOKEN: "-1",
      POWEUR_RESOLVER_DIAL: relay.addr,
      ...(push ? { OAUTH_PUSH_CLI: push.bin, OAUTH_PUSH_HOME: push.home, OAUTH_PUSH_IDENTITY: push.identity } : {}),
    },
    stdio: ["ignore", "pipe", "pipe"],
  });
  let stderr = "";
  child.stderr.on("data", (d) => { stderr += d.toString(); });
  child.stdout.on("data", () => {});
  const stop = () => {
    try { child.kill("SIGTERM"); } catch { /* gone */ }
    try { rmSync(dir, { recursive: true, force: true }); } catch { /* gone */ }
  };
  try {
    await waitForHealth(`http://127.0.0.1:${port}`, { timeoutMs: 60_000 });
  } catch (e) {
    stop();
    throw new Error(`${e.message}\nbridge stderr:\n${stderr}`);
  }
  return { issuer, port, stop, stderr: () => stderr, pushIdentity: push?.identity ?? "" };
}

export function pkce() {
  const verifier = randomBytes(32).toString("base64url");
  const challenge = createHash("sha256").update(verifier).digest("base64url");
  return { verifier, challenge };
}

export function authorizeURL(bridge, rp, { challenge, state = "st", nonce = "nn", scope = "openid poweur_id" }) {
  const q = new URLSearchParams({
    response_type: "code", client_id: "e2e", redirect_uri: rp.redirectUri,
    scope, state, nonce, code_challenge: challenge, code_challenge_method: "S256",
  });
  return `${bridge.issuer}/authorize?${q}`;
}

/** Redeem a code as the public client and return the ID token's claims. */
export async function redeem(bridge, rp, code, verifier) {
  const res = await fetch(`http://127.0.0.1:${bridge.port}/token`, {
    method: "POST",
    headers: { "content-type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({
      grant_type: "authorization_code", code, redirect_uri: rp.redirectUri,
      code_verifier: verifier, client_id: "e2e",
    }),
  });
  const body = await res.json();
  if (!res.ok) throw new Error(`token ${res.status}: ${JSON.stringify(body)}`);
  return JSON.parse(Buffer.from(body.id_token.split(".")[1], "base64url").toString());
}
