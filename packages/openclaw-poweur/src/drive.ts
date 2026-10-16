/**
 * Drive access for the agent: open someone's drive through the shares they
 * gave the agent's ID, and accept a share offer (PCP-0008) the way the
 * Poweur CLI does. Only the agent's own private keys are ever loaded;
 * collaborators' keys come from their public identity documents.
 */
import {
  fromBase64,
  MSG_TYPE_SHARE_ACCEPT,
  RelayClient,
  resolveEncryptionKey,
  resolveRecipientRelayUrl,
  resolveSigningKey,
  type PoweurClient,
  type StoredIdentityKeys,
} from "@poweur/client";
import {
  acceptOffer,
  DriveClient,
  DriveFiles,
  fileKeys,
  shareHash,
  validateShareOffer,
  type Mounts,
  type OpenFile,
  type ShareOffer,
} from "@poweur/client/drive";
import { FileChunkCache, nodeResolveOptions } from "@poweur/client/node";

export interface AgentContext {
  client: PoweurClient;
  keys: StoredIdentityKeys;
}

/** Files in `drive` (the agent's own, or an owner's it was shared into). */
export async function openDriveFiles(ctx: AgentContext, drive: string): Promise<DriveFiles> {
  const { client, keys } = ctx;
  if (!keys.encryptionPrivateKey) throw new Error("the agent's ID has no encryption key");
  const own = client.signer.identity;
  let relay = client.relay;
  if (drive !== own) {
    const scheme = client.relay.relayUrl.startsWith("http://") ? "http" : "https";
    relay = new RelayClient(await resolveRecipientRelayUrl(drive, scheme, nodeResolveOptions()));
  }
  const driveClient = new DriveClient(relay, client.signer, drive, new FileChunkCache());
  return new DriveFiles(driveClient, {
    ...fileKeys(keys.signingPrivateKey, keys.encryptionPrivateKey),
    async authorKey(author: string) {
      const key = await resolveSigningKey(author, nodeResolveOptions());
      if (!key) throw new Error(`cannot resolve ${author}'s signing key`);
      return fromBase64(key);
    },
    async encryptionKey(member: string) {
      const key = await resolveEncryptionKey(member, nodeResolveOptions());
      if (!key) throw new Error(`cannot resolve ${member}'s encryption key`);
      return fromBase64(key);
    },
    async groupMembers(group: string) {
      const { document } = await client.groups.roster(client.signer, group);
      return [...document.members, ...(document.admins ?? [])];
    },
  });
}

async function readAll(files: DriveFiles, file: OpenFile): Promise<Uint8Array> {
  const chunks: Uint8Array[] = [];
  for await (const c of files.read(file)) chunks.push(c);
  return new Uint8Array(Buffer.concat(chunks));
}

async function privateFolder(files: DriveFiles): Promise<OpenFile> {
  let current = await files.root();
  for (const name of [".poweur", "private"]) {
    const found = (await files.list(current)).find((f) => f.name === name && f.manifest.kind === "folder");
    current = found ?? (await files.create(current, name, "folder"));
  }
  return current;
}

export interface AcceptedFolder {
  drive: string;
  node: string;
  name: string;
  role: string;
  shareId: string;
}

/**
 * Accept an offer from `sender`: it must be for the agent, issued by the
 * sender, and exactly a share their drive holds (the offer alone proves
 * nothing). Records the mount in the agent's own drive and answers with
 * sys.share.accept, so the owner's app shows it as accepted.
 */
export async function acceptFolderOffer(ctx: AgentContext, sender: string, body: string): Promise<AcceptedFolder> {
  const offer = JSON.parse(body) as ShareOffer;
  validateShareOffer(offer);
  const self = ctx.client.signer.identity;
  if (offer.share.member !== self) throw new Error(`the offer is for ${offer.share.member}`);
  if (offer.share.issuer !== sender || offer.share.drive !== sender) throw new Error("only a drive's owner can offer it to the agent");
  if (offer.kind !== "folder") throw new Error("only folders are synced");

  const shared = await openDriveFiles(ctx, offer.share.drive);
  const held = (await shared.client.shares()).shares.find((s) => s.id === offer.share.id);
  if (!held || shareHash(held) !== shareHash(offer.share)) throw new Error("the offer does not match any share the drive holds for the agent");
  const node = await shared.open(offer.share.node);

  const own = await openDriveFiles(ctx, self);
  const folder = await privateFolder(own);
  const existing = (await own.list(folder)).find((f) => f.name === "mounts.json");
  const mounts: Mounts = existing ? (JSON.parse(Buffer.from(await readAll(own, existing)).toString()) as Mounts) : { format: 1, mounts: [] };
  const accept = acceptOffer(mounts, offer);
  const bytes = new TextEncoder().encode(JSON.stringify(mounts, null, 2));
  if (existing) await own.replace(existing, bytes);
  else await own.create(folder, "mounts.json", "file", bytes);

  const week = new Date(Date.now() + 7 * 24 * 3600 * 1000).toISOString().replace(/\.\d{3}Z$/, "Z");
  await ctx.client.send(offer.share.issuer, JSON.stringify(accept), {
    type: MSG_TYPE_SHARE_ACCEPT,
    metadata: { share_id: offer.share.id },
    expiresAt: week,
  });
  return { drive: offer.share.drive, node: offer.share.node, name: offer.name || node.name || "shared", role: offer.share.role, shareId: offer.share.id };
}
