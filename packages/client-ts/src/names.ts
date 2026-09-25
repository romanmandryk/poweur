/**
 * Identity name policy — the twin of `packages/identity/names.go`.
 * Pinned by conformance vectors: a name Go accepts must be accepted here.
 */

export const MIN_LABEL_LEN = 3;
export const MAX_LABEL_LEN = 63;
export const MAX_NAME_LEN = 253;

/**
 * Labels that cannot be the leftmost label of a hosted identity.
 *
 * Mirrors `ReservedLabels` in `packages/identity/names.go`; the names
 * conformance vectors fail if the two lists drift.
 */
export const RESERVED_LABELS = new Set([
  "www", "admin", "relay", "mail", "ftp", "api",
  "app", "static", "cdn", "ns", "ns1", "ns2",
  "mx", "smtp", "imap", "pop", "root", "localhost",
  "poweur", "well-known", "dav", "sync", "id", "ids",
  "launcher", "get", "join", "signup", "signin", "login",
  "auth", "account", "accounts", "console", "dashboard", "portal",
  "home", "data", "files", "file", "storage", "analytics",
  "metrics", "status", "health", "logs", "backup", "db",
  "search", "index", "assets", "media", "img", "images",
  "support", "help", "docs", "blog", "news", "about",
  "contact", "legal", "privacy", "terms", "security", "abuse",
  "postmaster", "hostmaster", "webmaster", "noreply", "no-reply", "pay",
  "payments", "billing", "wallet", "invoice", "verify", "verified",
  "official", "team", "staff", "system", "bot", "test",
  "demo", "example", "oauth", "sso", "idp", "openid",
  "indieauth",
]);

function looksLikeIpLiteral(value: string): boolean {
  if (value.startsWith("[")) return true;
  const parts = value.split(".");
  if (parts.length !== 4) return false;
  return parts.every((p) => p.length > 0 && /^[0-9]+$/.test(p));
}

/**
 * ASCII letter-digit-hyphen, exactly as `validateLabel` in names.go.
 *
 * This used to accept any Unicode letter (`\p{L}`), which — on both sides —
 * let a Cyrillic "а" stand in for "a", so "аdmin" passed while "admin" was
 * reserved (EPIC-018 E18-T1). A raw UTF-8 label is not a valid DNS label
 * either. Punycode stays legal here for real IDN domains; hosted *handles*
 * refuse it below.
 */
function validateLabel(label: string): void {
  if (label.length < 1 || label.length > MAX_LABEL_LEN) {
    throw new Error(`label "${label}": invalid length`);
  }
  for (let i = 0; i < label.length; i++) {
    const ch = label[i] as string;
    if (/[a-zA-Z0-9]/.test(ch)) continue;
    if (ch === "-" && i > 0 && i < label.length - 1) continue;
    throw new Error(`label "${label}": invalid character`);
  }
  if (label.startsWith("-") || label.endsWith("-")) {
    throw new Error(`label "${label}": cannot start or end with hyphen`);
  }
}

/**
 * Throws when the identity is not a usable FQDN: what every resolve, message,
 * grant and sign-in accepts, as `ValidateIdentityName` in names.go.
 *
 * Reserved labels are not checked here. They hold a name back from being
 * *claimed* (`validateClaimableName`, `validateHostedHandle`), not from being
 * used: an operator may create `support.example.org`, and it must be reachable.
 */
export function validateIdentityName(identity: string): void {
  const value = identity.trim().toLowerCase();
  if (value === "") throw new Error("identity is empty");
  if (value.length > MAX_NAME_LEN) throw new Error("identity too long");
  if (looksLikeIpLiteral(value)) throw new Error("IP-literal identities are not allowed");
  const labels = value.split(".");
  if (labels.length < 2) throw new Error("identity must be a FQDN with at least two labels");
  labels.forEach((label) => validateLabel(label));
}

/** `validateIdentityName` plus the reserved leftmost labels: what self-service registration may take. */
export function validateClaimableName(identity: string): void {
  validateIdentityName(identity);
  const label = identity.trim().toLowerCase().split(".")[0] as string;
  if (RESERVED_LABELS.has(label)) {
    throw new Error(`label "${label}" is reserved`);
  }
}

export function isValidIdentityName(identity: string): boolean {
  try {
    validateIdentityName(identity);
    return true;
  } catch {
    return false;
  }
}

/**
 * Hosted handles, against the *default* policy (`DefaultHostedPolicy` in Go).
 *
 * A relay's own policy — minimum length, extra reserved names, blocked terms —
 * is deployment configuration this client cannot know, so it comes back from
 * `GET /hosted/availability` instead (`IdentityApi.availability`). What is
 * checked here is what every relay enforces: shape, charset, and no IDN.
 */
export function validateHostedHandle(identity: string): void {
  validateClaimableName(identity);
  const label = identity.trim().toLowerCase().split(".")[0] as string;
  if (label.startsWith("xn--")) {
    throw new Error("hosted handles may not start with xn-- (no IDN in v1)");
  }
  if (label.includes("--")) {
    throw new Error("hosted handles cannot contain a double hyphen");
  }
  if (label.length < MIN_LABEL_LEN) {
    throw new Error(`hosted handle must be at least ${MIN_LABEL_LEN} characters`);
  }
}

/** True when `identity` is `parent` or a subdomain of it. */
export function isUnderDomain(identity: string, parent: string): boolean {
  const id = identity.toLowerCase().replace(/\.$/, "");
  const dom = parent.toLowerCase().replace(/\.$/, "");
  if (dom === "") return false;
  return id === dom || id.endsWith(`.${dom}`);
}

/** Map an identity FQDN to one safe directory name (dots become `__`). */
export function sanitizeIdentityDirName(identity: string): string {
  validateIdentityName(identity);
  const id = identity.toLowerCase().replace(/\.$/, "");
  if (id.includes("/") || id.includes("\\") || id.includes("..")) {
    throw new Error("identity contains path separators");
  }
  return id.replace(/\./g, "__");
}

/**
 * Multi-label public suffixes common enough that getting them wrong is a real
 * bug — a PSL-lite, deliberately not the Public Suffix List.
 *
 * Shipping the full PSL to compute one WebAuthn `rp.id` would add a megabyte
 * that goes stale; an operator on an exotic suffix overrides the value in
 * config instead (EPIC-018 E18-T4).
 */
const MULTI_LABEL_SUFFIXES = new Set([
  "co.uk", "org.uk", "me.uk", "ac.uk", "gov.uk", "net.uk", "sch.uk",
  "com.au", "net.au", "org.au", "edu.au", "gov.au", "id.au",
  "co.nz", "net.nz", "org.nz", "govt.nz",
  "co.za", "org.za", "net.za",
  "com.br", "com.mx", "com.ar", "com.tr", "com.cn", "com.sg", "com.hk",
  "co.jp", "or.jp", "ne.jp", "ac.jp", "go.jp",
  "co.in", "net.in", "org.in", "gov.in",
  "com.pl", "com.ua", "co.il", "co.kr", "or.kr",
  "github.io", "gitlab.io", "pages.dev", "workers.dev", "vercel.app", "netlify.app",
]);

/**
 * The registrable domain of a host: what a WebAuthn credential may be scoped
 * to, and the widest scope a page is allowed to claim.
 *
 * `alice.poweur.net` → `poweur.net`, so a credential minted on the launcher
 * host (`id.poweur.net`) opens on the identity's own origin without a second
 * enrollment. `bob.co.uk` → `bob.co.uk`, because `co.uk` is a public suffix
 * and nobody may scope a credential to it.
 */
export function registrableDomain(host: string): string {
  const value = String(host || "").trim().toLowerCase().replace(/\.$/, "");
  if (value === "" || value === "localhost") return value;
  // An IP literal has no registrable domain; WebAuthn refuses one anyway.
  if (/^[0-9.]+$/.test(value) || value.includes(":") || value.startsWith("[")) return value;

  const labels = value.split(".");
  if (labels.length <= 2) return value;
  const lastTwo = labels.slice(-2).join(".");
  if (MULTI_LABEL_SUFFIXES.has(lastTwo)) {
    return labels.slice(-3).join(".");
  }
  return lastTwo;
}
