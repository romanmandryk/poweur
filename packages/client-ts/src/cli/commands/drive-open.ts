/**
 * Opens a drive for the CLI as the caller: their own, or with `--drive`
 * another identity's drive on that drive's relay, reached through the
 * caller's shares. Only the caller's own private keys are ever loaded;
 * collaborators' signing keys come from their public identity documents.
 */
import { FileChunkCache } from "../../node/drive-cache.js";
import { DriveClient } from "../../drive/client.js";
import { DriveFiles, fileKeys } from "../../drive/files.js";
import { fromBase64 } from "../../encoding.js";
import { RelayClient } from "../../http.js";
import { resolveRecipientRelayUrl } from "../../messages.js";
import { resolveSigningKey } from "../../resolve.js";
import { nodeResolveOptions, openClient } from "../../node/session-factory.js";

export async function openDrive(options: { identity?: string; drive?: string }) {
  const { client, keys } = await openClient({ identity: options.identity });
  const own = client.signer.identity;
  const target = options.drive?.trim().toLowerCase() || own;
  let relay = client.relay;
  if (target !== own) {
    const scheme = client.relay.relayUrl.startsWith("http://") ? "http" : "https";
    relay = new RelayClient(await resolveRecipientRelayUrl(target, scheme, nodeResolveOptions()));
  }
  const drive = new DriveClient(relay, client.signer, target, new FileChunkCache());
  const files = keys.encryptionPrivateKey
    ? new DriveFiles(drive, {
      ...fileKeys(keys.signingPrivateKey, keys.encryptionPrivateKey),
      async authorKey(author: string) {
        const key = await resolveSigningKey(author, nodeResolveOptions());
        if (!key) throw new Error(`cannot resolve ${author}'s signing key`);
        return fromBase64(key);
      },
    })
    : undefined;
  return { client, keys, drive, files };
}
