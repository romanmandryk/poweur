/**
 * Typed messages, threads, expiry and metadata — the TypeScript twin of
 * `packages/identity/msgtypes.go` (EPIC-009 E09-T3).
 *
 * Go stays canonical. Every limit, every character class and the metadata
 * sort order below exist to match that file byte for byte; the conformance
 * vectors (`message-threaded`, `message-full-envelope`) are what prove it.
 *
 * # Why these four fields are plaintext
 *
 * The payload is end-to-end encrypted and the relay must never read it. These
 * four are the deliberate exception: the relay routes on `type` (inbox
 * policy), will expire on `expires_at`, and `thread_id` is what lets a client
 * group a conversation without opening every message first. Everything here
 * is visible to both relays on the path — **put nothing private in
 * `metadata`**. It is addressing, not content.
 */

import {
  MSG_TYPE_CONTACT_ACCEPT,
  MSG_TYPE_CONTACT_BLOCK,
  MSG_TYPE_CONTACT_REQUEST,
} from "./types.js";

/**
 * The default message type. An envelope with no `type` *is* a `chat.text`:
 * absence is the wire encoding of the default.
 *
 * Prefer leaving `type` unset for ordinary chat. Both forms are valid and
 * mean the same thing, but the absent form produces the canonical string
 * every pre-typing client already produces, which is what keeps old and new
 * implementations verifying each other.
 */
export const MSG_TYPE_CHAT_TEXT = "chat.text";

/** The reserved platform namespace. */
export const SYS_PREFIX = "sys.";

/**
 * Platform-owned message types (mirrors `conventions/registry.json`). The
 * `sys.contact.*` trio already lives in `types.ts` next to the inbox policy
 * that acts on it — same split as Go, where they sit in `inboxpolicy.go`.
 */
export const MSG_TYPE_SHARE_OFFER = "sys.share.offer";
export const MSG_TYPE_SHARE_ACCEPT = "sys.share.accept";
export const MSG_TYPE_SHARE_REVOKED = "sys.share.revoked";
export const MSG_TYPE_SYNC_CHANGED = "sys.sync.changed";
export const MSG_TYPE_ABUSE_REPORT = "sys.abuse.report";

/**
 * The closed set of `sys.*` types this protocol revision knows, sorted. A
 * relay refuses an unregistered `sys.*` envelope rather than routing it, so
 * this list and Go's `SystemMessageTypes()` have to agree.
 */
export const SYSTEM_MESSAGE_TYPES: readonly string[] = [
  MSG_TYPE_ABUSE_REPORT,
  MSG_TYPE_CONTACT_ACCEPT,
  MSG_TYPE_CONTACT_BLOCK,
  MSG_TYPE_CONTACT_REQUEST,
  MSG_TYPE_SHARE_ACCEPT,
  MSG_TYPE_SHARE_OFFER,
  MSG_TYPE_SHARE_REVOKED,
  MSG_TYPE_SYNC_CHANGED,
];

/** Whether a type sits in the reserved platform namespace, registered or not. */
export function isSystemType(type: string | undefined): boolean {
  return (type ?? "").startsWith(SYS_PREFIX);
}

/** Whether a type is a *registered* platform type. */
export function isKnownSystemType(type: string | undefined): boolean {
  return SYSTEM_MESSAGE_TYPES.includes(type ?? "");
}

/**
 * Map the wire value to its meaning: absent (or whitespace) is
 * `chat.text`. This never rewrites the envelope — the envelope is signed,
 * and the absent form is part of what was signed.
 */
export function normalizeMessageType(type: string | undefined | null): string {
  return (type ?? "").trim() === "" ? MSG_TYPE_CHAT_TEXT : (type as string);
}

/**
 * Envelope extension limits. Deliberately small: the relay stores these in
 * plaintext for every undelivered message.
 */
export const MAX_MESSAGE_TYPE_LEN = 64;
export const MAX_THREAD_ID_LEN = 128;
export const MAX_METADATA_KEYS = 16;
export const MAX_METADATA_KEY_LEN = 40;
export const MAX_METADATA_VAL_LEN = 256;
export const MAX_METADATA_BYTES = 2048;

/**
 * Validate an envelope type. The empty string is valid and means
 * `chat.text`.
 *
 * Shape: two or more lowercase dot-separated segments (`chat.text`,
 * `sys.contact.request`, `net.poweur.tasks.assigned`). A bare word is
 * rejected so every type carries a namespace — which is what lets the
 * registry say who owns it. Hyphens are allowed inside a segment but not at
 * either end.
 *
 * @returns an error message, or null when valid.
 */
export function validateMessageType(type: string): string | null {
  if (type === "") return null;
  if (type.length > MAX_MESSAGE_TYPE_LEN) {
    return `message type too long (max ${MAX_MESSAGE_TYPE_LEN})`;
  }
  const segments = type.split(".");
  if (segments.length < 2) {
    return `message type "${type}" must be namespaced (e.g. chat.text)`;
  }
  for (const segment of segments) {
    if (segment === "") return `message type "${type}" has an empty segment`;
    for (let i = 0; i < segment.length; i++) {
      const c = segment[i] as string;
      const alnum = (c >= "a" && c <= "z") || (c >= "0" && c <= "9");
      const innerHyphen = c === "-" && i > 0 && i < segment.length - 1;
      if (!alnum && !innerHyphen) {
        return `message type "${type}": invalid character "${c}"`;
      }
    }
  }
  return null;
}

/**
 * Validate a thread identifier. Threads are opaque to the relay: it never
 * invents one and never rewrites one. The only rule is that the value is a
 * single safe line, because the canonical string puts it on a line of its own
 * and a control character there would make the signing input ambiguous.
 */
export function validateThreadId(threadId: string): string | null {
  if (threadId === "") return null;
  if (threadId.length > MAX_THREAD_ID_LEN) {
    return `thread_id too long (max ${MAX_THREAD_ID_LEN})`;
  }
  for (const c of threadId) {
    const alnum = (c >= "a" && c <= "z") || (c >= "A" && c <= "Z") || (c >= "0" && c <= "9");
    if (!alnum && !"_-.:@+~".includes(c)) {
      return `thread_id: invalid character "${c}" (allowed: A-Z a-z 0-9 _ - . : @ + ~)`;
    }
  }
  return null;
}

/**
 * Validate the optional expiry stamp. Enforcement — refusing delivery past it
 * — is E09-T6; this revision fixes the format and binds it into the signature
 * so the value cannot be added, removed or moved by anyone on the path.
 */
export function validateExpiresAt(value: string): string | null {
  if (value === "") return null;
  // Go parses RFC3339, which is stricter than `Date.parse`: the date and time
  // are mandatory, they are separated by `T`, and an offset (or `Z`) is
  // required. Checking the shape first is what keeps the two in agreement.
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$/.test(value)) {
    return "expires_at must be RFC3339";
  }
  if (Number.isNaN(Date.parse(value))) return "expires_at must be RFC3339";
  return null;
}

function validateMetadataKey(key: string): string | null {
  if (key === "") return "metadata: empty key";
  if (key.length > MAX_METADATA_KEY_LEN) {
    return `metadata key "${key}" too long (max ${MAX_METADATA_KEY_LEN})`;
  }
  for (let i = 0; i < key.length; i++) {
    const c = key[i] as string;
    const alnum = (c >= "a" && c <= "z") || (c >= "0" && c <= "9");
    const inner = (c === "_" || c === "-" || c === ".") && i > 0;
    if (!alnum && !inner) {
      return `metadata key "${key}": invalid character "${c}" (allowed: a-z 0-9 _ - . and not leading)`;
    }
  }
  return null;
}

/**
 * Validate the envelope metadata map.
 *
 * Values are flat strings, not arbitrary JSON, and that is the point. Two
 * implementations signing "the same JSON object" would have to agree on key
 * order, number formatting, unicode escaping and nesting; a flat map of
 * printable strings has exactly one serialization in every language, which is
 * what a signature over it needs. Callers wanting structure encode it into a
 * value themselves and own its stability.
 *
 * Control characters are rejected rather than escaped — that is what lets
 * {@link metadataLines} emit `meta:<key>:<value>` with no escaping scheme.
 */
export function validateMetadata(metadata: Record<string, string> | undefined): string | null {
  if (!metadata) return null;
  const keys = Object.keys(metadata);
  if (keys.length === 0) return null;
  if (keys.length > MAX_METADATA_KEYS) {
    return `metadata has ${keys.length} keys (max ${MAX_METADATA_KEYS})`;
  }
  let total = 0;
  for (const key of keys) {
    const keyError = validateMetadataKey(key);
    if (keyError) return keyError;
    const value = metadata[key] as string;
    if (typeof value !== "string") return `metadata["${key}"]: value must be a string`;
    if (value.length > MAX_METADATA_VAL_LEN) {
      return `metadata["${key}"]: value too long (max ${MAX_METADATA_VAL_LEN})`;
    }
    for (const c of value) {
      const code = c.codePointAt(0) ?? 0;
      if (code < 0x20 || code === 0x7f) {
        return `metadata["${key}"]: value must not contain control characters`;
      }
    }
    total += key.length + value.length + 6; // "meta:" + ":" per line
  }
  if (total > MAX_METADATA_BYTES) {
    return `metadata too large (${total} bytes, max ${MAX_METADATA_BYTES})`;
  }
  return null;
}

/**
 * Render metadata into canonical signing lines: one `meta:<key>:<value>` per
 * entry, keys ascending.
 *
 * Keys are ASCII-only by validation, so JavaScript's default UTF-16
 * code-unit sort and Go's byte-wise sort agree — the two implementations
 * cannot disagree about the order.
 *
 * Callers must have validated the map first; an unvalidated one containing a
 * newline would produce an ambiguous signing input, which is exactly why the
 * relay rejects one at ingress.
 */
export function metadataLines(metadata: Record<string, string> | undefined): string[] {
  if (!metadata) return [];
  const keys = Object.keys(metadata).sort();
  return keys.map((key) => `meta:${key}:${metadata[key] as string}`);
}

/** Validate the four E09-T3 fields together. Returns an error message or null. */
export function validateEnvelopeExtensions(input: {
  type?: string;
  threadId?: string;
  expiresAt?: string;
  metadata?: Record<string, string>;
}): string | null {
  return (
    validateMessageType(input.type ?? "") ??
    validateThreadId(input.threadId ?? "") ??
    validateExpiresAt(input.expiresAt ?? "") ??
    validateMetadata(input.metadata)
  );
}

/**
 * How a generic client should present a message body.
 *
 * `chat.text` (and an absent type, which means the same thing) is shown as
 * written. Anything else gets a generic line naming the sender and the type,
 * because a chat UI has no idea how to render an application's payload and
 * showing the raw plaintext would show the user someone else's JSON. An app
 * that understands a type renders it itself and never calls this.
 */
export function describeMessage(sender: string, type: string | undefined, body: string): string {
  const normalized = normalizeMessageType(type);
  if (normalized === MSG_TYPE_CHAT_TEXT) return body;
  return `app message from ${sender} (${normalized})`;
}

/**
 * Group messages into threads for display, newest activity last.
 *
 * A message with no `thread_id` is its own thread keyed by its message id —
 * so an unthreaded conversation renders as a flat list exactly as it did
 * before threads existed, with no special case at the call site.
 */
export function groupByThread<T extends { id: string; thread_id?: string }>(
  messages: readonly T[],
): { threadId: string; threaded: boolean; messages: T[] }[] {
  const order: string[] = [];
  const groups = new Map<string, { threadId: string; threaded: boolean; messages: T[] }>();
  for (const message of messages) {
    const threaded = Boolean(message.thread_id);
    const key = threaded ? (message.thread_id as string) : `msg:${message.id}`;
    let group = groups.get(key);
    if (!group) {
      group = { threadId: threaded ? (message.thread_id as string) : message.id, threaded, messages: [] };
      groups.set(key, group);
      order.push(key);
    }
    group.messages.push(message);
  }
  return order.map((key) => groups.get(key) as { threadId: string; threaded: boolean; messages: T[] });
}
