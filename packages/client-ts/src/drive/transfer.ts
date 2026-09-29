/** Expiring encrypted multi-file transfers (EPIC-005 E05-T7). */
import { randomBytes, toBase64url } from "../encoding.js";
import { RelayError } from "../errors.js";
import type { DriveFileSource, DriveFiles, OpenFile } from "./files.js";
import type { Share } from "./share.js";

export interface TransferSource extends DriveFileSource {
  name: string;
  /** Stable local modification stamp, when the runtime supplies one. */
  modified?: number;
}

export interface TransferFileState {
  name: string;
  size: number;
  modified?: number;
  offset: number;
  complete: boolean;
  node?: string;
}

export interface TransferState {
  format: 1;
  id: string;
  drive: string;
  folder?: string;
  status: "uploading" | "ready" | "revoked" | "expired";
  created_at: string;
  updated_at: string;
  expires_at: string;
  max_downloads: number;
  password: boolean;
  files: TransferFileState[];
  share?: Share;
  url?: string;
}

export interface CreateTransferOptions {
  origin: string;
  expiresAt?: string;
  password?: string;
  maxDownloads?: number;
  message?: string;
  /** A prior checkpoint resumes completed files after the caller reselects
   * the same local sources. The interrupted file restarts; uploaded orphan
   * chunks are reclaimed by normal drive GC. */
  state?: TransferState;
  onState?: (state: TransferState) => void | Promise<void>;
}

const transferID = () => Array.from(randomBytes(12), b => b.toString(16).padStart(2, "0")).join("");
const stamp = () => new Date().toISOString().replace(/\.\d{3}Z$/, "Z");

function cleanName(value: string): string {
  const name = value.trim();
  if (!name || name === "." || name === ".." || name.includes("/") || name.includes("\\")) throw new Error(`invalid transfer filename ${JSON.stringify(value)}`);
  return name;
}

function initial(files: DriveFiles, sources: TransferSource[], options: CreateTransferOptions): TransferState {
  if (!sources.length) throw new Error("a transfer needs at least one file");
  const now = Date.now();
  const expiresAt = options.expiresAt ?? new Date(now + 7 * 86_400_000).toISOString().replace(/\.\d{3}Z$/, "Z");
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/.test(expiresAt) || Date.parse(expiresAt) <= now) throw new Error("transfer expiry must be a future RFC3339 timestamp");
  const maxDownloads = options.maxDownloads ?? 0;
  if (!Number.isSafeInteger(maxDownloads) || maxDownloads < 0) throw new Error("max downloads cannot be negative");
  const names = new Set<string>();
  const entries = sources.map((source) => {
    const name = cleanName(source.name);
    if (!Number.isSafeInteger(source.size) || source.size < 0) throw new Error(`${name}: invalid source size`);
    if (names.has(name)) throw new Error(`duplicate transfer filename ${name}`);
    names.add(name);
    return { name, size: source.size, ...(source.modified == null ? {} : { modified: source.modified }), offset: 0, complete: false };
  });
  const at = stamp();
  return { format: 1, id: transferID(), drive: files.client.drive, status: "uploading", created_at: at, updated_at: at,
    expires_at: expiresAt, max_downloads: maxDownloads, password: Boolean(options.password), files: entries };
}

function checkSources(state: TransferState, sources: TransferSource[]) {
  if (state.files.length !== sources.length) throw new Error("transfer file count changed");
  state.files.forEach((file, index) => {
    const source = sources[index]!;
    if (file.name !== cleanName(source.name) || file.size !== source.size || (file.modified != null && file.modified !== source.modified)) {
      throw new Error(`${file.name}: source changed since the transfer began`);
    }
  });
}

async function emit(state: TransferState, callback?: CreateTransferOptions["onState"]) {
  state.updated_at = stamp();
  await callback?.(structuredClone(state));
}

async function childFolder(files: DriveFiles, parent: OpenFile, name: string): Promise<OpenFile> {
  const existing = (await files.list(parent)).find(file => file.name === name);
  if (existing) {
    if (existing.manifest.kind !== "folder") throw new Error(`${name} is not a folder`);
    return existing;
  }
  return files.create(parent, name, "folder");
}

/** Upload and publish a transfer. Checkpoints contain no plaintext keys and
 * are safe to store encrypted with the browser's identity snapshot key. */
export async function createTransfer(files: DriveFiles, sources: TransferSource[], options: CreateTransferOptions): Promise<TransferState> {
  const state = options.state ? structuredClone(options.state) : initial(files, sources, options);
  if (state.drive !== files.client.drive) throw new Error(`transfer belongs to ${state.drive}`);
  if (state.status === "ready") return state;
  if (state.status !== "uploading") throw new Error(`transfer is ${state.status}`);
  if (Date.parse(state.expires_at) <= Date.now()) { state.status = "expired"; await emit(state, options.onState); throw new Error("transfer has expired"); }
  if (state.password && !options.password) throw new Error("password is required to resume this transfer");
  checkSources(state, sources);

  let folder: OpenFile;
  if (state.folder) folder = await files.open(state.folder);
  else {
    // Under .poweur, which file listings hide: a transfer is not a folder the
    // owner browses, it is reached through Send's history.
    const root = await files.root();
    const system = await childFolder(files, root, ".poweur");
    const transfers = await childFolder(files, system, "transfers");
    folder = await files.create(transfers, `tr_${state.id}`, "folder");
    state.folder = folder.manifest.node;
    await emit(state, options.onState);
  }

  for (let index = 0; index < sources.length; index++) {
    const saved = state.files[index]!, source = sources[index]!;
    if (saved.complete) continue;
    // A durable checkpoint can resume completed files. A partly uploaded file
    // has no committed manifest yet, so restart that file from zero.
    saved.offset = 0;
    const created = await files.createFromSource(folder, saved.name, source, async ({ offset }) => {
      saved.offset = offset;
      await emit(state, options.onState);
    });
    saved.node = created.manifest.node; saved.offset = saved.size; saved.complete = true;
    await emit(state, options.onState);
  }
  if (options.message?.trim() && !(await files.list(folder)).some(file => file.name === "Message.txt")) {
    await files.create(folder, "Message.txt", "file", new TextEncoder().encode(options.message.trim()));
  }
  if (!state.share) {
    const linked = await files.link(folder, "read", state.expires_at, options.password ?? "", { caps: { downloads: state.max_downloads } });
    state.share = linked.share;
    state.url = `${options.origin.replace(/\/$/, "")}/s/${linked.share.link}#${toBase64url(linked.fragment)}`;
  }
  state.status = "ready";
  await emit(state, options.onState);
  return state;
}

async function removeTree(files: DriveFiles, node: OpenFile): Promise<void> {
  if (node.manifest.kind === "folder") for (const child of await files.list(node)) await removeTree(files, child);
  await files.remove(node);
}

/** Revoke the public capability and release every file in its subtree.
 * `status` records why: the owner revoked it, or it expired. */
export async function revokeTransfer(files: DriveFiles, state: TransferState, status: "revoked" | "expired" = "revoked"): Promise<TransferState> {
  if (state.drive !== files.client.drive) throw new Error(`transfer belongs to ${state.drive}`);
  if (state.status === "revoked" || state.status === "expired") return state;
  if (state.folder) {
    try { await removeTree(files, await files.open(state.folder)); }
    catch (error) { if (!(error instanceof RelayError && error.status === 404)) throw error; }
  }
  if (state.share) {
    try { await files.client.unshare(state.share.id); }
    catch (error) { if (!(error instanceof RelayError && error.status === 404)) throw error; }
  }
  return { ...state, status, updated_at: stamp() };
}

/** Transfers past their expiry, which `revokeTransfer(…, "expired")` releases. */
export function expiredTransfers(states: TransferState[], now = Date.now()): TransferState[] {
  return states.filter((state) => (state.status === "ready" || state.status === "uploading") && Date.parse(state.expires_at) <= now);
}
