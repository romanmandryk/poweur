/** Storage-v2 primitives, conforming to packages/identity/drive. */
import { xchacha20poly1305 } from "@noble/ciphers/chacha.js";
import { sha256 } from "@noble/hashes/sha2.js";
import { concatBytes, randomBytes, utf8 } from "../encoding.js";
import { openWithDomain, sealWithDomain, type SealedPayload } from "../crypto/index.js";

export const MAX_PLAINTEXT = 4 * 1024 * 1024;
export const PADDING_BUCKET = 4096;
export const MAX_CHUNK_BYTES = 24 + MAX_PLAINTEXT + PADDING_BUCKET + 16;

export function driveContext(owner: string, node: string, purpose: string, generation: number): Uint8Array {
  if (!Number.isSafeInteger(generation) || generation < 0) throw new Error("invalid generation");
  const fields: Uint8Array[] = [];
  for (const field of [owner, node, purpose, String(generation)]) {
    const bytes = utf8(field);
    // TextEncoder replaces lone surrogates; refuse those rather than signing
    // bytes that do not represent the caller's original string.
    if (!bytes.length || bytes.length > 1024 || field.includes("\0") || new TextDecoder().decode(bytes) !== field) {
      throw new Error("invalid context field");
    }
    const length = new Uint8Array(4);
    new DataView(length.buffer).setUint32(0, bytes.length);
    fields.push(length, bytes);
  }
  return concatBytes(...fields);
}

export function sealKey(publicKey: Uint8Array, key: Uint8Array, context: Uint8Array): SealedPayload {
  if (key.length !== 32) throw new Error("key must be 32 bytes");
  return sealWithDomain(publicKey, key, "poweur/drive/seal/v1", context);
}

export function openKey(privateKey: Uint8Array, payload: SealedPayload, context: Uint8Array): Uint8Array {
  const key = openWithDomain(privateKey, payload, "poweur/drive/seal/v1", context);
  if (key.length !== 32) throw new Error("invalid sealed key length");
  return key;
}

function chunkAAD(context: Uint8Array): Uint8Array {
  if (!context.length) throw new Error("chunk context required");
  return concatBytes(utf8("poweur/drive/chunk/v1\n"), context);
}

export function encryptChunk(key: Uint8Array, plaintext: Uint8Array, context: Uint8Array): Uint8Array {
  const aad = chunkAAD(context);
  if (plaintext.length > MAX_PLAINTEXT) throw new Error("chunk plaintext exceeds 4 MiB");
  const padded = new Uint8Array(Math.ceil((plaintext.length + 4) / PADDING_BUCKET) * PADDING_BUCKET);
  new DataView(padded.buffer).setUint32(0, plaintext.length);
  padded.set(plaintext, 4);
  const nonce = randomBytes(24);
  return concatBytes(nonce, xchacha20poly1305(key, nonce, aad).encrypt(padded));
}

export function decryptChunk(key: Uint8Array, chunk: Uint8Array, context: Uint8Array): Uint8Array {
  const aad = chunkAAD(context);
  if (chunk.length < 40 + PADDING_BUCKET || chunk.length > MAX_CHUNK_BYTES || (chunk.length - 40) % PADDING_BUCKET !== 0) {
    throw new Error("invalid chunk length");
  }
  const padded = xchacha20poly1305(key, chunk.subarray(0, 24), aad).decrypt(chunk.subarray(24));
  const size = new DataView(padded.buffer, padded.byteOffset, padded.byteLength).getUint32(0);
  if (size > MAX_PLAINTEXT || size + 4 > padded.length || Math.ceil((size + 4) / PADDING_BUCKET) * PADDING_BUCKET !== padded.length) {
    throw new Error("invalid padded length");
  }
  if (padded.subarray(4 + size).some((byte) => byte !== 0)) throw new Error("invalid padding");
  return padded.subarray(4, 4 + size);
}

export function chunkID(chunk: Uint8Array): string {
  return Array.from(sha256(chunk), (byte) => byte.toString(16).padStart(2, "0")).join("");
}
