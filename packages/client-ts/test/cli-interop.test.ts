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

  it("one Go approval completes the TypeScript CLI requester and clears both queues", async () => {
    expect((await tsCli(["contacts", "request", goIdentity, "let us connect", "--use-identity", tsIdentity])).code).toBe(0);
    await goCli(["requests", "--use-identity", goIdentity]);
    // The test identities share this local relay; route the reply through it.
    const config = loadConfig();
    saveConfig({ ...config, via_home_relay: true });
    let approval;
    try { approval = await goCli(["contacts", "accept", tsIdentity, "--use-identity", goIdentity]); }
    finally { saveConfig(config); }
    expect(approval.stderr).not.toContain("could not be sent");
    const answer = await tsCli(["requests", "--use-identity", tsIdentity]);
    expect(answer.code, answer.stderr).toBe(0);
    expect(answer.stdout).not.toContain("accept with");
    const contacts = await tsCli(["contacts", "ls", "--json", "--use-identity", tsIdentity]);
    expect(JSON.parse(contacts.stdout).contacts).toContainEqual(expect.objectContaining({ identity: goIdentity, state: "accepted" }));
    expect((await tsCli(["requests", "--use-identity", tsIdentity])).stdout).toContain("no pending requests");
    expect((await goCli(["requests", "--use-identity", goIdentity])).stdout).toContain("no pending requests");
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

  it("creates identities from a seed, and the Go CLI derives the same keys from it", async () => {
    // A separate tree, so the shared config's active identity is untouched.
    const alt = mkdtempSync(join(tmpdir(), "ts-seed-"));
    const previous = process.env["POWEUR_HOME"];
    process.env["POWEUR_HOME"] = alt;
    try {
      const identity = uniqueIdentity("tsseed");
      const created = await tsCli(["identity", "create", identity, "--hosted", "--relay", relay.baseUrl, "--json"]);
      expect(created.code).toBe(0);
      const out = JSON.parse(created.stdout) as { seed: string; mnemonic: string; public_key: string };
      expect(out.seed).toBeTruthy();
      expect(out.mnemonic.split(" ")).toHaveLength(24);
      const derived = JSON.parse((await goCli(["key", "derive", "--seed", out.seed, "--json"])).stdout) as { public_key: string };
      expect(derived.public_key.replace(/^ed25519:/, "")).toBe(out.public_key);
    } finally {
      process.env["POWEUR_HOME"] = previous;
      rmSync(alt, { recursive: true, force: true });
    }
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

  it("--json --decrypt gives a script the plaintext in the Go CLI's shape, and consumes the inbox", async () => {
    await goCli(["send", tsIdentity, "plaintext for the script", "--via-home-relay"]);

    const polled = await tsCli(["inbox", "--json", "--decrypt", "--use-identity", tsIdentity]);
    expect(polled.code, polled.stderr).toBe(0);
    const doc = JSON.parse(polled.stdout) as {
      messages: Array<Record<string, unknown>>;
      acks: unknown[];
    };
    expect(doc.messages).toHaveLength(1);
    const [message] = doc.messages as [Record<string, unknown>];
    expect(message).toMatchObject({ sender: goIdentity, body: "plaintext for the script", decrypted: true });
    // The wire payload stays ciphertext, and the SDK's own field names are not leaked.
    expect(message["payload"]).not.toBe("plaintext for the script");
    expect(message["encryption"]).toBeTruthy();
    expect(message).not.toHaveProperty("plaintext");
    expect(message).not.toHaveProperty("decryptError");
    expect(polled.stdout.trim().split("\n")).toHaveLength(1);

    expect((await tsCli(["inbox", "--use-identity", tsIdentity])).stdout).toContain("no messages");
  });

  it("listen --once --json --decrypt waits for a message, prints it and exits", async () => {
    await goCli(["send", tsIdentity, "heard by listen", "--via-home-relay"]);
    const heard = await tsCli(["listen", "--once", "--json", "--decrypt", "--use-identity", tsIdentity]);
    expect(heard.code, heard.stderr).toBe(0);
    const docs = heard.stdout.trim().split("\n").map((line) => JSON.parse(line) as { messages: Array<{ body: string; decrypted: boolean }> });
    const delivered = docs.flatMap((doc) => doc.messages);
    expect(delivered).toEqual([expect.objectContaining({ body: "heard by listen", decrypted: true })]);
    expect((await tsCli(["inbox", "--use-identity", tsIdentity])).stdout).toContain("no messages");
  }, 60_000);

  it("the Go CLI reads a TypeScript message the same way", async () => {
    const sent = await tsCli(["send", goIdentity, "ts to go, decrypted by go", "--via-home-relay", "--use-identity", tsIdentity]);
    expect(sent.code, sent.stderr).toBe(0);
    const heard = await goCli(["listen", "--once", "--json", "--decrypt", "--use-identity", goIdentity]);
    const doc = JSON.parse(heard.stdout.trim().split("\n").filter((line) => line.includes('"body"'))[0]!) as {
      messages: Array<Record<string, unknown>>;
    };
    expect(doc.messages).toHaveLength(1);
    const message = doc.messages[0]!;
    expect(message).toMatchObject({ sender: tsIdentity, body: "ts to go, decrypted by go", decrypted: true });
    // Both CLIs name the same fields, so a script is portable between them.
    await goCli(["send", tsIdentity, "shape check", "--via-home-relay"]);
    const viaTs = await tsCli(["inbox", "--json", "--decrypt", "--use-identity", tsIdentity]);
    const tsFields = Object.keys((JSON.parse(viaTs.stdout) as { messages: Array<Record<string, unknown>> }).messages[0]!);
    for (const field of ["body", "decrypted", "id", "payload", "sender", "timestamp", "encryption"]) {
      expect(tsFields).toContain(field);
      expect(Object.keys(message)).toContain(field);
    }
  }, 60_000);

  it("refuses --decrypt without --json", async () => {
    for (const command of ["inbox", "listen"]) {
      const refused = await tsCli([command, "--decrypt", "--use-identity", tsIdentity]);
      expect(refused.code).not.toBe(0);
      expect(refused.stdout).toBe("");
      expect(refused.stderr).toContain("--json");
    }
  });

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
      "--hosted", "--relay", relay.baseUrl, "--json",
    ]);
    const { seed } = JSON.parse(created.stdout) as { seed: string };
    expect(seed).toBeTruthy();

    const api = new EnrollApi(new RelayClient(relay.baseUrl));
    const session = await api.offer(identity, "typescript device");
    const approve = (extra: string[]) =>
      goCli(["key", "approve", `  ${session.rendezvousId.toLowerCase()}\n`, "--use-identity", identity,
        "--relay", relay.baseUrl, "--seed", seed, "--json", ...extra]);
    // The Go side contributes its nonce; the TS device reveals and shows digits.
    await approve(["--no-wait"]);
    const step = await api.step(identity, session);
    if (step.state !== "compare") throw new Error(`state ${step.state}`);
    await approve(["--sas", step.sas]);
    const done = await api.step(identity, session);
    expect(done.state === "delivered" && toBase64url(done.seed)).toBe(seed);
  }, 180_000);

  it("moves a seed from TypeScript to the Go CLI", async () => {
    const identity = uniqueIdentity("tsenroll");
    const seed = newSeed();
    const keys = identityKeysFromSeed(identity, seed);
    await createIdentity(new IdentityApi(new RelayClient(relay.baseUrl)), identity, { hosted: true, keys });
    const { signer } = signerFor(keys);

    const newDevice = mkdtempSync(join(tmpdir(), "poweur-enroll-"));
    const enrolled = await goCli(["key", "enroll", identity, "--relay", relay.baseUrl, "--json"], newDevice);
    const offer = JSON.parse(enrolled.stdout) as { code: string; link: string };

    // Scanned: the TS approver checks the Go device's key against the link.
    const api = new EnrollApi(new RelayClient(relay.baseUrl));
    const approver = await api.begin(signer, identity, offer.link);
    expect(approver.mode).toBe("scan");
    await goCli(["key", "claim", identity, offer.code, "--json"], newDevice); // reveals
    const revealed = await api.wait(signer, identity, approver);
    if (revealed.state !== "revealed") throw new Error("not revealed");
    await api.approve(signer, identity, approver, revealed, seed);

    const claimed = await goCli(["key", "claim", identity, offer.code, "--json"], newDevice);
    expect(JSON.parse(claimed.stdout)).toMatchObject({ enrolled: true });
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
      "--hosted", "--relay", relay.baseUrl, "--json",
    ]);
    const { seed } = JSON.parse(created.stdout) as { seed: string };

    const previousHome = process.env["POWEUR_HOME"];
    const newDevice = mkdtempSync(join(tmpdir(), "poweur-ts-enroll-"));
    const asNewDevice = async (args: string[]) => {
      process.env["POWEUR_HOME"] = join(newDevice, ".poweur");
      try {
        return await tsCli(args);
      } finally {
        process.env["POWEUR_HOME"] = previousHome;
      }
    };
    const enrolled = await asNewDevice(["key", "enroll", identity, "--relay", relay.baseUrl, "--json"]);
    expect(enrolled.code, enrolled.stderr).toBe(0);
    const offer = JSON.parse(enrolled.stdout) as { code: string };
    const approve = (extra: string[]) =>
      goCli(["key", "approve", offer.code, "--use-identity", identity, "--relay", relay.baseUrl,
        "--seed", seed, "--json", ...extra]);

    await approve(["--no-wait"]);
    const shown = await asNewDevice(["key", "claim", identity, offer.code, "--json"]);
    const { sas } = JSON.parse(shown.stdout) as { sas: string };
    expect(sas).toMatch(/^\d{6}$/);
    await approve(["--sas", sas]);
    const claimed = await asNewDevice(["key", "claim", identity, offer.code, "--json"]);
    expect(claimed.code, claimed.stderr || claimed.stdout).toBe(0);
    expect(JSON.parse(claimed.stdout)).toMatchObject({ enrolled: true, identity });
  }, 180_000);
});
