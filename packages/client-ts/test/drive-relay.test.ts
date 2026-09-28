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
