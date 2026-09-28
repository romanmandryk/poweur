/** Authenticated storage-v2 transport. Callers retain custody of all keys. */
import type { Signer } from "../crypto/keys.js";
import { randomBytes } from "../encoding.js";
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
  share?: Share;
  unshare?: { id: string };
  transfer?: { node: string; to: string; to_node: string };
}
export interface CommitResult { seq: number; head: string; positions: number[] | null }
export interface DriveNode {
  id: string; head: string; generation: number; kind: "file" | "folder";
  mode?: "replace" | "append"; folder?: string; removed?: boolean; position?: number;
}
export interface DriveChange { seq: number; node?: string; operation: string; version?: string; position?: number }
export interface ChunkCache {
  get(id: string): Promise<Uint8Array | null>;
  put(id: string, bytes: Uint8Array): Promise<void>;
}
const segment = (value: string) => encodeURIComponent(value);

export class DriveClient {
  constructor(readonly relay: RelayClient, readonly signer: Signer, readonly drive = signer.identity,
    readonly cache?: ChunkCache, readonly sessionId?: string) {}

  private async auth(): Promise<Record<string, string>> {
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
  async upload(bytes: Uint8Array): Promise<ChunkRef> {
    const ref = { id: chunkID(bytes), size: bytes.length };
    await this.request("PUT", `/chunks/${ref.id}`, bytes);
    await this.cache?.put(ref.id, bytes);
    return ref;
  }
  /** Download ciphertext through its authorized node/version reference and
   * verify its address even when a local cache supplied the bytes. */
  async chunk(node: string, ref: ChunkRef, version?: string): Promise<Uint8Array> {
    const cached = await this.cache?.get(ref.id);
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
    await this.cache?.put(ref.id, bytes);
    return bytes;
  }
}
