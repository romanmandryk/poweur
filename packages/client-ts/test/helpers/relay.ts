/**
 * Spawn a real Go relay for the integration suites — same approach as
 * `apps/web/test/helpers/relay.mjs` and `apps/integration`. Protocol behaviour
 * is asserted against the real server, never a mock: a mock would happily
 * agree with a client that had drifted.
 */

import { spawn, type ChildProcess } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
// helpers → test → client-ts → packages → repo root
export const REPO_ROOT = join(here, "../../../..");
const API_DIR = join(REPO_ROOT, "apps/api");

export interface RunningRelay {
  baseUrl: string;
  address: string;
  port: number;
  dataDir: string;
  stop: () => void;
  child: ChildProcess;
}

export async function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const server = createServer();
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      const port = typeof address === "object" && address ? address.port : 0;
      server.close((error) => (error ? reject(error) : resolve(port)));
    });
    server.on("error", reject);
  });
}

async function waitForHealth(baseUrl: string, timeoutMs = 60_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  let lastError: unknown;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(`${baseUrl}/health`);
      if (response.ok) return;
      lastError = new Error(`health ${response.status}`);
    } catch (error) {
      lastError = error;
    }
    await new Promise((resolve) => setTimeout(resolve, 150));
  }
  throw new Error(`relay not healthy: ${(lastError as Error)?.message ?? lastError}`);
}

export async function startRelay(
  options: { hostedDomains?: string } = {},
): Promise<RunningRelay> {
  const port = await freePort();
  const dataDir = mkdtempSync(join(tmpdir(), "poweur-ts-relay-"));
  const address = `127.0.0.1:${port}`;
  const baseUrl = `http://${address}`;

  const child = spawn("go", ["run", "."], {
    cwd: API_DIR,
    env: {
      ...process.env,
      LISTEN_ADDR: `:${port}`,
      RELAY_ADDRESS: address,
      RELAY_SCHEME: "http",
      POWEUR_DATA: dataDir,
      HOSTED_DOMAINS: options.hostedDomains ?? "poweur.net",
      RESOLVER_ALLOW_PRIVATE: "1",
      // Rate limits would otherwise trip on a suite that registers several
      // identities in quick succession.
      RATE_LIMIT_MINUTE: "10000",
      RATE_LIMIT_HOUR: "100000",
      RATE_LIMIT_DAY: "1000000",
    },
    stdio: ["ignore", "pipe", "pipe"],
  });

  let stderr = "";
  child.stderr?.on("data", (chunk: Buffer) => {
    stderr += chunk.toString();
  });
  child.stdout?.on("data", () => {});

  const stop = () => {
    try {
      child.kill("SIGTERM");
    } catch {
      /* already gone */
    }
    try {
      rmSync(dataDir, { recursive: true, force: true });
    } catch {
      /* best effort */
    }
  };

  try {
    await waitForHealth(baseUrl);
  } catch (error) {
    stop();
    throw new Error(`${(error as Error).message}\nrelay stderr:\n${stderr}`);
  }

  return { baseUrl, address, port, dataDir, stop, child };
}
