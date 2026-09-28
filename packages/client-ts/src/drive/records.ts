/** Canonical signed append records; Go packages/identity/drive is authoritative. */
import { sha256 } from "@noble/hashes/sha2.js";
import { signBytes, verifyBytes, sealWithDomain, openWithDomain } from "../crypto/index.js";
import { concatBytes, fromBase64, toBase64url, utf8 } from "../encoding.js";
import { MAX_CHUNK_BYTES, PADDING_BUCKET, MAX_PLAINTEXT, driveContext } from "./crypto.js";
import { validateIdentityName } from "../names.js";

export interface ChunkRef { id: string; size: number }
export interface DriveSealedPayload { ephemeral_public_key: string; nonce: string; ciphertext: string }
export interface AppendRecord {
  format: number;
  drive: string;
  node: string;
  author: string;
  generation: number;
  sequence: number;
  previous: string;
  chunks: ChunkRef[];
  sealed?: DriveSealedPayload | null;
  signature: string;
}

function validHex(value: string, bytes: number): boolean {
  return value.length === bytes * 2 && /^[0-9a-f]+$/.test(value);
}
function counter(value: number): boolean { return Number.isSafeInteger(value) && value > 0 }
function strictBase64(value: string): Uint8Array {
  const bytes = fromBase64(value);
  if (toBase64url(bytes) !== value) throw new Error("invalid sealed field encoding");
  return bytes;
}

export function validateAppendRecord(record: AppendRecord): void {
  if (record.format !== 1) throw new Error("unsupported append format");
  for (const value of [record.drive, record.author]) {
    if (value !== value.trim().toLowerCase()) throw new Error("invalid drive or author");
    validateIdentityName(value);
  }
  if (!validHex(record.node, 16)) throw new Error("invalid node ID");
  if (!counter(record.generation) || !counter(record.sequence)) throw new Error("invalid generation or sequence");
  if (record.sequence === 1 ? record.previous !== "" : !validHex(record.previous, 32)) throw new Error("invalid previous record hash");
  if (!Array.isArray(record.chunks) || record.chunks.length > 1024 || (record.chunks.length === 0) === (record.sealed == null)) throw new Error("record needs chunks or a sealed payload, exclusively");
  for (const chunk of record.chunks) {
    if (!validHex(chunk.id, 32) || !Number.isSafeInteger(chunk.size) || chunk.size < 40 + PADDING_BUCKET || chunk.size > MAX_CHUNK_BYTES || (chunk.size - 40) % PADDING_BUCKET !== 0) {
      throw new Error("invalid chunk reference");
    }
  }
  if (record.sealed) {
    for (const [value, size] of [[record.sealed.ephemeral_public_key, 32], [record.sealed.nonce, 12], [record.sealed.ciphertext, 0]] as const) {
      if (value.length > Math.ceil(MAX_CHUNK_BYTES * 4 / 3)) throw new Error("sealed field too large");
      const raw = strictBase64(value);
      if (size ? raw.length !== size : raw.length < 16 || raw.length > MAX_CHUNK_BYTES) throw new Error("invalid sealed field length");
    }
  }
}

export function canonicalAppendRecord(record: AppendRecord): Uint8Array {
  validateAppendRecord(record);
  const fields = ["1", record.drive, record.node, record.author, String(record.generation), String(record.sequence), record.previous, String(record.chunks.length)];
  for (const chunk of record.chunks) fields.push(chunk.id, String(chunk.size));
  fields.push(record.sealed?.ephemeral_public_key ?? "", record.sealed?.nonce ?? "", record.sealed?.ciphertext ?? "");
  const out: Uint8Array[] = [utf8("poweur/drive/record-sign/v1\n")];
  for (const field of fields) {
    const bytes = utf8(field), size = new Uint8Array(4);
    new DataView(size.buffer).setUint32(0, bytes.length);
    out.push(size, bytes);
  }
  return concatBytes(...out);
}

export function signAppendRecord(record: AppendRecord, privateKey: Uint8Array): AppendRecord {
  return { ...record, signature: toBase64url(signBytes(privateKey, canonicalAppendRecord(record))) };
}

export function verifyAppendRecord(record: AppendRecord, publicKey: Uint8Array): void {
  const raw = canonicalAppendRecord(record), signature = strictBase64(record.signature);
  if (signature.length !== 64 || !verifyBytes(publicKey, raw, signature)) throw new Error("append signature verification failed");
}

export function appendRecordHash(record: AppendRecord): string {
  const raw = canonicalAppendRecord(record), signature = strictBase64(record.signature);
  if (signature.length !== 64) throw new Error("invalid signature encoding");
  return Array.from(sha256(concatBytes(utf8("poweur/drive/record-hash/v1\n"), raw, signature)), byte => byte.toString(16).padStart(2, "0")).join("");
}

/** Keep separate author cursors for each (drive,node,author). */
export function verifyNextRecord(record: AppendRecord, publicKey: Uint8Array, lastSequence: number, lastHash: string): void {
  verifyAppendRecord(record, publicKey);
  if (!Number.isSafeInteger(lastSequence) || lastSequence < 0 || (lastSequence === 0 ? lastHash !== "" : !validHex(lastHash, 32))) throw new Error("invalid author cursor");
  if (record.sequence <= lastSequence) throw new Error("duplicate or reordered author sequence");
  if (record.sequence !== lastSequence + 1) throw new Error("author sequence gap");
  if (record.previous !== lastHash) throw new Error("author chain mismatch");
}

function recordContext(record: AppendRecord): Uint8Array {
  return driveContext(record.drive, record.node, `record:${record.author}:${record.sequence}:${record.previous}`, record.generation);
}

/** Seal to the node's public key without giving the author read access. */
export function sealRecordContent(record: AppendRecord, nodePublic: Uint8Array, plaintext: Uint8Array): AppendRecord {
  if (plaintext.length > MAX_PLAINTEXT) throw new Error("sealed record exceeds 4 MiB");
  const payload = sealWithDomain(nodePublic, plaintext, "poweur/drive/record/v1", recordContext(record));
  const candidate: AppendRecord = { ...record, chunks: [], signature: "", sealed: {
    ephemeral_public_key: payload.ephemeralPublicKey, nonce: payload.nonce, ciphertext: payload.ciphertext,
  } };
  validateAppendRecord(candidate);
  return candidate;
}

/** Verify the author's signature and role before applying the opened content. */
export function openRecordContent(record: AppendRecord, nodePrivate: Uint8Array): Uint8Array {
  validateAppendRecord(record);
  if (!record.sealed) throw new Error("record contains chunk references");
  const plaintext = openWithDomain(nodePrivate, {
    ephemeralPublicKey: record.sealed.ephemeral_public_key, nonce: record.sealed.nonce, ciphertext: record.sealed.ciphertext,
  }, "poweur/drive/record/v1", recordContext(record));
  if (plaintext.length > MAX_PLAINTEXT) throw new Error("sealed record exceeds 4 MiB");
  return plaintext;
}
