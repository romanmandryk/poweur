/** Anonymous, create-only uploads through a storage-v2 file-request link. */
import { fromBase64, randomBytes, toBase64url } from "../encoding.js";
import { generateSigningKeypair, x25519PublicKey, type SealedPayload } from "../crypto/index.js";
import { LocalSigner } from "../crypto/keys.js";
import { RelayClient } from "../http.js";
import { solvePow } from "../pow.js";
import { resolveSigningKey, type ResolveOptions } from "../resolve.js";
import { DriveClient } from "./client.js";
import { driveContext, encryptChunk, MAX_PLAINTEXT, sealKey } from "./crypto.js";
import { LinkPasswordRequired, type LinkInfo } from "./link.js";
import { signManifest, splitPages, type Manifest } from "./manifest.js";
import { normalizeName, sealName } from "./names.js";
import { guestAuthor, verifyShare, type Share } from "./share.js";
import { argon2id } from "@noble/hashes/argon2.js";
import { utf8 } from "../encoding.js";
import { linkSecret } from "./files.js";

const hex = (bytes: Uint8Array) => Array.from(bytes, byte => byte.toString(16).padStart(2, "0")).join("");
const id = (bytes = 16) => hex(randomBytes(bytes));
const wire = (value: SealedPayload) => ({ ephemeral_public_key: value.ephemeralPublicKey, nonce: value.nonce, ciphertext: value.ciphertext });

export interface FileRequestOpenOptions {
  origin: string;
  drive: string;
  link: string;
  fragment: Uint8Array;
  password?: string;
  resolve?: ResolveOptions;
  fetch?: typeof fetch;
}

export interface FileRequestSubmission {
  node: string;
  name: string;
  size: number;
}

export class OpenedFileRequest {
  constructor(readonly info: LinkInfo, readonly share: Share, private readonly client: DriveClient, private readonly signingKey: Uint8Array) {}

  /** Upload one encrypted file. A create-only holder cannot list or read it back. */
  async submit(name: string, bytes: Uint8Array, onPowProgress?: (attempts: number) => void): Promise<FileRequestSubmission> {
    name = normalizeName(name);
    const node = id(), nodeKey = randomBytes(32), contentKey = randomBytes(32);
    const manifest: Manifest = {
      format: 1, drive: this.share.drive, node, version: id(), parent: "", operation: "create",
      author: this.client.signer.identity, generation: 1, kind: "file", mode: "replace", folder: this.share.node,
      name_hash: id(32), count: 0, pages: [], signature: "",
    };
    const folderPublic = fromBase64(this.share.node_public);
    manifest.name = wire(sealName(folderPublic, name, driveContext(manifest.drive, node, "name", 1)));
    manifest.node_key = wire(sealKey(folderPublic, nodeKey, driveContext(manifest.drive, node, "node-key", 1)));
    manifest.content_key = wire(sealKey(x25519PublicKey(nodeKey), contentKey, driveContext(manifest.drive, node, "content-key", 1)));

    const encrypted: Uint8Array[] = [];
    for (let offset = 0; offset < bytes.length; offset += MAX_PLAINTEXT) {
      encrypted.push(encryptChunk(contentKey, bytes.subarray(offset, offset + MAX_PLAINTEXT), driveContext(manifest.drive, node, "content", 1)));
    }
    const refs = await this.client.store(encrypted);
    const { pages, hashes } = splitPages(manifest.drive, node, refs);
    manifest.count = refs.length;
    manifest.pages = hashes;
    const signed = signManifest(manifest, this.signingKey);

    let headers: Record<string, string> | undefined;
    if ((this.info.pow ?? 0) > 0) {
      const challenge = await this.client.relay.request<{ token: string; bits: number }>({
        method: "GET",
        path: `/auth/pow?purpose=drive-link&identity=${encodeURIComponent(this.share.drive)}&link=${encodeURIComponent(this.share.link!)}`,
      });
      headers = {
        "X-Poweur-PoW-Token": challenge.token,
        "X-Poweur-PoW-Solution": await solvePow(challenge.token, challenge.bits, { onProgress: onPowProgress }),
      };
    }
    await this.client.commit({ manifest: signed, pages }, headers);
    return { node, name, size: bytes.length };
  }
}

/** Open and verify a create-only link without granting the guest read/list access. */
export async function openFileRequest(options: FileRequestOpenOptions): Promise<OpenedFileRequest> {
  if (options.fragment.length !== 32) throw new Error("file request fragment must be 32 bytes");
  const relay = new RelayClient(options.origin, options.fetch ? { fetch: options.fetch } : {});
  const info = await relay.request<LinkInfo>({ method: "GET", path: `/drive/${encodeURIComponent(options.drive)}/links/${encodeURIComponent(options.link)}` });
  if (info.role !== "create") throw new Error("this is not a file request");
  let verifier: Uint8Array | undefined;
  if (info.password) {
    if (!options.password) throw new LinkPasswordRequired();
    if (!info.salt) throw new Error("file request has no password salt");
    const derived = argon2id(utf8(options.password), fromBase64(info.salt), { t: 3, m: 65536, p: 1, dkLen: 64 });
    // Derive the same key half even though create-only shares carry no node key.
    linkSecret(options.fragment, derived.subarray(0, 32));
    verifier = derived.subarray(32);
  }
  const pair = generateSigningKeypair();
  const signer = new LocalSigner(guestAuthor(pair.publicKey), pair.privateKey);
  const client = new DriveClient(relay, signer, info.drive);
  client.link = { id: options.link, ...(verifier ? { verifier } : {}) };
  const share = (await client.shares()).shares.find(candidate => candidate.link === options.link && candidate.role === "create");
  if (!share || share.drive !== info.drive || share.issuer !== info.drive) throw new Error("invalid file request share");
  const key = await resolveSigningKey(share.issuer, options.resolve ?? {});
  if (!key) throw new Error(`cannot verify ${share.issuer}`);
  verifyShare(share, fromBase64(key));
  return new OpenedFileRequest(info, share, client, pair.privateKey);
}
