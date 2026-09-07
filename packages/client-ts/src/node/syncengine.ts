/**
 * The local sync engine — the twin of `apps/cli/internal/sync`.
 *
 * Node-only by nature: it walks a real directory and writes real files.
 * Browsers get the remote half (`SyncClient`) and nothing here.
 *
 * The reconciliation model is Dropbox-flavoured: the state DB at the sync
 * root records the last-synced content hash per path, and that hash is the
 * common base. Pull applies remote changes and preserves a locally-edited
 * loser as a "conflicted copy" rather than overwriting it; push is
 * last-writer-wins, which is why `run` pulls first.
 */

import { createHash } from "node:crypto";
import {
  createReadStream,
  existsSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  renameSync,
  rmSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { hostname } from "node:os";
import { dirname, join, posix, relative, sep } from "node:path";

import type { DavClient } from "../files.js";
import type { SyncClient } from "../sync.js";
import type { SyncChange, SyncEntry } from "../types.js";

export const STATE_FILE_NAME = ".poweur-sync.json";
export const IGNORE_FILE_NAME = ".poweurignore";

/**
 * Prefixes synced when none are given: everything except poweur-sys, which
 * is relay-managed config and only syncs when asked for explicitly.
 */
export const DEFAULT_ROOTS = ["public", "shared", "private", "apps"];

export interface FileState {
  sha?: string;
  size?: number;
  /**
   * Local mtime in nanoseconds at last sync — a dirty-check cache only.
   * JavaScript numbers cannot hold a full nanosecond timestamp exactly, so a
   * state file written by the Go CLI may round here; the cost is one extra
   * re-hash, never a wrong answer.
   */
  mtime?: number;
  dir?: boolean;
}

export interface SyncState {
  identity: string;
  cursor: string;
  files: Record<string, FileState>;
}

export interface SyncReport {
  downloaded: string[];
  uploaded: string[];
  deletedLocal: string[];
  deletedRemote: string[];
  conflicts: string[];
  mkdirLocal: string[];
  mkdirRemote: string[];
}

export function emptyReport(): SyncReport {
  return {
    downloaded: [], uploaded: [], deletedLocal: [], deletedRemote: [],
    conflicts: [], mkdirLocal: [], mkdirRemote: [],
  };
}

export function reportEmpty(report: SyncReport): boolean {
  return Object.values(report).every((list) => list.length === 0);
}

export interface SyncStatus {
  localNew: string[];
  localModified: string[];
  localDeleted: string[];
  remotePending: number;
  needsResync: boolean;
}

export function loadState(root: string): SyncState {
  const path = join(root, STATE_FILE_NAME);
  if (!existsSync(path)) return { identity: "", cursor: "", files: {} };
  const parsed = JSON.parse(readFileSync(path, "utf8")) as Partial<SyncState>;
  return {
    identity: parsed.identity ?? "",
    cursor: parsed.cursor ?? "",
    files: parsed.files ?? {},
  };
}

export function saveState(root: string, state: SyncState): void {
  const path = join(root, STATE_FILE_NAME);
  const tmp = `${path}.tmp`;
  writeFileSync(tmp, `${JSON.stringify(state, null, 2)}\n`, { mode: 0o600 });
  renameSync(tmp, path);
}

/**
 * `.poweurignore` matching — a gitignore-flavoured subset: one glob per line,
 * `#` comments, a trailing `/` anchors a directory (its subtree is skipped),
 * patterns without `/` match the basename anywhere. No negation in v1.
 */
export class Ignore {
  readonly patterns: string[];

  constructor(patterns: string[] = []) {
    this.patterns = patterns;
  }

  static load(root: string): Ignore {
    const path = join(root, IGNORE_FILE_NAME);
    if (!existsSync(path)) return new Ignore();
    return new Ignore(
      readFileSync(path, "utf8")
        .split("\n")
        .map((line) => line.trim())
        .filter((line) => line !== "" && !line.startsWith("#")),
    );
  }

  match(path: string): boolean {
    const base = posix.basename(path);
    if (base === STATE_FILE_NAME || base === IGNORE_FILE_NAME) return true;
    for (const pattern of this.patterns) {
      const dirPattern = pattern.replace(/\/$/, "");
      const anchored = dirPattern.includes("/");
      let probe = path;
      while (probe !== "." && probe !== "" && probe !== "/") {
        const target = anchored ? probe : posix.basename(probe);
        if (target === dirPattern || globMatch(dirPattern, target)) return true;
        const parent = posix.dirname(probe);
        if (parent === probe) break;
        probe = parent;
      }
    }
    return false;
  }
}

/** `path.Match`-equivalent globbing: `*` and `?` do not cross a `/`. */
function globMatch(pattern: string, value: string): boolean {
  const escaped = pattern.replace(/[.+^${}()|[\]\\]/g, "\\$&");
  const regex = new RegExp(`^${escaped.replace(/\*/g, "[^/]*").replace(/\?/g, "[^/]")}$`);
  return regex.test(value);
}

interface LocalEntry {
  size: number;
  mtimeNs: number;
  dir: boolean;
}

function under(path: string, prefix: string): boolean {
  return path === prefix || path.startsWith(`${prefix}/`);
}

export interface SyncEngineOptions {
  root: string;
  dav: DavClient;
  sync: SyncClient;
  state: SyncState;
  ignore?: Ignore;
  /** Tree prefixes in scope (DEFAULT_ROOTS when empty). */
  roots?: string[];
  /** Names this client in conflicted-copy filenames. */
  device?: string;
  log?: (message: string) => void;
}

export class SyncEngine {
  readonly root: string;
  readonly state: SyncState;
  readonly #dav: DavClient;
  readonly #sync: SyncClient;
  readonly #ignore: Ignore;
  readonly #roots: string[];
  readonly #device: string;
  readonly #log: (message: string) => void;

  constructor(options: SyncEngineOptions) {
    this.root = options.root;
    this.state = options.state;
    this.#dav = options.dav;
    this.#sync = options.sync;
    this.#ignore = options.ignore ?? new Ignore();
    this.#roots = options.roots?.length ? options.roots : DEFAULT_ROOTS;
    this.#device = options.device || hostname() || "this device";
    this.#log = options.log ?? (() => {});
  }

  #inScope(path: string): boolean {
    if (path === "" || this.#ignore.match(path)) return false;
    // `under(root, path)` keeps ancestors of a root in scope so the walk can
    // descend into e.g. `shared/` when only `shared/project` was requested.
    return this.#roots.some((root) => under(path, root) || under(root, path));
  }

  #full(treePath: string): string {
    return join(this.root, treePath.split("/").join(sep));
  }

  #scanLocal(): Map<string, LocalEntry> {
    const out = new Map<string, LocalEntry>();
    const walk = (dir: string): void => {
      let entries;
      try {
        entries = readdirSync(dir, { withFileTypes: true });
      } catch {
        return;
      }
      for (const entry of entries) {
        const absolute = join(dir, entry.name);
        const tree = relative(this.root, absolute).split(sep).join("/");
        // Symlinks are invisible by spec — never followed, never uploaded.
        if (entry.isSymbolicLink()) continue;
        if (!this.#inScope(tree)) continue;
        const stats = statSync(absolute, { bigint: true });
        out.set(tree, {
          size: Number(stats.size),
          mtimeNs: Number(stats.mtimeNs),
          dir: entry.isDirectory(),
        });
        if (entry.isDirectory()) walk(absolute);
      }
    };
    if (existsSync(this.root)) walk(this.root);
    return out;
  }

  /** Content hash, reusing the cached one when size+mtime are unchanged. */
  async #localSha(tree: string, entry: LocalEntry): Promise<string> {
    const cached = this.state.files[tree];
    if (cached && !cached.dir && cached.size === entry.size && cached.mtime === entry.mtimeNs) {
      return cached.sha ?? "";
    }
    return new Promise((resolve, reject) => {
      const hash = createHash("sha256");
      const stream = createReadStream(this.#full(tree));
      stream.on("data", (chunk) => hash.update(chunk));
      stream.on("error", reject);
      stream.on("end", () => resolve(hash.digest("hex")));
    });
  }

  #remember(tree: string, sha: string): void {
    try {
      const stats = statSync(this.#full(tree), { bigint: true });
      this.state.files[tree] = {
        sha,
        size: Number(stats.size),
        mtime: Number(stats.mtimeNs),
      };
    } catch {
      // The file vanished under us; the next scan will handle it.
    }
  }

  /** Dropbox/Syncthing-style rename for the losing side of a conflict. */
  #conflictedName(tree: string): string {
    const dir = tree.includes("/") ? `${posix.dirname(tree)}/` : "";
    const base = posix.basename(tree);
    const dot = base.lastIndexOf(".");
    const ext = dot > 0 ? base.slice(dot) : "";
    const stem = dot > 0 ? base.slice(0, dot) : base;
    const stamp = new Date().toISOString().slice(0, 10);
    let candidate = `${dir}${stem} (conflicted copy from ${this.#device} ${stamp})${ext}`;
    for (let i = 2; existsSync(this.#full(candidate)); i++) {
      candidate = `${dir}${stem} (conflicted copy from ${this.#device} ${stamp} ${i})${ext}`;
    }
    return candidate;
  }

  async #download(tree: string): Promise<void> {
    const bytes = await this.#dav.readBytes(tree);
    const full = this.#full(tree);
    mkdirSync(dirname(full), { recursive: true, mode: 0o700 });
    const tmp = `${full}.poweur-tmp`;
    writeFileSync(tmp, bytes, { mode: 0o600 });
    renameSync(tmp, full);
    this.#remember(tree, createHash("sha256").update(bytes).digest("hex"));
  }

  async #applyRemotePut(tree: string, etag: string, report: SyncReport): Promise<void> {
    const full = this.#full(tree);
    const base = this.state.files[tree];
    const exists = existsSync(full);
    const isDir = exists && statSync(full).isDirectory();

    if (!exists || isDir) {
      // A local delete of exactly our base wins; push will propagate it.
      if (base && base.sha === etag) return;
      await this.#download(tree);
      report.downloaded.push(tree);
      return;
    }

    const stats = statSync(full, { bigint: true });
    const localSha = await this.#localSha(tree, {
      size: Number(stats.size),
      mtimeNs: Number(stats.mtimeNs),
      dir: false,
    });

    if (localSha === etag) {
      // Both sides already agree — usually our own push echoing back.
      this.#remember(tree, localSha);
      return;
    }
    if (base && localSha === base.sha) {
      await this.#download(tree);
      report.downloaded.push(tree);
      return;
    }
    // Both sides changed: keep ours under a conflicted name, take theirs.
    const loser = this.#conflictedName(tree);
    renameSync(full, this.#full(loser));
    await this.#download(tree);
    report.conflicts.push(loser);
    report.downloaded.push(tree);
    this.#log(`conflict on ${tree} — local version kept as ${loser}`);
  }

  async #applyRemoteDelete(tree: string, report: SyncReport): Promise<void> {
    const local = this.#scanLocal();
    const affected = [...local.keys()].filter((p) => under(p, tree)).sort().reverse();
    for (const path of affected) {
      const entry = local.get(path) as LocalEntry;
      const full = this.#full(path);
      if (entry.dir) {
        // Directories go only when empty — a dirty child keeps its parent.
        try {
          rmSync(full, { recursive: false });
          report.deletedLocal.push(path);
          delete this.state.files[path];
        } catch {
          // Non-empty: leave it and let the next run reconsider.
        }
        continue;
      }
      const base = this.state.files[path];
      const localSha = await this.#localSha(path, entry);
      if (base && localSha === base.sha) {
        rmSync(full, { force: true });
        report.deletedLocal.push(path);
      } else {
        // Local edit vs remote delete: the edit wins and push re-uploads it.
        this.#log(`remote deleted ${path} but local copy changed — keeping local`);
      }
      delete this.state.files[path];
    }
    for (const path of Object.keys(this.state.files)) {
      if (under(path, tree)) delete this.state.files[path];
    }
  }

  #applyRemoteMkdir(tree: string, report: SyncReport): void {
    const full = this.#full(tree);
    if (!existsSync(full)) {
      const base = this.state.files[tree];
      // A tracked directory with no local copy was deleted here; recreating
      // it would resurrect it on every run.
      if (base?.dir) return;
      mkdirSync(full, { recursive: true, mode: 0o700 });
      report.mkdirLocal.push(tree);
    }
    this.state.files[tree] = { dir: true };
  }

  /** Apply remote changes locally: incremental, or a manifest full-resync. */
  async pull(): Promise<SyncReport> {
    const report = emptyReport();
    const { changes, cursor, fullResync } = await this.#sync.changes(this.state.cursor);
    if (fullResync || this.state.cursor === "") return this.#pullFromManifest();
    for (const change of changes) {
      if (!change.path || !this.#inScope(change.path)) continue;
      await this.#applyChange(change, report);
    }
    this.state.cursor = cursor;
    saveState(this.root, this.state);
    return report;
  }

  async #applyChange(change: SyncChange, report: SyncReport): Promise<void> {
    switch (change.op) {
      case "put":
        await this.#applyRemotePut(change.path, change.etag ?? change.sha ?? "", report);
        break;
      case "mkdir":
        this.#applyRemoteMkdir(change.path, report);
        break;
      case "delete":
        await this.#applyRemoteDelete(change.path, report);
        break;
      default:
        // Unknown ops are ignored so an older client survives a newer relay.
        break;
    }
  }

  async #pullFromManifest(): Promise<SyncReport> {
    const report = emptyReport();
    const { entries, cursor } = await this.#sync.manifest();
    const remote = new Map<string, SyncEntry>();
    for (const entry of entries) {
      if (!this.#inScope(entry.path)) continue;
      remote.set(entry.path, entry);
    }
    // Parents before children so directories exist before their contents.
    for (const path of [...remote.keys()].sort()) {
      const entry = remote.get(path) as SyncEntry;
      if (entry.dir) this.#applyRemoteMkdir(path, report);
      else await this.#applyRemotePut(path, entry.etag ?? entry.sha ?? "", report);
    }
    // Tracked paths that vanished remotely are remote deletes.
    const gone = Object.keys(this.state.files)
      .filter((path) => !remote.has(path) && this.#inScope(path))
      .sort()
      .reverse();
    for (const path of gone) {
      if (!(path in this.state.files)) continue; // an ancestor already covered it
      await this.#applyRemoteDelete(path, report);
    }
    this.state.cursor = cursor;
    saveState(this.root, this.state);
    return report;
  }

  /** Upload local changes. Last-writer-wins — pull first for conflict safety. */
  async push(): Promise<SyncReport> {
    const report = emptyReport();
    const local = this.#scanLocal();

    for (const path of [...local.keys()].sort()) {
      const entry = local.get(path) as LocalEntry;
      if (entry.dir) {
        // The five top-level roots always exist remotely and MKCOL on them is
        // forbidden; only subdirectories need creating.
        if (!path.includes("/")) continue;
        const tracked = this.state.files[path];
        if (!tracked?.dir) {
          await this.#dav.mkdir(path).catch((error: unknown) => {
            // 405 means it already exists, which is the desired state.
            const status = (error as { status?: number }).status;
            if (status !== 405) throw error;
          });
          this.state.files[path] = { dir: true };
          report.mkdirRemote.push(path);
        }
        continue;
      }
      const localSha = await this.#localSha(path, entry);
      const tracked = this.state.files[path];
      if (tracked && tracked.sha === localSha) {
        this.#remember(path, localSha); // refresh the mtime cache
        continue;
      }
      await this.#sync.upload(path, readFileSync(this.#full(path)));
      this.#remember(path, localSha);
      report.uploaded.push(path);
    }

    // Local deletions: tracked paths with no local counterpart, shallowest
    // first so an ancestor delete subsumes its children.
    const gone = Object.keys(this.state.files).filter((path) => !local.has(path)).sort();
    const deleted = new Set<string>();
    for (const path of gone) {
      let covered = false;
      for (let probe = posix.dirname(path); probe !== "." && probe !== "/"; probe = posix.dirname(probe)) {
        if (deleted.has(probe)) {
          covered = true;
          break;
        }
      }
      if (!covered) {
        await this.#dav.remove(path);
        deleted.add(path);
        report.deletedRemote.push(path);
      }
      delete this.state.files[path];
    }

    saveState(this.root, this.state);
    return report;
  }

  /** Pull then push — the bidirectional one-shot. */
  async run(): Promise<{ pull: SyncReport; push: SyncReport }> {
    return { pull: await this.pull(), push: await this.push() };
  }

  /** Pending work on both sides, changing nothing. */
  async status(): Promise<SyncStatus> {
    const status: SyncStatus = {
      localNew: [], localModified: [], localDeleted: [],
      remotePending: 0, needsResync: false,
    };
    const local = this.#scanLocal();
    for (const [path, entry] of local) {
      if (entry.dir) continue;
      const base = this.state.files[path];
      if (!base) {
        status.localNew.push(path);
        continue;
      }
      if ((await this.#localSha(path, entry)) !== base.sha) {
        status.localModified.push(path);
      }
    }
    for (const [path, base] of Object.entries(this.state.files)) {
      if (base.dir) continue;
      if (!local.has(path)) status.localDeleted.push(path);
    }
    status.localNew.sort();
    status.localModified.sort();
    status.localDeleted.sort();

    const { changes, fullResync } = await this.#sync.changes(this.state.cursor);
    status.needsResync = fullResync || this.state.cursor === "";
    status.remotePending = changes.filter((c) => c.path && this.#inScope(c.path)).length;
    return status;
  }
}
