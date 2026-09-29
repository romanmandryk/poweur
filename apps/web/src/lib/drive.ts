/** Browser key-custody and relay plumbing for storage-v2 drives. */
import { RelayClient, fromBase64 } from "@poweur/client";
import { IndexedDBChunkCache } from "@poweur/client/browser";
import { DriveClient, DriveFiles, type OpenFile } from "@poweur/client/drive";
import { clientFor, lookup, signerFor } from "./client.js";
import { relayUrlFor } from "./storage.js";

export interface OpenBrowserDrive {
  drive: DriveClient;
  files: DriveFiles;
}

function relayAddress(home: string, advertised?: string): string {
  if (!advertised) return relayUrlFor(home);
  if (/^https?:\/\//.test(advertised)) return advertised.replace(/\/+$/, "");
  const scheme = relayUrlFor(home).startsWith("http://") ? "http" : "https";
  return `${scheme}://${advertised.replace(/\/+$/, "")}`;
}

/** Open our own drive, or another identity's drive using a share held by us. */
export async function openBrowserDrive(home: string, target = home, advertisedRelay?: string): Promise<OpenBrowserDrive> {
  const client = clientFor(home);
  const keys = signerFor(home);
  if (!client || !keys?.decryptor || !keys.signer.signBytes) throw new Error("unlock this identity to open files");
  const encryptionPrivateKey = await keys.decryptor.privateKeyBytes();
  const relay = target === home && !advertisedRelay
    ? client.relay
    : new RelayClient(relayAddress(home, advertisedRelay));
  const drive = new DriveClient(relay, keys.signer, target, new IndexedDBChunkCache());
  const files = new DriveFiles(drive, {
    sign: keys.signer.signBytes.bind(keys.signer),
    encryptionPrivateKey,
    async authorKey(author) {
      const resolved = await lookup(author, relayUrlFor(home));
      return fromBase64(resolved.document.public_key.replace(/^ed25519:/, ""));
    },
    async encryptionKey(member) {
      const resolved = await lookup(member, relayUrlFor(home));
      const key = resolved.document.encryption_public_key;
      if (!key) throw new Error(`${member} has no encryption key`);
      return fromBase64(key.replace(/^x25519:/, ""));
    },
    // Admins write through group shares too, so they count as members.
    async groupMembers(group) {
      const { document } = await client.groups.roster(client.signer, group);
      return [...document.members, ...(document.admins ?? [])];
    },
  });
  return { drive, files };
}

export async function readFileBytes(files: DriveFiles, file: OpenFile): Promise<Uint8Array> {
  const chunks: Uint8Array[] = [];
  let length = 0;
  for await (const chunk of files.read(file)) {
    chunks.push(chunk);
    length += chunk.length;
  }
  const result = new Uint8Array(length);
  let offset = 0;
  for (const chunk of chunks) {
    result.set(chunk, offset);
    offset += chunk.length;
  }
  return result;
}
