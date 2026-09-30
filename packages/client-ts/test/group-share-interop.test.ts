/**
 * EPIC-024 E24-T3 across implementations: a group created and administered
 * with the Go CLI; TypeScript members open the group folder with the group
 * key, write to it, and share their own folder with the group.
 */
import { execFile } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { promisify } from "node:util";
import { afterAll, beforeAll, expect, it } from "vitest";
import { DriveClient } from "../src/drive/client.js";
import { DriveFiles, fileKeys, type FileKeys } from "../src/drive/files.js";
import { fromBase64 } from "../src/encoding.js";
import { resolveSigningKey } from "../src/resolve.js";
import { createTestIdentity, localResolveOptions, uniqueIdentity, type TestIdentity } from "./helpers/identities.js";
import { REPO_ROOT, startRelay, type RunningRelay } from "./helpers/relay.js";

const execFileAsync = promisify(execFile);
let relay: RunningRelay, home: string, goCache: Record<string, string>;
let alice: string, crew: string, bob: TestIdentity, carol: TestIdentity;

async function goCli(args: string[]): Promise<string> {
  try {
    const { stdout } = await execFileAsync("go", ["run", ".", ...args], {
      cwd: join(REPO_ROOT, "apps/cli"),
      env: { ...process.env, HOME: home, ...goCache, POWEUR_RESOLVER_SCHEME: "http", RESOLVER_ALLOW_PRIVATE: "1", POWEUR_RESOLVER_DIAL: relay.address },
      maxBuffer: 8 * 1024 * 1024,
    });
    return stdout;
  } catch (error) {
    throw new Error(`poweur ${args.join(" ")}: ${(error as { stderr?: string }).stderr ?? error}`);
  }
}

beforeAll(async () => {
  const { stdout } = await execFileAsync("go", ["env", "GOCACHE", "GOMODCACHE"]);
  const [cache, modCache] = stdout.trim().split("\n");
  goCache = { GOCACHE: cache ?? "", GOMODCACHE: modCache ?? "" };
  relay = await startRelay();
  home = mkdtempSync(join(tmpdir(), "poweur-group-interop-"));
  alice = uniqueIdentity("gialice");
  crew = uniqueIdentity("gicrew");
  await goCli(["identity", "create", alice, "--hosted", "--relay", relay.baseUrl]);
  bob = await createTestIdentity(relay.baseUrl, "gibob");
  carol = await createTestIdentity(relay.baseUrl, "gicarol");
  await goCli(["group", "create", crew, "--member", alice, "--member", bob.identity, "--relay", relay.baseUrl]);
}, 300_000);

afterAll(() => {
  relay?.stop();
  try { rmSync(home, { recursive: true, force: true }); } catch { /* a go child may still write */ }
});

function keysFor(member: TestIdentity, extra: Partial<FileKeys> = {}): FileKeys {
  const resolve = localResolveOptions(relay.baseUrl);
  return {
    ...fileKeys(member.keys.signingPrivateKey, member.keys.encryptionPrivateKey!),
    async authorKey(author: string) {
      const key = await resolveSigningKey(author, resolve);
      if (!key) throw new Error(`cannot resolve ${author}`);
      return fromBase64(key);
    },
    async groupMembers(group: string) {
      const { document } = await member.client.groups.roster(member.client.signer, group);
      return [...document.members, ...(document.admins ?? [])];
    },
    ...extra,
  };
}

const text = async (files: DriveFiles, path: string) => {
  const chunks: Uint8Array[] = [];
  for await (const chunk of files.read(await files.resolve(path))) chunks.push(chunk);
  return Buffer.concat(chunks).toString();
};

it("a TypeScript member opens and writes the group folder the Go CLI shared", async () => {
  const plan = join(home, "plan.md");
  writeFileSync(plan, "plan from the go cli\n");
  const folder = JSON.parse(await goCli(["drive", "mkdir", "/Crew", "--use-identity", crew, "--json"])) as { node: string };
  await goCli(["drive", "put", plan, "/Crew/plan.md", "--use-identity", crew, "--json"]);
  await goCli(["drive", "share", "add", "/Crew", crew, "--role", "write", "--no-offer", "--use-identity", crew, "--json"]);

  const { keys, roster } = await bob.client.groups.keys(bob.client.signer, crew, bob.keys.encryptionPrivateKey!);
  expect(keys.length).toBeGreaterThan(0);
  const client = new DriveClient(bob.client.relay, bob.client.signer, crew);
  client.groupRoster = roster;
  const member = new DriveFiles(client, keysFor(bob, { groupKeys: { [crew]: keys.map((k) => k.private) } }));
  expect(await text(member, `/${folder.node}/plan.md`)).toBe("plan from the go cli\n");
  await member.create(await member.resolve(`/${folder.node}`), "note.md", "file", new TextEncoder().encode("note from typescript\n"));

  // Back in Go, alice reads bob's note as a member of the group.
  const out = join(home, "note.md");
  await goCli(["drive", "get", `/${folder.node}/note.md`, out, "--drive", crew, "--group", crew, "--use-identity", alice]);
  expect(readFileSync(out, "utf8")).toBe("note from typescript\n");

  // Without the group's keys the share is not bob's.
  const outsider = new DriveFiles(new DriveClient(carol.client.relay, carol.client.signer, crew), keysFor(carol));
  await expect(outsider.open(folder.node)).rejects.toBeTruthy();
}, 240_000);

it("a TypeScript sharer seals to the group's public key; members open it", async () => {
  const owner = new DriveFiles(new DriveClient(carol.client.relay, carol.client.signer), keysFor(carol));
  const folder = await owner.create(await owner.root(), "for-crew", "folder");
  await owner.create(folder, "brief.md", "file", new TextEncoder().encode("brief from carol\n"));
  const { key } = await carol.client.groups.publicKey(crew);
  await owner.shareWith(folder, crew, key, "read");

  // The Go member reads it with the group key.
  const out = join(home, "brief.md");
  await goCli(["drive", "get", `/${folder.manifest.node}/brief.md`, out, "--drive", carol.identity, "--group", crew, "--use-identity", alice]);
  expect(readFileSync(out, "utf8")).toBe("brief from carol\n");
}, 240_000);
