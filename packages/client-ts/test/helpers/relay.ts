/**
 * Spawn a real Go relay for the integration suites — same approach as
 * `apps/web/test/helpers/relay.mjs` and `apps/integration`. Protocol behaviour
 * is asserted against the real server, never a mock: a mock would happily
 * agree with a client that had drifted.
 */

import { spawn, type ChildProcess } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { request as httpRequest } from "node:http";
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
  options: { hostedDomains?: string; env?: Record<string, string> } = {},
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
      ...(options.env ?? {}),
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

/**
 * Several relays that reach each other's hosted identities (E26-T1): they
 * share a hosts file (`RESOLVER_HOSTS_FILE`, honoured only with
 * `RESOLVER_ALLOW_PRIVATE`), and `host()` records which relay serves an
 * identity. `fetch` does the same for clients: an identity's well-known
 * document is fetched from its relay with the identity as Host (Node's fetch
 * drops a custom Host header, so this uses node:http).
 */
export interface RelayNetwork {
  relays: RunningRelay[];
  host(identity: string, relay: RunningRelay): void;
  fetch: typeof globalThis.fetch;
  stop(): void;
}

export async function startRelays(count: number, options: { env?: Record<string, string> } = {}): Promise<RelayNetwork> {
  const dir = mkdtempSync(join(tmpdir(), "poweur-ts-hosts-"));
  const hostsFile = join(dir, "hosts.json");
  const hosts: Record<string, string> = {};
  writeFileSync(hostsFile, "{}");
  const relays: RunningRelay[] = [];
  try {
    for (let i = 0; i < count; i++) {
      relays.push(await startRelay({ env: { RESOLVER_HOSTS_FILE: hostsFile, ...(options.env ?? {}) } }));
    }
  } catch (error) {
    relays.forEach((relay) => relay.stop());
    throw error;
  }
  const routedFetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(typeof input === "string" || input instanceof URL ? input.toString() : input.url);
    const address = hosts[url.hostname.toLowerCase()];
    if (!address) return globalThis.fetch(input, init);
    const [host, port] = address.split(":");
    return new Promise<Response>((resolve, reject) => {
      const request = httpRequest(
        { host, port: Number(port), path: url.pathname + url.search, method: init?.method ?? "GET", headers: { ...(init?.headers as Record<string, string>), Host: url.host } },
        (response) => {
          const chunks: Buffer[] = [];
          response.on("data", (chunk: Buffer) => chunks.push(chunk));
          response.on("end", () => {
            const headers = new Headers();
            for (const [name, value] of Object.entries(response.headers)) {
              if (typeof value === "string") headers.set(name, value);
            }
            resolve(new Response(response.statusCode === 204 || response.statusCode === 304 ? null : Buffer.concat(chunks), { status: response.statusCode ?? 500, headers }));
          });
        },
      );
      request.on("error", reject);
      init?.signal?.addEventListener("abort", () => request.destroy(new Error("aborted")));
      request.end();
    });
  }) as typeof globalThis.fetch;
  return {
    relays,
    host(identity, relay) {
      hosts[identity.toLowerCase()] = relay.address;
      writeFileSync(hostsFile, JSON.stringify(hosts));
    },
    fetch: routedFetch,
    stop() {
      relays.forEach((relay) => relay.stop());
      rmSync(dir, { recursive: true, force: true });
    },
  };
}
