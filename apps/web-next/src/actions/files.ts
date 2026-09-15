/**
 * Files and sharing (EPIC-003/004/005 web surface), ported from app.js: one
 * DAV client per open tree, chunked upload above the SDK threshold, grants
 * signed here with the identity key, and the changes feed refreshing the open
 * folder while Files is on screen.
 */
import { DEFAULT_CHUNK_THRESHOLD, grantExpired, SHARE_ROOTS, SyncClient } from "@poweur/client";
import { askConfirm, askText } from "../components/Dialogs";
import { useData, type DataFields } from "../state/data";
import { onIdentityTeardown, useSession } from "../state/session";
import { setLoading, toast } from "../state/ui";
import { activeClient, errorMessage } from "./relay";

type Files = DataFields["files"];

const setFiles = (patch: Partial<Files>) => useData.setState((state) => ({ files: { ...state.files, ...patch } }));

/**
 * The DAV client for whichever tree is open. Minting a token costs a
 * signature, so it is cached per tree and re-minted a minute before the relay
 * stops honouring it.
 */
let davCache: { client: any; owner: string | null; expires: number } | null = null;
onIdentityTeardown(() => {
  davCache = null;
});

export async function dav(): Promise<any> {
  const client = activeClient();
  if (!client) {
    toast("Unlock your identity first", "warning");
    return null;
  }
  const owner = useData.getState().files.owner;
  if (davCache && davCache.owner === owner && davCache.expires > Date.now() + 60_000) return davCache.client;
  // A visitor asks for `dav:full` and lets the grant engine decide: the
  // owner's signed grant is the permission, not the token scope.
  const connected = await client.dav(owner ? { audience: owner, scope: "dav:full", force: true } : { force: true });
  davCache = { client: connected, owner, expires: Date.now() + 55 * 60_000 };
  return connected;
}

/** A SyncClient over the open tree, on the cached token (no fresh signature per poll). */
export async function syncFor(): Promise<any> {
  const client = activeClient();
  const davClient = await dav();
  if (!client || !davClient) return null;
  return new SyncClient(client.relay, davClient.identity, davClient.token);
}

/** A path a grant may cover: under a shareable root, and not the root itself. */
export function isShareablePath(path: string): boolean {
  const top = String(path ?? "").split("/")[0];
  return (SHARE_ROOTS as readonly string[]).includes(top) && path.includes("/");
}

/** Live grants covering exactly this path (what the "Shared" chip reports). */
export function grantsForPath(grants: any[], path: string) {
  return grants.filter((grant) => grant.path === path && !grantExpired(grant));
}

export function describeAudience(grant: any): string {
  return (grant.audience ?? []).map((entry: any) => entry.id || `group:${entry.group}`).join(", ");
}

/** Switch between our tree and someone else's; everything cached is per tree. */
export function setFilesOwner(owner: string | null, { picking = false } = {}) {
  davCache = null;
  setFiles({ owner, picking, path: "", entries: [], quota: null, cursor: "", loaded: false });
}

export function openOwnerTree(identity: string) {
  setFilesOwner(identity.trim().toLowerCase());
  void loadFiles("");
}

export async function loadFiles(path: string) {
  try {
    const client = await dav();
    if (!client) return;
    setFiles({ loading: true });
    const [entries, quota] = await Promise.all([client.list(path), client.quota().catch(() => useData.getState().files.quota)]);
    setFiles({ path, entries, quota, loaded: true });
    if (!useData.getState().files.owner) void loadGrants();
  } catch (error) {
    toast(errorMessage(error), "error");
  } finally {
    setFiles({ loading: false });
  }
}

/** Which of the two "no" answers this was. */
export function uploadErrorMessage(error: any, owner: string | null): string {
  if (error?.status === 507) return "Storage quota exceeded";
  if (error?.status === 403 && owner) return `${owner} granted you read-only access here`;
  return errorMessage(error);
}

export async function uploadFiles(fileList: FileList | File[] | null) {
  const files = Array.from(fileList ?? []);
  if (!files.length) return;
  const { path, owner } = useData.getState().files;
  try {
    const client = await dav();
    if (!client) return;
    let sync: any = null;
    setLoading(true, `Uploading ${files.length} file${files.length > 1 ? "s" : ""}…`);
    for (const file of files) {
      const target = `${path}/${file.name}`;
      if (file.size >= DEFAULT_CHUNK_THRESHOLD) {
        // One all-or-nothing PUT over a phone's connection is a gamble; the
        // resumable endpoint uploads in chunks the relay can pick up again.
        setLoading(true, `Uploading ${file.name} in chunks…`);
        sync ??= await syncFor();
        await sync.uploadChunked(target, new Uint8Array(await file.arrayBuffer()));
      } else {
        await client.write(target, file);
      }
    }
    setLoading(false);
    toast(`Uploaded ${files.length} file${files.length > 1 ? "s" : ""}`, "success");
    await loadFiles(path);
  } catch (error) {
    setLoading(false);
    toast(uploadErrorMessage(error, owner), "error");
  }
}

export async function downloadEntry(path: string) {
  try {
    const client = await dav();
    if (!client) return;
    const bytes = await client.readBytes(path);
    const anchor = document.createElement("a");
    anchor.href = URL.createObjectURL(new Blob([bytes]));
    anchor.download = path.split("/").pop() ?? "download";
    anchor.click();
    setTimeout(() => URL.revokeObjectURL(anchor.href), 60_000);
  } catch (error) {
    toast(errorMessage(error), "error");
  }
}

export async function newFolder() {
  const name = await askText({ title: "New folder", label: "Folder name", confirmLabel: "Create" });
  if (!name) return;
  const { path } = useData.getState().files;
  try {
    const client = await dav();
    if (!client) return;
    await client.mkdir(`${path}/${name}`);
    await loadFiles(path);
  } catch (error) {
    toast(errorMessage(error), "error");
  }
}

export async function renameEntry(entryPath: string) {
  const oldName = entryPath.split("/").pop() ?? "";
  const name = await askText({ title: `Rename ${oldName}`, label: "New name", initial: oldName, confirmLabel: "Rename" });
  if (!name || name === oldName) return;
  const parent = entryPath.split("/").slice(0, -1).join("/");
  try {
    const client = await dav();
    if (!client) return;
    await client.move(entryPath, `${parent}/${name}`);
    await loadFiles(useData.getState().files.path);
  } catch (error) {
    toast(errorMessage(error), "error");
  }
}

export async function deleteEntry(entryPath: string) {
  const name = entryPath.split("/").pop() ?? entryPath;
  const confirmed = await askConfirm({
    title: `Delete ${name}?`,
    message: "It is removed from every device that syncs this folder.",
    confirmLabel: "Delete",
    confirmId: "panel-confirm-delete",
  });
  if (!confirmed) return;
  try {
    const client = await dav();
    if (!client) return;
    await client.remove(entryPath);
    await loadFiles(useData.getState().files.path);
  } catch (error) {
    toast(errorMessage(error), "error");
  }
}

export async function loadGrants({ force = false } = {}) {
  const files = useData.getState().files;
  if (files.grantsLoaded && !force) return;
  const client = activeClient();
  if (!client || files.owner) return;
  try {
    const shares = await client.shares();
    setFiles({ grants: await shares.list(), grantsLoaded: true });
  } catch (error) {
    console.warn("Share list failed:", errorMessage(error));
  }
}

/**
 * Grant access to one path. Signed here with the identity key and stored in
 * our own tree — the relay verifies the signature, so it cannot widen the audience.
 */
export async function addShare(path: string, { audience, permissions, expiry }: { audience: string[]; permissions: "read" | "rw"; expiry?: string }) {
  const client = activeClient();
  if (!client) {
    toast("Unlock your identity first", "warning");
    return;
  }
  setLoading(true, "Signing the grant…");
  try {
    const shares = await client.shares();
    await shares.add(client.signer, path, {
      with: audience,
      permissions,
      // A date input gives a day; "until the 5th" means the end of that day.
      ...(expiry ? { expiresAt: `${expiry}T23:59:59Z` } : {}),
    });
    toast(`Shared /${path} with ${audience.length} ${audience.length === 1 ? "person" : "people"}`, "success");
    await loadGrants({ force: true });
  } catch (error) {
    toast(errorMessage(error), "error");
  } finally {
    setLoading(false);
  }
}

/** Revocation is a file delete: the relay reloads grants per request. */
export async function revokeShare(shareId: string): Promise<boolean> {
  const client = activeClient();
  if (!client) return false;
  try {
    const shares = await client.shares();
    await shares.revoke(shareId);
    await loadGrants({ force: true });
    toast("Access revoked", "success");
    return true;
  } catch (error) {
    toast(errorMessage(error), "error");
    return false;
  }
}

const CHANGES_POLL_MS = 5000;

/**
 * Refresh the open folder from the changes feed (EPIC-004 E04-T5) until the
 * returned stop function runs. Polling — there is no change socket yet — and
 * reloading only when a change touches the folder's own entries.
 */
export function watchChanges(): () => void {
  let active = true;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let wake: (() => void) | null = null;
  const sleep = (ms: number) =>
    new Promise<void>((resolve) => {
      wake = resolve;
      timer = setTimeout(resolve, ms);
    });

  void (async () => {
    try {
      while (active && useSession.getState().unlocked) {
        const sync = await syncFor();
        if (!sync || !active) break;
        const { changes, cursor, fullResync } = await sync.changes(useData.getState().files.cursor);
        if (!active) break;
        setFiles({ cursor: fullResync ? "" : cursor });
        const current = useData.getState().files;
        const prefix = current.path ? `${current.path}/` : "";
        const touched = (changes ?? []).some((change: any) => {
          const path = change.path ?? "";
          // This folder's own entries only; a change deep inside a subfolder
          // does not change what this listing shows.
          return path.startsWith(prefix) && !path.slice(prefix.length).includes("/");
        });
        if (touched && !current.loading) await loadFiles(current.path);
        await sleep(CHANGES_POLL_MS);
      }
    } catch (error) {
      console.warn("Changes feed stopped:", errorMessage(error));
    }
  })();

  return () => {
    active = false;
    clearTimeout(timer);
    wake?.();
  };
}

/** Test seam. */
export function resetFilesForTests() {
  davCache = null;
}
