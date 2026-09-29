/**
 * Share offers for the TS CLI (PCP-0008), matching the Go CLI: sharing sends
 * sys.share.offer, `drive accept <offer.json|->` mounts it in the caller's own
 * encrypted drive (.poweur/private/mounts.json) and answers
 * sys.share.accept, `drive mounts` lists them. Messages grant nothing.
 */
import { readFile } from "node:fs/promises";
import { readFileSync } from "node:fs";
import type { DriveFiles, OpenFile } from "../../drive/files.js";
import { acceptOffer, OFFER_FORMAT, validateShareOffer, type Mounts, type ShareOffer } from "../../drive/offer.js";
import { shareHash, type Share } from "../../drive/share.js";
import { flagBool, flagString, parseArgs, UsageError } from "../args.js";
import { write, type Streams } from "../output.js";
import { openDrive } from "./drive-open.js";

type Sender = { send(recipient: string, plaintext: string, options: { type: string; metadata: Record<string, string>; expiresAt: string }): Promise<unknown> };
const stamp = (d = new Date()) => d.toISOString().replace(/\.\d{3}Z$/, "Z");
const week = () => stamp(new Date(Date.now() + 7 * 24 * 3600 * 1000));

export async function sendShareMessage(client: Sender, recipient: string, type: string, shareId: string, body: unknown): Promise<void> {
  await client.send(recipient, JSON.stringify(body), { type, metadata: { share_id: shareId }, expiresAt: week() });
}

/** Offer a just-made share to its member. */
export async function offerShare(client: Sender, relayUrl: string, file: OpenFile, share: Share): Promise<void> {
  const offer: ShareOffer = { format: OFFER_FORMAT, share, relay: new URL(relayUrl).host, kind: file.manifest.kind,
    ...(file.name ? { name: file.name } : {}), offered_at: stamp() };
  validateShareOffer(offer);
  await sendShareMessage(client, share.member!, "sys.share.offer", share.id, offer);
}

async function privateFolder(files: DriveFiles): Promise<OpenFile> {
  let current = await files.root();
  for (const name of [".poweur", "private"]) {
    const found = (await files.list(current)).find(f => f.name === name && f.manifest.kind === "folder");
    current = found ?? await files.create(current, name, "folder");
  }
  return current;
}
async function readMounts(files: DriveFiles): Promise<{ mounts: Mounts; folder: OpenFile; file?: OpenFile }> {
  const folder = await privateFolder(files);
  const file = (await files.list(folder)).find(f => f.name === "mounts.json");
  if (!file) return { mounts: { format: 1, mounts: [] }, folder };
  const chunks: Uint8Array[] = [];
  for await (const c of files.read(file)) chunks.push(c);
  return { mounts: JSON.parse(Buffer.concat(chunks).toString()) as Mounts, folder, file };
}

export async function driveOffersCommand(argv: string[], streams: Streams): Promise<number> {
  const sub = argv[0];
  const args = parseArgs(argv.slice(1), { bool: ["json"] });
  const json = flagBool(args, "json");
  const identity = flagString(args, "use-identity");
  const own = await openDrive({ identity });
  if (!own.files) throw new Error("identity has no encryption key");
  if (sub === "mounts") {
    const { mounts } = await readMounts(own.files);
    return write(streams, json, mounts, mounts.mounts.map(m => `Shared/${m.drive}/${m.name}\t${m.role}\t--drive ${m.drive} /${m.node}`).join("\n") + "\n");
  }
  const source = args.positional[0];
  if (sub !== "accept" || !source) throw new UsageError("usage: poweur drive accept <offer.json|->; mounts [--json]");
  const raw = source === "-" ? readFileSync(0, "utf8") : await readFile(source, "utf8");
  const offer = JSON.parse(raw) as ShareOffer;
  validateShareOffer(offer);
  if (offer.share.member !== own.client.signer.identity) throw new Error(`this offer is for ${offer.share.member}`);
  // The offer proves nothing alone: it must be exactly the share the drive
  // holds for us, and that share must open the node.
  const shared = await openDrive({ identity, drive: offer.share.drive });
  const held = (await shared.drive.shares()).shares.find(s => s.id === offer.share.id);
  if (!held || shareHash(held) !== shareHash(offer.share)) throw new Error("the offer does not match any share the drive holds for you");
  await shared.files!.open(offer.share.node);
  const { mounts, folder, file } = await readMounts(own.files);
  const accept = acceptOffer(mounts, offer);
  const bytes = new TextEncoder().encode(JSON.stringify(mounts, null, 2));
  if (file) await own.files.replace(file, bytes); else await own.files.create(folder, "mounts.json", "file", bytes);
  await sendShareMessage(own.client, offer.share.issuer, "sys.share.accept", offer.share.id, accept);
  const mount = mounts.mounts[mounts.mounts.length - 1]!;
  return write(streams, json, mount, `Shared/${mount.drive}/${mount.name}\t/${mount.node}\n`);
}
