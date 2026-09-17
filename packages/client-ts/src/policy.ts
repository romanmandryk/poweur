/**
 * Inbox policy — the twin of `poweur policy` and
 * `packages/identity/inboxpolicy.go`.
 *
 * The relay evaluates the recipient's policy after signature verification on
 * every inbound message. Absence of the file means `open`, for
 * backward-compatibility with pre-policy identities; anonymous ingress is a
 * separate opt-in block that always defaults to deny.
 */

import { PoweurError } from "./errors.js";
import type { DavClient } from "./files.js";
import { validateIdentityName } from "./names.js";
import {
  ANON_CHALLENGE_NONE,
  ANON_CHALLENGE_PAYMENT,
  ANON_CHALLENGE_POW,
  ANON_CHALLENGE_VERIFIED,
  DEFAULT_INBOX_MODE,
  INBOX_CONTACTS_AND_REQUESTS,
  INBOX_CONTACTS_ONLY,
  INBOX_OPEN,
  type AnonymousPolicy,
  type InboxMode,
  type InboxPolicy,
} from "./types.js";

export const INBOX_POLICY_PATH = "poweur-sys/relay/inbox-policy.json";

export const ANON_DEFAULT_MAX_BYTES = 4096;
export const ANON_DEFAULT_MAX_PER_DAY = 20;

export function effectiveChallenge(policy: AnonymousPolicy): string {
  return policy.challenge || ANON_CHALLENGE_NONE;
}

export function effectiveMaxBytes(policy: AnonymousPolicy): number {
  return policy.max_bytes && policy.max_bytes > 0 ? policy.max_bytes : ANON_DEFAULT_MAX_BYTES;
}

export function effectiveMaxPerDay(policy: AnonymousPolicy): number {
  return policy.max_per_day && policy.max_per_day > 0
    ? policy.max_per_day
    : ANON_DEFAULT_MAX_PER_DAY;
}

export function validateAnonymousPolicy(policy: AnonymousPolicy): void {
  const challenge = effectiveChallenge(policy);
  const known = [
    ANON_CHALLENGE_NONE,
    ANON_CHALLENGE_POW,
    ANON_CHALLENGE_VERIFIED,
    ANON_CHALLENGE_PAYMENT,
  ];
  if (!known.includes(challenge)) {
    throw new PoweurError(
      "invalid_argument",
      `invalid anonymous challenge "${policy.challenge}" (want none, pow, verified or payment)`,
    );
  }
  if ((policy.pow_bits ?? 0) < 0 || (policy.pow_bits ?? 0) > 64) {
    throw new PoweurError("invalid_argument", "pow_bits out of range");
  }
  if ((policy.max_bytes ?? 0) < 0 || (policy.max_bytes ?? 0) > 64 * 1024) {
    throw new PoweurError("invalid_argument", "max_bytes out of range (max 65536)");
  }
  if ((policy.max_per_day ?? 0) < 0 || (policy.max_per_day ?? 0) > 10_000) {
    throw new PoweurError("invalid_argument", "max_per_day out of range (max 10000)");
  }
}

export function validateInboxPolicy(policy: InboxPolicy): void {
  if (policy.version !== 0 && policy.version !== 1) {
    throw new PoweurError("invalid_document", `unsupported inbox-policy version ${policy.version}`);
  }
  if (![INBOX_OPEN, INBOX_CONTACTS_ONLY, INBOX_CONTACTS_AND_REQUESTS].includes(policy.mode)) {
    throw new PoweurError(
      "invalid_argument",
      `invalid inbox policy mode "${policy.mode}" (want ${INBOX_OPEN}, ${INBOX_CONTACTS_ONLY} or ${INBOX_CONTACTS_AND_REQUESTS})`,
    );
  }
  if (policy.anonymous) validateAnonymousPolicy(policy.anonymous);
  validateTrustedAuthServices(policy.trusted_auth_services);
  if (policy.read_receipts) {
    if (typeof policy.read_receipts.enabled !== "boolean") {
      throw new PoweurError("invalid_document", "read_receipts.enabled must be a boolean");
    }
    if (!Array.isArray(policy.read_receipts.disabled_for ?? [])) {
      throw new PoweurError("invalid_document", "read_receipts.disabled_for must be an array");
    }
    if ((policy.read_receipts.disabled_for?.length ?? 0) > 1000) {
      throw new PoweurError("invalid_document", "read_receipts.disabled_for has too many entries (max 1000)");
    }
    const seen = new Set<string>();
    for (const identity of policy.read_receipts.disabled_for ?? []) {
      const normalized = identity.trim().toLowerCase();
      try {
        validateIdentityName(normalized);
      } catch {
        throw new PoweurError("invalid_document", `invalid read-receipt identity "${identity}"`);
      }
      if (seen.has(normalized)) {
        throw new PoweurError("invalid_document", `duplicate read-receipt identity "${normalized}"`);
      }
      seen.add(normalized);
    }
  }
}

export const MAX_TRUSTED_AUTH_SERVICES = 16;

function validateTrustedAuthServices(list: unknown): void {
  if (list === undefined) return;
  if (!Array.isArray(list)) throw new PoweurError("invalid_document", "trusted_auth_services must be an array");
  if (list.length > MAX_TRUSTED_AUTH_SERVICES) {
    throw new PoweurError("invalid_document", `trusted_auth_services has too many entries (max ${MAX_TRUSTED_AUTH_SERVICES})`);
  }
  const seen = new Set<string>();
  for (const entry of list) {
    const normalized = String(entry ?? "").trim().toLowerCase();
    try {
      validateIdentityName(normalized);
    } catch {
      throw new PoweurError("invalid_document", `invalid trusted auth service "${entry}"`);
    }
    if (seen.has(normalized)) throw new PoweurError("invalid_document", `duplicate trusted auth service "${normalized}"`);
    seen.add(normalized);
  }
}

/** Whether `sender` may deliver sign-in prompts. Mirrors Go `TrustsAuthService`. */
export function trustsAuthService(policy: InboxPolicy, sender: string): boolean {
  const wanted = String(sender ?? "").trim().toLowerCase();
  return !!wanted && (policy.trusted_auth_services ?? []).some((s) => s.trim().toLowerCase() === wanted);
}

export function sendsReadReceiptsTo(policy: InboxPolicy, peer: string): boolean {
  if (!policy.read_receipts) return true;
  if (!policy.read_receipts.enabled) return false;
  const wanted = peer.trim().toLowerCase();
  return !(policy.read_receipts.disabled_for ?? []).some((name) => name.trim().toLowerCase() === wanted);
}

/** Read the policy, or the relay default when no file exists. */
export async function readInboxPolicy(
  dav: DavClient,
): Promise<{ policy: InboxPolicy; explicit: boolean }> {
  const raw = await dav.readOptional(INBOX_POLICY_PATH);
  if (!raw) {
    return { policy: { version: 1, mode: DEFAULT_INBOX_MODE }, explicit: false };
  }
  const policy = JSON.parse(raw) as InboxPolicy;
  validateInboxPolicy(policy);
  return { policy, explicit: true };
}

export async function writeInboxPolicy(
  dav: DavClient,
  mode: InboxMode,
  anonymous?: AnonymousPolicy,
  readReceipts?: InboxPolicy["read_receipts"],
  trustedAuthServices?: string[],
): Promise<InboxPolicy> {
  const policy: InboxPolicy = { version: 1, mode };
  if (anonymous) policy.anonymous = anonymous;
  if (readReceipts) policy.read_receipts = readReceipts;
  if (trustedAuthServices?.length) {
    policy.trusted_auth_services = trustedAuthServices.map((s) => s.trim().toLowerCase());
  }
  validateInboxPolicy(policy);
  await dav.writeJson(INBOX_POLICY_PATH, policy);
  return policy;
}
