/** Canonical-encoding helpers shared by drive formats. Not part of the public API. */
import { concatBytes, fromBase64, toBase64url, utf8 } from "../encoding.js";
import { validateIdentityName } from "../names.js";
import { MAX_CHUNK_BYTES, PADDING_BUCKET } from "./crypto.js";
import type { ChunkRef, DriveSealedPayload } from "./records.js";

export const MAX_COUNTER = Number.MAX_SAFE_INTEGER;

export function validHex(value: string, bytes: number): boolean {
  return typeof value === "string" && value.length === bytes * 2 && /^[0-9a-f]+$/.test(value);
}
export function validIdentity(value: string): void {
  if (value !== value.trim().toLowerCase()) throw new Error("invalid drive or author");
  validateIdentityName(value);
}
export function validChunk(chunk: ChunkRef): boolean {
  return validHex(chunk.id, 32) && Number.isSafeInteger(chunk.size) && chunk.size >= 40 + PADDING_BUCKET &&
    chunk.size <= MAX_CHUNK_BYTES && (chunk.size - 40) % PADDING_BUCKET === 0;
}
export function strictBase64(value: string): Uint8Array {
  const bytes = fromBase64(value);
  if (toBase64url(bytes) !== value) throw new Error("invalid sealed field encoding");
  return bytes;
}
export function validatePayload(p: DriveSealedPayload): void {
  for (const [value, size] of [[p.ephemeral_public_key, 32], [p.nonce, 12], [p.ciphertext, 0]] as const) {
    if (typeof value !== "string" || value.length > Math.ceil(MAX_CHUNK_BYTES * 4 / 3)) throw new Error("sealed field too large");
    const raw = strictBase64(value);
    if (size ? raw.length !== size : raw.length < 16 || raw.length > MAX_CHUNK_BYTES) throw new Error("invalid sealed field length");
  }
}
export function hex(bytes: Uint8Array): string {
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
}
export function lengthPrefixed(prefix: string, fields: string[]): Uint8Array {
  const out: Uint8Array[] = [utf8(prefix)];
  for (const field of fields) {
    const bytes = utf8(field), size = new Uint8Array(4);
    new DataView(size.buffer).setUint32(0, bytes.length);
    out.push(size, bytes);
  }
  return concatBytes(...out);
}
export function sealedFields(p: DriveSealedPayload | null | undefined): string[] {
  return p ? [p.ephemeral_public_key, p.nonce, p.ciphertext] : ["", "", ""];
}

