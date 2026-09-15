import { spawn } from "node:child_process";
import { createServer } from "node:net";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = dirname(fileURLToPath(import.meta.url));
// helpers → test → web → apps → repo root
const REPO_ROOT = join(__dirname, "../../../..");
const API_DIR = join(REPO_ROOT, "apps/api");
// The built app, served at /app/ (`pnpm web` first).
const WEB_DIR = join(REPO_ROOT, "apps/web/dist");

export async function freePort() {
  return new Promise((resolve, reject) => {
    const s = createServer();
    s.listen(0, "127.0.0.1", () => {
      const { port } = s.address();
      s.close((err) => (err ? reject(err) : resolve(port)));
    });
    s.on("error", reject);
  });
}

export async function waitForHealth(baseUrl, { timeoutMs = 30_000 } = {}) {
  const deadline = Date.now() + timeoutMs;
  let lastErr;
  while (Date.now() < deadline) {
    try {
      const res = await fetch(`${baseUrl}/health`);
      if (res.ok) return;
      lastErr = new Error(`health ${res.status}`);
    } catch (e) {
      lastErr = e;
    }
    await new Promise((r) => setTimeout(r, 150));
  }
  throw new Error(`relay not healthy: ${lastErr?.message || lastErr}`);
}

/**
 * Start a real Go relay with POWEUR_DATA + HOSTED_DOMAINS + WEB_STATIC_DIR.
 * Returns { baseUrl, port, dataDir, stop }.
 */
export async function startRelay({ hostedDomains = "poweur.net" } = {}) {
  const port = await freePort();
  const dataDir = mkdtempSync(join(tmpdir(), "poweur-web-relay-"));
  const addr = `127.0.0.1:${port}`;
  const baseUrl = `http://${addr}`;

  const child = spawn("go", ["run", "."], {
    cwd: API_DIR,
    env: {
      ...process.env,
      LISTEN_ADDR: `:${port}`,
      RELAY_ADDRESS: addr,
      RELAY_SCHEME: "http",
      POWEUR_DATA: dataDir,
      HOSTED_DOMAINS: hostedDomains,
      WEB_STATIC_DIR: WEB_DIR,
      RESOLVER_ALLOW_PRIVATE: "1",
      RATE_LIMIT_MINUTE: "10000",
      RATE_LIMIT_HOUR: "100000",
      RATE_LIMIT_DAY: "1000000",
    },
    stdio: ["ignore", "pipe", "pipe"],
  });

  let stderr = "";
  child.stderr.on("data", (d) => {
    stderr += d.toString();
  });
  child.stdout.on("data", () => {});

  const stop = () => {
    try {
      child.kill("SIGTERM");
    } catch {
      /* ignore */
    }
    try {
      rmSync(dataDir, { recursive: true, force: true });
    } catch {
      /* ignore */
    }
  };

  try {
    await waitForHealth(baseUrl);
  } catch (e) {
    stop();
    throw new Error(`${e.message}\nrelay stderr:\n${stderr}`);
  }

  return { baseUrl, port, addr, dataDir, stop, child };
}
