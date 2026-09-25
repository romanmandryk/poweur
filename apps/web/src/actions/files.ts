/**
 * Files and sharing (EPIC-003/004/005 web surface), ported from app.js: one
 * DAV client per open tree, chunked upload above the SDK threshold, grants
 * signed here with the identity key, and the changes feed refreshing the open
 * folder while Files is on screen.
 */
import { DEFAULT_CHUNK_THRESHOLD, grantExpired, SHARE_ROOTS, SyncClient, validateShareClaim } from "@poweur/client";
import { askConfirm, askText } from "../components/Dialogs";
import { useData, type DataFields } from "../state/data";
import { onIdentityTeardown, useSession } from "../state/session";
import { setLoading, toast } from "../state/ui";
import { trackAction } from "../lib/observability";
import { clearPendingShareClaim, pendingShareClaim } from "../lib/share-claim";
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
  return (grant.audience ?? []).map((entry: any) => entry.id || (entry.group ? `group:${entry.group}` : grant.link?.file_request ? "file request" : "public link")).join(", ");
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

/** Only the newest listing may land: a slow read of the root must not overwrite the folder opened after it. */
let loadToken = 0;
/**
 * The folder the user asked for, which `files.path` only becomes once its
 * listing lands. The changes feed reloads *this* — reloading the old path
 * mid-navigation would win the race and snap the view back.
 */
let requestedPath = "";

export async function loadFiles(path: string) {
  const token = ++loadToken;
  requestedPath = path;
  // Loading from the first moment, not after the token is minted, so a
  // changes poll in between sees a navigation in flight and stands back.
  // Moving to another folder clears the old rows: they would stay clickable
  // for the moment the new listing takes, and a tap on one opens the wrong
  // thing. Reloading the same folder keeps them, so a refresh does not blink.
  const navigating = path !== useData.getState().files.path;
  setFiles(navigating ? { loading: true, entries: [] } : { loading: true });
  try {
    const client = await dav();
    if (!client || token !== loadToken) return;
    const [entries, quota] = await Promise.all([client.list(path), client.quota().catch(() => useData.getState().files.quota)]);
    if (token !== loadToken) return;
    setFiles({ path, entries, quota, loaded: true });
    if (!useData.getState().files.owner) void loadGrants();
  } catch (error) {
    if (token === loadToken) toast(errorMessage(error), "error");
  } finally {
    if (token === loadToken) setFiles({ loading: false });
  }
}

/**
 * What a full drive says. The free limit is not advertised; running into it
 * is the moment to ask the operator's contact for more, and to say what for.
 */
export function storageFullMessage(contact: string | null | undefined, full = true): string {
  const lead = full ? "Your storage is full." : "You're almost out of storage.";
  return contact ? `${lead} Message ${contact} to ask for more space, and tell us what you need it for.` : lead;
}

/** Which of the two "no" answers this was. */
export function uploadErrorMessage(error: any, owner: string | null): string {
  if (error?.status === 507) return storageFullMessage(useData.getState().files.quota?.contact);
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
    trackAction("files", { kind: "upload", count: files.length });
    await loadFiles(path);
  } catch (error) {
    setLoading(false);
    toast(uploadErrorMessage(error, owner), "error", (error as any)?.status === 507 ? 12000 : 3500);
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

export async function loadMounts({ force = false } = {}) {
  const files = useData.getState().files;
  if (files.mountsLoaded && !force) return;
  const client = activeClient();
  if (!client) return;
  try {
    const shares = await client.shares();
    setFiles({ mounts: await shares.listMounts(), mountsLoaded: true });
  } catch (error) {
    console.warn("Share mounts failed:", errorMessage(error));
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
    const result = await client.offerShare(path, audience, {
      permissions,
      // A date input gives a day; "until the 5th" means the end of that day.
      ...(expiry ? { expiresAt: `${expiry}T23:59:59Z` } : {}),
    });
    const failed = result.deliveries.filter((delivery: any) => !delivery.delivered).length;
    toast(
      failed
        ? `Access granted, but ${failed} ${failed === 1 ? "offer" : "offers"} could not be delivered`
        : `Shared /${path} with ${audience.length} ${audience.length === 1 ? "person" : "people"}`,
      failed ? "warning" : "success",
    );
    trackAction("files", { kind: "share" });
    await loadGrants({ force: true });
  } catch (error) {
    toast(errorMessage(error), "error");
  } finally {
    setLoading(false);
  }
}

export async function addFileRequest(path: string, options: {
  expiry?: string;
  password?: string;
  maxUploads?: number;
  maxBytes?: number;
  maxObjectBytes?: number;
  allowedTypes?: string[];
  notify?: boolean;
}): Promise<string | null> {
  const client = activeClient();
  if (!client) {
    toast("Unlock your identity first", "warning");
    return null;
  }
  setLoading(true, "Signing the file request…");
  try {
    const shares = await client.shares();
    const { expiry, ...requestOptions } = options;
    const { grant, token } = await shares.addFileRequest(client.signer, path, {
      ...requestOptions,
      ...(expiry ? { expiresAt: `${expiry}T23:59:59Z` } : {}),
    });
    setFiles({ grants: [...useData.getState().files.grants, grant], grantsLoaded: true });
    trackAction("files", { kind: "file_request_create" });
    toast("Upload-only request created", "success");
    return `https://${client.identity}/s/${token}`;
  } catch (error) {
    toast(errorMessage(error), "error");
    return null;
  } finally {
    setLoading(false);
  }
}

export async function addPublicLink(path: string, options: {
  expiry?: string;
  password?: string;
  maxDownloads?: number;
}): Promise<string | null> {
  const client = activeClient();
  if (!client) {
    toast("Unlock your identity first", "warning");
    return null;
  }
  setLoading(true, "Signing the public link…");
  try {
    const shares = await client.shares();
    const { expiry, ...linkOptions } = options;
    const { grant, token } = await shares.addLink(client.signer, path, {
      ...linkOptions,
      ...(expiry ? { expiresAt: `${expiry}T23:59:59Z` } : {}),
    });
    setFiles({ grants: [...useData.getState().files.grants, grant], grantsLoaded: true });
    trackAction("files", { kind: "public_link_create" });
    toast("Read-only public link created", "success");
    return `https://${client.identity}/s/${token}`;
  } catch (error) {
    toast(errorMessage(error), "error");
    return null;
  } finally {
    setLoading(false);
  }
}

let claimSubmission: Promise<void> | null = null;

/** Send a preserved anonymous-link context once an identity is unlocked. */
export function submitPendingShareClaim(): Promise<void> {
  if (claimSubmission) return claimSubmission;
  const pending = pendingShareClaim();
  const client = activeClient();
  if (!pending || !client) return Promise.resolve();
  claimSubmission = (async () => {
    try {
      await client.requestShareClaim(pending);
      clearPendingShareClaim();
      trackAction("files", { kind: "share_claim_sent" });
      toast(`Asked ${pending.owner} for ongoing access`, "success", 7000);
    } catch (error) {
      toast(`Could not request access: ${errorMessage(error)}`, "error", 8000);
    } finally {
      claimSubmission = null;
    }
  })();
  return claimSubmission;
}

/** Approve an encrypted claim request by issuing a fresh direct-ID grant. */
export async function approveShareClaimMessage(message: any, consumeLink: boolean): Promise<boolean> {
  const client = activeClient();
  const identity = useSession.getState().identity;
  if (!client || !identity || !message?.plaintext) return false;
  try {
    const claim = JSON.parse(message.plaintext);
    validateShareClaim(claim);
    if (String(message.sender).toLowerCase() !== claim.claimant.toLowerCase() ||
        String(claim.owner).toLowerCase() !== identity.toLowerCase() ||
        String(message.metadata?.share_id ?? "") !== claim.share_id) {
      throw new Error("Claim message roles do not match its signed envelope");
    }
    setLoading(true, "Granting ongoing access…");
    await client.approveShareClaim(claim, { consumeLink, permissions: "rw" });
    useData.setState((state) => ({
      requests: { ...state.requests, incoming: state.requests.incoming.filter((entry: any) => entry.id !== message.id) },
      messages: state.messages.filter((entry: any) => entry.id !== message.id),
    }));
    await loadGrants({ force: true });
    trackAction("files", { kind: "share_claim_approved", consumeLink });
    toast(consumeLink ? "Access granted and public link closed" : "Ongoing access granted", "success");
    return true;
  } catch (error) {
    toast(errorMessage(error), "error");
    return false;
  } finally {
    setLoading(false);
  }
}

/** Revocation is a file delete: the relay reloads grants per request. */
export async function revokeShare(shareId: string): Promise<boolean> {
  const client = activeClient();
  if (!client) return false;
  try {
    const result = await client.revokeShareAndNotify(shareId);
    if (!result.revoked) throw new Error("Share was already revoked");
    await loadGrants({ force: true });
    toast("Access revoked", "success");
    return true;
  } catch (error) {
    toast(errorMessage(error), "error");
    return false;
  }
}

/** Verify an encrypted offer, write its local mount pointer and acknowledge it. */
export async function acceptShareOffer(message: any): Promise<boolean> {
  const client = activeClient();
  if (!client || !message?.plaintext) return false;
  setLoading(true, "Accepting share…");
  try {
    const offer = JSON.parse(message.plaintext);
    const result = await client.acceptShareOffer(offer);
    useData.setState((state) => ({
      requests: { ...state.requests, incoming: state.requests.incoming.filter((entry: any) => entry.id !== message.id) },
      messages: state.messages.filter((entry: any) => entry.id !== message.id),
    }));
    await loadMounts({ force: true });
    toast(result.notified ? `Mounted files from ${offer.grant.owner}` : `Mounted files from ${offer.grant.owner}; acceptance could not be delivered`, result.notified ? "success" : "warning");
    trackAction("files", { kind: "share_accept" });
    return true;
  } catch (error) {
    toast(errorMessage(error), "error");
    return false;
  } finally {
    setLoading(false);
  }
}

/** Remove local pointers when a verified sender says its grant is gone. */
export async function processShareRevocations(messages: any[]): Promise<void> {
  const revocations = (messages ?? []).filter((message) => message.type === "sys.share.revoked" && message.plaintext);
  if (!revocations.length) return;
  const client = activeClient();
  if (!client) return;
  const shares = await client.shares();
  const mounts = await shares.listMounts();
  let removed = 0;
  for (const message of revocations) {
    try {
      const payload = JSON.parse(message.plaintext);
      if (
        payload?.version !== 1 || payload.owner !== message.sender ||
        payload.share_id !== message.metadata?.share_id
      ) continue;
      for (const entry of mounts.filter((mount: any) => mount.mount.share_id === payload.share_id && mount.mount.owner === payload.owner)) {
        if (await shares.removeMount(entry.mountPath, entry.mount.owner)) removed += 1;
      }
    } catch {
      // A malformed notice cannot remove local state.
    }
  }
  if (removed) {
    await loadMounts({ force: true });
    toast(`${removed} revoked share ${removed === 1 ? "mount was" : "mounts were"} removed`, "info");
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
        const folder = requestedPath;
        const prefix = folder ? `${folder}/` : "";
        const touched = (changes ?? []).some((change: any) => {
          const path = change.path ?? "";
          // This folder's own entries only; a change deep inside a subfolder
          // does not change what this listing shows.
          return path.startsWith(prefix) && !path.slice(prefix.length).includes("/");
        });
        if (touched && !current.loading) await loadFiles(folder);
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
  loadToken = 0;
  requestedPath = "";
}
