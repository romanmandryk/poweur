/**
 * E26-T1, TypeScript side: SDK actors on two relays. Alice (relay A) shares a
 * folder with Bob (relay B); Bob edits through his share on Alice's relay with
 * only his own keys; Alice reads it back; relay A's store holds none of it.
 */
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { afterAll, beforeAll, expect, it } from "vitest";
import { signerFor } from "../src/crypto/keys.js";
import { DriveClient } from "../src/drive/client.js";
import { DriveFiles, fileKeys } from "../src/drive/files.js";
import { fromBase64 } from "../src/encoding.js";
import { RelayClient } from "../src/http.js";
import { createIdentity, IdentityApi } from "../src/identity.js";
import { resolveEncryptionKey, resolveSigningKey, type ResolveOptions } from "../src/resolve.js";
import { uniqueIdentity } from "./helpers/identities.js";
import { startRelays, type RelayNetwork, type RunningRelay } from "./helpers/relay.js";

let net: RelayNetwork;
beforeAll(async () => { net = await startRelays(2); }, 240_000);
afterAll(() => net?.stop());

async function actor(prefix: string, relay: RunningRelay) {
  const identity = uniqueIdentity(prefix);
  const created = await createIdentity(new IdentityApi(new RelayClient(relay.baseUrl)), identity, { hosted: true });
  net.host(identity, relay);
  return { identity, keys: created.keys, signer: signerFor(created.keys).signer, relay };
}

/** Every stored object under a relay's data directory, as bytes. */
function storedObjects(dir: string): Buffer[] {
  const out: Buffer[] = [];
  for (const name of readdirSync(dir)) {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) out.push(...storedObjects(path));
    else out.push(readFileSync(path));
  }
  return out;
}

it("a member on another relay edits through their share; the host store stays ciphertext", async () => {
  const [relayA, relayB] = net.relays as [RunningRelay, RunningRelay];
  const alice = await actor("tsalice", relayA);
  const bob = await actor("tsbob", relayB);
  const resolve: ResolveOptions = { scheme: "http", allowPrivate: true, skipDns: true, fetch: net.fetch };
  const publicKeys = {
    async authorKey(author: string) {
      const key = await resolveSigningKey(author, resolve);
      if (!key) throw new Error(`cannot resolve ${author}`);
      return fromBase64(key);
    },
  };
  const folderName = "ts-harness-folder-8e2b", first = "ts first draft c41f", edited = "ts edit by bob 07aa";

  const owner = new DriveFiles(new DriveClient(new RelayClient(relayA.baseUrl), alice.signer), { ...fileKeys(alice.keys.signingPrivateKey, alice.keys.encryptionPrivateKey!), ...publicKeys });
  const folder = await owner.create(await owner.root(), folderName, "folder");
  await owner.create(folder, "note.txt", "file", new TextEncoder().encode(first));
  const bobEncryption = fromBase64((await resolveEncryptionKey(bob.identity, resolve))!);
  await owner.shareWith(folder, bob.identity, bobEncryption, "write");

  // Bob talks to Alice's relay, which resolves him on relay B to check his signature.
  const member = new DriveFiles(new DriveClient(new RelayClient(relayA.baseUrl), bob.signer, alice.identity), { ...fileKeys(bob.keys.signingPrivateKey, bob.keys.encryptionPrivateKey!), ...publicKeys });
  const text = async (files: DriveFiles, path: string) => {
    const chunks: Uint8Array[] = [];
    for await (const chunk of files.read(await files.resolve(path))) chunks.push(chunk);
    return Buffer.concat(chunks).toString();
  };
  expect(await text(member, `/${folder.manifest.node}/note.txt`)).toBe(first);
  await member.replace(await member.resolve(`/${folder.manifest.node}/note.txt`), new TextEncoder().encode(edited));

  const fresh = new DriveFiles(new DriveClient(new RelayClient(relayA.baseUrl), alice.signer), { ...fileKeys(alice.keys.signingPrivateKey, alice.keys.encryptionPrivateKey!), ...publicKeys });
  expect(await text(fresh, `/${folderName}/note.txt`)).toBe(edited);

  for (const relay of [relayA, relayB]) {
    const objects = storedObjects(relay.dataDir);
    expect(objects.length).toBeGreaterThan(0);
    for (const secret of [folderName, first, edited]) {
      expect(objects.some((data) => data.includes(secret))).toBe(false);
    }
  }
}, 120_000);
