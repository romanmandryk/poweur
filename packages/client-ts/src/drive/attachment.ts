/**
 * Message attachments on the drive (EPIC-020 E20-T11), the twin of
 * `packages/identity/attachment.go` and the Go CLI's `send --attach`.
 *
 * The file is sealed onto the sender's drive under `.poweur/private/attachments/`
 * and shared read-only with the recipient. The encrypted message carries the
 * content key, file name and MIME type; the plaintext envelope metadata keeps
 * only the node id, ciphertext size and ciphertext hash.
 */
import { sha256Bytes } from "../crypto/index.js";
import { fromBase64, randomBytes, toBase64url } from "../encoding.js";
import { RelayError } from "../errors.js";
import type { DriveClient } from "./client.js";
import { decryptChunk, driveContext } from "./crypto.js";
import type { DriveFiles, OpenFile } from "./files.js";
import { verifyManifest, verifyManifestPages, type ChunkPage } from "./manifest.js";
import type { ChunkRef } from "./records.js";

export const ATTACHMENT_FORMAT = 1;
/** The largest file a client seals into one message. */
export const MAX_ATTACHMENT_BYTES = 20 * 1024 * 1024;

/** The decrypted `chat.attachment` payload. */
export interface Attachment {
  format: number;
  name: string;
  mime: string;
  content_key: string;
  caption?: string;
}

const hex = (bytes: Uint8Array) => Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");

export function validateAttachment(a: Attachment): void {
  if (a.format !== ATTACHMENT_FORMAT) throw new Error(`unsupported attachment format ${a.format}`);
  if (!a.name || new TextEncoder().encode(a.name).length > 255 || /[/\\\0]/.test(a.name) || a.name === "." || a.name === "..") throw new Error("invalid attachment name");
  if (!a.mime || a.mime.length > 127 || /[\r\n]/.test(a.mime)) throw new Error("invalid attachment type");
  let key: Uint8Array;
  try { key = fromBase64(a.content_key); } catch { throw new Error("invalid attachment content key"); }
  if (key.length !== 32 || toBase64url(key) !== a.content_key) throw new Error("invalid attachment content key");
}

/** Decode a decrypted `chat.attachment` body. */
export function parseAttachment(raw: string): Attachment {
  let parsed: Attachment;
  try { parsed = JSON.parse(raw) as Attachment; } catch (cause) { throw new Error("invalid attachment", { cause }); }
  validateAttachment(parsed);
  return parsed;
}

/** Plaintext envelope metadata: node id, ciphertext size and hash only. */
export function attachmentMetadata(node: string, size: number, hash: string): Record<string, string> {
  return { node, size: String(size), hash };
}

async function folder(files: DriveFiles, parent: OpenFile, name: string): Promise<OpenFile> {
  const find = async () => (await files.list(parent)).find((child) => child.name === name && child.manifest.kind === "folder");
  const found = await find();
  if (found) return found;
  try { return await files.create(parent, name, "folder"); }
  catch (error) {
    // Another device created it first.
    const again = error instanceof RelayError && error.status === 409 ? await find() : undefined;
    if (!again) throw error;
    return again;
  }
}

async function ciphertext(client: DriveClient, node: string, version: string, refs: ChunkRef[]): Promise<Uint8Array[]> {
  return Promise.all(refs.map((ref) => client.chunk(node, ref, version)));
}

async function pageRefs(client: DriveClient, node: string, version: string, hashes: string[], verify: (pages: ChunkPage[]) => ChunkRef[]): Promise<ChunkRef[]> {
  return verify(await Promise.all(hashes.map((hash) => client.page(node, version, hash))));
}

function digest(blobs: Uint8Array[]): { hash: string; size: number } {
  const size = blobs.reduce((sum, blob) => sum + blob.length, 0);
  const all = new Uint8Array(size);
  let offset = 0;
  for (const blob of blobs) { all.set(blob, offset); offset += blob.length; }
  return { hash: hex(sha256Bytes(all)), size };
}

/**
 * Seal a file onto the sender's drive, share it read-only with the recipient,
 * and return the message body (to encrypt to them) and the plaintext metadata.
 */
export async function prepareAttachment(files: DriveFiles, recipient: string, recipientPublic: Uint8Array,
  file: { name: string; mime: string; bytes: Uint8Array; caption?: string }): Promise<{ body: string; metadata: Record<string, string> }> {
  if (file.bytes.length > MAX_ATTACHMENT_BYTES) throw new Error(`attachments are limited to ${MAX_ATTACHMENT_BYTES / 1024 / 1024} MB`);
  const draft: Attachment = { format: ATTACHMENT_FORMAT, name: file.name, mime: file.mime || "application/octet-stream", content_key: toBase64url(new Uint8Array(32)), ...(file.caption ? { caption: file.caption } : {}) };
  validateAttachment(draft);
  let dir = await files.root();
  for (const name of [".poweur", "private", "attachments"]) dir = await folder(files, dir, name);
  const stored = await files.create(dir, hex(randomBytes(16)), "file", file.bytes);
  await files.shareWith(stored, recipient, recipientPublic, "read");
  const m = stored.manifest;
  const refs = await pageRefs(files.client, m.node, m.version, m.pages, (pages) => verifyManifestPages(m, pages));
  const { hash, size } = digest(await ciphertext(files.client, m.node, m.version, refs));
  const body: Attachment = { ...draft, content_key: toBase64url(stored.contentKey!) };
  validateAttachment(body);
  return { body: JSON.stringify(body), metadata: attachmentMetadata(m.node, size, hash) };
}

/**
 * Download and decrypt an attachment from the sender's drive. `reader` is a
 * client for that drive authenticated as the recipient (or the sender
 * themselves); `authorKey` is the sender's signing key. The ciphertext must
 * match the size and hash the message carried.
 */
export async function openAttachment(reader: DriveClient, attachment: Attachment, metadata: Record<string, string>, authorKey: Uint8Array): Promise<Uint8Array> {
  validateAttachment(attachment);
  const node = metadata.node, wantSize = Number(metadata.size), wantHash = metadata.hash;
  if (!node || !Number.isSafeInteger(wantSize) || !wantHash) throw new Error("attachment metadata is incomplete");
  const info = await reader.node(node);
  if (info.id !== node || info.removed || !info.head) throw new Error("the attachment is no longer available");
  const m = await reader.version(node, info.head);
  if (m.node !== node || m.version !== info.head || m.kind !== "file" || m.mode !== "replace") throw new Error("attachment manifest mismatch");
  verifyManifest(m, authorKey);
  const refs = await pageRefs(reader, node, m.version, m.pages, (pages) => verifyManifestPages(m, pages));
  const blobs = await ciphertext(reader, node, m.version, refs);
  const { hash, size } = digest(blobs);
  if (hash !== wantHash || size !== wantSize) throw new Error("the attachment does not match the message");
  const key = fromBase64(attachment.content_key);
  const context = driveContext(m.drive, m.node, "content", m.generation);
  const parts = blobs.map((blob) => decryptChunk(key, blob, context));
  const out = new Uint8Array(parts.reduce((sum, part) => sum + part.length, 0));
  let offset = 0;
  for (const part of parts) { out.set(part, offset); offset += part.length; }
  return out;
}
