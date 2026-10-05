/**
 * The guestbook (apps/demoapps/guestbook) beside a test relay, keeping its log in memory,
 * for browser journeys through "Sign in with Poweur ID".
 */
import { spawn } from "node:child_process";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { freePort, waitForHealth } from "./relay.mjs";

const __dirname = dirname(fileURLToPath(import.meta.url));
const DEMOAPPS_DIR = join(__dirname, "../../../demoapps");

/**
 * Its origin is `guestbook.localhost` (the sign-in audience needs a dotted host, and Chromium
 * resolves *.localhost to loopback); identity lookups all go to the test relay.
 */
export async function startGuestbook({ relay }) {
  const port = await freePort();
  const origin = `http://guestbook.localhost:${port}`;
  const child = spawn("go", ["run", "./cmd/guestbook"], {
    cwd: DEMOAPPS_DIR,
    env: {
      ...process.env,
      ORIGIN: origin,
      LISTEN: `127.0.0.1:${port}`,
      GUESTBOOK_MEMORY: "1",
      GUESTBOOK_NAME: "Poweur Guestbook",
      IDENTITY: "",
      POWEUR_RESOLVER_SCHEME: "http",
      RESOLVER_ALLOW_PRIVATE: "1",
      GUESTBOOK_RESOLVER_DIAL: relay.addr,
    },
    stdio: ["ignore", "pipe", "pipe"],
  });
  let stderr = "";
  child.stderr.on("data", (d) => { stderr += d.toString(); });
  child.stdout.on("data", () => {});
  const stop = () => {
    try { child.kill("SIGTERM"); } catch { /* gone */ }
  };
  try {
    await waitForHealth(`http://127.0.0.1:${port}`, { timeoutMs: 60_000 }).catch(async () => {
      // The guestbook answers /healthz, not /health.
      const deadline = Date.now() + 60_000;
      for (;;) {
        try {
          if ((await fetch(`http://127.0.0.1:${port}/healthz`)).ok) return;
        } catch { /* not up yet */ }
        if (Date.now() > deadline) throw new Error("guestbook not healthy");
        await new Promise((r) => setTimeout(r, 200));
      }
    });
  } catch (e) {
    stop();
    throw new Error(`${e.message}\nguestbook stderr:\n${stderr}`);
  }
  return { origin, port, stop, stderr: () => stderr };
}
