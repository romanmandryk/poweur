/** Attachment upload/grant/reference/download flow (EPIC-009 E09-T4). */

import { normalizeGrantPath } from "./canonical.js";
import { sha256Bytes } from "./crypto/index.js";
import type { Signer } from "./crypto/keys.js";
import { PoweurError } from "./errors.js";
import { DavClient } from "./files.js";
import { RelayClient } from "./http.js";
import { newShareId } from "./ids.js";
import { resolveRecipientRelayUrl } from "./messages.js";
import { validateIdentityName } from "./names.js";
import type { ResolveOptions } from "./resolve.js";
import { Shares } from "./shares.js";
import { SyncClient } from "./sync.js";

export const MSG_TYPE_CHAT_ATTACHMENT = "chat.attachment";
export const MAX_ATTACHMENT_BYTES = 20 * 1024 * 1024;

function toHex(bytes: Uint8Array): string {
  return [...bytes].map(byte => byte.toString(16).padStart(2, "0")).join("");
}

export interface AttachmentRef {
  owner: string; path: string; name: string; size: number; mime: string; sha256: string; shareId: string;
}

export function attachmentMetadata(ref: AttachmentRef): Record<string, string> {
  return {
    attachment_owner: ref.owner, attachment_path: ref.path, attachment_name: ref.name,
    attachment_size: String(ref.size), attachment_mime: ref.mime,
    attachment_sha256: ref.sha256, attachment_share_id: ref.shareId,
  };
}

export function parseAttachmentMetadata(metadata: Record<string, string> = {}): AttachmentRef {
  const size = Number(metadata.attachment_size);
  const ref = { owner: metadata.attachment_owner ?? "", path: metadata.attachment_path ?? "",
    name: metadata.attachment_name ?? "", size, mime: metadata.attachment_mime ?? "",
    sha256: (metadata.attachment_sha256 ?? "").toLowerCase(), shareId: metadata.attachment_share_id ?? "" };
  let normalizedPath = "";
  try {
    validateIdentityName(ref.owner);
    normalizedPath = normalizeGrantPath(ref.path);
  } catch {
    throw new PoweurError("invalid_document", "invalid chat.attachment metadata");
  }
  if (normalizedPath !== ref.path || !ref.path.startsWith("shared/.attachments/") ||
      !ref.name || /[/\\\r\n]/.test(ref.name) || !Number.isInteger(size) || size < 0 ||
      ref.path.split("/").at(-1) !== ref.name || size > MAX_ATTACHMENT_BYTES ||
      !ref.mime || ref.mime.length > 128 || /[\r\n]/.test(ref.mime) ||
      !/^[0-9a-f]{64}$/.test(ref.sha256) || !ref.shareId || /[/\\\r\n]/.test(ref.shareId)) {
    throw new PoweurError("invalid_document", "invalid chat.attachment metadata");
  }
  return ref;
}

export async function uploadAttachment(dav: DavClient, signer: Signer, recipient: string,
  bytes: Uint8Array, options: { name: string; mime?: string }): Promise<AttachmentRef> {
  if (bytes.byteLength > MAX_ATTACHMENT_BYTES) throw new PoweurError("invalid_argument", "attachment exceeds 20 MB");
  const name = options.name.trim();
  if (!name || /[/\\\r\n]/.test(name)) throw new PoweurError("invalid_argument", "attachment name must be a file name");
  const shareId = newShareId();
  const attachmentId = shareId.replace(/^shr_/, "att_");
  const path = `shared/.attachments/${attachmentId}/${name}`;
  for (const directory of ["shared/.attachments", `shared/.attachments/${attachmentId}`]) {
    await dav.mkdir(directory).catch(() => {});
  }
  await dav.write(path, bytes);
  const shares = new Shares(dav, new SyncClient(dav.client, dav.identity, dav.token));
  try {
    await shares.add(signer, path, { with: [recipient], permissions: "read", shareId });
  } catch (error) {
    await dav.remove(path).catch(() => {});
    throw error;
  }
  return { owner: signer.identity, path, name, size: bytes.byteLength,
    mime: options.mime || "application/octet-stream", sha256: toHex(sha256Bytes(bytes)), shareId };
}

export async function downloadAttachment(signer: Signer, metadata: Record<string, string>,
  resolve: ResolveOptions = {}): Promise<{ ref: AttachmentRef; bytes: Uint8Array }> {
  const ref = parseAttachmentMetadata(metadata);
  const scheme = resolve.scheme === "http" ? "http" : "https";
  const relayUrl = await resolveRecipientRelayUrl(ref.owner, scheme, resolve);
  const relay = new RelayClient(relayUrl, { ...(resolve.fetch ? { fetch: resolve.fetch } : {}) });
  const dav = await DavClient.connect(relay, signer, { audience: ref.owner, scope: "dav:read" });
  const bytes = await dav.readBytes(ref.path);
  if (bytes.byteLength !== ref.size || toHex(sha256Bytes(bytes)) !== ref.sha256) {
    throw new PoweurError("invalid_document", "attachment size or SHA-256 does not match the signed message reference");
  }
  return { ref, bytes };
}

/** Sender-owned garbage collection: revoke the grant, then delete the file. */
export async function removeAttachment(dav: DavClient, ref: AttachmentRef): Promise<void> {
  const shares = new Shares(dav, new SyncClient(dav.client, dav.identity, dav.token));
  await shares.revoke(ref.shareId);
  await dav.remove(ref.path);
}
