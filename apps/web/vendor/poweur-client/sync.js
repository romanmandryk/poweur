/**
 * Sync protocol client: the changes feed, the manifest, and resumable
 * chunked upload. This is the runtime-agnostic *remote* half — it never
 * touches a filesystem, so it works in a browser too. The local
 * reconciliation engine (`poweur sync pull|push`) needs real files and lives
 * in `./node`.
 */
import { PoweurError, RelayError } from "./errors.js";
/** Files at or above this size go through the resumable endpoint. */
export const DEFAULT_CHUNK_THRESHOLD = 64 * 1024 * 1024;
export const DEFAULT_CHUNK_SIZE = 8 * 1024 * 1024;
export class SyncClient {
    client;
    identity;
    token;
    #chunkThreshold;
    #chunkSize;
    constructor(client, identity, token, options = {}) {
        this.client = client;
        this.identity = identity;
        this.token = token;
        this.#chunkThreshold = options.chunkThreshold ?? DEFAULT_CHUNK_THRESHOLD;
        this.#chunkSize = options.chunkSize ?? DEFAULT_CHUNK_SIZE;
    }
    #path(suffix) {
        return `/sync/${encodeURIComponent(this.identity)}${suffix}`;
    }
    #auth() {
        return { Authorization: `Bearer ${this.token}` };
    }
    /**
     * The full manifest as NDJSON: a header line carrying the cursor, then one
     * line per path. Optionally filtered to a set of tree prefixes.
     */
    async manifest(paths) {
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
        const entries = [];
        lines.forEach((line, index) => {
            const parsed = JSON.parse(line);
            if (index === 0) {
                if (typeof parsed["manifest"] !== "number" || parsed["manifest"] < 1) {
                    throw new PoweurError("relay_error", "manifest: malformed header");
                }
                cursor = String(parsed["cursor"] ?? "");
                return;
            }
            entries.push(parsed);
        });
        return { entries, cursor };
    }
    /**
     * Drain the changes feed from `since` to the latest cursor.
     * `fullResync` means the relay's journal no longer covers our cursor and
     * the caller must fall back to a manifest comparison.
     */
    async changes(since = "") {
        const all = [];
        let cursor = since;
        for (;;) {
            const query = cursor ? `?since=${encodeURIComponent(cursor)}` : "";
            const page = await this.client.request({
                method: "GET",
                path: this.#path(`/changes${query}`),
                headers: this.#auth(),
            });
            if (page.full_resync)
                return { changes: [], cursor: since, fullResync: true };
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
    async upload(path, body) {
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
    async uploadChunked(path, body) {
        const created = await this.client.request({
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
    async uploadStatus(id) {
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
    async cancelUpload(id) {
        await this.client.raw({
            method: "DELETE",
            path: this.#path(`/upload/${encodeURIComponent(id)}`),
            headers: this.#auth(),
        });
    }
}
