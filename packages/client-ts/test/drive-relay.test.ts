import { DriveFiles, fileKeys, type OpenFile } from "../src/drive/files.js";
import { DriveLog } from "../src/drive/log.js";
import { DriveScope } from "../src/drive/scope.js";
import { SystemFiles, DeviceRegistry } from "../src/systemfiles.js";
import { sessionSigner } from "../src/session.js";
import { afterAll, beforeAll, expect, it } from "vitest";
import { DriveClient } from "../src/drive/client.js";
import { driveContext, sealKey } from "../src/drive/crypto.js";
import { signManifest, type Manifest } from "../src/drive/manifest.js";
import { x25519PublicKey } from "../src/crypto/index.js";
import { createTestIdentity, type TestIdentity } from "./helpers/identities.js";
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
