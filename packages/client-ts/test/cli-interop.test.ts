/**
 * Go CLI ↔ TypeScript interop, sharing one `~/.poweur` tree.
 *
 * This is the test that backs the two claims the SDK makes about parity:
 * a message sent by either client decrypts in the other, and both read and
 * write the same config, key and session files. It drives the real `poweur`
 * binary as a subprocess against a real relay — no mocks on either side.
 */

import { execFile } from "node:child_process";
import { existsSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { promisify } from "node:util";
import { afterAll, beforeAll, describe, expect, it } from "vitest";

import { run } from "../src/cli/index.js";
import { identityKeysFromSeed, signerFor } from "../src/crypto/keys.js";
import { ed25519PublicKey } from "../src/crypto/index.js";
import { newSeed } from "../src/crypto/seed.js";
import { toBase64url } from "../src/encoding.js";
import { EnrollApi } from "../src/enroll.js";
import { loadConfig, saveConfig } from "../src/node/config.js";
import { FileKeyStore } from "../src/node/keystore.js";
import { FileSessionStore } from "../src/node/sessionstore.js";
import { PoweurClient } from "../src/client.js";
import { localResolveOptions, uniqueIdentity } from "./helpers/identities.js";
import { createIdentity, IdentityApi } from "../src/identity.js";
import { RelayClient } from "../src/http.js";
import { REPO_ROOT, startRelay, type RunningRelay } from "./helpers/relay.js";

const execFileAsync = promisify(execFile);
const GO_CLI_DIR = join(REPO_ROOT, "apps/cli");

let relay: RunningRelay;
let home: string;
let poweurHome: string;
let goIdentity: string;
let tsIdentity: string;
let goCache: { GOCACHE: string; GOMODCACHE: string };
const originalEnv = { ...process.env };

/** Env that points the Go CLI at the local relay and a `~/.poweur` tree. */
function goEnv(homeDir = home): NodeJS.ProcessEnv {
  return {
    ...process.env,
    // HOME is redirected so the Go CLI writes ~/.poweur into the temp tree,
    // but its build caches must stay where they are or every invocation
    // recompiles the world.
    HOME: homeDir,
    ...goCache,
    POWEUR_RESOLVER_SCHEME: "http",
    RESOLVER_ALLOW_PRIVATE: "1",
    // Force every resolver fetch at the local relay: there is no DNS zone
    // here, so this is what stands in for one.
    POWEUR_RESOLVER_DIAL: relay.address,
  };
}

async function goCli(
  args: string[],
  homeDir = home,
): Promise<{ stdout: string; stderr: string }> {
  return execFileAsync("go", ["run", ".", ...args], {
    cwd: GO_CLI_DIR,
    env: goEnv(homeDir),
    maxBuffer: 8 * 1024 * 1024,
  });
}

/** Capture the TypeScript CLI's output instead of writing to the terminal. */
async function tsCli(args: string[]): Promise<{ code: number; stdout: string; stderr: string }> {
  let stdout = "";
  let stderr = "";
  const code = await run(args, {
    stdout: (text) => {
      stdout += text;
    },
    stderr: (text) => {
      stderr += text;
    },
  });
  return { code, stdout, stderr };
}

describe("Go CLI ↔ TypeScript client", () => {
  beforeAll(async () => {
    const { stdout: env } = await execFileAsync("go", ["env", "GOCACHE", "GOMODCACHE"]);
    const [cache, modCache] = env.trim().split("\n");
    goCache = { GOCACHE: cache ?? "", GOMODCACHE: modCache ?? "" };

    relay = await startRelay();
    home = mkdtempSync(join(tmpdir(), "poweur-interop-"));
    poweurHome = join(home, ".poweur");
    // The TypeScript client points at the same tree the Go CLI derives from HOME.
    process.env["POWEUR_HOME"] = poweurHome;
    process.env["POWEUR_RESOLVER_SCHEME"] = "http";
    process.env["RESOLVER_ALLOW_PRIVATE"] = "1";
    process.env["POWEUR_RESOLVER_DIAL"] = relay.address;

    goIdentity = uniqueIdentity("gocli");
    tsIdentity = uniqueIdentity("tscli");

    // The Go CLI creates its identity and writes ~/.poweur itself.
    await goCli(["identity", "create", goIdentity, "--hosted", "--relay", relay.baseUrl]);

    // The TypeScript side registers a second identity and stores its keys in
    // the same directory the Go CLI just populated.
    const api = new IdentityApi(new RelayClient(relay.baseUrl));
    const created = await createIdentity(api, tsIdentity, { hosted: true });
    await new FileKeyStore(join(poweurHome, "keys")).save(created.keys);
  }, 300_000);

  afterAll(() => {
    relay?.stop();
    process.env = originalEnv;
    try {
      rmSync(home, { recursive: true, force: true });
    } catch {
      // A `go run` child may still be writing into the temp HOME; leaving a
      // temp directory behind is not worth failing the suite over.
    }
  });

  it("shares the config file the Go CLI wrote", () => {
    const config = loadConfig();
    expect(config.identity).toBe(goIdentity);
    expect(config.relay_url).toBe(relay.baseUrl);
    expect(config.keys_dir).toBe(join(poweurHome, "keys"));
  });

  it("reads keys the Go CLI wrote", async () => {
    const store = new FileKeyStore(join(poweurHome, "keys"));
    const keys = await store.load(goIdentity);
    expect(keys).not.toBeNull();
    // Go writes the 64-byte seed||public form; we must read it as-is.
    expect(keys?.signingPrivateKey).toHaveLength(64);
    expect(keys?.encryptionPrivateKey).toHaveLength(32);
    expect(existsSync(join(poweurHome, "keys", `${goIdentity}.key`))).toBe(true);
  });

  it("lists both identities from the shared tree", async () => {
    const listed = await tsCli(["identity", "list", "--json"]);
    expect(listed.code).toBe(0);
    const payload = JSON.parse(listed.stdout) as { identities: string[]; active: string };
    expect(payload.identities).toContain(goIdentity);
    expect(payload.identities).toContain(tsIdentity);
    expect(payload.active).toBe(goIdentity);
  });

  it("decrypts in TypeScript a message the Go CLI sent", async () => {
    // --via-home-relay skips DNS routing: the relay forwards on our behalf.
    await goCli(["send", tsIdentity, "hello from the go cli", "--via-home-relay"]);

    const keys = await new FileKeyStore(join(poweurHome, "keys")).load(tsIdentity);
    const { signer, decryptor } = signerFor(keys!);
    const client = new PoweurClient({
      relayUrl: relay.baseUrl,
      signer,
      decryptor,
      sessionStore: new FileSessionStore(join(poweurHome, "sessions")),
      resolve: localResolveOptions(relay.baseUrl),
    });
    const { messages } = await client.inbox();
    const received = messages.find((m) => m.plaintext === "hello from the go cli");
    expect(received).toBeDefined();
    expect(received?.sender).toBe(goIdentity);
  }, 180_000);

  it("decrypts in the Go CLI a message TypeScript sent", async () => {
    const keys = await new FileKeyStore(join(poweurHome, "keys")).load(tsIdentity);
    const { signer, decryptor } = signerFor(keys!);
    const client = new PoweurClient({
      relayUrl: relay.baseUrl,
      signer,
      decryptor,
      sessionStore: new FileSessionStore(join(poweurHome, "sessions")),
      resolve: localResolveOptions(relay.baseUrl),
    });
    await client.send(goIdentity, "hello from typescript", { viaHomeRelay: true });

    const { stdout } = await goCli(["inbox"]);
    expect(stdout).toContain("hello from typescript");
    expect(stdout).toContain(tsIdentity);
  }, 180_000);

  it("sends from the TypeScript CLI and reads it with the Go CLI", async () => {
    // Point the shared config at the TypeScript identity, exactly as
    // `poweur identity use` would.
    const config = loadConfig();
    saveConfig({ ...config, identity: tsIdentity });

    const sent = await tsCli(["send", goIdentity, "sent by the ts cli", "--via-home-relay", "--json"]);
    expect(sent.code).toBe(0);
    const payload = JSON.parse(sent.stdout) as { id: string };
    expect(payload.id).toMatch(/^msg_/);

    // The journal both clients share now knows about this message.
    const status = await tsCli(["messages", "status", "--id", payload.id, "--json"]);
    const statuses = JSON.parse(status.stdout) as Array<{ message_id: string; state: string }>;
    expect(statuses[0]?.message_id).toBe(payload.id);
    expect(statuses[0]?.state).toBe("delivered_recipient_relay");

    // The Go CLI reads the same config, so put the active identity back
    // before asking it to open *its* inbox.
    saveConfig(config);
    const { stdout } = await goCli(["inbox"]);
    expect(stdout).toContain("sent by the ts cli");
  }, 180_000);

  it("writes a session file the Go CLI accepts", async () => {
    const config = loadConfig();
    saveConfig({ ...config, identity: tsIdentity });
    await tsCli(["session", "refresh", "--json"]);
    expect(existsSync(join(poweurHome, "sessions", `${tsIdentity}.toml`))).toBe(true);

    // The Go CLI must read that session rather than registering a new one —
    // `session status` reports the id it found on disk.
    const { stdout } = await goCli(["session", "status", "--json"]);
    const goSession = JSON.parse(stdout) as { session_id: string; valid: boolean };
    const tsSession = await new FileSessionStore(join(poweurHome, "sessions")).load(tsIdentity);
    expect(goSession.session_id).toBe(tsSession?.sessionId);
    expect(goSession.valid).toBe(true);

    saveConfig(config);
  }, 180_000);

  it("agrees with the Go CLI on identity lookup", async () => {
    const ts = await tsCli(["identity", "lookup", goIdentity, "--json"]);
    const tsDoc = JSON.parse(ts.stdout) as { public_key: string; relay: string };
    const { stdout } = await goCli(["identity", "lookup", goIdentity, "--json"]);
    const goDoc = JSON.parse(stdout) as { public_key: string; relay: string };
    expect(tsDoc.public_key).toBe(goDoc.public_key);
    expect(tsDoc.relay).toBe(goDoc.relay);
  }, 180_000);

  it("moves a seed from the Go CLI to the TypeScript client", async () => {
    const identity = uniqueIdentity("goenroll");
    const created = await goCli([
      "identity", "create", identity,
      "--hosted", "--from-seed", "--relay", relay.baseUrl, "--json",
    ]);
    const { seed } = JSON.parse(created.stdout) as { seed: string };
    expect(seed).toBeTruthy();

    const api = new EnrollApi(new RelayClient(relay.baseUrl));
    const session = await api.offer(identity, "typescript device");
    // A pasted request code on a phone is rarely clean.
    const messy = `  ${session.rendezvousId}\n`;
    await goCli([
      "key", "approve", messy,
      "--use-identity", identity,
      "--relay", relay.baseUrl,
      "--seed", seed,
      "--sas", session.sas,
      "--json",
    ]);
    const received = await api.claim(identity, session);
    expect(received).not.toBeNull();
    expect(toBase64url(received!)).toBe(seed);
  }, 180_000);

  it("moves a seed from TypeScript to the Go CLI", async () => {
    const identity = uniqueIdentity("tsenroll");
    const seed = newSeed();
    const keys = identityKeysFromSeed(identity, seed);
    await createIdentity(new IdentityApi(new RelayClient(relay.baseUrl)), identity, {
      hosted: true,
      keys,
    });
    const { signer } = signerFor(keys);

    const newDevice = mkdtempSync(join(tmpdir(), "poweur-enroll-"));
    const enrolled = await goCli(
      ["key", "enroll", identity, "--relay", relay.baseUrl, "--json"],
      newDevice,
    );
    const offer = JSON.parse(enrolled.stdout) as {
      rendezvous_id: string;
      sas: string;
      ephemeral_private_key: string;
    };

    const api = new EnrollApi(new RelayClient(relay.baseUrl));
    const pending = await api.pending(signer, identity, offer.rendezvous_id);
    expect(pending.sas).toBe(offer.sas);
    await api.approve(signer, identity, pending, seed);

    await goCli(
      [
        "key", "claim", identity, offer.rendezvous_id,
        "--ephemeral-key", offer.ephemeral_private_key,
        "--relay", relay.baseUrl,
        "--json",
      ],
      newDevice,
    );
    const listed = await goCli(["identity", "show", "--use-identity", identity, "--json"], newDevice);
    const shown = JSON.parse(listed.stdout) as { public_key: string };
    expect(shown.public_key.replace(/^ed25519:/, "")).toBe(
      toBase64url(ed25519PublicKey(keys.signingPrivateKey)),
    );
  }, 180_000);

  it("runs the ceremony through both CLIs", async () => {
    const identity = uniqueIdentity("clienroll");
    const created = await goCli([
      "identity", "create", identity,
      "--hosted", "--from-seed", "--relay", relay.baseUrl, "--json",
    ]);
    const { seed } = JSON.parse(created.stdout) as { seed: string };

    const previousHome = process.env["POWEUR_HOME"];
    const newDevice = mkdtempSync(join(tmpdir(), "poweur-ts-enroll-"));
    process.env["POWEUR_HOME"] = join(newDevice, ".poweur");
    let offer: { rendezvous_id: string; sas: string; ephemeral_private_key: string };
    try {
      const enrolled = await tsCli([
        "key", "enroll", identity, "--relay", relay.baseUrl, "--json",
      ]);
      expect(enrolled.code).toBe(0);
      offer = JSON.parse(enrolled.stdout) as typeof offer;
    } finally {
      process.env["POWEUR_HOME"] = previousHome;
    }

    await goCli([
      "key", "approve", offer.rendezvous_id,
      "--use-identity", identity,
      "--relay", relay.baseUrl,
      "--seed", seed,
      "--sas", offer.sas,
      "--json",
    ]);

    process.env["POWEUR_HOME"] = join(newDevice, ".poweur");
    try {
      const claimed = await tsCli([
        "key", "claim", identity, offer.rendezvous_id,
        "--ephemeral-key", offer.ephemeral_private_key,
        "--relay", relay.baseUrl,
        "--json",
      ]);
      expect(claimed.code).toBe(0);
      const payload = JSON.parse(claimed.stdout) as { enrolled: boolean; identity: string };
      expect(payload.enrolled).toBe(true);
      expect(payload.identity).toBe(identity);
    } finally {
      process.env["POWEUR_HOME"] = previousHome;
    }
  }, 180_000);
});
