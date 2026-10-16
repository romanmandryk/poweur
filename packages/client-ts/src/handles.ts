/**
 * `.poweur/public/handles.json` — the twin of `packages/identity/handles.go`
 * (INT-006-T5, PCP-0011).
 *
 * Links the ID to its accounts on other networks so its name works there: a
 * Bluesky DID makes `alice.poweur.net` a Bluesky handle, and a Nostr key makes
 * it a NIP-05 address (`alice@poweur.net` for a hosted ID). The relay serves
 * the discovery files; this module validates before the write and turns the
 * `npub` people copy from Nostr apps into the hex the document stores.
 */

import { PoweurError } from "./errors.js";
import type { SystemFiles } from "./systemfiles.js";

export const HANDLES_PATH = ".poweur/public/handles.json";
export const MAX_NOSTR_RELAYS = 8;

export interface Handles {
  version: number;
  /** The Bluesky account's DID (`did:plc:…` or `did:web:…`). */
  atproto_did?: string;
  /** The Nostr public key, 64 lowercase hex characters. */
  nostr_pubkey?: string;
  /** Optional `wss://` relay hints for that key. */
  nostr_relays?: string[];
  /** The linked Fediverse account, `user@server`. */
  fediverse_acct?: string;
  /** That account's ActivityPub actor URL. */
  fediverse_actor?: string;
}

const DID_PLC = /^did:plc:[a-z2-7]{24}$/;
const DID_WEB = /^did:web:[a-z0-9]([a-z0-9.-]*[a-z0-9])?(%3[aA][0-9]{1,5})?(:[A-Za-z0-9._~-]+)*$/;
const NOSTR_HEX = /^[0-9a-f]{64}$/;
const ACCT = /^[A-Za-z0-9_.-]{1,64}@[a-z0-9]([a-z0-9.-]*[a-z0-9])?\.[a-z]{2,63}$/;

/** identity.ValidFediverseAcct */
export function validFediverseAcct(s: string): boolean {
  return s.length <= 256 && ACCT.test(s);
}

function validActorURL(s: string): boolean {
  if (s.length > 512 || !s.startsWith("https://") || /[\s"<>#]/.test(s)) return false;
  const host = s.slice("https://".length).split("/")[0] ?? "";
  return host !== "" && !/[@?]/.test(host);
}

/** identity.ValidATProtoDID */
export function validATProtoDID(s: string): boolean {
  return s.length <= 256 && (DID_PLC.test(s) || DID_WEB.test(s));
}

/** identity.ValidNostrPubkey */
export function validNostrPubkey(s: string): boolean {
  return NOSTR_HEX.test(s);
}

/** Structural validation mirroring `Handles.Validate`. */
export function validateHandles(h: Handles): void {
  const fail = (detail: string) => {
    throw new PoweurError("invalid_document", detail);
  };
  if (h.version !== 1) fail(`unsupported handles version ${h.version}`);
  if (h.atproto_did && !validATProtoDID(h.atproto_did)) fail("atproto_did must be a did:plc or did:web identifier");
  if (h.nostr_pubkey && !validNostrPubkey(h.nostr_pubkey)) {
    fail("nostr_pubkey must be 64 lowercase hex characters (convert an npub first)");
  }
  const relays = h.nostr_relays ?? [];
  if (relays.length > 0 && !h.nostr_pubkey) fail("nostr_relays need a nostr_pubkey");
  if (relays.length > MAX_NOSTR_RELAYS) fail(`too many nostr_relays (max ${MAX_NOSTR_RELAYS})`);
  if (!h.fediverse_acct !== !h.fediverse_actor) fail("fediverse_acct and fediverse_actor go together");
  if (h.fediverse_acct && !validFediverseAcct(h.fediverse_acct)) fail("fediverse_acct must look like user@server.example");
  if (h.fediverse_actor && !validActorURL(h.fediverse_actor)) fail("fediverse_actor must be an https URL");
  for (const r of relays) {
    if (!r.startsWith("wss://") || r.length > 256 || /[\s"<>]/.test(r)) fail(`nostr relay "${r}" must be a wss:// URL`);
  }
}

const BECH32 = "qpzry9x8gf2tvdw0s3jn54khce6mua7l";

function bech32Polymod(values: number[]): number {
  const gen = [0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3];
  let chk = 1;
  for (const v of values) {
    const top = chk >>> 25;
    chk = ((chk & 0x1ffffff) << 5) ^ v;
    for (let i = 0; i < 5; i++) if ((top >>> i) & 1) chk ^= gen[i]!;
  }
  return chk;
}

/**
 * Accepts a Nostr public key as hex or as an `npub…` (NIP-19) and returns the
 * hex form, or null when it is neither.
 */
export function normalizeNostrPubkey(input: string): string | null {
  const s = input.trim().toLowerCase().replace(/^nostr:/, "");
  if (validNostrPubkey(s)) return s;
  if (!s.startsWith("npub1") || s.length !== 63) return null;
  const data: number[] = [];
  for (const c of s.slice(5)) {
    const v = BECH32.indexOf(c);
    if (v < 0) return null;
    data.push(v);
  }
  const hrp = [..."npub"].map((c) => c.charCodeAt(0));
  const expanded = [...hrp.map((c) => c >> 5), 0, ...hrp.map((c) => c & 31)];
  if (bech32Polymod([...expanded, ...data]) !== 1) return null;
  // 5-bit groups (without the 6-character checksum) → bytes.
  let acc = 0;
  let bits = 0;
  const bytes: number[] = [];
  for (const v of data.slice(0, -6)) {
    acc = (acc << 5) | v;
    bits += 5;
    if (bits >= 8) {
      bits -= 8;
      bytes.push((acc >> bits) & 0xff);
    }
  }
  if (bytes.length !== 32) return null;
  return bytes.map((b) => b.toString(16).padStart(2, "0")).join("");
}

/**
 * Looks up a Fediverse account (`@alice@mastodon.social` or `alice@…`) with
 * WebFinger at its own server and returns the acct and actor URL to store, or
 * throws with a message for the person.
 */
export async function lookupFediverseAccount(
  input: string,
  fetchImpl: typeof globalThis.fetch,
): Promise<{ acct: string; actor: string }> {
  const acct = input.trim().replace(/^@/, "").replace(/@([^@]+)$/, (_, d: string) => "@" + d.toLowerCase());
  if (!validFediverseAcct(acct)) {
    throw new PoweurError("invalid_argument", "Enter the account like @alice@mastodon.social");
  }
  const server = acct.split("@")[1]!;
  let response: Response;
  try {
    response = await fetchImpl(`https://${server}/.well-known/webfinger?resource=${encodeURIComponent("acct:" + acct)}`, {
      headers: { Accept: "application/jrd+json, application/json" },
    });
  } catch {
    throw new PoweurError("resolve_failed", `Could not reach ${server}`);
  }
  if (!response.ok) throw new PoweurError("not_found", `${server} does not know @${acct}`);
  const jrd = (await response.json()) as { subject?: string; links?: { rel?: string; type?: string; href?: string }[] };
  const self = (jrd.links ?? []).find(
    (l) => l.rel === "self" && /^application\/(activity\+json|ld\+json)/.test(l.type ?? "") && typeof l.href === "string",
  );
  if (!self?.href || !validActorURL(self.href)) throw new PoweurError("not_found", `${server} did not return an account for @${acct}`);
  const subject = (jrd.subject ?? "").replace(/^acct:/, "");
  return { acct: validFediverseAcct(subject) ? subject : acct, actor: self.href };
}

export function emptyHandles(): Handles {
  return { version: 1 };
}

/** Read our handles (absent is none). */
export async function readHandles(files: SystemFiles): Promise<Handles> {
  const raw = await files.readOptional(HANDLES_PATH);
  if (!raw) return emptyHandles();
  const handles = JSON.parse(raw) as Handles;
  validateHandles(handles);
  return handles;
}

/** Write them, dropping empty fields; with nothing linked the file is removed. */
export async function writeHandles(files: SystemFiles, handles: Handles): Promise<Handles> {
  const document: Handles = { version: 1 };
  if (handles.atproto_did?.trim()) document.atproto_did = handles.atproto_did.trim();
  if (handles.nostr_pubkey?.trim()) document.nostr_pubkey = handles.nostr_pubkey.trim();
  const relays = (handles.nostr_relays ?? []).map((r) => r.trim()).filter(Boolean);
  if (relays.length && document.nostr_pubkey) document.nostr_relays = relays;
  if (handles.fediverse_acct?.trim() && handles.fediverse_actor?.trim()) {
    document.fediverse_acct = handles.fediverse_acct.trim();
    document.fediverse_actor = handles.fediverse_actor.trim();
  }
  validateHandles(document);
  if (!document.atproto_did && !document.nostr_pubkey && !document.fediverse_acct) {
    await files.remove(HANDLES_PATH);
    return document;
  }
  await files.writeJson(HANDLES_PATH, document);
  return document;
}
