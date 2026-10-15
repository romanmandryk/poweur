/**
 * Optional subject lines (EPIC-009 E09-T13, PCP-0009) — the twin of
 * `packages/identity/subject.go`, pinned by `message-subject.json`.
 *
 * A subject is content, so it travels inside the encrypted payload: the
 * plaintext becomes `Subject: <subject>\n\n<text>` and the signed metadata
 * carries `body_format: headers-v1`. Without the flag a plaintext is plain
 * text, so a message without a subject is byte-identical to one from a client
 * that predates subjects.
 */
import { PoweurError } from "./errors.js";

export const META_BODY_FORMAT = "body_format";
export const BODY_FORMAT_HEADERS_V1 = "headers-v1";
export const MAX_SUBJECT_BYTES = 256;
export const SUBJECT_KEY_PREFIX = "subj:";

const encoder = new TextEncoder();

function isSubjectControl(cp: number): boolean {
  return cp < 0x20 || cp === 0x7f || (cp >= 0x80 && cp <= 0x9f) || cp === 0x2028 || cp === 0x2029;
}

/** Throws when a (trimmed) subject is not allowed; "" is valid and means none. */
export function validateSubject(subject: string): void {
  const value = subject.trim();
  if (encoder.encode(value).length > MAX_SUBJECT_BYTES) {
    throw new PoweurError("invalid_argument", `subject too long (max ${MAX_SUBJECT_BYTES} bytes)`);
  }
  for (const ch of value) {
    if (isSubjectControl(ch.codePointAt(0)!)) {
      throw new PoweurError("invalid_argument", "subject must not contain control characters");
    }
  }
}

function subjectValid(subject: string): boolean {
  try {
    validateSubject(subject);
    return true;
  } catch {
    return false;
  }
}

/**
 * Encode a subject and text as one plaintext. An empty subject returns the
 * text unchanged and no metadata; otherwise `metadata` holds the flag to merge
 * into the envelope's.
 */
export function formatBody(subject: string, text: string): { plaintext: string; metadata?: Record<string, string> } {
  const value = subject.trim();
  validateSubject(value);
  if (!value) return { plaintext: text };
  return { plaintext: `Subject: ${value}\n\n${text}`, metadata: { [META_BODY_FORMAT]: BODY_FORMAT_HEADERS_V1 } };
}

const HEADER_NAME = /^[A-Za-z0-9-]+$/;

/**
 * Split a received plaintext into subject and text. Only a `headers-v1`
 * plaintext is parsed, and one that does not parse comes back whole.
 */
export function parseBody(plaintext: string, metadata?: Record<string, string> | null): { subject: string; text: string } {
  if (metadata?.[META_BODY_FORMAT] !== BODY_FORMAT_HEADERS_V1) return { subject: "", text: plaintext };
  const end = plaintext.indexOf("\n\n");
  if (end < 0) return { subject: "", text: plaintext };
  let found = "";
  for (const line of plaintext.slice(0, end).split("\n")) {
    const colon = line.indexOf(":");
    if (colon < 0) return { subject: "", text: plaintext };
    const name = line.slice(0, colon);
    if (!HEADER_NAME.test(name)) return { subject: "", text: plaintext };
    if (name.toLowerCase() === "subject") found = line.slice(colon + 1).trim();
  }
  if (!subjectValid(found)) return { subject: "", text: plaintext };
  return { subject: found, text: plaintext.slice(end + 2) };
}

const REPLY_PREFIXES = ["re", "fwd", "fw", "aw", "sv"];

/** Lowercase one code point the way Go's unicode.ToLower does (never expands). */
function lowerCodePoint(ch: string): string {
  const lowered = ch.toLowerCase();
  const first = [...lowered][0];
  return first ?? ch;
}

function stripReplyPrefix(s: string): string {
  for (const prefix of REPLY_PREFIXES) {
    if (!s.startsWith(prefix)) continue;
    let rest = s.slice(prefix.length);
    if (rest.startsWith("[")) {
      const close = rest.indexOf("]");
      if (close < 2 || !/^[0-9]+$/.test(rest.slice(1, close))) continue;
      rest = rest.slice(close + 1);
    }
    rest = rest.replace(/^ +/, "");
    if (!rest.startsWith(":")) continue;
    return rest.slice(1).replace(/^ +/, "");
  }
  return s;
}

/** Whitespace as Go's strings.Fields sees it. */
const FIELD_SPACE = new RegExp("[\\t\\n\\v\\f\\r \\u0085\\u00a0\\u1680\\u2000-\\u200a\\u2028\\u2029\\u202f\\u205f\\u3000]+", "u");

/**
 * The comparison form of a subject: trimmed, whitespace collapsed, lowercased
 * per code point, leading Re:/Fwd:/Fw:/Aw:/Sv: (and Re[n]:) removed.
 */
export function normalizeSubject(subject: string): string {
  let s = subject.split(FIELD_SPACE).filter(Boolean).join(" ");
  s = [...s].map(lowerCodePoint).join("");
  for (;;) {
    const stripped = stripReplyPrefix(s);
    if (stripped === s) return s;
    s = stripped;
  }
}

/** Grouping key inside one conversation: thread, else `subj:<normalized>`, else "". */
export function conversationKey(threadId: string | undefined | null, subject: string | undefined | null): string {
  if (threadId) return threadId;
  const normalized = normalizeSubject(subject ?? "");
  return normalized ? SUBJECT_KEY_PREFIX + normalized : "";
}

const CROCKFORD = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";

/** A fresh ULID thread id for a new subject — never derived from the subject. */
export function newThreadId(now: number = Date.now()): string {
  const bytes = new Uint8Array(16);
  let ms = BigInt(now);
  for (let i = 5; i >= 0; i--) {
    bytes[i] = Number(ms & 0xffn);
    ms >>= 8n;
  }
  globalThis.crypto.getRandomValues(bytes.subarray(6));
  let value = 0n;
  for (const byte of bytes) value = (value << 8n) | BigInt(byte);
  let out = "";
  for (let i = 0; i < 26; i++) {
    out = CROCKFORD[Number(value & 31n)] + out;
    value >>= 5n;
  }
  return out;
}
