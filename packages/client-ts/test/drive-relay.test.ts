import { DriveFiles, fileKeys, type OpenFile } from "../src/drive/files.js";
import { LinkPasswordRequired, openLink, parseLinkUrl } from "../src/drive/link.js";
import { openFileRequest } from "../src/drive/request.js";
import { DriveLog } from "../src/drive/log.js";
import { DriveScope } from "../src/drive/scope.js";
import { SystemFiles, DeviceRegistry } from "../src/systemfiles.js";
import { sessionSigner } from "../src/session.js";
import { afterAll, beforeAll, expect, it } from "vitest";
import { DriveClient } from "../src/drive/client.js";
import { driveContext, sealKey } from "../src/drive/crypto.js";
import { signManifest, type Manifest } from "../src/drive/manifest.js";
import { x25519PublicKey } from "../src/crypto/index.js";
import { createTestIdentity, localResolveOptions, type TestIdentity } from "./helpers/identities.js";
import { resolveEncryptionKey, resolveSigningKey } from "../src/resolve.js";
import { fromBase64 } from "../src/encoding.js";
import { startRelay, type RunningRelay } from "./helpers/relay.js";
let relay: RunningRelay, alice: TestIdentity;
beforeAll(async () => { relay = await startRelay(); alice = await createTestIdentity(relay.baseUrl, "drive"); }, 180_000);
afterAll(() => relay?.stop());
it("commits a root against the Go relay and resumes from the changes cursor", async () => {
  const client = new DriveClient(alice.client.relay, alice.client.signer);
  expect((await client.info()).root).toBe("");
  const node = "1".repeat(32);
  const wrapped = sealKey(x25519PublicKey(alice.keys.encryptionPrivateKey!), new Uint8Array(32).fill(2), driveContext(alice.identity, node, "node-key", 1));
  const manifest: Manifest = signManifest({ format: 1, drive: alice.identity, node, version: "2".repeat(32),
    parent: "", operation: "create", author: alice.identity, generation: 1, kind: "folder", mode: "", folder: "", name_hash: "",
    node_key: { ephemeral_public_key: wrapped.ephemeralPublicKey, nonce: wrapped.nonce, ciphertext: wrapped.ciphertext },
    count: 0, pages: [], signature: "" }, alice.keys.signingPrivateKey);
  const request = { id: "3".repeat(32), manifest };
  const committed = await client.commit(request);
  expect(await client.commit(request)).toEqual(committed);
  expect((await client.info()).root).toBe(node);
  expect((await client.node(node)).head).toBe(manifest.version);
  expect(await client.version(node, manifest.version)).toMatchObject({ node, signature: manifest.signature });
  expect((await client.children(node)).children).toEqual([]);
  const changes = await client.changes();
  expect(changes.changes.some(change => change.node === node)).toBe(true);
  expect((await client.changes(changes.cursor)).changes).toEqual([]);
});

it("uses a session key for system files, devices and drive reads, then rejects it after revocation", async () => {
  const session = await alice.client.sessions.ensure(alice.client.signer);
  const signer = sessionSigner(session);
  const files = new SystemFiles(alice.client.relay, signer, session.sessionId);
  await files.writeJson(".poweur/public/profile.json", { version: 1, display_name: "Session writer" });
  expect(await files.readOptional(".poweur/public/profile.json")).toContain("Session writer");
  expect((await new DeviceRegistry(files).list()).identity).toBe(alice.identity);
  const drive = new DriveClient(alice.client.relay, signer, alice.identity, undefined, session.sessionId);
  expect((await drive.info()).drive).toBe(alice.identity);
  await alice.client.sessions.revoke(alice.client.signer);
  await expect(files.readOptional(".poweur/public/profile.json")).rejects.toMatchObject({ status: 401 });
  await expect(drive.info()).rejects.toMatchObject({ status: 401 });
});

it("encrypts multi-chunk files, replaces, moves and reopens their key tree", async () => {
  const files = new DriveFiles(new DriveClient(alice.client.relay, alice.client.signer), fileKeys(alice.keys.signingPrivateKey, alice.keys.encryptionPrivateKey!));
  const root = await files.root();
  const folder = await files.create(root, "projects", "folder");
  const bytes = new Uint8Array(4 * 1024 * 1024 + 37).fill(91);
  const file = await files.create(folder, "cafe\u0301.bin", "file", bytes);
  const read = async (file: OpenFile) => { const chunks = []; for await (const chunk of files.read(file)) chunks.push(chunk); return Buffer.concat(chunks); };
  expect(await read(await files.resolve("/projects/café.bin"))).toEqual(Buffer.from(bytes));
  const stale = await files.open(file.manifest.node);
  await files.replace(file, new TextEncoder().encode("replaced"));
  await expect(files.replace(stale, new Uint8Array([1]))).rejects.toMatchObject({ status: 409 });
  await files.move(file, root, "renamed.bin");
  expect((await read(await files.resolve("/renamed.bin"))).toString()).toBe("replaced");
  expect((await files.list(folder)).length).toBe(0);
  await expect(files.resolve("/../renamed.bin")).rejects.toThrow();
  await files.remove(file);
  await expect(files.resolve("/renamed.bin")).rejects.toThrow("not found");
  const empty = await files.create(folder, "empty", "file");
  expect((await read(await files.open(empty.manifest.node))).length).toBe(0);
  await expect(files.create(folder, "../escape", "file")).rejects.toThrow();
}, 60_000);

it("folds an append log and refuses nodes outside a scoped folder", async () => {
  const files = new DriveFiles(new DriveClient(alice.client.relay, alice.client.signer), fileKeys(alice.keys.signingPrivateKey, alice.keys.encryptionPrivateKey!));
  const root = await files.root();
  const logFile = await files.create(root, "board.log", "file", new Uint8Array(), "append");
  const text = new TextEncoder();
  await files.append(logFile, text.encode("one"));
  await files.append(logFile, text.encode("two"));
  const tail = await files.tail(await files.open(logFile.manifest.node), 1);
  expect(tail.map(record => new TextDecoder().decode(record.plain))).toEqual(["one", "two"]);
  const log = await DriveLog.open(files, logFile, (state, entry) => [...(Array.isArray(state) ? state : []), new TextDecoder().decode(entry.plaintext)], []);
  expect(log.value).toEqual(["one", "two"]);
  const snap = await log.snapshot(root, "board.snap");
  expect(snap.manifest.version).toMatch(/^[0-9a-f]{32}$/);
  expect(log.position).toBe(2);
  const app = await files.create(root, "app-folder", "folder");
  const scope = new DriveScope(files, app.manifest.node);
  await expect(scope.open(logFile.manifest.node)).rejects.toThrow("outside");
  const note = await scope.create(await scope.resolve("/"), "note", "file", text.encode("hi"));
  expect((await scope.resolve("/note")).manifest.node).toBe(note.manifest.node);
});

it("archives a conversation as a sealed append log and dedupes by id", async () => {
  const history = await alice.client.history();
  const record = { id: "m1", sender: alice.identity, recipient: "bob.poweur.net", timestamp: "2026-09-28T12:00:00Z", queue: "sent" as const, body: "sealed-history-body" };
  await history.append(record);
  await history.append(record);
  expect((await history.tail("bob.poweur.net")).map(row => row.body)).toEqual(["sealed-history-body"]);
  expect(await history.before("bob.poweur.net", 1)).toEqual([]);
  await history.putReadState({ conversations: { "bob.poweur.net": { timestamp: record.timestamp, id: "m1" } } });
  expect((await history.readState()).conversations["bob.poweur.net"]?.id).toBe("m1");
});

it("a member reads and writes through their share with only their own keys", async () => {
  const bob = await createTestIdentity(relay.baseUrl, "member");
  const carol = await createTestIdentity(relay.baseUrl, "stranger");
  const resolve = localResolveOptions(relay.baseUrl);
  const publicKeys = {
    async authorKey(author: string) {
      const key = await resolveSigningKey(author, resolve);
      if (!key) throw new Error(`cannot resolve ${author}`);
      return fromBase64(key);
    },
  };
  const owner = new DriveFiles(new DriveClient(alice.client.relay, alice.client.signer), { ...fileKeys(alice.keys.signingPrivateKey, alice.keys.encryptionPrivateKey!), ...publicKeys });
  const team = await owner.create(await owner.root(), "team", "folder");
  await owner.create(team, "plan.txt", "file", new TextEncoder().encode("alice's plan"));
  const bobEnc = fromBase64((await resolveEncryptionKey(bob.identity, resolve))!);
  await owner.shareWith(team, bob.identity, bobEnc, "write");

  // Bob: his signer, his encryption key, alice's drive.
  const member = new DriveFiles(new DriveClient(bob.client.relay, bob.client.signer, alice.identity), { ...fileKeys(bob.keys.signingPrivateKey, bob.keys.encryptionPrivateKey!), ...publicKeys });
  const shared = await member.shared();
  expect(shared.map(file => file.manifest.node)).toEqual([team.manifest.node]);
  const text = async (files: DriveFiles, path: string) => { const chunks = []; for await (const c of files.read(await files.resolve(path))) chunks.push(c); return Buffer.concat(chunks).toString(); };
  expect(await text(member, `/${team.manifest.node}/plan.txt`)).toBe("alice's plan");
  await member.replace(await member.resolve(`/${team.manifest.node}/plan.txt`), new TextEncoder().encode("bob's plan"));
  await member.create(await member.resolve(`/${team.manifest.node}`), "notes.txt", "file", new TextEncoder().encode("bob's notes"));
  // Alice's client accepts bob's versions only because his share allows them.
  const fresh = new DriveFiles(new DriveClient(alice.client.relay, alice.client.signer), { ...fileKeys(alice.keys.signingPrivateKey, alice.keys.encryptionPrivateKey!), ...publicKeys });
  expect(await text(fresh, "/team/plan.txt")).toBe("bob's plan");
  expect(await text(fresh, "/team/notes.txt")).toBe("bob's notes");
  // Without a share, carol gets nothing — the relay refuses and so does her client.
  const stranger = new DriveFiles(new DriveClient(carol.client.relay, carol.client.signer, alice.identity), { ...fileKeys(carol.keys.signingPrivateKey, carol.keys.encryptionPrivateKey!), ...publicKeys });
  await expect(stranger.open(team.manifest.node)).rejects.toBeTruthy();
});

it("keeps a revoked member's versions valid and rotates keys so writes resume", async () => {
  const bob = await createTestIdentity(relay.baseUrl, "revoked");
  const dave = await createTestIdentity(relay.baseUrl, "remaining");
  const resolve = localResolveOptions(relay.baseUrl);
  const publicKeys = {
    async authorKey(author: string) { return fromBase64((await resolveSigningKey(author, resolve))!); },
    async encryptionKey(member: string) { return fromBase64((await resolveEncryptionKey(member, resolve))!); },
  };
  const ownerFiles = () => new DriveFiles(new DriveClient(alice.client.relay, alice.client.signer), { ...fileKeys(alice.keys.signingPrivateKey, alice.keys.encryptionPrivateKey!), ...publicKeys });
  const owner = ownerFiles();
  const text = async (files: DriveFiles, file: OpenFile) => { const chunks = []; for await (const c of files.read(file)) chunks.push(c); return Buffer.concat(chunks).toString(); };
  const lab = await owner.create(await owner.root(), "lab", "folder");
  const inner = await owner.create(lab, "inner", "folder");
  await owner.create(inner, "deep.txt", "file", new TextEncoder().encode("deep"));
  const journal = await owner.create(lab, "journal.log", "file", new Uint8Array(), "append");
  await owner.append(journal, new TextEncoder().encode("before"));
  const bobShare = await owner.shareWith(lab, bob.identity, await publicKeys.encryptionKey(bob.identity), "write");
  await owner.shareWith(lab, dave.identity, await publicKeys.encryptionKey(dave.identity), "read");
  const bobFiles = new DriveFiles(new DriveClient(bob.client.relay, bob.client.signer, alice.identity), { ...fileKeys(bob.keys.signingPrivateKey, bob.keys.encryptionPrivateKey!), ...publicKeys });
  await bobFiles.create(await bobFiles.resolve(`/${lab.manifest.node}`), "bob.txt", "file", new TextEncoder().encode("by bob"));

  await owner.client.unshare(bobShare.id);
  // Bob's version stays valid for the owner: his revoked share is evidence.
  const after = ownerFiles();
  expect(await text(after, await after.resolve("/lab/bob.txt"))).toBe("by bob");
  // Writes wait for a rotation; the rotation re-keys the subtree and re-issues dave's share.
  await expect(after.create(await after.resolve("/lab"), "blocked.txt", "file", new Uint8Array([1]))).rejects.toMatchObject({ status: 409 });
  const { reissued, stale } = await after.rotate(await after.resolve("/lab"));
  expect(reissued).toBe(1);
  expect(stale).toEqual([]);
  const rotated = ownerFiles();
  await rotated.create(await rotated.resolve("/lab"), "after.txt", "file", new TextEncoder().encode("after"));
  expect(await text(rotated, await rotated.resolve("/lab/inner/deep.txt"))).toBe("deep");
  expect(await text(rotated, await rotated.resolve("/lab/bob.txt"))).toBe("by bob");
  const log = await rotated.resolve("/lab/journal.log");
  await rotated.append(log, new TextEncoder().encode("after"));
  expect((await rotated.tail(log, 1)).map(r => new TextDecoder().decode(r.plain))).toEqual(["before", "after"]);
  // Dave reopens through his re-issued share; bob's old key opens nothing new.
  const daveFiles = new DriveFiles(new DriveClient(dave.client.relay, dave.client.signer, alice.identity), { ...fileKeys(dave.keys.signingPrivateKey, dave.keys.encryptionPrivateKey!), ...publicKeys });
  expect(await text(daveFiles, await daveFiles.resolve(`/${lab.manifest.node}/after.txt`))).toBe("after");
  await expect(bobFiles.open(lab.manifest.node)).rejects.toBeTruthy();
}, 120_000);

it("opens a password link with only the fragment and the password", async () => {
  const owner = new DriveFiles(new DriveClient(alice.client.relay, alice.client.signer), fileKeys(alice.keys.signingPrivateKey, alice.keys.encryptionPrivateKey!));
  const folder = await owner.create(await owner.root(), "published", "folder");
  await owner.create(folder, "report.txt", "file", new TextEncoder().encode("quarterly numbers"));
  const { share, fragment } = await owner.link(folder, "read", "", "open sesame");
  const base = { origin: relay.baseUrl, drive: alice.identity, link: share.link!, fragment, resolve: localResolveOptions(relay.baseUrl) };
  await expect(openLink(base)).rejects.toBeInstanceOf(LinkPasswordRequired);
  await expect(openLink({ ...base, password: "wrong" })).rejects.toMatchObject({ status: 401 });
  const { files, root, info } = await openLink({ ...base, password: "open sesame" });
  expect(info).toMatchObject({ drive: alice.identity, role: "read", password: true });
  const [report] = await files.list(root);
  expect(report?.name).toBe("report.txt");
  const chunks = []; for await (const c of files.read(report!)) chunks.push(c);
  expect(Buffer.concat(chunks).toString()).toBe("quarterly numbers");
  // A wrong fragment decrypts nothing, even with the right password.
  await expect(openLink({ ...base, fragment: new Uint8Array(32).fill(1), password: "open sesame" })).rejects.toBeTruthy();
  expect(parseLinkUrl(`https://${alice.identity}/s/${share.link}#${Buffer.from(fragment).toString("base64url")}`)).toMatchObject({ drive: alice.identity, link: share.link });
});

it("uploads through a password-protected create-only file request", async () => {
  const owner = new DriveFiles(new DriveClient(alice.client.relay, alice.client.signer), fileKeys(alice.keys.signingPrivateKey, alice.keys.encryptionPrivateKey!));
  const folder = await owner.create(await owner.root(), "requests", "folder");
  const { share, fragment } = await owner.link(folder, "create", "", "drop here", { caps: { files: 2 }, pow: 8 });
  const options = { origin: relay.baseUrl, drive: alice.identity, link: share.link!, fragment, resolve: localResolveOptions(relay.baseUrl) };
  await expect(openFileRequest(options)).rejects.toBeInstanceOf(LinkPasswordRequired);
  const request = await openFileRequest({ ...options, password: "drop here" });
  await request.submit("budget.pdf", new TextEncoder().encode("encrypted submission"));
  await request.submit("notes.txt", new TextEncoder().encode("second submission"));
  await expect(request.submit("over-cap.txt", new Uint8Array([1]))).rejects.toMatchObject({ status: 429 });

  const fresh = new DriveFiles(new DriveClient(alice.client.relay, alice.client.signer), fileKeys(alice.keys.signingPrivateKey, alice.keys.encryptionPrivateKey!));
  const uploaded = await fresh.list(await fresh.open(folder.manifest.node));
  expect(uploaded.map(file => file.name).sort()).toEqual(["budget.pdf", "notes.txt"]);
  const budget = uploaded.find(file => file.name === "budget.pdf")!;
  const chunks = []; for await (const chunk of fresh.read(budget)) chunks.push(chunk);
  expect(Buffer.concat(chunks).toString()).toBe("encrypted submission");
});
