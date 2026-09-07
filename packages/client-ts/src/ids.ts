/**
 * Client-assigned identifiers — the twin of `apps/cli/internal/identity/ids.go`.
 * `msg_<unix-ms>_<rand>` sorts by send time and stays one terminal line wide;
 * the prefix lets logs tell messages from acks at a glance.
 */

import { randomBytes, toBase64url } from "./encoding.js";

function prefixedId(prefix: string): string {
  return `${prefix}_${Date.now()}_${toBase64url(randomBytes(9))}`;
}

export function newMessageId(): string {
  return prefixedId("msg");
}

export function newAckId(): string {
  return prefixedId("ack");
}

/** 16 bytes of entropy for owner-only admin envelopes. */
export function newNonce(bytes = 16): string {
  return toBase64url(randomBytes(bytes));
}

/** `shr_<16 hex>` — the share-id shape `poweur share add` produces. */
export function newShareId(): string {
  const bytes = randomBytes(8);
  return `shr_${[...bytes].map((b) => b.toString(16).padStart(2, "0")).join("")}`;
}
