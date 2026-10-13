/**
 * The email bridge (EPIC-023, PCP-0010) — the twin of
 * `packages/identity/email.go`, pinned by `testdata/vectors/email.json`.
 *
 * An opted-in identity `john.poweur.net` gets the address `john@poweur.net`.
 * The bridge is a Poweur identity: inbound mail arrives as an encrypted
 * `email.message` from it, and the user sends mail by sending it an encrypted
 * `email.send`. Opting in writes the signed `.poweur/public/email.json` and
 * the inbox policy's `email` block, both with the user's own key.
 *
 * Trust rule: show a message as email only when its type is `email.*` **and**
 * its sender is the bridge the user's own policy names (`trustsEmailBridge`).
 */
import { openWithDomain, parseEd25519PublicKey, sealWithDomain, sha256Bytes, verifyCanonical, type SealedPayload } from "./crypto/index.js";
import type { Decryptor, Signer } from "./crypto/keys.js";
import { fromBase64, rfc3339, utf8 } from "./encoding.js";
import { PoweurError } from "./errors.js";
import { validateIdentityName } from "./names.js";
import type { InboxPolicy } from "./types.js";

export const MSG_TYPE_EMAIL_MESSAGE = "email.message";
export const MSG_TYPE_EMAIL_SEND = "email.send";
export const MSG_TYPE_EMAIL_STATUS = "email.status";
export const EMAIL_TYPE_PREFIX = "email.";

export const EMAIL_OPTIN_PATH = ".poweur/public/email.json";
export const WELL_KNOWN_EMAIL_PATH = "/.well-known/poweur/email.json";
export const EMAIL_OPTIN_VERSION = 1;
export const EMAIL_PAYLOAD_VERSION = 1;
export const MAX_EMAIL_TAG_LEN = 64;
export const MAX_EMAIL_RECIPIENTS = 50;
export const MAX_EMAIL_OUTBOUND_BYTES = 20 * 1024 * 1024;

export const EMAIL_BLOB_SEAL_DOMAIN = "poweur/email/blob/v1";
export const EMAIL_UPLOAD_SEAL_DOMAIN = "poweur/email/upload/v1";

export const EMAIL_VERIFIED = "verified";
export const EMAIL_UNVERIFIED = "unverified";
export const EMAIL_FAILED = "failed";

export const EMAIL_STATE_ACCEPTED = "accepted";
export const EMAIL_STATE_SENT = "sent";
export const EMAIL_STATE_DEFERRED = "deferred";
export const EMAIL_STATE_BOUNCED = "bounced";
export const EMAIL_STATE_COMPLAINED = "complained";
export const EMAIL_STATE_REFUSED = "refused";

export function isEmailType(type: string | undefined | null): boolean {
  return Boolean(type?.startsWith(EMAIL_TYPE_PREFIX));
}

// ── Documents ────────────────────────────────────────────────────────────────

export interface EmailOptIn {
  version: number;
  identity: string;
  address: string;
  bridge: string;
  created_at: string;
  signature?: string;
}

export interface EmailPolicy {
  allow: boolean;
  bridge?: string;
}

/** The address an identity's name gives it: `john.poweur.net` → `john@poweur.net`. */
export function emailAddressFor(identity: string): string {
  const name = identity.trim().toLowerCase();
  validateIdentityName(name);
  const dot = name.indexOf(".");
  const domain = name.slice(dot + 1);
  if (!domain.includes(".")) {
    throw new PoweurError("invalid_argument", `${name} has no email address: its parent ${domain} is not a domain with a dot`);
  }
  return `${name.slice(0, dot)}@${domain}`;
}

export interface EmailRoute {
  local: string;
  tag: string;
  domain: string;
}

const LABEL = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;
const TAG = /^[a-z0-9._-]*$/;

/** Split `john+news@poweur.net`; throws when it could not route to an identity. */
export function parseEmailAddress(address: string): EmailRoute {
  let value = address.trim().toLowerCase();
  if (value.startsWith("<")) value = value.slice(1);
  if (value.endsWith(">")) value = value.slice(0, -1);
  const at = value.lastIndexOf("@");
  if (at <= 0 || at === value.length - 1) throw new PoweurError("invalid_argument", "not an email address");
  let local = value.slice(0, at);
  const domain = value.slice(at + 1).replace(/\.$/, "");
  validateIdentityName(domain);
  let tag = "";
  const plus = local.indexOf("+");
  if (plus >= 0) {
    tag = local.slice(plus + 1);
    local = local.slice(0, plus);
    if (tag.length > MAX_EMAIL_TAG_LEN) throw new PoweurError("invalid_argument", "subaddress too long");
    if (!TAG.test(tag)) throw new PoweurError("invalid_argument", "subaddress has an invalid character");
  }
  if (!LABEL.test(local)) throw new PoweurError("invalid_argument", "local part: invalid character");
  return { local, tag, domain };
}

export function canonicalEmailOptIn(o: EmailOptIn): string {
  return [
    "poweur-email-optin",
    `v${o.version}`,
    o.identity.trim().toLowerCase(),
    o.address.trim().toLowerCase(),
    o.bridge.trim().toLowerCase(),
    o.created_at,
  ].join("\n");
}

export function validateEmailOptIn(o: EmailOptIn): void {
  if (o.version !== EMAIL_OPTIN_VERSION) throw new PoweurError("invalid_document", `unsupported email.json version ${o.version}`);
  const identity = o.identity.trim().toLowerCase();
  validateIdentityName(identity);
  const want = emailAddressFor(identity);
  if (o.address.trim().toLowerCase() !== want) {
    throw new PoweurError("invalid_document", `email.json address ${o.address} is not ${identity}'s address (${want})`);
  }
  validateIdentityName(o.bridge.trim().toLowerCase());
  if (Number.isNaN(Date.parse(o.created_at))) throw new PoweurError("invalid_document", "email.json created_at must be RFC3339");
}

/** Build and sign the opt-in for the signer's identity through `bridge`. */
export async function signEmailOptIn(signer: Signer, bridge: string, now: Date = new Date()): Promise<EmailOptIn> {
  const identity = signer.identity.trim().toLowerCase();
  const optIn: EmailOptIn = {
    version: EMAIL_OPTIN_VERSION,
    identity,
    address: emailAddressFor(identity),
    bridge: bridge.trim().toLowerCase(),
    created_at: rfc3339(now),
  };
  validateEmailOptIn(optIn);
  optIn.signature = await signer.sign(canonicalEmailOptIn(optIn), "base64url");
  return optIn;
}

/** Verify the signature with the identity's Ed25519 key ("ed25519:…" or raw). */
export function verifyEmailOptIn(o: EmailOptIn, publicKey: Uint8Array | string): boolean {
  if (!o.signature) return false;
  try {
    validateEmailOptIn(o);
    const key = typeof publicKey === "string" ? parseEd25519PublicKey(publicKey) : publicKey;
    return verifyCanonical(key, canonicalEmailOptIn(o), o.signature);
  } catch {
    return false;
  }
}

/** Mirrors Go `InboxPolicy.TrustsEmailBridge`. */
export function trustsEmailBridge(policy: InboxPolicy | null | undefined, sender: string | undefined | null): boolean {
  const email = policy?.email;
  if (!email?.allow) return false;
  const wanted = String(sender ?? "").trim().toLowerCase();
  return Boolean(wanted) && wanted === String(email.bridge ?? "").trim().toLowerCase();
}

export function validateEmailPolicy(email: EmailPolicy | undefined): void {
  if (!email) return;
  if (typeof email.allow !== "boolean") throw new PoweurError("invalid_document", "email.allow must be a boolean");
  if (!email.allow && !email.bridge) return;
  try {
    validateIdentityName(String(email.bridge ?? "").trim().toLowerCase());
  } catch {
    throw new PoweurError("invalid_document", `invalid email bridge "${email.bridge}"`);
  }
}

// ── Payloads ─────────────────────────────────────────────────────────────────

export interface EmailAddress {
  name?: string;
  address: string;
}

export interface EmailAuth {
  spf?: string;
  dkim?: string;
  dmarc?: string;
  arc?: string;
  verdict: string;
  spam?: string;
  spam_score?: number;
}

export interface EmailBlobRef {
  id: string;
  url: string;
  size: number;
  sha256: string;
  expires_at?: string;
}

export interface EmailAttachment {
  name: string;
  mime: string;
  size: number;
  content_id?: string;
  inline?: boolean;
  data?: string;
  blob?: EmailBlobRef;
}

export interface EmailBounce {
  send_id?: string;
  message_id?: string;
  recipient?: string;
  action?: string;
  status?: string;
  diagnostic?: string;
}

export interface EmailMessage {
  version: number;
  message_id?: string;
  in_reply_to?: string;
  references?: string[];
  from: EmailAddress;
  to?: EmailAddress[];
  cc?: EmailAddress[];
  reply_to?: EmailAddress[];
  envelope_to?: string;
  tag?: string;
  subject?: string;
  date?: string;
  auth: EmailAuth;
  text?: string;
  html?: string;
  attachments?: EmailAttachment[];
  raw?: EmailBlobRef;
  truncated?: boolean;
  bounce?: EmailBounce;
}

export interface EmailOutAttachment {
  name: string;
  mime: string;
  size: number;
  data?: string;
  upload?: string;
  sha256?: string;
}

export interface EmailSend {
  version: number;
  send_id: string;
  from_name?: string;
  to: EmailAddress[];
  cc?: EmailAddress[];
  bcc?: EmailAddress[];
  subject: string;
  text: string;
  html?: string;
  in_reply_to?: string;
  references?: string[];
  attachments?: EmailOutAttachment[];
}

export interface EmailStatus {
  version: number;
  send_id: string;
  state: string;
  message_id?: string;
  recipient?: string;
  reason?: string;
  at: string;
}

/** The message a mail thread hangs from: first reference, else in-reply-to, else itself. */
export function emailConversationRoot(messageId?: string, inReplyTo?: string, references?: string[] | null): string {
  for (const ref of references ?? []) if (ref.trim()) return ref.trim();
  if (inReplyTo?.trim()) return inReplyTo.trim();
  return (messageId ?? "").trim();
}

export const EMAIL_THREAD_KEY_PREFIX = "mail:";

/** Read-state thread key of a mail thread (Go `EmailThreadKey`). */
export function emailThreadKey(m: Pick<EmailMessage, "message_id" | "in_reply_to" | "references">, recordId: string): string {
  const root = emailConversationRoot(m.message_id, m.in_reply_to, m.references);
  return EMAIL_THREAD_KEY_PREFIX + (root || recordId);
}

/** Parse an `email.*` plaintext; null when it is not a version-1 object. */
export function parseEmailPayload<T extends { version: number }>(plaintext: string | null | undefined): T | null {
  if (plaintext == null) return null;
  try {
    const parsed = JSON.parse(plaintext) as T;
    return parsed && typeof parsed === "object" && parsed.version === EMAIL_PAYLOAD_VERSION ? parsed : null;
  } catch {
    return null;
  }
}

// ── Blobs and uploads ────────────────────────────────────────────────────────

export function emailBlobContext(id: string): Uint8Array {
  return utf8(`email-blob:${id}`);
}

export function canonicalEmailUpload(identity: string, uploadId: string, sha256Hex: string, size: number, issuedAt: string, nonce: string): string {
  return ["poweur-email-upload", identity.trim().toLowerCase(), uploadId, sha256Hex.toLowerCase(), String(size), issuedAt, nonce].join("\n");
}

export function hex(bytes: Uint8Array): string {
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

interface WireSealed {
  ciphertext: string;
  ephemeral_public_key: string;
  nonce: string;
}

/** Fetch a blob the bridge holds for us, open it and check its hash. */
export async function openEmailBlob(
  decryptor: Decryptor,
  ref: EmailBlobRef,
  fetchImpl: typeof globalThis.fetch = globalThis.fetch,
): Promise<Uint8Array> {
  if (!/^https?:\/\//.test(ref.url)) throw new PoweurError("invalid_argument", "blob url must be http(s)");
  const response = await fetchImpl(ref.url, { credentials: "omit", referrerPolicy: "no-referrer" });
  if (response.status === 404 || response.status === 410) throw new PoweurError("not_found", "this attachment has expired on the bridge");
  if (!response.ok) throw new PoweurError("relay_error", `blob fetch failed: HTTP ${response.status}`, { status: response.status });
  const wire = (await response.json()) as WireSealed;
  const sealed: SealedPayload = { ciphertext: wire.ciphertext, ephemeralPublicKey: wire.ephemeral_public_key, nonce: wire.nonce };
  const plain = openWithDomain(await decryptor.privateKeyBytes(), sealed, EMAIL_BLOB_SEAL_DOMAIN, emailBlobContext(ref.id));
  if (ref.sha256 && hex(sha256Bytes(plain)) !== ref.sha256.toLowerCase()) {
    throw new PoweurError("invalid_signature", "blob does not match its hash");
  }
  return plain;
}

/** Seal outbound attachment bytes to the bridge's encryption key. */
export function sealEmailUpload(bridgeEncryptionKey: Uint8Array | string, bytes: Uint8Array, uploadId: string): string {
  const sealed = sealWithDomain(bridgeEncryptionKey, bytes, EMAIL_UPLOAD_SEAL_DOMAIN, emailBlobContext(uploadId));
  return JSON.stringify({ ciphertext: sealed.ciphertext, ephemeral_public_key: sealed.ephemeralPublicKey, nonce: sealed.nonce });
}

/** Base64 (standard alphabet) of bytes, for inline attachment data. */
export function toBase64Data(bytes: Uint8Array): string {
  let binary = "";
  for (let i = 0; i < bytes.length; i += 0x8000) binary += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(binary);
}

/** Decode inline attachment data. */
export function fromBase64Data(data: string): Uint8Array {
  return fromBase64(data);
}
