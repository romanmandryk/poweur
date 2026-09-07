/**
 * Byte/string encoding helpers shared by every module.
 *
 * The wire format is base64url without padding throughout, but the Go CLI
 * emits *standard* base64 for message/ack/admin signatures and base64url for
 * document signatures and keys. The relay accepts any of the four variants
 * (see apps/api/internal/crypto/signing.go decodeAnyBase64), so decoding is
 * permissive and encoding is explicit per call site.
 */

const encoder = new TextEncoder();
const decoder = new TextDecoder();

export function utf8(value: string): Uint8Array {
  return encoder.encode(value);
}

export function fromUtf8(bytes: Uint8Array): string {
  return decoder.decode(bytes);
}

function binaryToBytes(binary: string): Uint8Array {
  const out = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) out[i] = binary.charCodeAt(i);
  return out;
}

function bytesToBinary(bytes: Uint8Array): string {
  // Chunked so very large payloads don't blow the argument limit of
  // String.fromCharCode.
  let binary = "";
  const CHUNK = 0x8000;
  for (let i = 0; i < bytes.length; i += CHUNK) {
    binary += String.fromCharCode(...bytes.subarray(i, i + CHUNK));
  }
  return binary;
}

/** base64url, no padding (the protocol default). */
export function toBase64url(bytes: Uint8Array): string {
  return btoa(bytesToBinary(bytes))
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
}

/** Standard base64 with padding — matches Go's base64.StdEncoding. */
export function toBase64Std(bytes: Uint8Array): string {
  return btoa(bytesToBinary(bytes));
}

/**
 * Decode any of the four base64 variants Go's decodeAnyBase64 accepts:
 * raw/padded × standard/URL-safe.
 */
export function fromBase64(value: string): Uint8Array {
  const normalized = value.trim().replace(/-/g, "+").replace(/_/g, "/");
  const padded = normalized.padEnd(
    normalized.length + ((4 - (normalized.length % 4)) % 4),
    "=",
  );
  return binaryToBytes(atob(padded));
}

export function concatBytes(...arrays: Uint8Array[]): Uint8Array {
  let total = 0;
  for (const a of arrays) total += a.length;
  const out = new Uint8Array(total);
  let offset = 0;
  for (const a of arrays) {
    out.set(a, offset);
    offset += a.length;
  }
  return out;
}

export function equalBytes(a: Uint8Array, b: Uint8Array): boolean {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= (a[i] as number) ^ (b[i] as number);
  return diff === 0;
}

export function randomBytes(length: number): Uint8Array {
  const out = new Uint8Array(length);
  crypto.getRandomValues(out);
  return out;
}

/**
 * RFC 3339 in UTC with second precision — the exact shape Go's
 * time.Format(time.RFC3339) produces, which matters because timestamps are
 * bound into canonical signing strings.
 */
export function rfc3339(date: Date = new Date()): string {
  return date.toISOString().replace(/\.\d{3}Z$/, "Z");
}

/** Strip an `ed25519:` / `x25519:` scheme prefix if present. */
export function stripKeyPrefix(value: string): string {
  const trimmed = value.trim();
  if (trimmed.startsWith("ed25519:")) return trimmed.slice("ed25519:".length);
  if (trimmed.startsWith("x25519:")) return trimmed.slice("x25519:".length);
  return trimmed;
}

/** Ensure an `ed25519:` prefix (identity documents carry prefixed keys). */
export function withEd25519Prefix(value: string): string {
  const trimmed = value.trim();
  return trimmed.startsWith("ed25519:") ? trimmed : `ed25519:${trimmed}`;
}

/** Ensure an `x25519:` prefix. */
export function withX25519Prefix(value: string): string {
  const trimmed = value.trim();
  return trimmed.startsWith("x25519:") ? trimmed : `x25519:${trimmed}`;
}
