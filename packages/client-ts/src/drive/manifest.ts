/**
 * Signed version manifests and chunk-list pages (E20-T2); Go
 * packages/identity/drive/manifest.go is authoritative and the vectors in
 * drive-manifests.json pin both implementations.
 *
 * A manifest is one signed version of a node: a create, a content replace, a
 * move, a key rotation or a removal, plus the node's complete ordered content
 * as hashes of immutable pages of at most PAGE_SIZE chunk references.
 */
import { sha256 } from "@noble/hashes/sha2.js";
import { signBytes, verifyBytes } from "../crypto/index.js";
import { concatBytes, toBase64url, utf8 } from "../encoding.js";
import { driveContext } from "./crypto.js";
import { MAX_COUNTER, hex, lengthPrefixed, sealedFields, strictBase64, validChunk, validHex, validIdentity, validatePayload } from "./internal.js";
import type { ChunkRef, DriveSealedPayload } from "./records.js";

export const PAGE_SIZE = 1024;
export const MANIFEST_OPERATIONS = ["create", "replace", "move", "rotate", "remove"] as const;
export type ManifestOperation = (typeof MANIFEST_OPERATIONS)[number];
export type NodeKind = "file" | "folder";
export type FileMode = "replace" | "append" | "";

/** Context purposes for envelopes carried in a manifest. */
export const PURPOSE_NAME = "name";
export const PURPOSE_NODE_KEY = "node-key";
export const PURPOSE_CONTENT_KEY = "content-key";
export const PURPOSE_CONTENT = "content";

export interface ChunkPage {
  format: number;
  drive: string;
  node: string;
  chunks: ChunkRef[];
}

export interface Manifest {
  format: number;
  drive: string;
  node: string;
  version: string;
  parent: string;
  operation: ManifestOperation;
  author: string;
  generation: number;
  kind: NodeKind;
  mode: FileMode;
  /** Containing folder node ID on create and move; empty only for the root. */
  folder: string;
  name?: DriveSealedPayload | null;
  name_hash: string;
  node_key?: DriveSealedPayload | null;
  content_key?: DriveSealedPayload | null;
  count: number;
  pages: string[];
  signature: string;
}

export function validateChunkPage(page: ChunkPage): void {
  if (page.format !== 1) throw new Error("unsupported page format");
  validIdentity(page.drive);
  if (!validHex(page.node, 16)) throw new Error("invalid page drive or node");
  if (!Array.isArray(page.chunks) || page.chunks.length === 0 || page.chunks.length > PAGE_SIZE) {
    throw new Error("a page holds 1 to 1024 chunk references");
  }
  for (const chunk of page.chunks) if (!validChunk(chunk)) throw new Error("invalid chunk reference");
}

export function canonicalChunkPage(page: ChunkPage): Uint8Array {
  validateChunkPage(page);
  const fields = ["1", page.drive, page.node, String(page.chunks.length)];
  for (const chunk of page.chunks) fields.push(chunk.id, String(chunk.size));
  return lengthPrefixed("poweur/drive/page/v1\n", fields);
}

/** A page's content address: SHA-256 of its canonical bytes, lowercase hex. */
export function chunkPageHash(page: ChunkPage): string {
  return hex(sha256(canonicalChunkPage(page)));
}

/** Cut refs into ordered pages of at most PAGE_SIZE references, with hashes. */
export function splitPages(drive: string, node: string, refs: ChunkRef[]): { pages: ChunkPage[]; hashes: string[] } {
  const pages: ChunkPage[] = [];
  const hashes: string[] = [];
  for (let start = 0; start < refs.length; start += PAGE_SIZE) {
    const page: ChunkPage = { format: 1, drive, node, chunks: refs.slice(start, start + PAGE_SIZE) };
    pages.push(page);
    hashes.push(chunkPageHash(page));
  }
  return { pages, hashes };
}

export function validateManifest(m: Manifest): void {
  if (m.format !== 1) throw new Error("unsupported manifest format");
  validIdentity(m.drive);
  validIdentity(m.author);
  if (!validHex(m.node, 16) || !validHex(m.version, 16)) throw new Error("invalid node or version ID");
  if (!Number.isSafeInteger(m.generation) || m.generation < 1 || !Number.isSafeInteger(m.count) || m.count < 0) {
    throw new Error("invalid generation or count");
  }
  if (m.folder !== "" && (!validHex(m.folder, 16) || m.folder === m.node)) throw new Error("invalid containing folder");
  if ((m.parent !== "" && !validHex(m.parent, 16)) || m.parent === m.version) throw new Error("invalid parent version");
  if (m.kind === "folder") {
    if (m.mode !== "" || m.content_key || m.count !== 0 || m.pages?.length) throw new Error("a folder has no mode, content key or content");
  } else if (m.kind === "file") {
    if (m.mode !== "replace" && m.mode !== "append") throw new Error("a file's mode is replace or append");
  } else {
    throw new Error("kind is file or folder");
  }
  if (!Array.isArray(m.pages) || m.pages.length > Math.ceil(MAX_COUNTER / PAGE_SIZE)) throw new Error("invalid page list");
  for (const page of m.pages) if (!validHex(page, 32)) throw new Error("invalid page hash");
  const pages = m.pages.length;
  if ((pages === 0 && m.count !== 0) || (pages > 0 && (m.count <= (pages - 1) * PAGE_SIZE || m.count > pages * PAGE_SIZE))) {
    throw new Error("page list does not match the chunk count");
  }
  const hasName = Boolean(m.name) || m.name_hash !== "";
  if (Boolean(m.name) !== (m.name_hash !== "") || (m.name_hash !== "" && !validHex(m.name_hash, 32))) {
    throw new Error("name and name hash go together");
  }
  for (const payload of [m.name, m.node_key, m.content_key]) if (payload) validatePayload(payload);
  switch (m.operation) {
    case "create":
      if (m.parent !== "" || !m.node_key) throw new Error("a create has no parent version and carries the node key");
      if ((m.folder === "") === hasName) throw new Error("a create names its folder and name, except the root");
      if (m.kind === "file" && !m.content_key) throw new Error("a file create carries its content key");
      break;
    case "replace":
      if (m.parent === "" || m.kind !== "file" || hasName || m.node_key || m.content_key || m.folder !== "") {
        throw new Error("a replace changes only a file's content");
      }
      break;
    case "move":
      if (m.parent === "" || m.folder === "" || !hasName || !m.node_key || m.content_key) {
        throw new Error("a move carries the new folder, name and re-sealed node key");
      }
      break;
    case "rotate":
      if (m.parent === "" || hasName || m.folder !== "" || !m.node_key || (m.kind === "file") !== Boolean(m.content_key)) {
        throw new Error("a rotation carries new keys and nothing else");
      }
      break;
    case "remove":
      if (m.parent === "" || hasName || m.folder !== "" || m.node_key || m.content_key || m.count !== 0) {
        throw new Error("a removal carries no keys, name or content");
      }
      break;
    default:
      throw new Error("unknown manifest operation");
  }
}

export function canonicalManifest(m: Manifest): Uint8Array {
  validateManifest(m);
  const fields = ["1", m.drive, m.node, m.version, m.parent, m.operation, m.author, String(m.generation), m.kind, m.mode, m.folder,
    ...sealedFields(m.name), m.name_hash, ...sealedFields(m.node_key), ...sealedFields(m.content_key),
    String(m.count), String(m.pages.length), ...m.pages];
  return lengthPrefixed("poweur/drive/manifest-sign/v1\n", fields);
}

export function signManifest(m: Manifest, privateKey: Uint8Array): Manifest {
  return { ...m, signature: toBase64url(signBytes(privateKey, canonicalManifest(m))) };
}

export function verifyManifest(m: Manifest, publicKey: Uint8Array): void {
  const raw = canonicalManifest(m), signature = strictBase64(m.signature);
  if (signature.length !== 64 || !verifyBytes(publicKey, raw, signature)) throw new Error("manifest signature verification failed");
}

export function manifestHash(m: Manifest): string {
  const raw = canonicalManifest(m), signature = strictBase64(m.signature);
  if (signature.length !== 64) throw new Error("invalid signature encoding");
  return hex(sha256(concatBytes(utf8("poweur/drive/manifest-hash/v1\n"), raw, signature)));
}

/** Check pages are exactly the manifest's content; returns the ordered refs. */
export function verifyManifestPages(m: Manifest, pages: ChunkPage[]): ChunkRef[] {
  validateManifest(m);
  if (pages.length !== m.pages.length) throw new Error("page count does not match the manifest");
  const refs: ChunkRef[] = [];
  pages.forEach((page, i) => {
    if (page.drive !== m.drive || page.node !== m.node) throw new Error("page belongs to another node");
    if (chunkPageHash(page) !== m.pages[i]) throw new Error("page does not match the manifest");
    refs.push(...page.chunks);
  });
  if (refs.length !== m.count) throw new Error("chunk count does not match the manifest");
  return refs;
}

/** The context an envelope in m is bound to (drive, node, purpose, generation). */
export function manifestEnvelopeContext(m: Manifest, purpose: string): Uint8Array {
  return driveContext(m.drive, m.node, purpose, m.generation);
}
