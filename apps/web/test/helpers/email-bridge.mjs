/**
 * The email bridge (apps/email) beside a test relay, with an LMTP client that
 * plays the mail server and the log transport standing in for submission.
 */
import { execFileSync, spawn } from "node:child_process";
import { mkdtempSync, readdirSync, readFileSync, rmSync } from "node:fs";
import { randomBytes } from "node:crypto";
import { connect } from "node:net";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { freePort, waitForHealth } from "./relay.mjs";

const __dirname = dirname(fileURLToPath(import.meta.url));
const EMAIL_DIR = join(__dirname, "../../../email");
const CLI_DIR = join(__dirname, "../../../cli");

export const OPERATOR_TOKEN = "e2e-operator-token-0123456789";
export const BRIDGE_ID = "email.poweur.net";
const LMTP_PASSWORD = "e2e-lmtp-password-0123456789";

/** Start a bridge for `relay` (which must run with EMAIL_BRIDGE_ID and OPERATOR_TOKEN). */
export async function startEmailBridge(relay) {
  const dir = mkdtempSync(join(tmpdir(), "poweur-email-e2e-"));
  const seed = randomBytes(32).toString("base64url");
  const bin = join(dir, "poweur");
  execFileSync("go", ["build", "-o", bin, "."], { cwd: CLI_DIR, stdio: "pipe" });
  execFileSync(bin, ["identity", "create", BRIDGE_ID, "--hosted", "--seed", seed, "--operator-token", OPERATOR_TOKEN, "--relay", relay.baseUrl, "--json"], {
    env: { ...process.env, HOME: join(dir, "home"), POWEUR_RESOLVER_SCHEME: "http", RESOLVER_ALLOW_PRIVATE: "1", POWEUR_RESOLVER_DIAL: relay.addr },
    stdio: "pipe",
  });
  const httpPort = await freePort();
  const lmtpPort = await freePort();
  const logDir = join(dir, "sent");
  const child = spawn("go", ["run", "./cmd/poweur-email"], {
    cwd: EMAIL_DIR,
    env: {
      ...process.env,
      EMAIL_BRIDGE_ID: BRIDGE_ID,
      EMAIL_BRIDGE_SEED: seed,
      EMAIL_RELAY_URL: relay.baseUrl,
      EMAIL_PUBLIC_URL: `http://127.0.0.1:${httpPort}`,
      EMAIL_ADDR: `127.0.0.1:${httpPort}`,
      EMAIL_LMTP_ADDR: `127.0.0.1:${lmtpPort}`,
      EMAIL_DOMAINS: "poweur.net",
      EMAIL_INBOUND: "lmtp",
      EMAIL_HOOK_SECRET: "e2e-hook-secret-0123456789",
      EMAIL_LMTP_USERNAME: "stalwart",
      EMAIL_LMTP_PASSWORD: LMTP_PASSWORD,
      EMAIL_LMTP_INSECURE: "1",
      EMAIL_AUTHSERV_ID: "mail.poweur.net",
      EMAIL_TRANSPORT: "log",
      EMAIL_LOG_DIR: logDir,
      EMAIL_DATA: join(dir, "data"),
      EMAIL_OPTIN_CACHE: "1ms",
      EMAIL_RESOLVE_SCHEME: "http",
      EMAIL_RESOLVE_ALLOW_PRIVATE: "1",
      EMAIL_RESOLVE_DIAL: relay.addr,
    },
    stdio: ["ignore", "pipe", "pipe"],
  });
  let stderr = "";
  child.stderr.on("data", (d) => { stderr += d.toString(); });
  child.stdout.on("data", () => {});
  const stop = () => {
    try { child.kill("SIGTERM"); } catch { /* ignore */ }
    try { rmSync(dir, { recursive: true, force: true }); } catch { /* ignore */ }
  };
  try {
    await waitForHealth(`http://127.0.0.1:${httpPort}`, { timeoutMs: 120_000 });
  } catch (e) {
    stop();
    throw new Error(`${e.message}\nbridge stderr:\n${stderr}`);
  }
  return {
    stop,
    log: () => stderr,
    sent: () => {
      try {
        return readdirSync(logDir).map((name) => readFileSync(join(logDir, name), "utf8"));
      } catch {
        return [];
      }
    },
    deliver: (to, message) => lmtpDeliver(lmtpPort, to, message),
  };
}

/** A minimal LMTP client: LHLO, AUTH PLAIN, MAIL, RCPT, DATA. Resolves with the final reply. */
function lmtpDeliver(port, to, message) {
  return new Promise((resolve, reject) => {
    const socket = connect(port, "127.0.0.1");
    const auth = Buffer.from(`\0stalwart\0${LMTP_PASSWORD}`).toString("base64");
    const steps = ["LHLO mail.poweur.net", `AUTH PLAIN ${auth}`, "MAIL FROM:<ann@example.com>", `RCPT TO:<${to}>`, "DATA",
      message.replace(/\r?\n/g, "\r\n").replace(/^\./gm, "..") + "\r\n.", "QUIT"];
    let buffer = "";
    let next = 0;
    socket.on("data", (chunk) => {
      buffer += chunk.toString();
      // A reply is complete when its last line reads "NNN text".
      const lines = buffer.split("\r\n").filter(Boolean);
      const last = lines.at(-1) ?? "";
      if (!/^\d{3} /.test(last)) return;
      buffer = "";
      if (next > 0 && /^[45]/.test(last)) {
        socket.end();
        reject(new Error(`LMTP after ${steps[next - 1].slice(0, 24)}: ${last}`));
        return;
      }
      if (steps[next] === "QUIT") {
        // `last` is the per-recipient answer to the message.
        socket.end("QUIT\r\n");
        resolve(last);
        return;
      }
      socket.write(steps[next++] + "\r\n");
    });
    socket.on("error", reject);
  });
}
