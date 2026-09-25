/**
 * What may leave the browser. Everything Faro sends passes through
 * `scrubItem` first; the collector (Alloy) redacts again as a backstop.
 *
 * Always removed, consent or not:
 *   - URL query strings and fragments. Claim and pairing links carry wrapped
 *     keys in `#claim=` / `#pair=`, and sign-in links carry request codes.
 * Removed unless the user opted in (`keepIds`):
 *   - Poweur IDs and other domain names, wherever they appear: page URLs,
 *     stack-frame file names (an identity host is in every frame's URL),
 *     error messages and event attributes.
 *   - Email addresses.
 */

/** Hosts that are the service, not a person: kept so errors stay readable. */
const SERVICE_HOSTS = new Set([
  "poweur.net",
  "poweur.org",
  "www.poweur.org",
  "tmpwww.poweur.org",
  "oauth.poweur.org",
  "relay.poweur.net",
  "id.poweur.net",
  "localhost",
]);

/** Last labels that mean "file", not "domain", so `index.js` is not redacted. */
const FILE_EXTENSIONS = new Set([
  "js", "mjs", "cjs", "ts", "tsx", "jsx", "css", "map", "json", "html", "htm", "svg", "png", "jpg", "jpeg",
  "gif", "webp", "ico", "txt", "md", "wasm", "woff", "woff2", "xml", "pdf", "zip",
]);

/**
 * Top-level domains an ID is likely to live under. A dotted token only counts
 * as a domain when it ends in one of these, so `e.target.value` and
 * `t.map is not a function` stay intact. Two-letter endings are country codes.
 */
const TLDS = new Set([
  "com", "net", "org", "io", "dev", "app", "ai", "co", "me", "info", "biz", "xyz", "online", "site", "tech",
  "cloud", "page", "id", "social", "email", "blog", "family", "name", "pro", "eu", "one", "world", "life",
]);

export const REDACTED_ID = "<id>";
export const REDACTED_EMAIL = "<email>";

const EMAIL_RE = /\b[a-z0-9._%+-]+@[a-z0-9-]+(?:\.[a-z0-9-]+)+\b/gi;
// Dotted tokens of DNS labels. Checked against TLDS/SERVICE_HOSTS in code.
const DOMAIN_RE = /\b(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,62}\b/gi;
// An absolute URL's query and fragment.
const URL_TAIL_RE = /(\b[a-z][a-z0-9+.-]*:\/\/[^\s"'<>?#]*)[?#][^\s"'<>)]*/gi;
// Secrets that travel in fragments even outside a full URL.
const SECRET_FRAGMENT_RE = /#(?:claim|pair|request|request_uri|seed|kit)=[^\s"'<>)]*/gi;

function isDomainLike(token: string): boolean {
  const lower = token.toLowerCase();
  const last = lower.slice(lower.lastIndexOf(".") + 1);
  if (FILE_EXTENSIONS.has(last)) return false;
  return last.length === 2 || TLDS.has(last);
}

/** Scrub one string. `keepIds` keeps names and emails; secrets always go. */
export function scrubText(text: string, keepIds = false): string {
  let out = text.replace(URL_TAIL_RE, "$1").replace(SECRET_FRAGMENT_RE, "");
  if (keepIds) return out;
  out = out.replace(EMAIL_RE, REDACTED_EMAIL);
  return out.replace(DOMAIN_RE, (token) => {
    const lower = token.toLowerCase();
    if (SERVICE_HOSTS.has(lower) || !isDomainLike(token)) return token;
    return REDACTED_ID;
  });
}

/** Scrub every string in a JSON-like value, returning a copy. */
export function scrubValue<T>(value: T, keepIds = false): T {
  if (typeof value === "string") return scrubText(value, keepIds) as T;
  if (Array.isArray(value)) return value.map((entry) => scrubValue(entry, keepIds)) as T;
  if (value && typeof value === "object") {
    const out: Record<string, unknown> = {};
    for (const [key, entry] of Object.entries(value as Record<string, unknown>)) {
      out[key] = scrubValue(entry, keepIds);
    }
    return out as T;
  }
  return value;
}

/** The parts of a Faro transport item that are scrubbed. */
export interface ScrubbableItem {
  type?: string;
  payload?: unknown;
  meta?: Record<string, unknown> & { user?: unknown; browser?: unknown };
}

/**
 * Scrub a Faro transport item. Without consent the user meta is dropped too,
 * whatever set it.
 */
export function scrubItem<T extends ScrubbableItem>(item: T, keepIds = false): T {
  const meta: Record<string, unknown> = { ...(item.meta ?? {}) };
  if (!keepIds) {
    delete meta.user;
    // Name, version, OS and viewport say enough; the full user-agent string
    // mostly adds fingerprinting power.
    if (meta.browser && typeof meta.browser === "object") {
      const { userAgent: _ua, ...browser } = meta.browser as Record<string, unknown>;
      meta.browser = browser;
    }
  }
  return { ...item, payload: scrubValue(item.payload, keepIds), meta: scrubValue(meta, keepIds) };
}
