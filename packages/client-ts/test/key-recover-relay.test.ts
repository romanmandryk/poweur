/**
 * `poweur key recover` on a real relay: the right recovery phrase puts the
 * identity's keys on a fresh machine; a wrong one saves nothing.
 */
import { existsSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, beforeAll, describe, expect, it } from "vitest";

import { run } from "../src/cli/index.js";
import { identityKeysFromSeed } from "../src/crypto/keys.js";
import { newSeed } from "../src/crypto/seed.js";
import { RelayClient } from "../src/http.js";
import { createIdentity, IdentityApi } from "../src/identity.js";
import { newRecoveryKit } from "../src/kit.js";
import { loadConfig } from "../src/node/config.js";
import { uniqueIdentity } from "./helpers/identities.js";
import { startRelay, type RunningRelay } from "./helpers/relay.js";

let relay: RunningRelay;
let work: string;
const originalHome = process.env["POWEUR_HOME"];

async function cli(args: string[]) {
  let stdout = "";
  let stderr = "";
  const code = await run(args, { stdout: (t) => void (stdout += t), stderr: (t) => void (stderr += t) });
  return { code, stdout, stderr };
}

beforeAll(async () => {
  relay = await startRelay();
  work = mkdtempSync(join(tmpdir(), "poweur-recover-"));
}, 120_000);

afterAll(() => {
  relay?.stop();
  if (originalHome === undefined) delete process.env["POWEUR_HOME"];
  else process.env["POWEUR_HOME"] = originalHome;
  rmSync(work, { recursive: true, force: true });
});

describe("key recover", () => {
  it("restores an identity from its phrase, and refuses another identity's phrase", async () => {
    const identity = uniqueIdentity("recover");
    const seed = newSeed();
    await createIdentity(new IdentityApi(new RelayClient(relay.baseUrl)), identity, { hosted: true, keys: identityKeysFromSeed(identity, seed) });
    const phrase = join(work, "phrase.txt");

    // A phrase that belongs to nobody: refused, nothing saved.
    process.env["POWEUR_HOME"] = join(work, "wrong");
    writeFileSync(phrase, newRecoveryKit(identity, "", newSeed()).mnemonic);
    const wrong = await cli(["key", "recover", identity, "--seed-file", phrase, "--relay", relay.baseUrl]);
    expect(wrong.code).not.toBe(0);
    expect(wrong.stderr).toMatch(/not .*'s/);
    expect(existsSync(join(work, "wrong", "keys", `${identity}.key`))).toBe(false);

    // The right phrase on a fresh machine.
    process.env["POWEUR_HOME"] = join(work, "fresh");
    writeFileSync(phrase, newRecoveryKit(identity, "", seed).mnemonic);
    const ok = await cli(["key", "recover", identity, "--seed-file", phrase, "--relay", relay.baseUrl]);
    expect(ok.stderr).toBe("");
    expect(ok.code).toBe(0);
    expect(ok.stdout).toContain(`recovered ${identity}`);
    expect(existsSync(join(work, "fresh", "keys", `${identity}.key`))).toBe(true);
    expect(loadConfig().identity).toBe(identity);
  }, 60_000);
});
