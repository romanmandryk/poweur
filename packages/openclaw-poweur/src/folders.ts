/**
 * Shared folders (INT-005-T3): a person shares a folder with the agent's ID,
 * the agent works on a local copy in its workspace, and what it writes goes
 * back into the person's folder, so the files are theirs.
 *
 * Two-way sync, deliberately conservative:
 *   - the owner's new and changed files come down; their deletions remove the
 *     local copy;
 *   - the agent's new and changed files go up (with a write role);
 *   - both changed: the owner's version wins locally and the agent's goes up
 *     beside it as "name (agent).ext", so nothing is lost;
 *   - the agent never deletes anything in the owner's drive: a file deleted
 *     locally comes back on the next sync.
 */
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import { dirname, join, relative, sep } from "node:path";
import type { DriveFiles, OpenFile } from "@poweur/client/drive";
import { openDriveFiles, type AgentContext } from "./drive.js";

export interface FolderMount {
  drive: string;
  node: string;
  name: string;
  role: string;
  shareId: string;
  /** Absolute local directory. */
  local: string;
}

export interface SyncState {
  format: 1;
  mounts: FolderMount[];
  /** key `${drive}/${node}` → relative path → what both sides were at the last sync. */
  files: Record<string, Record<string, { version: string; hash: string }>>;
}

export type SyncAction =
  | { kind: "download"; path: string }
  | { kind: "upload"; path: string; create: boolean }
  | { kind: "conflict"; path: string } // download theirs, upload ours as a copy
  | { kind: "delete-local"; path: string }
  | { kind: "record"; path: string } // identical on both sides, just remember it
  | { kind: "forget"; path: string };

/** Largest file synced either way. */
export const MAX_SYNC_BYTES = 50 * 1024 * 1024;
const IGNORED = new Set([".DS_Store", "Thumbs.db"]);

/**
 * Decide what to do from three snapshots: remote versions, local content
 * hashes, and both as of the last sync. `remoteHash` is the hash of remote
 * content where it is already known (it is not for changed remote files).
 */
export function planSync(
  remote: Map<string, { version: string }>,
  local: Map<string, { hash: string }>,
  last: Record<string, { version: string; hash: string }>,
  canWrite: boolean,
): SyncAction[] {
  const actions: SyncAction[] = [];
  const paths = new Set([...remote.keys(), ...local.keys(), ...Object.keys(last)]);
  for (const path of [...paths].sort()) {
    const r = remote.get(path);
    const l = local.get(path);
    const s = last[path];
    if (r && l && s) {
      const rc = r.version !== s.version;
      const lc = l.hash !== s.hash;
      if (rc && lc) actions.push(canWrite ? { kind: "conflict", path } : { kind: "download", path });
      else if (rc) actions.push({ kind: "download", path });
      else if (lc && canWrite) actions.push({ kind: "upload", path, create: false });
    } else if (r && l) {
      // Both appeared since the last sync: compare after fetching.
      actions.push(canWrite ? { kind: "conflict", path } : { kind: "download", path });
    } else if (r) {
      // New remote file, or deleted locally: the agent never deletes the owner's files.
      actions.push({ kind: "download", path });
    } else if (l) {
      if (s) actions.push({ kind: "delete-local", path }); // the owner deleted it
      else if (canWrite) actions.push({ kind: "upload", path, create: true });
    } else if (s) {
      actions.push({ kind: "forget", path });
    }
  }
  return actions;
}

export const sha256 = (bytes: Uint8Array) => createHash("sha256").update(bytes).digest("hex");

/** "report.md" → "report (agent).md" */
export function conflictName(path: string): string {
  const slash = path.lastIndexOf("/");
  const dir = slash >= 0 ? path.slice(0, slash + 1) : "";
  const base = path.slice(slash + 1);
  const dot = base.lastIndexOf(".");
  return dot > 0 ? `${dir}${base.slice(0, dot)} (agent)${base.slice(dot)}` : `${dir}${base} (agent)`;
}

function walkLocal(root: string): Map<string, { hash: string }> {
  const out = new Map<string, { hash: string }>();
  if (!existsSync(root)) return out;
  const visit = (dir: string) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      if (IGNORED.has(entry.name) || entry.isSymbolicLink()) continue;
      const full = join(dir, entry.name);
      if (entry.isDirectory()) visit(full);
      else if (entry.isFile() && statSync(full).size <= MAX_SYNC_BYTES) {
        out.set(relative(root, full).split(sep).join("/"), { hash: sha256(readFileSync(full)) });
      }
    }
  };
  visit(root);
  return out;
}

async function walkRemote(files: DriveFiles, folder: OpenFile, prefix = "", out = new Map<string, OpenFile>()): Promise<Map<string, OpenFile>> {
  for (const child of await files.list(folder)) {
    const path = prefix + child.name;
    if (child.manifest.kind === "folder") await walkRemote(files, child, `${path}/`, out);
    else if (child.manifest.mode !== "append" && !IGNORED.has(child.name)) out.set(path, child);
  }
  return out;
}

async function readAll(files: DriveFiles, file: OpenFile): Promise<Uint8Array> {
  const chunks: Uint8Array[] = [];
  for await (const c of files.read(file)) chunks.push(c);
  return new Uint8Array(Buffer.concat(chunks));
}

/** Local path for a remote path, refusing anything that escapes the mount. */
function localPath(root: string, path: string): string {
  const full = join(root, ...path.split("/"));
  if (!full.startsWith(root + sep)) throw new Error(`unsafe path ${path}`);
  return full;
}

export class FolderSync {
  private state: SyncState;
  private running: Promise<void> = Promise.resolve();

  constructor(
    private readonly ctx: AgentContext,
    private readonly stateFile: string,
    private readonly log: (line: string) => void = () => {},
  ) {
    this.state = existsSync(stateFile)
      ? (JSON.parse(readFileSync(stateFile, "utf8")) as SyncState)
      : { format: 1, mounts: [], files: {} };
  }

  get mounts(): readonly FolderMount[] {
    return this.state.mounts;
  }

  mountsFrom(owner: string): FolderMount[] {
    return this.state.mounts.filter((m) => m.drive === owner);
  }

  private save() {
    mkdirSync(dirname(this.stateFile), { recursive: true });
    writeFileSync(this.stateFile, JSON.stringify(this.state, null, 2));
  }

  add(mount: FolderMount) {
    this.state.mounts = this.state.mounts.filter((m) => !(m.drive === mount.drive && m.node === mount.node));
    this.state.mounts.push(mount);
    this.save();
  }

  remove(drive: string, shareId: string): FolderMount | undefined {
    const gone = this.state.mounts.find((m) => m.drive === drive && m.shareId === shareId);
    if (!gone) return undefined;
    this.state.mounts = this.state.mounts.filter((m) => m !== gone);
    delete this.state.files[`${gone.drive}/${gone.node}`];
    this.save();
    return gone;
  }

  /** Sync every mount (or those of one owner); calls are serialized. */
  sync(owner?: string): Promise<void> {
    const run = this.running.then(async () => {
      for (const mount of this.state.mounts) {
        if (owner && mount.drive !== owner) continue;
        try {
          await this.syncOne(mount);
        } catch (error) {
          this.log(`sync of ${mount.drive}/${mount.name} failed: ${String(error)}`);
        }
      }
      this.save();
    });
    this.running = run.catch(() => undefined);
    return run;
  }

  private async syncOne(mount: FolderMount) {
    const files = await openDriveFiles(this.ctx, mount.drive);
    const root = await files.open(mount.node);
    const remoteFiles = await walkRemote(files, root);
    const remote = new Map([...remoteFiles].map(([p, f]) => [p, { version: f.manifest.version }]));
    mkdirSync(mount.local, { recursive: true });
    const local = walkLocal(mount.local);
    const key = `${mount.drive}/${mount.node}`;
    const last = (this.state.files[key] ??= {});
    const canWrite = mount.role === "write" || mount.role === "admin";

    const download = async (path: string) => {
      const file = remoteFiles.get(path)!;
      const bytes = await readAll(files, file);
      const target = localPath(mount.local, path);
      mkdirSync(dirname(target), { recursive: true });
      writeFileSync(target, bytes);
      last[path] = { version: file.manifest.version, hash: sha256(bytes) };
      return bytes;
    };
    const folderFor = async (path: string): Promise<OpenFile> => {
      let current = root;
      for (const name of path.split("/").slice(0, -1)) {
        const next = (await files.list(current)).find((c) => c.name === name && c.manifest.kind === "folder");
        current = next ?? (await files.create(current, name, "folder"));
      }
      return current;
    };
    const upload = async (path: string, bytes: Uint8Array, asPath = path) => {
      const existing = remoteFiles.get(asPath);
      if (existing) {
        await files.replace(existing, bytes);
        const reopened = await files.open(existing.manifest.node);
        return reopened.manifest.version;
      }
      const parent = await folderFor(asPath);
      const created = await files.create(parent, asPath.split("/").pop()!, "file", bytes);
      return created.manifest.version;
    };

    for (const action of planSync(remote, local, last, canWrite)) {
      const { path } = action;
      switch (action.kind) {
        case "download":
          await download(path);
          this.log(`↓ ${mount.name}/${path}`);
          break;
        case "upload": {
          const bytes = readFileSync(localPath(mount.local, path));
          last[path] = { version: await upload(path, bytes), hash: sha256(bytes) };
          this.log(`↑ ${mount.name}/${path}`);
          break;
        }
        case "conflict": {
          const ours = readFileSync(localPath(mount.local, path));
          const theirs = await download(path);
          if (sha256(theirs) !== sha256(ours)) {
            const copy = conflictName(path);
            const version = await upload(copy, ours, copy);
            const target = localPath(mount.local, copy);
            writeFileSync(target, ours);
            last[copy] = { version, hash: sha256(ours) };
            this.log(`⇅ ${mount.name}/${path}: both changed, the agent's version saved as ${copy}`);
          }
          break;
        }
        case "delete-local":
          rmSync(localPath(mount.local, path), { force: true });
          delete last[path];
          this.log(`✕ ${mount.name}/${path} (deleted by its owner)`);
          break;
        case "record":
        case "forget":
          if (action.kind === "forget") delete last[path];
          break;
      }
    }
  }
}

/** The line the agent sees with each message, so it knows where to work. */
export function folderContext(mounts: readonly FolderMount[], workspace: string): string {
  if (!mounts.length) return "";
  const lines = mounts.map((m) => {
    const where = relative(workspace, m.local).split(sep).join("/") || ".";
    const can = m.role === "write" || m.role === "admin" ? "you can add and edit files" : "read-only";
    return `- ${where} — "${m.name}", shared by ${m.drive} (${can})`;
  });
  return `[Poweur shared folders in your workspace, synced with their owner. Files you write there are saved to the owner's folder:\n${lines.join("\n")}]`;
}
