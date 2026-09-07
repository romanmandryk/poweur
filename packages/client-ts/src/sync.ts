/**
 * Sync protocol client: the changes feed, the manifest, and resumable
 * chunked upload. This is the runtime-agnostic *remote* half — it never
 * touches a filesystem, so it works in a browser too. The local
 * reconciliation engine (`poweur sync pull|push`) needs real files and lives
 * in `./node`.
 */

import { PoweurError, RelayError } from "./errors.js";
import type { RelayClient } from "./http.js";
import type { SyncChange, SyncEntry } from "./types.js";

/** Files at or above this size go through the resumable endpoint. */
export const DEFAULT_CHUNK_THRESHOLD = 64 * 1024 * 1024;
export const DEFAULT_CHUNK_SIZE = 8 * 1024 * 1024;

export interface SyncClientOptions {
  chunkThreshold?: number;
  chunkSize?: number;
}

export class SyncClient {
  readonly client: RelayClient;
  readonly identity: string;
  readonly token: string;
  readonly #chunkThreshold: number;
  readonly #chunkSize: number;

  constructor(
    client: RelayClient,
    identity: string,
    token: string,
    options: SyncClientOptions = {},
  ) {
    this.client = client;
    this.identity = identity;
    this.token = token;
    this.#chunkThreshold = options.chunkThreshold ?? DEFAULT_CHUNK_THRESHOLD;
    this.#chunkSize = options.chunkSize ?? DEFAULT_CHUNK_SIZE;
  }

  #path(suffix: string): string {
    return `/sync/${encodeURIComponent(this.identity)}${suffix}`;
  }

  #auth(): Record<string, string> {
    return { Authorization: `Bearer ${this.token}` };
  }

  /**
   * The full manifest as NDJSON: a header line carrying the cursor, then one
   * line per path. Optionally filtered to a set of tree prefixes.
   */
  async manifest(paths?: string[]): Promise<{ entries: SyncEntry[]; cursor: string }> {
    const query = paths?.length
      ? `?paths=${paths.map((p) => encodeURIComponent(`/${p.replace(/^\/+|\/+$/g, "")}/`)).join(",")}`
      : "";
    const response = await this.client.raw({
      method: "GET",
      path: this.#path(`/manifest${query}`),
      headers: this.#auth(),
    });
    if (!response.ok) {
      throw new RelayError(`manifest failed: HTTP ${response.status}`, {
        status: response.status,
      });
    }
    const lines = (await response.text()).split("\n").filter((line) => line.trim() !== "");
    let cursor = "";
    const entries: SyncEntry[] = [];
    lines.forEach((line, index) => {
      const parsed = JSON.parse(line) as Record<string, unknown>;
      if (index === 0) {
        if (typeof parsed["manifest"] !== "number" || (parsed["manifest"] as number) < 1) {
          throw new PoweurError("relay_error", "manifest: malformed header");
        }
        cursor = String(parsed["cursor"] ?? "");
        return;
      }
      entries.push(parsed as unknown as SyncEntry);
    });
    return { entries, cursor };
  }

  /**
   * Drain the changes feed from `since` to the latest cursor.
   * `fullResync` means the relay's journal no longer covers our cursor and
   * the caller must fall back to a manifest comparison.
   */
  async changes(
    since = "",
  ): Promise<{ changes: SyncChange[]; cursor: string; fullResync: boolean }> {
    const all: SyncChange[] = [];
    let cursor = since;
    for (;;) {
      const query = cursor ? `?since=${encodeURIComponent(cursor)}` : "";
      const page = await this.client.request<{
        next?: string;
        latest?: string;
        full_resync?: boolean;
        changes?: SyncChange[];
      }>({
        method: "GET",
        path: this.#path(`/changes${query}`),
        headers: this.#auth(),
      });
      if (page.full_resync) return { changes: [], cursor: since, fullResync: true };
      const batch = page.changes ?? [];
      all.push(...batch);
      cursor = page.next ?? cursor;
      if (page.next === page.latest || batch.length === 0) {
        return { changes: all, cursor, fullResync: false };
      }
    }
  }

  /**
   * Upload bytes to a tree path, switching to the resumable endpoint above
   * the chunk threshold. `Content-Length` is mandatory — the relay checks it
   * against the quota before accepting a byte.
   */
  async upload(path: string, body: Uint8Array): Promise<void> {
    if (body.length >= this.#chunkThreshold) {
      return this.uploadChunked(path, body);
    }
    const response = await this.client.raw({
      method: "PUT",
      path: `/dav/${encodeURIComponent(this.identity)}/${path
        .replace(/^\/+/, "")
        .split("/")
        .map(encodeURIComponent)
        .join("/")}`,
      body,
      headers: { ...this.#auth(), "Content-Length": String(body.length) },
    });
    if (response.status >= 300) {
      throw new RelayError(`put ${path} failed: HTTP ${response.status}`, {
        status: response.status,
        detail: await response.text().catch(() => ""),
      });
    }
  }

  /** Resumable upload: create, then PATCH chunks at increasing offsets. */
  async uploadChunked(path: string, body: Uint8Array): Promise<void> {
    const created = await this.client.request<{ id: string }>({
      method: "POST",
      path: this.#path(`/upload?path=${encodeURIComponent(`/${path.replace(/^\/+/, "")}`)}`),
      headers: { ...this.#auth(), "Upload-Length": String(body.length) },
    });
    if (!created?.id) {
      throw new PoweurError("relay_error", `upload create ${path}: no upload id returned`);
    }
    let offset = 0;
    while (offset < body.length) {
      const chunk = body.subarray(offset, Math.min(offset + this.#chunkSize, body.length));
      const response = await this.client.raw({
        method: "PATCH",
        path: this.#path(`/upload/${encodeURIComponent(created.id)}`),
        body: chunk,
        headers: {
          ...this.#auth(),
          "Upload-Offset": String(offset),
          "Content-Type": "application/offset+octet-stream",
        },
      });
      if (response.status !== 204 && response.status !== 200) {
        throw new RelayError(`upload chunk ${path} @${offset}: HTTP ${response.status}`, {
          status: response.status,
        });
      }
      offset += chunk.length;
    }
  }

  /** Bytes already accepted for a resumable upload (HEAD → Upload-Offset). */
  async uploadStatus(id: string): Promise<number> {
    const response = await this.client.raw({
      method: "HEAD",
      path: this.#path(`/upload/${encodeURIComponent(id)}`),
      headers: this.#auth(),
    });
    if (!response.ok) {
      throw new RelayError(`upload status ${id}: HTTP ${response.status}`, {
        status: response.status,
      });
    }
    return Number(response.headers.get("Upload-Offset") ?? 0);
  }

  async cancelUpload(id: string): Promise<void> {
    await this.client.raw({
      method: "DELETE",
      path: this.#path(`/upload/${encodeURIComponent(id)}`),
      headers: this.#auth(),
    });
  }
}
