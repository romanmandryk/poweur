/**
 * Sign in with Poweur ID — the TypeScript twin of `packages/identity/signin.go`
 * and `packages/identity/signin/`.
 *
 * A relying party needs three things from this module:
 *
 * ```ts
 * const verifier = new SignInVerifier({ origin: "https://guestbook.example" });
 * const request = verifier.newRequest({ statement: "Sign in to the Guestbook" });
 * // … hand `request` to the user's signer, receive `encoded` back …
 * const result = await verifier.verify(encoded);   // throws on any failure
 * result.identity;                                  // the login subject
 * ```
 *
 * The verifier is stateless beyond a five-minute nonce cache: no tokens are
 * issued by Poweur, nothing is registered with any authority, and the only
 * network call is identity resolution.
 *
 * Go stays canonical. Every rule here is pinned by
 * `packages/identity/testdata/vectors/signin.json` via `test/conformance.test.ts`.
 */

import { canonicalSessionRegistration } from "./canonical.js";
import {
  parseEd25519PublicKey,
  signCanonical,
  verifyCanonical,
} from "./crypto/index.js";
import { keyValidAt } from "./document.js";
import { fromBase64, fromUtf8, randomBytes, rfc3339, toBase64url, utf8 } from "./encoding.js";
import { PoweurError } from "./errors.js";
import { validateIdentityName } from "./names.js";
import { resolveIdentity, type ResolveOptions } from "./resolve.js";
import type { IdentityDocument, ResolveResult } from "./types.js";

/** Protocol version carried in `poweur_auth`. */
export const SIGNIN_VERSION = "1";
/** Max `expires_at - issued_at`, on a request and on the approval it produces. */
export const SIGNIN_MAX_TTL_MS = 5 * 60_000;
/** Clock skew tolerated on `issued_at`. */
export const SIGNIN_MAX_SKEW_MS = 2 * 60_000;
/** `key_id` for an approval signed by the long-lived identity key. */
export const SIGNIN_KEY_ID_IDENTITY = "identity";
/** `key_id` prefix for an approval signed by a registered session key. */
export const SIGNIN_KEY_ID_SESSION_PREFIX = "session:";
/** Longest statement a signer will render (and a user will read). */
export const SIGNIN_MAX_STATEMENT_LEN = 300;
/** Most scopes one consent screen may carry. */
export const SIGNIN_MAX_SCOPES = 16;
/** Longest session TTL a delegation proof may claim — mirrors the relay's cap. */
export const SESSION_MAX_TTL_MS = 24 * 60 * 60_000;

export const SIGNIN_ACTIONS = ["signin", "signup", "link"] as const;
export type SignInAction = (typeof SIGNIN_ACTIONS)[number];

export const SCOPE_PROFILE_READ = "profile:read";
export const SCOPE_MESSAGES_SEND = "messages:send";
const SCOPE_DAV_RW = "dav:rw:";
const SCOPE_DAV_READ = "dav:read:";
/** Tree root a connected app may be granted space under. */
export const APPS_ROOT = "apps";

/** Where a relying party publishes its Sign-In metadata. */
export const RP_METADATA_PATH = "/.well-known/poweur.json";
export const MAX_RP_METADATA_BYTES = 16 * 1024;

export interface SignInRequest {
  poweur_auth: string;
  request_id: string;
  domain?: string;
  audience: string;
  nonce: string;
  issued_at: string;
  expires_at: string;
  action: string;
  statement?: string;
  response_uri?: string;
  scopes?: string[];
}

/**
 * Session-delegation proof. Byte-identical to the relay's `SessionProof`, and
 * validated by the same rules (`verifySessionProof`).
 */
export interface SignInSessionProof {
  session_public_key: string;
  issued_at: string;
  expires_at: string;
  nonce: string;
  identity_signature: string;
}

export interface SignInResponse {
  poweur_auth: string;
  request_id: string;
  identity: string;
  audience: string;
  nonce: string;
  issued_at: string;
  expires_at: string;
  action: string;
  statement?: string;
  scopes?: string[];
  key_id: string;
  session_proof?: SignInSessionProof | null;
  signature: string;
}

/** Relying-party metadata served at `/.well-known/poweur.json`. */
export interface RelyingPartyMetadata {
  poweur_auth: string;
  origin: string;
  name: string;
  logo_uri?: string;
  response_uris?: string[];
  scopes?: string[];
  app_id?: string;
  transports?: string[];
  poll_uri?: string;
  contact_uri?: string;
}

function fail(message: string): never {
  throw new PoweurError("invalid_argument", `sign-in: ${message}`);
}

// ── Origins and app namespaces ───────────────────────────────────────────────

/**
 * Canonical origin form used in `audience`: lowercase scheme and host, default
 * port dropped, nothing else. An origin is not a URL — a path, query, fragment
 * or userinfo is rejected rather than trimmed away.
 */
export function normalizeOrigin(raw: string): string {
  const trimmed = (raw ?? "").trim();
  if (!trimmed) fail("empty origin");
  let u: URL;
  try {
    u = new URL(trimmed);
  } catch {
    return fail("origin unparseable");
  }
  const scheme = u.protocol.replace(/:$/, "").toLowerCase();
  if (scheme !== "https" && scheme !== "http") {
    fail("origin scheme must be https (http only for local development)");
  }
  if (u.username || u.password) fail("origin must not carry userinfo");
  if (u.search || u.hash) fail("origin must not carry query or fragment");
  if (u.pathname && u.pathname !== "/") fail("origin must not carry a path");
  const host = u.hostname.toLowerCase();
  if (!host) fail("origin has no host");
  let port = u.port;
  if ((scheme === "https" && port === "443") || (scheme === "http" && port === "80")) port = "";
  return port ? `${scheme}://${host}:${port}` : `${scheme}://${host}`;
}

/**
 * Does `rawUrl` live at `origin`? The `response_uri` check: a signer never
 * posts an approval anywhere but the origin it just showed the user.
 */
export function sameOrigin(origin: string, rawUrl: string): boolean {
  try {
    const u = new URL((rawUrl ?? "").trim());
    return normalizeOrigin(`${u.protocol}//${u.host}`) === normalizeOrigin(origin);
  } catch {
    return false;
  }
}

/**
 * App namespace derived from a verified origin by reversing the host labels:
 * `https://guestbook.poweur.net` → `net.poweur.guestbook`.
 *
 * Derived from the *signed* audience rather than declared by the RP, which is
 * what makes scope escalation structurally impossible rather than policy-checked.
 */
export function signInAppId(origin: string): string {
  const host = new URL(normalizeOrigin(origin)).hostname;
  const labels = host.split(".");
  if (labels.length < 2) fail("origin host needs at least two labels to derive an app id");
  if (labels.some((l) => l === "")) fail("origin host has an empty label");
  return labels.reverse().join(".");
}

// ── Scopes ───────────────────────────────────────────────────────────────────

/**
 * Canonicalize one scope. `dav:` paths lose leading and trailing slashes, so
 * `dav:rw:/apps/x/` and `dav:rw:apps/x` are one scope with one signature.
 */
export function normalizeSignInScope(raw: string): string {
  const s = (raw ?? "").trim();
  if (!s) fail("empty scope");
  if (s === SCOPE_PROFILE_READ || s === SCOPE_MESSAGES_SEND) return s;
  const prefix = s.startsWith(SCOPE_DAV_RW)
    ? SCOPE_DAV_RW
    : s.startsWith(SCOPE_DAV_READ)
      ? SCOPE_DAV_READ
      : "";
  if (!prefix) fail(`unknown scope ${JSON.stringify(raw)}`);
  const path = s.slice(prefix.length).replace(/^\/+/, "").replace(/\/+$/, "");
  if (!path) fail(`${prefix} needs a path`);
  for (const seg of path.split("/")) {
    if (seg === "" || seg === "." || seg === "..") {
      fail(`invalid path segment in ${JSON.stringify(raw)}`);
    }
  }
  return prefix + path;
}

/** The tree path a `dav:` scope covers, or null for non-dav scopes. */
export function signInScopePath(scope: string): { path: string; write: boolean } | null {
  if (scope.startsWith(SCOPE_DAV_RW)) return { path: scope.slice(SCOPE_DAV_RW.length), write: true };
  if (scope.startsWith(SCOPE_DAV_READ)) {
    return { path: scope.slice(SCOPE_DAV_READ.length), write: false };
  }
  return null;
}

/**
 * Normalize, de-duplicate and sort. Sorting is what makes the canonical string
 * order-independent, so a signer may reorder scopes for display.
 */
export function normalizeSignInScopes(scopes: string[] | undefined | null): string[] {
  const list = scopes ?? [];
  if (list.length > SIGNIN_MAX_SCOPES) fail(`at most ${SIGNIN_MAX_SCOPES} scopes`);
  const out: string[] = [];
  for (const s of list) {
    const n = normalizeSignInScope(s);
    if (!out.includes(n)) out.push(n);
  }
  return out.sort();
}

/** A `dav:` scope must stay under `apps/<appId>`. */
export function checkSignInScopeNamespace(scope: string, appId: string): void {
  const parsed = signInScopePath(scope);
  if (!parsed) return;
  const want = `${APPS_ROOT}/${appId}`;
  if (parsed.path !== want && !parsed.path.startsWith(`${want}/`)) {
    fail(`${JSON.stringify(scope)} is outside this app's namespace ${JSON.stringify(want)}`);
  }
}

// ── Canonical strings ────────────────────────────────────────────────────────

/**
 * The exact bytes a signer signs (`identity.CanonicalSignInResponse`).
 * Line-oriented, which is why `statement` may not contain CR or LF.
 */
export function canonicalSignInResponse(input: {
  version: string;
  requestId: string;
  identity: string;
  audience: string;
  nonce: string;
  issuedAt: string;
  expiresAt: string;
  action: string;
  statement?: string;
  scopes?: string[];
  keyId: string;
}): string {
  return [
    "poweur-signin",
    input.version,
    input.requestId,
    input.identity.trim().toLowerCase(),
    input.audience,
    input.nonce,
    input.issuedAt,
    input.expiresAt,
    input.action,
    input.statement ?? "",
    (input.scopes ?? []).join(","),
    input.keyId,
  ].join("\n");
}

/** The signing string for a response object. */
export function signInResponseCanonical(resp: SignInResponse): string {
  return canonicalSignInResponse({
    version: resp.poweur_auth,
    requestId: resp.request_id,
    identity: resp.identity,
    audience: resp.audience,
    nonce: resp.nonce,
    issuedAt: resp.issued_at,
    expiresAt: resp.expires_at,
    action: resp.action,
    statement: resp.statement,
    scopes: resp.scopes,
    keyId: resp.key_id,
  });
}

// The session-registration string is defined once, in `canonical.ts`: the
// relay, the CLI and this module all sign the same bytes. It is re-exported
// from the package root there, not here.

// ── Field validation ─────────────────────────────────────────────────────────

/** Reject statements that break the line-oriented string or a consent screen. */
export function validateSignInStatement(statement: string | undefined): void {
  const s = statement ?? "";
  if (new TextEncoder().encode(s).length > SIGNIN_MAX_STATEMENT_LEN) {
    fail(`statement longer than ${SIGNIN_MAX_STATEMENT_LEN} bytes`);
  }
  for (const ch of s) {
    const code = ch.codePointAt(0)!;
    if (ch === "\n" || ch === "\r") fail("statement must be a single line");
    if (code < 0x20 || code === 0x7f) fail("statement contains a control character");
  }
}

export function validateSignInAction(action: string): void {
  if (!(SIGNIN_ACTIONS as readonly string[]).includes(action)) {
    fail(`invalid action ${JSON.stringify(action)}`);
  }
}

function parseRfc3339(value: string, field: string): number {
  const ms = Date.parse(value ?? "");
  if (Number.isNaN(ms)) fail(`${field} must be RFC3339`);
  return ms;
}

function validateWindow(issuedAt: string, expiresAt: string, nowMs: number): void {
  const iat = parseRfc3339(issuedAt, "issued_at");
  const exp = parseRfc3339(expiresAt, "expires_at");
  if (exp <= iat) fail("expires_at must be after issued_at");
  if (exp - iat > SIGNIN_MAX_TTL_MS) fail("validity window exceeds the 5 minute maximum");
  if (iat > nowMs + SIGNIN_MAX_SKEW_MS) fail("issued_at is in the future");
  if (nowMs > exp) fail("expired");
}

// ── Requests ─────────────────────────────────────────────────────────────────

/** Canonicalized copy of a request, so signer and verifier see one object. */
export function normalizeSignInRequest(req: SignInRequest): SignInRequest {
  const audience = normalizeOrigin(req.audience);
  const out: SignInRequest = {
    ...req,
    poweur_auth: (req.poweur_auth ?? "").trim() || SIGNIN_VERSION,
    request_id: (req.request_id ?? "").trim(),
    audience,
    nonce: (req.nonce ?? "").trim(),
    action: (req.action ?? "").trim().toLowerCase(),
    statement: (req.statement ?? "").trim(),
    domain: ((req.domain ?? "").trim() || new URL(audience).hostname).toLowerCase(),
    response_uri: (req.response_uri ?? "").trim(),
  };
  const scopes = normalizeSignInScopes(req.scopes);
  if (scopes.length) out.scopes = scopes;
  else delete out.scopes;
  if (!out.statement) delete out.statement;
  if (!out.response_uri) delete out.response_uri;
  return out;
}

/** Check a request against the protocol rules at `nowMs`. */
export function validateSignInRequest(req: SignInRequest, nowMs = Date.now()): SignInRequest {
  const n = normalizeSignInRequest(req);
  if (n.poweur_auth !== SIGNIN_VERSION) {
    fail(`unsupported protocol version ${JSON.stringify(n.poweur_auth)}`);
  }
  if (!n.request_id || !n.nonce) fail("request_id and nonce are required");
  validateSignInAction(n.action);
  validateSignInStatement(n.statement);
  validateWindow(n.issued_at, n.expires_at, nowMs);
  // The confused-deputy guard: an approval only travels back to the origin
  // whose name the user was shown.
  if (n.response_uri && !sameOrigin(n.audience, n.response_uri)) {
    fail("response_uri must be same-origin with audience");
  }
  const appId = signInAppId(n.audience);
  for (const s of n.scopes ?? []) checkSignInScopeNamespace(s, appId);
  return n;
}

// ── Encoding ─────────────────────────────────────────────────────────────────

function b64urlEncodeJson(value: unknown): string {
  return toBase64url(utf8(JSON.stringify(value)));
}

function b64urlDecodeToText(value: string): string {
  // fromBase64 accepts every variant the protocol has emitted (raw/padded,
  // std/url) — a user pasting a code should not have to know which they copied.
  return fromUtf8(fromBase64(value));
}

function decodeBlob<T>(encoded: string, what: string): T {
  const s = (encoded ?? "").trim();
  if (!s) fail(`empty ${what}`);
  if (s.length > MAX_RP_METADATA_BYTES) fail(`${what} too large`);
  const json = s.startsWith("{") ? s : (() => {
    try {
      return b64urlDecodeToText(s);
    } catch {
      return fail(`${what} is neither JSON nor base64url`);
    }
  })();
  try {
    return JSON.parse(json) as T;
  } catch {
    return fail(`${what} is not valid JSON`);
  }
}

/** Compact JSON, base64url without padding — the one form every transport carries. */
export function encodeSignInRequest(req: SignInRequest): string {
  return b64urlEncodeJson(req);
}

/** Accepts the encoded form, padded base64url, or raw JSON. */
export function decodeSignInRequest(encoded: string): SignInRequest {
  return decodeBlob<SignInRequest>(encoded, "request");
}

export function encodeSignInResponse(resp: SignInResponse): string {
  return b64urlEncodeJson(resp);
}

export function decodeSignInResponse(encoded: string): SignInResponse {
  return decodeBlob<SignInResponse>(encoded, "response");
}

/** The deep-link / QR payload for a request. */
export function signInDeepLink(req: SignInRequest): string {
  return `poweur://auth?request=${encodeSignInRequest(req)}`;
}

/** The same request as a handoff URL into a web signer. */
export function signInWebLink(signerBase: string, req: SignInRequest): string {
  const base = signerBase.trim().replace(/[?&]+$/, "");
  const sep = base.includes("?") ? "&" : "?";
  return `${base}${sep}auth=${encodeURIComponent(encodeSignInRequest(req))}`;
}

// ── Session delegation ───────────────────────────────────────────────────────

export interface VerifiedSessionProof {
  publicKey: Uint8Array;
  issuedAtMs: number;
  expiresAtMs: number;
}

/**
 * Validate a session-delegation proof against the identity's long-lived key
 * and return the session key the payload must then verify against.
 *
 * Same rules as `identity.VerifySessionProof` in Go, which is also what the
 * relay runs on a forwarded session-signed message.
 */
export function verifySessionProof(
  identityPublicKey: Uint8Array,
  identityName: string,
  proof: SignInSessionProof,
  nowMs = Date.now(),
): VerifiedSessionProof {
  if (
    !proof?.session_public_key ||
    !proof.issued_at ||
    !proof.expires_at ||
    !proof.nonce ||
    !proof.identity_signature
  ) {
    fail("session proof incomplete");
  }
  const iat = parseRfc3339(proof.issued_at, "session proof issued_at");
  const exp = parseRfc3339(proof.expires_at, "session proof expires_at");
  if (nowMs > exp) fail("session proof expired");
  if (exp - iat > SESSION_MAX_TTL_MS) fail("session proof exceeds max TTL");
  const pub = parseEd25519PublicKey(proof.session_public_key);
  // The proof is signed over the *normalized* key encoding, so re-encode
  // rather than trusting whatever variant travelled on the wire.
  const normalized = toBase64url(pub);
  const canonical = canonicalSessionRegistration(
    identityName,
    normalized,
    proof.issued_at,
    proof.expires_at,
    proof.nonce,
  );
  if (!verifyCanonical(identityPublicKey, canonical, proof.identity_signature)) {
    fail("session proof signature invalid");
  }
  return { publicKey: pub, issuedAtMs: iat, expiresAtMs: exp };
}

/** The session id in `key_id`, or "" when the identity key signed. */
export function sessionIdFromKeyId(keyId: string): string {
  return keyId?.startsWith(SIGNIN_KEY_ID_SESSION_PREFIX)
    ? keyId.slice(SIGNIN_KEY_ID_SESSION_PREFIX.length)
    : "";
}

// ── Signing ──────────────────────────────────────────────────────────────────

export interface SignInSignOptions {
  identity: string;
  /** Ed25519 seed or 64-byte private key: the identity key, or a session key. */
  privateKey: Uint8Array;
  sessionId?: string;
  sessionProof?: SignInSessionProof;
  nowMs?: number;
}

/**
 * Produce the user-signed approval for a request.
 *
 * The request is validated first: a signer must never sign an object it would
 * not itself accept. Origin verification (`fetchRelyingPartyMetadata`) is the
 * caller's job — this function does no I/O.
 */
export function signSignInRequest(
  req: SignInRequest,
  opts: SignInSignOptions,
): SignInResponse {
  const nowMs = opts.nowMs ?? Date.now();
  const n = validateSignInRequest(req, nowMs);
  const identity = (opts.identity ?? "").trim().toLowerCase();
  validateIdentityName(identity);

  let keyId = SIGNIN_KEY_ID_IDENTITY;
  if (opts.sessionProof || opts.sessionId) {
    if (!opts.sessionProof || !opts.sessionId) {
      fail("session signing needs both a session id and a proof");
    }
    keyId = SIGNIN_KEY_ID_SESSION_PREFIX + opts.sessionId;
  }

  const resp: SignInResponse = {
    poweur_auth: SIGNIN_VERSION,
    request_id: n.request_id,
    identity,
    audience: n.audience,
    nonce: n.nonce,
    // The approval copies the challenge's window verbatim: valid exactly as
    // long as the request was, never longer.
    issued_at: n.issued_at,
    expires_at: n.expires_at,
    action: n.action,
    key_id: keyId,
    signature: "",
  };
  if (n.statement) resp.statement = n.statement;
  if (n.scopes?.length) resp.scopes = n.scopes;
  if (opts.sessionProof) resp.session_proof = opts.sessionProof;
  resp.signature = signCanonical(opts.privateKey, signInResponseCanonical(resp));
  return resp;
}

// ── Consent rendering ────────────────────────────────────────────────────────

/**
 * Render a scope as a sentence. Every scope a user approves must be shown this
 * way — "wants dav:rw:apps/net.example" tells a user nothing about the risk.
 */
export function describeScope(scope: string, appName = ""): string {
  const who = appName || "This app";
  if (scope === SCOPE_PROFILE_READ) return `${who} can read your public profile (name, avatar).`;
  if (scope === SCOPE_MESSAGES_SEND) return `${who} can send messages from your identity.`;
  const parsed = signInScopePath(scope);
  if (!parsed) {
    return `${who} requests an unrecognized permission (${scope}) — do not approve.`;
  }
  return parsed.write
    ? `${who} can read and write files in /${parsed.path} in your home. Nothing outside that folder.`
    : `${who} can read files in /${parsed.path} in your home. Nothing outside that folder, and it cannot write.`;
}

export function describeScopes(scopes: string[], appName = ""): string[] {
  return scopes.map((s) => describeScope(s, appName));
}

/** The one-line headline of a consent screen. */
export function summarizeSignInRequest(req: SignInRequest, appName = ""): string {
  const host = req.domain || req.audience.replace(/^https?:\/\//, "");
  const label = appName && appName.toLowerCase() !== host.toLowerCase() ? `${appName} (${host})` : host;
  if (req.action === "signup") return `Create an account at ${label} with your Poweur ID`;
  if (req.action === "link") return `Link your Poweur ID to your existing account at ${label}`;
  return `Sign in to ${label} with your Poweur ID`;
}

// ── Relying-party metadata ───────────────────────────────────────────────────

/** Check an RP metadata document against the origin it was served from. */
export function validateRelyingPartyMetadata(
  meta: RelyingPartyMetadata,
  servedFrom = "",
): void {
  if (meta?.poweur_auth !== SIGNIN_VERSION) {
    fail(`metadata poweur_auth is ${JSON.stringify(meta?.poweur_auth)}`);
  }
  const origin = normalizeOrigin(meta.origin);
  if (servedFrom && origin !== normalizeOrigin(servedFrom)) {
    fail(`metadata at ${normalizeOrigin(servedFrom)} claims origin ${origin}`);
  }
  if (!(meta.name ?? "").trim()) fail("metadata name is required");
  for (const u of meta.response_uris ?? []) {
    if (!sameOrigin(origin, u)) fail(`response_uri ${JSON.stringify(u)} is not same-origin`);
  }
  // A third-party logo would leak the pending approval to whoever serves it.
  if (meta.poll_uri && !sameOrigin(origin, meta.poll_uri)) fail("poll_uri is not same-origin");
  if (meta.logo_uri && !sameOrigin(origin, meta.logo_uri)) fail("logo_uri is not same-origin");
  const appId = signInAppId(origin);
  if (meta.app_id && meta.app_id !== appId) {
    fail(`app_id ${JSON.stringify(meta.app_id)} does not match the origin's namespace`);
  }
  for (const s of meta.scopes ?? []) checkSignInScopeNamespace(normalizeSignInScope(s), appId);
}

/**
 * Has the RP published `uri` as a delivery target? Publishing no list is the
 * permissive demo default; publishing it stops an open redirect elsewhere on
 * the RP from becoming an approval leak.
 */
export function metadataAllowsResponseUri(meta: RelyingPartyMetadata, uri: string): boolean {
  if (!(uri ?? "").trim()) return true;
  if (!sameOrigin(meta.origin, uri)) return false;
  const list = meta.response_uris ?? [];
  if (!list.length) return true;
  const want = uri.replace(/\/+$/, "").toLowerCase();
  return list.some((a) => a.replace(/\/+$/, "").toLowerCase() === want);
}

/**
 * Fetch and validate an RP's metadata. This is how a signer *verifies* an
 * origin: anyone can put any audience in a request, but only the operator of
 * that origin can serve a document at it over a valid TLS certificate.
 *
 * Redirects are refused — a redirect would let one origin answer for another,
 * which is exactly the confusion this fetch exists to prevent.
 */
export async function fetchRelyingPartyMetadata(
  origin: string,
  options: { fetch?: typeof globalThis.fetch; timeoutMs?: number } = {},
): Promise<RelyingPartyMetadata> {
  const norm = normalizeOrigin(origin);
  const doFetch = options.fetch ?? globalThis.fetch;
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), options.timeoutMs ?? 5_000);
  try {
    const res = await doFetch(norm + RP_METADATA_PATH, {
      headers: { accept: "application/json" },
      redirect: "error",
      signal: controller.signal,
    });
    if (!res.ok) {
      throw new PoweurError(
        "relay_error",
        `relying-party metadata at ${norm}${RP_METADATA_PATH} returned ${res.status}`,
        { status: res.status },
      );
    }
    const text = await res.text();
    if (text.length > MAX_RP_METADATA_BYTES) fail("relying-party metadata too large");
    let meta: RelyingPartyMetadata;
    try {
      meta = JSON.parse(text) as RelyingPartyMetadata;
    } catch {
      return fail("relying-party metadata is not JSON");
    }
    validateRelyingPartyMetadata(meta, norm);
    return meta;
  } finally {
    clearTimeout(timer);
  }
}

/**
 * Signer-side origin verification: the request's audience must be the origin
 * the metadata came from, and its `response_uri` must be one the RP published.
 */
export function checkRequestAgainstMetadata(
  req: SignInRequest,
  meta: RelyingPartyMetadata,
): void {
  const audience = normalizeOrigin(req.audience);
  validateRelyingPartyMetadata(meta, audience);
  if (!metadataAllowsResponseUri(meta, req.response_uri ?? "")) {
    fail(`response_uri ${JSON.stringify(req.response_uri)} is not published by ${audience}`);
  }
}

// ── Verifier ─────────────────────────────────────────────────────────────────

/**
 * The only state a verifier keeps. An entry must live until the response it
 * came in would have expired anyway — at most five minutes, which is why the
 * TTL cap exists.
 *
 * Implementations must be atomic: two concurrent `use` calls for the same key
 * must not both resolve true, or the replay guard has a race.
 */
export interface NonceCache {
  use(key: string, expiresAtMs: number): Promise<boolean> | boolean;
}

/**
 * Default single-process cache. A relying party running more than one process
 * needs a shared implementation (Redis, a unique index in Postgres): a
 * per-process cache lets a response be replayed once per process.
 */
export class MemoryNonceCache implements NonceCache {
  private readonly seen = new Map<string, number>();
  constructor(private readonly now: () => number = () => Date.now()) {}

  use(key: string, expiresAtMs: number): boolean {
    const now = this.now();
    // Opportunistic pruning keeps the map bounded by the request rate over
    // one expiry window, without a timer.
    for (const [k, exp] of this.seen) if (now > exp) this.seen.delete(k);
    const existing = this.seen.get(key);
    if (existing !== undefined && now <= existing) return false;
    this.seen.set(key, expiresAtMs);
    return true;
  }

  get size(): number {
    return this.seen.size;
  }
}

export interface SignInVerifierOptions {
  /** This relying party's own origin. Required. */
  origin: string;
  nonces?: NonceCache;
  /** Overrides identity resolution (tests, private deployments). */
  resolve?: (identity: string) => Promise<ResolveResult>;
  resolveOptions?: ResolveOptions;
  now?: () => number;
  /** How long `newRequest` challenges stay valid (default 2 min, capped at 5). */
  requestTtlMs?: number;
}

export interface NewSignInRequestOptions {
  action?: SignInAction;
  statement?: string;
  responseUri?: string;
  scopes?: string[];
  ttlMs?: number;
}

/** What a successful verification tells the relying party. */
export interface SignInResult {
  /** The verified Poweur ID — the login subject. */
  identity: string;
  audience: string;
  requestId: string;
  action: string;
  statement: string;
  scopes: string[];
  keyId: string;
  /** True when a short-lived session key signed rather than the identity key. */
  sessionDelegated: boolean;
  /** App namespace derived from the audience. */
  appId: string;
  /** When the approval stops being usable, including as a grant at the relay. */
  expiresAtMs: number;
  /** The identity's home relay — where to present the approval for resources. */
  relay: string;
  document: IdentityDocument;
  /** The decoded approval, for forwarding to the relay's POST /auth/grant. */
  response: SignInResponse;
}

/**
 * Validates sign-in responses addressed to one origin. Requires an origin,
 * because the whole anti-phishing property is that a response is bound to the
 * origin it was collected at.
 */
export class SignInVerifier {
  readonly origin: string;
  private readonly nonces: NonceCache;
  private readonly now: () => number;
  private readonly requestTtlMs: number;
  private readonly resolveFn: (identity: string) => Promise<ResolveResult>;

  constructor(options: SignInVerifierOptions) {
    this.origin = normalizeOrigin(options.origin);
    this.now = options.now ?? (() => Date.now());
    this.nonces = options.nonces ?? new MemoryNonceCache(this.now);
    this.requestTtlMs = Math.min(options.requestTtlMs ?? 2 * 60_000, SIGNIN_MAX_TTL_MS);
    this.resolveFn =
      options.resolve ?? ((name) => resolveIdentity(name, options.resolveOptions ?? {}));
  }

  /**
   * Build a fresh challenge. The RP stores nothing: everything the verifier
   * needs later travels inside the signed response.
   */
  newRequest(opts: NewSignInRequestOptions = {}): SignInRequest {
    const now = this.now();
    const ttl = Math.min(opts.ttlMs ?? this.requestTtlMs, SIGNIN_MAX_TTL_MS);
    const req: SignInRequest = {
      poweur_auth: SIGNIN_VERSION,
      request_id: `req_${randomToken(16)}`,
      domain: new URL(this.origin).hostname,
      audience: this.origin,
      nonce: randomToken(16),
      issued_at: rfc3339(new Date(now)),
      expires_at: rfc3339(new Date(now + ttl)),
      action: opts.action ?? "signin",
    };
    if (opts.statement) req.statement = opts.statement;
    if (opts.responseUri) req.response_uri = opts.responseUri;
    if (opts.scopes?.length) req.scopes = opts.scopes;
    return validateSignInRequest(req, now);
  }

  /** Parse, validate and authenticate an approval. Throws on any failure. */
  async verify(encoded: string): Promise<SignInResult> {
    return this.verifyResponse(decodeSignInResponse(encoded));
  }

  /**
   * `verify` for an already-decoded response. Checks run cheapest-and-most-local
   * first, so a replayed or misaddressed response never costs a DNS lookup.
   */
  async verifyResponse(resp: SignInResponse): Promise<SignInResult> {
    const now = this.now();

    if (resp?.poweur_auth !== SIGNIN_VERSION) {
      fail(`unsupported protocol version ${JSON.stringify(resp?.poweur_auth)}`);
    }
    if (!resp.request_id || !resp.nonce || !resp.signature) {
      fail("request_id, nonce and signature are required");
    }
    const identity = (resp.identity ?? "").trim().toLowerCase();
    validateIdentityName(identity);

    const audience = normalizeOrigin(resp.audience);
    if (audience !== this.origin) {
      // The WebAuthn property: an approval harvested at evil.example is signed
      // over evil.example and cannot be spent here.
      fail(`response is bound to ${audience}, this verifier is ${this.origin}`);
    }

    validateSignInAction(resp.action);
    validateSignInStatement(resp.statement);
    const scopes = normalizeSignInScopes(resp.scopes);
    const given = resp.scopes ?? [];
    if (scopes.length !== given.length || scopes.some((s, i) => s !== given[i])) {
      // The canonical string is built from the normalized, sorted list, so a
      // non-canonical list would verify against bytes the user never saw.
      fail("scopes are not in canonical form");
    }
    const appId = signInAppId(audience);
    for (const s of scopes) checkSignInScopeNamespace(s, appId);

    validateWindow(resp.issued_at, resp.expires_at, now);
    const expiresAtMs = parseRfc3339(resp.expires_at, "expires_at");

    // Keyed by audience + identity + request + nonce, so one user's nonce
    // cannot lock another out and a cache shared between RPs stays correct.
    const key = [audience, identity, resp.request_id, resp.nonce].join("|");
    let fresh: boolean;
    try {
      fresh = await this.nonces.use(key, expiresAtMs);
    } catch (err) {
      throw new PoweurError("invalid_signature", "sign-in: nonce cache unavailable", {
        cause: err,
      });
    }
    if (!fresh) fail("nonce already used");

    const resolved = await this.resolveFn(identity);
    const doc = resolved.document;
    const canonical = signInResponseCanonical({ ...resp, identity });

    const sessionId = sessionIdFromKeyId(resp.key_id);
    let sessionDelegated = false;
    if (sessionId || resp.session_proof) {
      if (!resp.session_proof) fail("key_id names a session but no session_proof is attached");
      if (!sessionId) fail(`session_proof attached but key_id is ${JSON.stringify(resp.key_id)}`);
      const keys = identityKeysAt(doc, resp.session_proof.issued_at);
      let verified: VerifiedSessionProof | null = null;
      let lastErr: unknown = null;
      for (const key of keys) {
        try {
          verified = verifySessionProof(key, identity, resp.session_proof, now);
          break;
        } catch (err) {
          lastErr = err;
        }
      }
      if (!verified) throw lastErr ?? new PoweurError("invalid_signature", "sign-in: session proof invalid");
      if (!verifyCanonical(verified.publicKey, canonical, resp.signature)) {
        fail("signature verification failed");
      }
      sessionDelegated = true;
    } else if (resp.key_id !== SIGNIN_KEY_ID_IDENTITY) {
      fail(`key_id must be "identity" or "session:<session id>"`);
    } else {
      const keys = identityKeysAt(doc, resp.issued_at);
      if (!keys.some((k) => verifyCanonical(k, canonical, resp.signature))) {
        fail("signature verification failed");
      }
    }

    return {
      identity,
      audience,
      requestId: resp.request_id,
      action: resp.action,
      statement: resp.statement ?? "",
      scopes,
      keyId: resp.key_id,
      sessionDelegated,
      appId,
      expiresAtMs,
      relay: doc.relay,
      document: doc,
      response: resp,
    };
  }
}

/**
 * Every signing key the document says was valid at `at`: the current one, then
 * any retired key still inside its declared rotation grace window. A list,
 * because a response carries no key hint — the verifier learns which key signed
 * it by trying them.
 */
function identityKeysAt(doc: IdentityDocument, at: string): Uint8Array[] {
  const parsed = Date.parse(at ?? "");
  const when = new Date(Number.isNaN(parsed) ? Date.now() : parsed);
  const keys: Uint8Array[] = [];
  try {
    keys.push(parseEd25519PublicKey(doc.public_key));
  } catch {
    /* a document with an unusable current key still has previous ones */
  }
  for (const prev of doc.previous_keys ?? []) {
    // Same grace-window rule the rest of the SDK uses (`document.keyValidAt`),
    // so a rotation is judged by one implementation everywhere.
    if (!keyValidAt(doc, prev.public_key, when)) continue;
    try {
      keys.push(parseEd25519PublicKey(prev.public_key));
    } catch {
      /* skip */
    }
  }
  if (!keys.length) fail(`identity document has no usable key valid at ${at}`);
  return keys;
}

function randomToken(size: number): string {
  return toBase64url(randomBytes(size));
}
