/**
 * An owner-only, end-to-end encrypted JSON-lines log on the drive: one
 * append record per entry, sealed inline and padded like chunks so the relay
 * learns neither content nor length. Used for the sign-in consent log
 * (`.poweur/private/logs/auth.log`, EPIC-008 E08-T3).
 */
import { utf8 } from "../encoding.js";
import { RelayError } from "../errors.js";
import { PADDING_BUCKET } from "./crypto.js";
import type { DriveFiles, OpenFile } from "./files.js";

function pad(bytes: Uint8Array): Uint8Array {
  const out = new Uint8Array(Math.ceil(Math.max(bytes.length, 1) / PADDING_BUCKET) * PADDING_BUCKET).fill(0x20);
  out.set(bytes);
  return out;
}

export class DriveJsonLog<T> {
  readonly #files: DriveFiles;
  readonly #path: string[];
  readonly #name: string;

  /** `path` is the log's location from the drive root, e.g.
   * `.poweur/private/logs/auth.log`. */
  constructor(files: DriveFiles, path: string) {
    const parts = path.split("/").filter(Boolean);
    if (parts.length < 1) throw new Error("log path needs a file name");
    this.#files = files;
    this.#name = parts.pop()!;
    this.#path = parts;
  }

  async #folder(parent: OpenFile, name: string, create: boolean): Promise<OpenFile | undefined> {
    const find = async () => (await this.#files.list(parent)).find((child) => child.name === name && child.manifest.kind === "folder");
    const found = await find();
    if (found || !create) return found;
    try { return await this.#files.create(parent, name, "folder"); }
    catch (error) {
      const again = error instanceof RelayError && error.status === 409 ? await find() : undefined;
      if (!again) throw error;
      return again;
    }
  }

  async #file(create: boolean): Promise<OpenFile | undefined> {
    let dir: OpenFile | undefined = await this.#files.root();
    for (const name of this.#path) {
      dir = await this.#folder(dir, name, create);
      if (!dir) return undefined;
    }
    const existing = (await this.#files.list(dir)).find((child) => child.name === this.#name);
    if (existing) {
      if (existing.manifest.mode !== "append") throw new Error(`${this.#name} is not an append log`);
      return existing;
    }
    return create ? this.#files.create(dir, this.#name, "file", new Uint8Array(), "append") : undefined;
  }

  /** Add one entry. */
  async append(entry: T): Promise<void> {
    const file = (await this.#file(true))!;
    await this.#files.append(file, pad(utf8(JSON.stringify(entry))), { inline: true });
  }

  /** The newest `limit` entries, oldest first. Entries that do not parse are
   * skipped: one bad write must not cost the rest of the log. */
  async recent(limit = 50): Promise<T[]> {
    const file = await this.#file(false);
    if (!file || !file.position) return [];
    const from = Math.max(1, file.trimmedBefore ?? 1, file.position - limit + 1);
    const out: T[] = [];
    for (const row of await this.#files.tail(file, from)) {
      try { out.push(JSON.parse(new TextDecoder().decode(row.plain).trim()) as T); } catch { /* skipped */ }
    }
    return out;
  }
}
