import { DriveFiles, fileKeys, type OpenFile } from "../src/drive/files.js";
import { LinkPasswordRequired, openLink, parseLinkUrl } from "../src/drive/link.js";
import { openFileRequest } from "../src/drive/request.js";
import { openAttachment, parseAttachment, prepareAttachment } from "../src/drive/attachment.js";
import { createTransfer, expiredTransfers, revokeTransfer } from "../src/drive/transfer.js";
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
import { RelayClient } from "../src/http.js";
import { get as httpGet } from "node:http";
import { MessageHistory } from "../src/history.js";
import type { Decryptor } from "../src/crypto/keys.js";
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

it("reads history incrementally across two devices", async () => {
  const carol = await createTestIdentity(relay.baseUrl, "history");
  const requests: string[] = [];
  const device = (count: boolean) => {
    const transport = count
      ? new RelayClient(relay.baseUrl, { fetch: async (url, init) => { requests.push(`${init?.method ?? "GET"} ${new URL(String(url)).pathname}`); return fetch(url, init); } })
      : carol.client.relay;
    const files = new DriveFiles(new DriveClient(transport, carol.client.signer), fileKeys(carol.keys.signingPrivateKey, carol.keys.encryptionPrivateKey!));
    return new MessageHistory(carol.identity, (carol.client as unknown as { decryptor: Decryptor }).decryptor, files);
  };
  const phone = device(true), laptop = device(false);
  const record = (id: string, minute: number) => ({ id, sender: carol.identity, recipient: "dave.poweur.net", timestamp: `2026-09-28T12:0${minute}:00Z`, queue: "sent" as const, body: `body ${id}` });
  for (const [i, id] of ["a", "b", "c"].entries()) await phone.append(record(id, i));
  expect((await phone.load()).map(r => r.id)).toEqual(["a", "b", "c"]);
  // Nothing new: one listing, no record reads.
  requests.length = 0;
  expect((await phone.load()).length).toBe(3);
  expect(requests).toEqual([expect.stringMatching(/\/listing$/)]);
  // The laptop adds two; the phone reads only those, continuing the chain.
  await laptop.append(record("d", 3));
  await laptop.append(record("e", 4));
  requests.length = 0;
  expect((await phone.load()).map(r => r.id)).toEqual(["a", "b", "c", "d", "e"]);
  expect(requests.filter(r => r.endsWith("/records"))).toHaveLength(1);
  // The laptop's chain is behind the phone's now: its append is refused,
  // it re-reads, and the record still lands once.
  await phone.append(record("f", 5));
  await laptop.append(record("g", 6));
  expect((await laptop.load()).map(r => r.id)).toEqual(["a", "b", "c", "d", "e", "f", "g"]);
  expect((await phone.load()).map(r => r.id)).toEqual(["a", "b", "c", "d", "e", "f", "g"]);
  // Inline records, padded like chunks: no chunk reads at all.
  expect(requests.some(r => r.includes("/chunks/"))).toBe(false);
}, 60_000);

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

it("lists a folder in one request, without challenge round trips", async () => {
  const setup = new DriveFiles(new DriveClient(alice.client.relay, alice.client.signer), fileKeys(alice.keys.signingPrivateKey, alice.keys.encryptionPrivateKey!));
  const photos = await setup.create(await setup.create(await setup.root(), "albums", "folder"), "photos", "folder");
  await Promise.all(Array.from({ length: 12 }, (_, i) => setup.create(photos, `p${i}.jpg`, "file", new TextEncoder().encode(`photo ${i}`))));
  const requests: string[] = [];
  const counting = new RelayClient(relay.baseUrl, { fetch: async (url, init) => { requests.push(`${init?.method ?? "GET"} ${new URL(String(url)).pathname}`); return fetch(url, init); } });
  const files = new DriveFiles(new DriveClient(counting, alice.client.signer), fileKeys(alice.keys.signingPrivateKey, alice.keys.encryptionPrivateKey!));
  // Opening a folder two levels down: one request for it and its ancestors.
  const folder = await files.open(photos.manifest.node);
  expect(folder.name).toBe("photos");
  expect(requests.length).toBe(1);
  requests.length = 0;
  const listed = await files.list(folder);
  expect(listed.map(f => f.name).sort()).toEqual(Array.from({ length: 12 }, (_, i) => `p${i}.jpg`).sort());
  expect(requests).toEqual([`GET /drive/${alice.identity}/nodes/${photos.manifest.node}/listing`]);
  // Reading a file needs no further opens.
  requests.length = 0;
  const chunks = []; for await (const c of files.read(listed.find(f => f.name === "p3.jpg")!)) chunks.push(c);
  expect(Buffer.concat(chunks).toString()).toBe("photo 3");
  expect(requests.every(r => !r.includes("/auth/challenge"))).toBe(true);
  expect(requests.length).toBeLessThanOrEqual(2);
}, 60_000);

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
}, 30_000);

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

it("creates a bounded-memory multi-file transfer and revokes its bytes", async () => {
  const owner = new DriveFiles(new DriveClient(alice.client.relay, alice.client.signer), fileKeys(alice.keys.signingPrivateKey, alice.keys.encryptionPrivateKey!));
  const large = new Uint8Array(4 * 1024 * 1024 + 19).fill(73);
  const ranges: number[] = [];
  const checkpoints: string[] = [];
  const state = await createTransfer(owner, [
    { name: "large.bin", size: large.length, async slice(start, end) { ranges.push(end - start); return large.slice(start, end); } },
    { name: "note.txt", size: 5, async slice(start, end) { return new TextEncoder().encode("hello").slice(start, end); } },
  ], {
    origin: relay.baseUrl,
    password: "secret",
    maxDownloads: 2,
    message: "A private note",
    onState(next) { checkpoints.push(`${next.status}:${next.files.reduce((sum, file) => sum + file.offset, 0)}`); },
  });
  expect(state.status).toBe("ready");
  expect(ranges).toHaveLength(2);
  expect(Math.max(...ranges)).toBeLessThanOrEqual(4 * 1024 * 1024);
  expect(checkpoints.some(value => value.startsWith("uploading:"))).toBe(true);

  const url = new URL(state.url!);
  const opened = await openLink({ origin: relay.baseUrl, drive: alice.identity, link: state.share!.link!,
    fragment: fromBase64(url.hash.slice(1)), password: "secret", resolve: localResolveOptions(relay.baseUrl) });
  expect((await opened.files.list(opened.root)).map(file => file.name).sort()).toEqual(["Message.txt", "large.bin", "note.txt"]);

  const revoked = await revokeTransfer(owner, state);
  expect(revoked.status).toBe("revoked");
  await expect(openLink({ origin: relay.baseUrl, drive: alice.identity, link: state.share!.link!,
    fragment: fromBase64(url.hash.slice(1)), password: "secret", resolve: localResolveOptions(relay.baseUrl) })).rejects.toMatchObject({ status: 404 });
}, 120_000);

it("releases an expired transfer's files and link", async () => {
  const owner = new DriveFiles(new DriveClient(alice.client.relay, alice.client.signer), fileKeys(alice.keys.signingPrivateKey, alice.keys.encryptionPrivateKey!));
  const soon = new Date(Date.now() + 60_000).toISOString().replace(/\.\d{3}Z$/, "Z");
  const state = await createTransfer(owner, [{ name: "a.txt", size: 1, async slice() { return new Uint8Array([65]); } }], { origin: relay.baseUrl, expiresAt: soon });
  // Kept out of the owner's browsable tree.
  expect((await owner.list(await owner.root())).map(file => file.name)).not.toContain("shared");
  expect(expiredTransfers([state])).toEqual([]);
  const due = expiredTransfers([state], Date.parse(soon) + 1);
  expect(due).toHaveLength(1);
  const released = await revokeTransfer(owner, due[0]!, "expired");
  expect(released.status).toBe("expired");
  await expect(owner.open(state.folder!)).rejects.toBeTruthy();
  // Releasing again is a no-op.
  expect(await revokeTransfer(owner, released, "expired")).toBe(released);
});

it("sends an attachment only its recipient can open", async () => {
  const bob = await createTestIdentity(relay.baseUrl, "attach");
  const carol = await createTestIdentity(relay.baseUrl, "snoop");
  const resolve = localResolveOptions(relay.baseUrl);
  const sender = new DriveFiles(new DriveClient(alice.client.relay, alice.client.signer), fileKeys(alice.keys.signingPrivateKey, alice.keys.encryptionPrivateKey!));
  const bytes = new Uint8Array(4 * 1024 * 1024 + 99).map((_, i) => i % 251);
  const { body, metadata } = await prepareAttachment(sender, bob.identity, fromBase64((await resolveEncryptionKey(bob.identity, resolve))!),
    { name: "scan.pdf", mime: "application/pdf", bytes, caption: "the scan" });
  // The plaintext metadata names no file and carries no key.
  expect(Object.keys(metadata).sort()).toEqual(["hash", "node", "size"]);
  const attachment = parseAttachment(body);
  expect(attachment).toMatchObject({ name: "scan.pdf", mime: "application/pdf", caption: "the scan" });
  const authorKey = fromBase64((await resolveSigningKey(alice.identity, resolve))!);
  const asBob = new DriveClient(bob.client.relay, bob.client.signer, alice.identity);
  expect(Buffer.from(await openAttachment(asBob, attachment, metadata, authorKey)).equals(Buffer.from(bytes))).toBe(true);
  // The sender opens their own copy too.
  expect((await openAttachment(sender.client, attachment, metadata, authorKey)).length).toBe(bytes.length);
  // Nobody else can read it, and a message that lies about the file fails.
  await expect(openAttachment(new DriveClient(carol.client.relay, carol.client.signer, alice.identity), attachment, metadata, authorKey)).rejects.toBeTruthy();
  await expect(openAttachment(asBob, attachment, { ...metadata, hash: "0".repeat(64) }, authorKey)).rejects.toThrow("does not match");
  expect(() => parseAttachment(JSON.stringify({ ...attachment, name: "../x" }))).toThrow();
}, 60_000);

it("pages a large archive: newest per conversation first, then one request per older page", async () => {
  const dana = await createTestIdentity(relay.baseUrl, "archive");
  const decryptor = (dana.client as unknown as { decryptor: Decryptor }).decryptor;
  const keys = fileKeys(dana.keys.signingPrivateKey, dana.keys.encryptionPrivateKey!);
  const seeder = new MessageHistory(dana.identity, decryptor, new DriveFiles(new DriveClient(dana.client.relay, dana.client.signer), keys));
  const peers = ["erin.poweur.net", "fred.poweur.net"];
  const at = (i: number) => new Date(Date.UTC(2026, 8, 1) + i * 60_000).toISOString().replace(/\.\d{3}Z$/, "Z");
  for (const peer of peers) {
    for (let i = 0; i < 150; i++) await seeder.append({ id: `${peer}-${i}`, sender: dana.identity, recipient: peer, timestamp: at(i), queue: "sent", body: `${peer} ${i}` });
  }

  const requests: string[] = [];
  const counting = new RelayClient(relay.baseUrl, { fetch: async (url, init) => { requests.push(new URL(String(url)).pathname.split("/").slice(-1)[0]!); return fetch(url, init); } });
  const phone = new MessageHistory(dana.identity, decryptor, new DriveFiles(new DriveClient(counting, dana.client.signer), keys));
  const tray = await phone.load({ perConversation: 20 });
  expect(tray).toHaveLength(40);
  // One records request per conversation for the tray.
  expect(requests.filter(r => r === "records")).toHaveLength(2);
  expect(await phone.hasOlder(peers[0]!)).toBe(true);

  requests.length = 0;
  const page = await phone.older(peers[0]!, 50);
  expect(page.map(r => r.id)).toEqual(Array.from({ length: 50 }, (_, i) => `${peers[0]}-${80 + i}`));
  expect(requests).toEqual(["records"]);

  const older = await phone.before(peers[0]!, 81, { limit: 10 });
  expect(older.map(r => r.id)).toEqual(Array.from({ length: 10 }, (_, i) => `${peers[0]}-${70 + i}`));
  const whole = await phone.conversation(peers[0]!);
  expect(whole.map(r => r.id)).toEqual(Array.from({ length: 150 }, (_, i) => `${peers[0]}-${i}`));
  expect(await phone.hasOlder(peers[0]!)).toBe(false);

  // Appending never reads the log back: the relay has our chain position.
  requests.length = 0;
  await phone.append({ id: "new", sender: dana.identity, recipient: peers[1]!, timestamp: at(500), queue: "sent", body: "latest" });
  expect(requests.filter(r => r === "records")).toHaveLength(0);
  expect(requests).toContain("author-cursor");
  expect((await phone.tail(peers[1]!, { limit: 1 })).map(r => r.id)).toEqual(["new"]);
  // And the seeding device still reads it, continuing its own chain.
  expect((await seeder.load()).filter(r => r.recipient === peers[1]).at(-1)?.id).toBe("new");
}, 180_000);

it("publishes a public folder that anyone reads at /pub", async () => {
  const owner = new DriveFiles(new DriveClient(alice.client.relay, alice.client.signer), fileKeys(alice.keys.signingPrivateKey, alice.keys.encryptionPrivateKey!));
  const root = await owner.root();
  const site = await owner.createPublic(root, `site${Date.now().toString(36)}`, "folder");
  const page = await owner.create(site, "index.html", "file", new TextEncoder().encode("<p>v1</p>"));
  expect(page.public).toBe(true);
  // The relay routes /pub by Host: ask as the identity's own host (fetch
  // drops a custom Host header, node:http keeps it).
  const pub = (path: string) => new Promise<{ status: number; body: string; csp: string | undefined }>((resolve, reject) => {
    const url = new URL(`${relay.baseUrl}/pub/${path}`);
    httpGet({ host: url.hostname, port: url.port, path: url.pathname, headers: { Host: alice.identity } }, (response) => {
      const parts: Buffer[] = [];
      response.on("data", (part: Buffer) => parts.push(part));
      response.on("end", () => resolve({ status: response.statusCode ?? 0, body: Buffer.concat(parts).toString(), csp: response.headers["content-security-policy"] as string | undefined }));
    }).on("error", reject);
  });
  expect(await pub(`${site.name}/index.html`)).toMatchObject({ status: 200, body: "<p>v1</p>" });
  await owner.replace(page, new TextEncoder().encode("<p>v2</p>"));
  expect((await pub(`${site.name}/index.html`)).body).toBe("<p>v2</p>");
  // Another client of the owner sees it as public, by its plaintext name.
  const fresh = new DriveFiles(new DriveClient(alice.client.relay, alice.client.signer), fileKeys(alice.keys.signingPrivateKey, alice.keys.encryptionPrivateKey!));
  const listed = (await fresh.list(await fresh.root())).find(file => file.manifest.node === site.manifest.node);
  expect(listed).toMatchObject({ name: site.name, public: true });
  const text = []; for await (const chunk of fresh.read((await fresh.list(listed!)).find(f => f.name === "index.html")!)) text.push(chunk);
  expect(Buffer.concat(text).toString()).toBe("<p>v2</p>");
  // Publishing is explicit: a private file cannot slip into a public folder.
  const secret = await owner.create(root, `secret${Date.now().toString(36)}.txt`, "file", new TextEncoder().encode("private"));
  await expect(owner.move(secret, site, "secret.txt")).rejects.toThrow("publishing needs a copy");
  await expect(owner.createPublic(site, "nested", "folder")).resolves.toMatchObject({ public: true });
});
