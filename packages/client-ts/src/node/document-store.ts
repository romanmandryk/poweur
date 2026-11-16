/**
 * File-backed `DocumentStore`: one JSON file per identity, in the format the
 * Go CLI writes (`packages/identity/store.go`), so both CLIs share
 * `~/.poweur/cache/identities/`.
 *
 *   { "identity": "...", "fetched_at": "<RFC3339>", "document": { ... } }
 */

import { mkdirSync, readFileSync, renameSync, rmSync, utimesSync, writeFileSync } from "node:fs";
import { randomBytes } from "node:crypto";
import { join } from "node:path";

import { documentStoreKey, type DocumentStore, type StoredDocument } from "../document-store.js";
import { rfc3339 } from "../encoding.js";
import { validateIdentityName } from "../names.js";
import { identityCacheDir } from "./paths.js";

export class FileDocumentStore implements DocumentStore {
  readonly #dir: string;

  constructor(dir: string = identityCacheDir()) {
    this.#dir = dir;
  }

  #path(identity: string): string {
    const key = documentStoreKey(identity);
    validateIdentityName(key);
    // The name becomes a file name; validateIdentityName already limits it to
    // DNS characters, this is the second lock.
    if (/[\\/]/.test(key) || key.includes("..")) {
      throw new Error(`identity ${key} is not a safe file name`);
    }
    return join(this.#dir, `${key}.json`);
  }

  async get(identity: string): Promise<StoredDocument | null> {
    try {
      const key = documentStoreKey(identity);
      const file = JSON.parse(readFileSync(this.#path(identity), "utf8")) as {
        identity?: string;
        fetched_at?: string;
        document?: unknown;
      };
      const fetchedAt = Date.parse(file.fetched_at ?? "");
      if (!file.document || Number.isNaN(fetchedAt) || documentStoreKey(file.identity ?? "") !== key) {
        return null;
      }
      return { identity: key, raw: JSON.stringify(file.document), fetchedAt };
    } catch {
      return null;
    }
  }

  async put(identity: string, raw: string, fetchedAt: number): Promise<void> {
    const path = this.#path(identity);
    const body = JSON.stringify({
      identity: documentStoreKey(identity),
      fetched_at: rfc3339(new Date(fetchedAt)),
      document: JSON.parse(raw) as unknown,
    });
    mkdirSync(this.#dir, { recursive: true, mode: 0o700 });
    const tmp = join(this.#dir, `.tmp-${randomBytes(8).toString("hex")}`);
    try {
      writeFileSync(tmp, `${body}\n`, { mode: 0o600 });
      renameSync(tmp, path);
    } catch (error) {
      rmSync(tmp, { force: true });
      throw error;
    }
    // Go's store reads fetched_at back from the mtime when it rebuilds its
    // eviction index, so keep the two in step.
    try {
      const at = new Date(fetchedAt);
      utimesSync(path, at, at);
    } catch {
      // The file is written; a filesystem without settable times is fine.
    }
  }
}
