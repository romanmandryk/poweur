/**
 * Web-first identity resolution — the twin of `packages/identity/resolve.go`.
 *
 * Order is: HTTPS `/.well-known/poweur/id.json`, then DNS TXT. When both
 * answer and the keys disagree the resolve *fails* rather than picking one —
 * a key mismatch is the signature of a compromised relay or registrar, and
 * failing closed is the whole point of publishing in two places.
 *
 * DNS is injected (`TxtResolver`) because runtimes differ: Node uses
 * `node:dns`, browsers use DNS-over-HTTPS. See `./node` and `./browser`.
 */

import { parseDocument } from "./document.js";
import type { DocumentStore } from "./document-store.js";
import { stripKeyPrefix, withEd25519Prefix, withX25519Prefix, rfc3339 } from "./encoding.js";
import { PoweurError } from "./errors.js";
import { defaultFetch } from "./http.js";
import { validateIdentityName } from "./names.js";
import type { IdentityDocument, ResolveResult, ResolveSource } from "./types.js";

export const WELL_KNOWN_PATH = "/.well-known/poweur/id.json";
export const MAX_DOCUMENT_BYTES = 16 * 1024;
export const DEFAULT_TIMEOUT_MS = 5_000;

/** DNS TXT lookups, injected per runtime. */
export interface TxtResolver {
  lookupTxt(name: string): Promise<string[]>;
}

export interface ResolveOptions {
  /** "https" (default) or "http" for local integration runs. */
  scheme?: string;
  /**
   * Permit loopback/private targets. Test and dev only — this is the switch
   * that disables the SSRF guard, so it is never on by default.
   */
  allowPrivate?: boolean;
  timeoutMs?: number;
  fetch?: typeof globalThis.fetch;
  txt?: TxtResolver | null;
  skipWeb?: boolean;
  skipDns?: boolean;
  /** Set internally when already following a `moved_to`; no chains. */
  skipMovedTo?: boolean;
  cache?: ResolveCache | null;
  cacheTtlMs?: number;
  /**
   * Extra base URL to try for the well-known document. The browser cannot set
   * a Host header, so a same-origin relay endpoint is the only way to reach a
   * virtual-hosted document; the CLI never needs this.
   */
  relayUrl?: string;
  /**
   * Keeps the last verified signed document per identity (EPIC-029). Every
   * successful web fetch is written to it, and a fetched document older than
   * the stored one is refused with `document_rollback`.
   */
  store?: DocumentStore | null;
  /**
   * How old a stored document may be and still be returned when the fetch
   * fails for a reason that says nothing about the identity (network error,
   * timeout, 5xx). Zero or unset never returns a stored document.
   */
  maxStaleMs?: number;
  /** Clock override, for tests. */
  now?: () => number;
}

/** A stale result stays in the in-memory cache this long, so a dead host is
 * retried about once a minute rather than on every call. */
const STALE_CACHE_TTL_MS = 60_000;

/**
 * Errors that say nothing about the identity: the host could not be reached,
 * timed out or answered 5xx. Only these let a stored document stand in. Not
 * found, a bad signature, the wrong identity and a refused address are
 * answers, and the store never hides them.
 *
 * One difference from Go: `fetch` reports a refused redirect as the same
 * `TypeError` as a dead host, so here a redirecting host counts as transient.
 */
const transientErrors = new WeakSet<object>();

function transient<E extends object>(error: E): E {
  transientErrors.add(error);
  return error;
}

function isTransient(error: unknown): boolean {
  return typeof error === "object" && error !== null && transientErrors.has(error);
}

interface FetchedDocument {
  document: IdentityDocument;
  raw: string;
}

interface CacheEntry {
  result: ResolveResult;
  expiresAt: number;
}

/** Small TTL cache mirroring the well-known `Cache-Control: max-age=300`. */
export class ResolveCache {
  readonly #entries = new Map<string, CacheEntry>();

  get(identity: string): ResolveResult | null {
    const entry = this.#entries.get(identity);
    if (!entry) return null;
    if (Date.now() > entry.expiresAt) {
      this.#entries.delete(identity);
      return null;
    }
    return entry.result;
  }

  put(identity: string, result: ResolveResult, ttlMs: number): void {
    this.#entries.set(identity, { result, expiresAt: Date.now() + ttlMs });
  }

  clear(): void {
    this.#entries.clear();
  }
}

/**
 * Names that must never be fetched: loopback and private-range literals, and
 * the suffixes that resolve inside a LAN.
 *
 * Go checks the *resolved* IPs, which a browser cannot do — so this is a
 * name-shaped approximation. It is the conservative half of the check: it can
 * refuse something Go would allow, never the reverse, and `allowPrivate`
 * (test and dev only) turns it off entirely.
 */
const PRIVATE_LITERAL =
  /^(localhost|0\.0\.0\.0|\[?::1\]?|127\.\d{1,3}\.\d{1,3}\.\d{1,3}|10\.\d{1,3}\.\d{1,3}\.\d{1,3}|192\.168\.\d{1,3}\.\d{1,3}|169\.254\.\d{1,3}\.\d{1,3})$/i;
const PRIVATE_SUFFIX = [".localhost", ".local", ".internal", ".home.arpa"];

function isPrivateHost(identity: string): boolean {
  const host = identity.toLowerCase().replace(/\.$/, "");
  if (PRIVATE_LITERAL.test(host)) return true;
  if (PRIVATE_SUFFIX.some((suffix) => host.endsWith(suffix))) return true;
  // 172.16.0.0/12
  const match = /^172\.(\d{1,3})\.\d{1,3}\.\d{1,3}$/.exec(host);
  if (match) {
    const octet = Number(match[1]);
    return octet >= 16 && octet <= 31;
  }
  return false;
}

function assertPublicHost(identity: string, allowPrivate: boolean): void {
  if (allowPrivate) return;
  if (isPrivateHost(identity)) {
    throw new PoweurError(
      "resolve_failed",
      "refusing to fetch identity document from a private/loopback address",
    );
  }
}

async function fetchWellKnown(url: string, opts: ResolveOptions): Promise<FetchedDocument> {
  const doFetch = opts.fetch ?? defaultFetch();
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), opts.timeoutMs ?? DEFAULT_TIMEOUT_MS);
  let response: Response;
  let body: string;
  try {
    response = await doFetch(url, {
      method: "GET",
      // Redirects are refused: a redirect could walk the fetch to an
      // attacker-chosen origin after the SSRF check already passed.
      redirect: "error",
      headers: { Accept: "application/json" },
      signal: controller.signal,
    });
    if (!response.ok) {
      const failure = new PoweurError("resolve_failed", `well-known returned ${response.status}`, {
        status: response.status,
      });
      throw response.status >= 500 ? transient(failure) : failure;
    }
    body = await response.text();
  } catch (error) {
    if (error instanceof PoweurError) throw error;
    // The request never got an answer, or the answer broke off.
    throw transient(error instanceof Error ? error : new Error(String(error)));
  } finally {
    clearTimeout(timer);
  }
  if (body.length > MAX_DOCUMENT_BYTES) {
    throw new PoweurError("resolve_failed", "identity document exceeds 16KB");
  }
  let parsed: Record<string, unknown>;
  try {
    parsed = JSON.parse(body) as Record<string, unknown>;
  } catch (cause) {
    throw new PoweurError("invalid_document", "identity document is not valid JSON", { cause });
  }
  // GET /identities/{id} wraps the document; the well-known path serves it bare.
  const nested = parsed["identity_document"];
  if (nested) {
    const raw = typeof nested === "string" ? nested : JSON.stringify(nested);
    return { document: parseDocument(raw, true), raw };
  }
  return { document: parseDocument(body, true), raw: body };
}

async function resolveWeb(identity: string, opts: ResolveOptions): Promise<FetchedDocument> {
  assertPublicHost(identity, opts.allowPrivate === true);
  const scheme = opts.scheme ?? "https";
  const candidates = [`${scheme}://${identity}${WELL_KNOWN_PATH}`];
  if (opts.relayUrl) {
    const base = opts.relayUrl.replace(/\/$/, "");
    candidates.push(`${base}/identities/${encodeURIComponent(identity)}`);
  }
  let lastError: unknown;
  // The identity's own host is the one that can say "not found". If it could
  // not be reached, the outage stands even when another candidate (a relay
  // that simply doesn't host this identity) answers 404. A candidate that
  // serves a document which fails verification ends that: it is an answer.
  let outage = false;
  for (const [index, url] of candidates.entries()) {
    try {
      const fetched = await fetchWellKnown(url, opts);
      if (fetched.document.identity.toLowerCase() !== identity.toLowerCase()) {
        throw new PoweurError(
          "key_mismatch",
          "document identity does not match requested identity",
        );
      }
      return fetched;
    } catch (error) {
      // A key/identity mismatch is an attack signal, not a "try the next
      // candidate" condition — surface it immediately.
      if (error instanceof PoweurError && error.code === "key_mismatch") throw error;
      if (index === 0) {
        outage = isTransient(error);
      } else if (
        error instanceof PoweurError &&
        (error.code === "invalid_document" || error.code === "invalid_signature")
      ) {
        outage = false;
      }
      lastError = error;
    }
  }
  const failure =
    lastError instanceof Error
      ? lastError
      : new PoweurError("resolve_failed", "identity document not found");
  if (outage) {
    throw transient(failure);
  }
  if (isTransient(failure)) {
    // The last candidate was unreachable but an earlier one gave an answer:
    // report it without the outage mark.
    throw new PoweurError("resolve_failed", failure.message, { cause: failure });
  }
  throw failure;
}

async function resolveDns(identity: string, txt: TxtResolver): Promise<IdentityDocument> {
  const records = await txt.lookupTxt(`_poweur.${identity}`);
  let publicKey = "";
  for (const record of records) {
    const value = record.trim();
    if (value.startsWith("poweur-pubkey=")) {
      publicKey = withEd25519Prefix(value.slice("poweur-pubkey=".length));
      break;
    }
  }
  if (!publicKey) {
    throw new PoweurError("resolve_failed", "no poweur-pubkey TXT record");
  }

  let encryptionPublicKey = "";
  try {
    for (const record of await txt.lookupTxt(`_poweur-enc.${identity}`)) {
      const value = record.trim();
      if (value.startsWith("poweur-enckey=")) {
        encryptionPublicKey = withX25519Prefix(value.slice("poweur-enckey=".length));
        break;
      }
    }
  } catch {
    // An absent encryption record is normal; only the signing key is required.
  }

  const doc: IdentityDocument = {
    version: 1,
    identity,
    public_key: publicKey,
    relay: identity, // DNS path: the A/CNAME on the identity is the relay
    updated_at: rfc3339(),
  };
  if (encryptionPublicKey) doc.encryption_public_key = encryptionPublicKey;
  return doc;
}

/**
 * Resolve an identity to a verified document.
 *
 * @throws PoweurError `key_mismatch` when web and DNS disagree, `resolve_failed`
 *   when neither source answers, `document_rollback` when the fetched document
 *   is older than the one in `store`.
 */
export async function resolveIdentity(
  identity: string,
  options: ResolveOptions = {},
): Promise<ResolveResult> {
  const name = identity.trim().toLowerCase();
  validateIdentityName(name);
  const opts: ResolveOptions = { scheme: "https", timeoutMs: DEFAULT_TIMEOUT_MS, ...options };
  const ttl = opts.cacheTtlMs ?? 5 * 60_000;

  const now = opts.now ?? Date.now;

  const cached = opts.cache?.get(name);
  if (cached) return cached;

  let web: FetchedDocument | null = null;
  let webError: unknown;
  if (!opts.skipWeb) {
    try {
      web = await resolveWeb(name, opts);
    } catch (error) {
      if (error instanceof PoweurError && error.code === "key_mismatch") throw error;
      webError = error;
    }
  }
  const webDoc = web?.document ?? null;

  let dnsDoc: IdentityDocument | null = null;
  let dnsError: unknown;
  if (!opts.skipDns && opts.txt) {
    try {
      dnsDoc = await resolveDns(name, opts.txt);
    } catch (error) {
      dnsError = error;
    }
  }

  // A stored document may stand in only when the web fetch was tried and
  // failed transiently.
  const stale = !webDoc && isTransient(webError) ? await loadStale(name, opts, now()) : null;

  let result: ResolveResult;
  if (web && dnsDoc) {
    if (stripKeyPrefix(web.document.public_key) !== stripKeyPrefix(dnsDoc.public_key)) {
      throw new PoweurError("key_mismatch", "identity key mismatch between web and DNS sources");
    }
    result = { document: web.document, source: "both" as ResolveSource, raw: web.raw };
  } else if (web) {
    result = { document: web.document, source: "web", raw: web.raw };
  } else if (dnsDoc) {
    // DNS answered with a key only. When it is the key of the stored signed
    // document, that document says more (encryption key, relay) and DNS has
    // just confirmed it. A different key is a fresh answer and wins.
    result =
      stale && stripKeyPrefix(stale.document.public_key) === stripKeyPrefix(dnsDoc.public_key)
        ? stale
        : { document: dnsDoc, source: "dns" };
  } else if (stale) {
    result = stale;
  } else {
    // Error phrasing mirrors Go so operators reading either client's logs
    // see the same sentence for the same failure.
    if (webError && dnsError) {
      throw new PoweurError(
        "resolve_failed",
        `identity not found: web: ${(webError as Error).message}; dns: ${(dnsError as Error).message}`,
      );
    }
    if (webError) {
      throw new PoweurError(
        "resolve_failed",
        `identity not found via web: ${(webError as Error).message}`,
      );
    }
    if (dnsError) {
      throw new PoweurError(
        "resolve_failed",
        `identity not found via dns: ${(dnsError as Error).message}`,
      );
    }
    throw new PoweurError("resolve_failed", "identity not found");
  }

  if (!result.stale) {
    result.fetchedAt = now();
    // Only signed documents are stored: a DNS-only result is unsigned.
    if (opts.store && web) {
      const stored = await storeGet(opts.store, name);
      if (stored) {
        let previous: IdentityDocument | null = null;
        try {
          previous = parseDocument(stored.raw, true);
        } catch {
          // A stored document that no longer verifies guards nothing.
        }
        if (previous && documentOlder(web.document, previous)) {
          throw new PoweurError(
            "document_rollback",
            `fetched identity document is older than the stored one (${name})`,
          );
        }
      }
      try {
        await opts.store.put(name, web.raw, result.fetchedAt);
      } catch {
        // Best effort: a full or blocked store must not fail a good answer.
      }
    }
  }

  // Follow a hosted-migration tombstone exactly once — never a chain. The
  // target is stored under its own name; this identity keeps its tombstone.
  if (!opts.skipMovedTo && result.document.moved_to) {
    const target = result.document.moved_to.trim().toLowerCase();
    if (target && target !== name) {
      try {
        result = await resolveIdentity(target, { ...opts, skipMovedTo: true });
      } catch {
        // A dangling tombstone leaves the original document in play.
      }
    }
  }

  opts.cache?.put(name, result, result.stale ? Math.min(ttl, STALE_CACHE_TTL_MS) : ttl);
  return result;
}

async function storeGet(store: DocumentStore, identity: string) {
  try {
    return await store.get(identity);
  } catch {
    return null;
  }
}

/** The stored document, when there is one, it still verifies and it is no
 * older than `maxStaleMs`. */
async function loadStale(
  identity: string,
  opts: ResolveOptions,
  now: number,
): Promise<ResolveResult | null> {
  const maxStale = opts.maxStaleMs ?? 0;
  if (!opts.store || maxStale <= 0) return null;
  const stored = await storeGet(opts.store, identity);
  if (!stored || now - stored.fetchedAt > maxStale) return null;
  try {
    // Verified again on the way out, so a changed record is not trusted.
    const document = parseDocument(stored.raw, true);
    if (document.identity.toLowerCase() !== identity) return null;
    return { document, source: "stale", stale: true, fetchedAt: stored.fetchedAt, raw: stored.raw };
  } catch {
    return null;
  }
}

/** Whether `fresh` was signed before `previous`. Unparseable timestamps are
 * not compared. */
function documentOlder(fresh: IdentityDocument, previous: IdentityDocument): boolean {
  const a = Date.parse(fresh.updated_at);
  const b = Date.parse(previous.updated_at);
  return !Number.isNaN(a) && !Number.isNaN(b) && a < b;
}

/** The recipient's signing key, bare base64url, or null when unresolvable. */
export async function resolveSigningKey(
  identity: string,
  options: ResolveOptions = {},
): Promise<string | null> {
  try {
    const { document } = await resolveIdentity(identity, options);
    return document.public_key ? stripKeyPrefix(document.public_key) : null;
  } catch {
    return null;
  }
}

/** The recipient's X25519 key, bare base64url, or null when unresolvable. */
export async function resolveEncryptionKey(
  identity: string,
  options: ResolveOptions = {},
): Promise<string | null> {
  try {
    const { document } = await resolveIdentity(identity, options);
    return document.encryption_public_key
      ? stripKeyPrefix(document.encryption_public_key)
      : null;
  } catch {
    return null;
  }
}
