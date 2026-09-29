/** User actions behind the storage-v2 Files destination (E20-T10). */
import { fromBase64, toBase64url } from "@poweur/client";
import {
  OFFER_FORMAT,
  acceptOffer,
  keyBearing,
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
import { loadSnapshot, saveSnapshot } from "../lib/snapshot";
import { useData, type FilesPreviewEntry } from "../state/data.js";

const encoder = new TextEncoder();
const decoder = new TextDecoder();
const stamp = (date = new Date()) => date.toISOString().replace(/\.\d{3}Z$/, "Z");

const privateFolders = new WeakMap<DriveFiles, Promise<OpenFile>>();
/** `.poweur/private`, resolved once per opened drive. */
function privateFolder(files: DriveFiles): Promise<OpenFile> {
  let folder = privateFolders.get(files);
  if (!folder) {
    folder = (async () => {
      let current = await files.root();
      for (const name of [".poweur", "private"]) {
        const found = (await files.list(current)).find((file) => file.name === name && file.manifest.kind === "folder");
        current = found ?? await files.create(current, name, "folder");
      }
      return current;
    })();
    privateFolders.set(files, folder);
    folder.catch(() => privateFolders.delete(files));
  }
  return folder;
}

async function mountsDocument(files: DriveFiles): Promise<{ mounts: Mounts; folder: OpenFile; file?: OpenFile }> {
  const folder = await privateFolder(files);
  const file = (await files.list(folder)).find((entry) => entry.name === "mounts.json");
  if (!file) return { mounts: { format: 1, mounts: [] }, folder };
  const parsed = JSON.parse(decoder.decode(await readFileBytes(files, file))) as Mounts;
  return { mounts: { format: 1, mounts: Array.isArray(parsed.mounts) ? parsed.mounts : [] }, folder, file };
}

export async function loadMounts(identity: string, opened?: DriveFiles): Promise<Mount[]> {
  const files = opened ?? (await openBrowserDrive(identity)).files;
  return (await mountsDocument(files)).mounts.mounts;
}

const folderKey = (files: DriveFiles, folder: OpenFile) => `${files.client.drive}:${folder.manifest.node}`;
let loadingFiles: { identity: string; promise: Promise<void> } | null = null;

function visibleEntries(files: DriveFiles, folder: OpenFile, entries: OpenFile[]): OpenFile[] {
  const visible = folder.folder === "" && files.client.drive === files.client.signer.identity
    ? entries.filter((entry) => entry.name !== ".poweur")
    : entries;
  return [...visible].sort((a, b) => Number(b.manifest.kind === "folder") - Number(a.manifest.kind === "folder") || a.name.localeCompare(b.name));
}

/** Open the active identity's drive once and retain its decrypted listings across navigation. */
export async function ensureBrowserFiles(identity: string, force = false): Promise<void> {
  const cached = useData.getState().files;
  if (cached.loaded && !force) return;
  if (loadingFiles?.identity === identity) return loadingFiles.promise;
  const promise = (async () => {
    useData.setState((state) => ({ files: { ...state.files, identity, loading: true, error: null } }));
    // What this device listed last shows at once, while the drive opens.
    if (!cached.own) {
      void loadSnapshot<FilesPreviewEntry[]>(identity, "files").then((preview) => {
        const now = useData.getState().files;
        if (preview && now.identity === identity && !now.own) useData.setState((state) => ({ files: { ...state.files, preview } }));
      });
    }
    try {
      const opened = cached.own ? { files: cached.own.files } : await openBrowserDrive(identity);
      // The info call also gives the changes cursor as of this read.
      const info = await opened.files.client.info();
      const root = cached.own?.root ?? (info.root ? await opened.files.open(info.root) : await opened.files.root());
      const [listed, mounts] = await Promise.all([opened.files.list(root), loadMounts(identity, opened.files)]);
      const entries = visibleEntries(opened.files, root, listed);
      if (useData.getState().files.identity !== identity) return;
      const key = folderKey(opened.files, root);
      useData.setState((state) => ({ files: {
        ...state.files,
        own: { files: opened.files, root },
        folders: { ...state.files.folders, [key]: { folder: root, entries } },
        mounts,
        preview: null,
        cursor: info.seq ?? null,
        loading: false,
        loaded: true,
        error: null,
      } }));
      void saveSnapshot(identity, "files", previewOf(entries));
    } catch (cause) {
      if (useData.getState().files.identity !== identity) return;
      const message = cause instanceof Error ? cause.message : String(cause);
      useData.setState((state) => ({ files: { ...state.files, loading: false, loaded: true, error: message } }));
    }
  })();
  loadingFiles = { identity, promise };
  try { await promise; } finally { if (loadingFiles?.promise === promise) loadingFiles = null; }
}

/** Read one folder, using the decrypted in-memory listing unless explicitly refreshed. */
export async function loadBrowserFolder(identity: string, files: DriveFiles, folder: OpenFile, force = false): Promise<OpenFile[]> {
  const key = folderKey(files, folder);
  const cached = useData.getState().files.folders[key];
  if (cached && !force) return cached.entries;
  const entries = visibleEntries(files, folder, await files.list(folder));
  // Ignore a late read belonging to an identity that has since been switched out.
  if (files.client.signer.identity === identity && useData.getState().files.identity === identity) {
    useData.setState((state) => ({ files: { ...state.files, folders: { ...state.files.folders, [key]: { folder, entries } }, error: null } }));
  }
  return entries;
}

const previewOf = (entries: OpenFile[]): FilesPreviewEntry[] =>
  entries.map((entry) => ({ node: entry.manifest.node, name: entry.name, kind: entry.manifest.kind }));

/** After pull-to-refresh or drive SSE: ask the drive's change feed whether
 * anything moved (one request); only then re-read mounts and every folder
 * visited this session, in parallel. */
export async function refreshBrowserFiles(identity: string): Promise<void> {
  await ensureBrowserFiles(identity);
  const snapshot = useData.getState().files;
  if (!snapshot.own) return;
  const client = snapshot.own.files.client;
  if (snapshot.cursor) {
    const { changes, cursor } = await client.changes(snapshot.cursor, 1);
    if (!changes.length) {
      if (cursor !== snapshot.cursor) useData.setState((state) => ({ files: { ...state.files, cursor } }));
      return;
    }
  }
  const known = Object.values(snapshot.folders).filter(({ folder }) => folder.manifest.drive === identity);
  try {
    const head = client.info();
    const results = await Promise.all(known.map(async ({ folder }) => ({
      key: folderKey(snapshot.own!.files, folder),
      folder,
      entries: visibleEntries(snapshot.own!.files, folder, await snapshot.own!.files.list(folder)),
    })));
    const [mounts, { seq }] = await Promise.all([loadMounts(identity, snapshot.own.files), head]);
    if (useData.getState().files.identity !== identity) return;
    useData.setState((state) => ({ files: {
      ...state.files,
      folders: { ...state.files.folders, ...Object.fromEntries(results.map((result) => [result.key, { folder: result.folder, entries: result.entries }])) },
      mounts,
      cursor: seq ?? null,
      loaded: true,
      error: null,
    } }));
    const root = results.find((result) => result.folder.manifest.node === snapshot.own!.root.manifest.node);
    if (root) void saveSnapshot(identity, "files", previewOf(root.entries));
  } catch (cause) {
    if (useData.getState().files.identity !== identity) return;
    const message = cause instanceof Error ? cause.message : String(cause);
    useData.setState((state) => ({ files: { ...state.files, error: message } }));
    throw cause;
  }
}

export function cachedBrowserFolder(files: DriveFiles, folder: OpenFile): OpenFile[] | undefined {
  return useData.getState().files.folders[folderKey(files, folder)]?.entries;
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
export async function shareBrowserFile(identity: string, file: OpenFile, member: string, role: ShareRole, expires = ""): Promise<{ share: Share; notified: boolean }> {
  const target = member.trim().toLowerCase();
  const resolved = await lookup(target, relayUrlFor(identity));
  const published = resolved.document.encryption_public_key;
  if (!published) throw new Error(`${target} has no published encryption key`);
  const key = fromBase64(published.replace(/^x25519:/, ""));
  if (key.length !== 32) throw new Error(`${target} has no valid encryption key`);

  const { drive, files } = await openBrowserDrive(identity);
  const share = await files.shareWith(file, target, key, role, expires);
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
      expiresAt: expires || stamp(new Date(Date.now() + 7 * 24 * 3600 * 1000)),
    });
    return { share, notified: true };
  } catch {
    // The grant already exists and must not be described as a failed share.
    return { share, notified: false };
  }
}

export async function sharesForFile(identity: string, file: OpenFile): Promise<Share[]> {
  const { drive } = await openBrowserDrive(identity);
  return (await drive.shares()).shares.filter((share) => share.node === file.manifest.node);
}

/** Create a key-in-fragment browser link; the fragment is never sent to the relay. */
export async function linkBrowserFile(identity: string, file: OpenFile, password = "", expires = ""): Promise<{ share: Share; url: string }> {
  const { files } = await openBrowserDrive(identity);
  const { share, fragment } = await files.link(file, "read", expires, password);
  const relay = new URL(relayUrlFor(identity));
  const origin = `${relay.protocol}//${share.drive}${relay.port ? `:${relay.port}` : ""}`;
  return { share, url: `${origin}/s/${share.link}#${toBase64url(fragment)}` };
}

export async function fileRequestBrowserLink(identity: string, folder: OpenFile, password = "", expires = "", maxFiles = 20): Promise<{ share: Share; url: string }> {
  if (folder.manifest.kind !== "folder") throw new Error("file requests need a folder");
  if (!Number.isSafeInteger(maxFiles) || maxFiles < 1 || maxFiles > 1000) throw new Error("file request limit must be between 1 and 1000");
  const { files } = await openBrowserDrive(identity);
  const { share, fragment } = await files.link(folder, "create", expires, password, { caps: { files: maxFiles, per_hour: 50 }, pow: 18 });
  const relay = new URL(relayUrlFor(identity));
  const origin = `${relay.protocol}//${share.drive}${relay.port ? `:${relay.port}` : ""}`;
  return { share, url: `${origin}/s/${share.link}#${toBase64url(fragment)}` };
}

export async function revokeBrowserShare(identity: string, share: Share): Promise<void> {
  const { drive, files } = await openBrowserDrive(identity);
  await drive.unshare(share.id);
  // Revoking a key-bearing share holds writes until the node is re-keyed;
  // rotating re-issues the remaining members' shares at the new keys.
  if (keyBearing(share.role)) await files.rotateIfRequired(share.node);
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
