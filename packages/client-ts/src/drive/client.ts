/** Authenticated storage-v2 transport. Callers retain custody of all keys. */
import type { Signer } from "../crypto/keys.js";
import { randomBytes, toBase64url } from "../encoding.js";
import { RelayError } from "../errors.js";
import { RelayClient, toRelayError } from "../http.js";
import { chunkID } from "./crypto.js";
import { chunkPageHash, type ChunkPage, type Manifest } from "./manifest.js";
import type { AppendRecord, ChunkRef } from "./records.js";
import type { Share } from "./share.js";

export interface DriveCommit {
  id?: string;
  manifest?: Manifest;
  pages?: ChunkPage[];
  records?: AppendRecord[];
  trim?: { node: string; before: number; snapshot: { node: string; version: string } };
  share?: Share;
  unshare?: { id: string };
  transfer?: { node: string; to: string; to_node: string };
}
export interface DriveStreamEvent {
  type: string;
  identity: string;
  timestamp: string;
  drive?: { drive: string; seq: number; node?: string; operation: string; version?: string; position?: number };
}
export interface CommitResult { seq: number; head: string; positions: number[] | null }
export interface DriveNode {
  id: string; head: string; generation: number; kind: "file" | "folder";
  mode?: "replace" | "append"; folder?: string; removed?: boolean; position?: number;
  trimmed_before?: number;
  trim_snapshot?: { node: string; version: string };
}
export interface DriveChange { seq: number; node?: string; operation: string; version?: string; position?: number }
export interface ChunkCache {
  get(id: string): Promise<Uint8Array | null>;
  put(id: string, bytes: Uint8Array): Promise<void>;
}
const segment = (value: string) => encodeURIComponent(value);

export class DriveClient {
  /** Authenticate as a link holder instead of an identity; the signer is then
   * only a guest key (see guestAuthor). */
  link?: { id: string; verifier?: Uint8Array };
  constructor(readonly relay: RelayClient, readonly signer: Signer, readonly drive = signer.identity,
    readonly cache?: ChunkCache, readonly sessionId?: string) {}

  private async auth(): Promise<Record<string, string>> {
    if (this.link) {
      return { "X-Poweur-Link": this.link.id, ...(this.link.verifier ? { "X-Poweur-Link-Verifier": toBase64url(this.link.verifier) } : {}) };
    }
    const { challenge } = await this.relay.request<{ challenge: string }>({
      method: "GET", path: `/auth/challenge?identity=${segment(this.signer.identity)}`,
    });
    return { ...(this.sessionId ? { "X-Poweur-Session-Id": this.sessionId } : {}), "X-Poweur-Identity": this.signer.identity, "X-Poweur-Challenge": challenge,
      "X-Poweur-Signature": await this.signer.sign(challenge, "base64std") };
  }
  private path(suffix: string): string { return `/drive/${segment(this.drive)}${suffix}`; }
  private async request<T>(method: string, suffix: string, body?: unknown): Promise<T> {
    return this.relay.request<T>({ method, path: this.path(suffix), body, headers: await this.auth() });
  }
  info(): Promise<{ drive: string; root: string; used: number; quota: number }> {
    return this.request("GET", "");
  }
  node(node: string): Promise<DriveNode> { return this.request("GET", `/nodes/${segment(node)}`); }
  /** Shares on a node and its ancestors, for anyone who may read the node. */
  nodeShares(node: string): Promise<{ shares: Share[] }> { return this.request("GET", `/nodes/${segment(node)}/shares`); }
  children(node: string, cursor = "", limit = 100): Promise<{ children: DriveNode[]; cursor: string }> {
    return this.request("GET", `/nodes/${segment(node)}/children?cursor=${segment(cursor)}&limit=${limit}`);
  }
  changes(cursor = "0", limit = 100): Promise<{ changes: DriveChange[]; cursor: string }> {
    return this.request("GET", `/changes?cursor=${segment(cursor)}&limit=${limit}`);
  }
  records(node: string, from = 0, limit = 100): Promise<{ records: { position: number; record: AppendRecord }[]; next: number }> {
    return this.request("GET", `/nodes/${segment(node)}/records?from=${from}&limit=${limit}`);
  }
  version(node: string, version: string): Promise<Manifest> {
    return this.request("GET", `/nodes/${segment(node)}/versions/${segment(version)}`);
  }
  async page(node: string, version: string, hash: string): Promise<ChunkPage> {
    const page = await this.request<ChunkPage>("GET", `/nodes/${segment(node)}/versions/${segment(version)}/pages/${segment(hash)}`);
    if (page.drive !== this.drive || page.node !== node || chunkPageHash(page) !== hash) throw new Error("invalid chunk page");
    return page;
  }
  /** Retry only transient failures; reuse the exact request ID and bytes. A
   * head conflict must be merged by the caller, never silently overwritten. */
  async commit(input: DriveCommit): Promise<CommitResult> {
    const id = input.id ?? Array.from(randomBytes(16), b => b.toString(16).padStart(2, "0")).join("");
    const body = JSON.stringify({ ...input, id });
    for (let attempt = 0; ; attempt++) {
      try { return await this.request("POST", "/commit", body); }
      catch (error) {
        const transient = error instanceof TypeError || (error instanceof RelayError && [502, 503, 504].includes(error.status ?? 0));
        if (!transient || attempt >= 2) throw error;
        await new Promise(resolve => setTimeout(resolve, 100 * 2 ** attempt));
      }
    }
  }
  /** Upload only chunks the relay does not already have. Presigned URLs go
   * straight to the object store, with the provider's headers and no Poweur credential. */
  async store(chunks: Uint8Array[]): Promise<ChunkRef[]> {
    const refs = chunks.map(bytes => ({ id: chunkID(bytes), size: bytes.length }));
    const byID = new Map(refs.map((ref, index) => [ref.id, chunks[index]!]));
    for (let start = 0; start < refs.length; start += 1024) {
      const batch = refs.slice(start, start + 1024);
      const body = await this.request<{ missing: { id: string; size: number; upload: { method?: string; url: string; headers?: Record<string, string> } }[] }>("POST", "/chunks/missing", { chunks: batch });
      for (const item of body.missing) {
        const bytes = byID.get(item.id);
        if (!bytes || bytes.length !== item.size) throw new Error("missing chunk is not in this upload");
        await this.putChunk(item.upload, bytes);
      }
    }
    for (const [id, bytes] of byID) await this.cache?.put(id, bytes).catch(() => {});
    return refs;
  }
  private async putChunk(upload: { method?: string; url: string; headers?: Record<string, string> }, bytes: Uint8Array): Promise<void> {
    const absolute = /^https?:\/\//.test(upload.url);
    const headers = { ...(upload.headers ?? {}), ...(absolute ? {} : await this.auth()) };
    const response = await this.relay.raw({ method: upload.method || "PUT", path: upload.url, body: bytes, headers, redirect: "error" });
    if (!response.ok) {
      const text = await response.text();
      throw toRelayError(response.status, text, undefined);
    }
  }
  async upload(bytes: Uint8Array): Promise<ChunkRef> {
    const [ref] = await this.store([bytes]);
    if (!ref) throw new Error("upload produced no chunk");
    return ref;
  }
  history(node: string): Promise<{ versions: string[] }> {
    return this.request("GET", `/nodes/${segment(node)}/history`);
  }
  shares(): Promise<{ shares: Share[] }> { return this.request("GET", "/shares"); }
  unshare(id: string): Promise<CommitResult> { return this.commit({ unshare: { id } }); }
  /** Advisory `drive.changed` stream. A missed event is recovered from `changes`. */
  async subscribe(onEvent: (event: DriveStreamEvent) => void | Promise<void>, signal?: AbortSignal): Promise<void> {
    const headers = { ...(await this.auth()), Accept: "text/event-stream" };
    const response = await this.relay.raw({ method: "GET", path: this.path("/events"), headers, stream: true, ...(signal ? { signal } : {}) });
    if (!response.ok || !response.body) throw new Error(`drive stream refused (${response.status})`);
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    try {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) return;
        buffer += decoder.decode(value, { stream: true });
        let split = buffer.indexOf("\n\n");
        while (split >= 0) {
          const data = buffer.slice(0, split).split("\n").filter(line => line.startsWith("data:")).map(line => line.slice(5).trim()).join("\n");
          buffer = buffer.slice(split + 2);
          if (data) {
            try { await onEvent(JSON.parse(data) as DriveStreamEvent); } catch (error) { if (error instanceof SyntaxError) continue; throw error; }
          }
          split = buffer.indexOf("\n\n");
        }
      }
    } finally {
      await reader.cancel().catch(() => {});
    }
  }
  /** Download ciphertext through its authorized node/version reference and
   * verify its address even when a local cache supplied the bytes. */
  async chunk(node: string, ref: ChunkRef, version?: string): Promise<Uint8Array> {
    const cached = await this.cache?.get(ref.id).catch(() => null);
    if (cached && cached.length === ref.size && chunkID(cached) === ref.id) return cached;
    const suffix = `/nodes/${segment(node)}${version ? `/versions/${segment(version)}` : ""}/chunks/${segment(ref.id)}`;
    const response = await this.relay.raw({ method: "GET", path: this.path(suffix), headers: await this.auth() });
    if (!response.ok) {
      const text = await response.text();
      let data: unknown; try { data = JSON.parse(text); } catch { /* non-JSON relay failure */ }
      throw toRelayError(response.status, text, data);
    }
    const bytes = new Uint8Array(await response.arrayBuffer());
    if (bytes.length !== ref.size || chunkID(bytes) !== ref.id) throw new Error("invalid chunk hash or size");
    await this.cache?.put(ref.id, bytes).catch(() => {});
    return bytes;
  }
}
