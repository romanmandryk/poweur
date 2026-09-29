/** User actions behind the storage-v2 Files destination (E20-T10). */
import { fromBase64 } from "@poweur/client";
import {
  OFFER_FORMAT,
  acceptOffer,
  shareHash,
  validateShareOffer,
  type DriveFiles,
  type Mount,
  type Mounts,
  type OpenFile,
  type Share,
  type ShareAccept,
  type ShareOffer,
  type ShareRole,
} from "@poweur/client/drive";
import { clientFor, lookup } from "../lib/client.js";
import { openBrowserDrive, readFileBytes } from "../lib/drive";
import { relayUrlFor } from "../lib/storage.js";

const encoder = new TextEncoder();
const decoder = new TextDecoder();
const stamp = (date = new Date()) => date.toISOString().replace(/\.\d{3}Z$/, "Z");

async function privateFolder(files: DriveFiles): Promise<OpenFile> {
  let current = await files.root();
  for (const name of [".poweur", "private"]) {
    const found = (await files.list(current)).find((file) => file.name === name && file.manifest.kind === "folder");
    current = found ?? await files.create(current, name, "folder");
  }
  return current;
}

async function mountsDocument(files: DriveFiles): Promise<{ mounts: Mounts; folder: OpenFile; file?: OpenFile }> {
  const folder = await privateFolder(files);
  const file = (await files.list(folder)).find((entry) => entry.name === "mounts.json");
  if (!file) return { mounts: { format: 1, mounts: [] }, folder };
  const parsed = JSON.parse(decoder.decode(await readFileBytes(files, file))) as Mounts;
  return { mounts: { format: 1, mounts: Array.isArray(parsed.mounts) ? parsed.mounts : [] }, folder, file };
}

export async function loadMounts(identity: string): Promise<Mount[]> {
  const { files } = await openBrowserDrive(identity);
  return (await mountsDocument(files)).mounts.mounts;
}

export function shareOffers(messages: any[], identity: string): ShareOffer[] {
  const offers: ShareOffer[] = [];
  for (const message of messages) {
    if (message.type !== "sys.share.offer" || message.recipient?.toLowerCase() !== identity.toLowerCase()) continue;
    try {
      const offer = JSON.parse(message.plaintext ?? message.body ?? "") as ShareOffer;
      validateShareOffer(offer);
      if (offer.share.member?.toLowerCase() === identity.toLowerCase()) offers.push(offer);
    } catch {
      // Invalid offers grant nothing and stay out of the Files UI.
    }
  }
  return offers;
}

/** Verify an offer against the source relay, persist its mount encrypted, and acknowledge it. */
export async function acceptBrowserOffer(identity: string, offer: ShareOffer): Promise<Mount> {
  validateShareOffer(offer);
  if (offer.share.member?.toLowerCase() !== identity.toLowerCase()) throw new Error("this share offer is for another identity");
  const shared = await openBrowserDrive(identity, offer.share.drive, offer.relay);
  const held = (await shared.drive.shares()).shares.find((share) => share.id === offer.share.id);
  if (!held || shareHash(held) !== shareHash(offer.share)) throw new Error("the offer no longer matches the drive's share");
  await shared.files.open(offer.share.node);

  const own = await openBrowserDrive(identity);
  const { mounts, folder, file } = await mountsDocument(own.files);
  const accepted: ShareAccept = acceptOffer(mounts, offer);
  const bytes = encoder.encode(JSON.stringify(mounts, null, 2));
  if (file) await own.files.replace(file, bytes);
  else await own.files.create(folder, "mounts.json", "file", bytes);

  const client = clientFor(identity);
  if (!client) throw new Error("unlock this identity to accept the offer");
  await client.sendAndArchive(offer.share.issuer, JSON.stringify(accepted), {
    type: "sys.share.accept",
    metadata: { share_id: offer.share.id },
  }).catch(() => {});
  return mounts.mounts.at(-1)!;
}

/** Create a direct share and notify the recipient with the canonical offer. */
export async function shareBrowserFile(identity: string, file: OpenFile, member: string, role: ShareRole): Promise<{ share: Share; notified: boolean }> {
  const target = member.trim().toLowerCase();
  const resolved = await lookup(target, relayUrlFor(identity));
  const published = resolved.document.encryption_public_key;
  if (!published) throw new Error(`${target} has no published encryption key`);
  const key = fromBase64(published.replace(/^x25519:/, ""));
  if (key.length !== 32) throw new Error(`${target} has no valid encryption key`);

  const { drive, files } = await openBrowserDrive(identity);
  const share = await files.shareWith(file, target, key, role);
  const offer: ShareOffer = {
    format: OFFER_FORMAT,
    share,
    relay: new URL(drive.relay.relayUrl).host,
    kind: file.manifest.kind,
    ...(file.name ? { name: file.name } : {}),
    offered_at: stamp(),
  };
  validateShareOffer(offer);
  const client = clientFor(identity);
  if (!client) throw new Error("unlock this identity to share files");
  try {
    await client.sendAndArchive(target, JSON.stringify(offer), {
      type: "sys.share.offer",
      metadata: { share_id: share.id },
      expiresAt: stamp(new Date(Date.now() + 7 * 24 * 3600 * 1000)),
    });
    return { share, notified: true };
  } catch {
    // The grant already exists and must not be described as a failed share.
    return { share, notified: false };
  }
}

export async function sharesForFile(identity: string, file: OpenFile): Promise<Share[]> {
  const { drive } = await openBrowserDrive(identity);
  return (await drive.shares()).shares.filter((share) => share.node === file.manifest.node && Boolean(share.member));
}

export async function revokeBrowserShare(identity: string, share: Share): Promise<void> {
  const { drive } = await openBrowserDrive(identity);
  await drive.unshare(share.id);
  if (!share.member) return;
  const client = clientFor(identity);
  if (!client) return;
  await client.sendAndArchive(share.member, JSON.stringify({
    format: OFFER_FORMAT,
    drive: share.drive,
    share_id: share.id,
    revoked_at: stamp(),
  }), { type: "sys.share.revoked", metadata: { share_id: share.id } }).catch(() => {});
}
