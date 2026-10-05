/**
 * Persistence for the Poweur ID web client.
 *
 * localStorage   — identity records and app config, across sessions
 * sessionStorage — the relay session, cleared when the tab closes
 * memory         — unwrapped private keys, never written anywhere
 *
 * Two rules this module exists to enforce (EPIC-015 E15-T1):
 *
 * 1. **State is keyed by identity, not global.** One browser holds several
 *    identities, and EPIC-019's shell holds them across several relays.
 * 2. **A relay URL comes from the identity record.** `location.origin` is
 *    right for a relay-served SPA and wrong for a Capacitor shell on
 *    `capacitor://localhost`, so it appears exactly once in the app — in
 *    `defaultRelayUrl()` below — and only as the seed for a *new* identity.
 */

/** The hosted production relay. A shell has no origin of its own, so this is the seed. */
export const PRODUCTION_RELAY_URL = "https://poweur.net";

const IDENTITY_PREFIX = "poweur:identity:";
const ACTIVE_KEY = "poweur:active";
const CONFIG_KEY = "poweur:config";
const SESSION_PREFIX = "poweur:session:";
/** Last `GET /` document. Shared with `mode.js` so hosted names can use their own origin. */
export const ROOT_SESSION_KEY = "poweur:root";

// ─── Relay URLs ───────────────────────────────────────────────────────────────

const LOCAL_RELAY_URLS = [
  "http://127.0.0.1:8080",
  "http://localhost:8080",
  "http://10.0.2.2:8080",
];

/**
 * A loopback relay for the local `go run` process.
 *
 * An Android emulator cannot reach the host at `127.0.0.1` — that address is
 * the emulated device — so it uses `10.0.2.2`. iOS Simulator and a browser on
 * this machine use loopback. A physical phone is neither: it needs the Mac's
 * LAN address, typed under Other….
 */
export function localDevRelayUrl() {
  if (globalThis.Capacitor?.getPlatform?.() === "android") return "http://10.0.2.2:8080";
  return "http://127.0.0.1:8080";
}

/**
 * Which first-run preset a stored URL maps to, so the picker can restore it.
 *
 * @returns {"production" | "local" | "custom"}
 */
export function relayPresetFor(url) {
  const trimmed = String(url ?? "").replace(/\/+$/, "");
  if (!trimmed || trimmed === PRODUCTION_RELAY_URL) return "production";
  if (LOCAL_RELAY_URLS.includes(trimmed)) return "local";
  return "custom";
}

/**
 * The relay to register a *new* identity with, when the user has not named one.
 *
 * The only place in the app that reads the page's origin. A relay serves this
 * SPA under `/app/`, so its own origin is the sensible default for a fresh
 * registration — but the moment an identity exists, `relayUrlFor()` takes over
 * and the origin is never consulted again. `test/origin.test.js` enforces that.
 *
 * A shell's origin is not a relay (iOS `capacitor://localhost`, Android
 * `https://localhost`). Returning it produced an app that registered against
 * itself. The shell therefore seeds from the production hosted relay, and the
 * first-run picker can switch to a local or custom one.
 */
export function defaultRelayUrl() {
  const configured = readConfigRaw().relayUrl;
  if (configured) return configured;
  if (isShellRuntime()) return PRODUCTION_RELAY_URL;
  const origin = globalThis.location?.origin ?? "";
  return /^https?:$/.test(globalThis.location?.protocol ?? "") ? origin : "";
}

/**
 * Inside a native shell, whichever way it serves its own bundle.
 *
 * The protocol test alone was an iOS answer: iOS serves the app from
 * `capacitor://localhost`, but **Android serves it from `https://localhost`** —
 * a perfectly ordinary web origin by every syntactic test, and one this
 * function would happily hand back as a relay. The shell says so itself, so
 * ask it first and keep the protocol test for the cases it still covers
 * (`file://`, a future scheme).
 */
export function isShellRuntime() {
  if (globalThis.Capacitor?.isNativePlatform?.()) return true;
  return !/^https?:$/.test(globalThis.location?.protocol ?? "");
}

/** True when a relay is known without asking the user (EPIC-019 E19-T1). */
export function hasRelayUrl() {
  return Boolean(defaultRelayUrl());
}

function cachedRoot() {
  try {
    const raw = globalThis.sessionStorage?.getItem(ROOT_SESSION_KEY);
    return raw ? JSON.parse(raw) : null;
  } catch {
    return null;
  }
}

/**
 * `https://alice.poweur.net` (scheme and port taken from `fallbackUrl`).
 *
 * Identity-scoped calls should hit this host when it *is* the relay — hosted
 * names on this operator already are, via Host-routing. A shell's seed
 * (`poweur.net`) is only for claiming a new name, not for talking to one.
 */
export function identityOriginUrl(identity, fallbackUrl) {
  const name = String(identity || "").trim().toLowerCase().replace(/\.$/, "");
  if (!name.includes(".") || name.includes("/") || name.includes(":") || isPrivateHost(name)) {
    return "";
  }
  if (/^\d{1,3}(\.\d{1,3}){3}$/.test(name)) return "";
  try {
    const next = new URL(new URL(fallbackUrl).origin);
    next.hostname = name;
    return next.origin;
  } catch {
    return "";
  }
}

function hostedDomains() {
  const fromRoot = cachedRoot()?.hosted_domains;
  if (Array.isArray(fromRoot) && fromRoot.length) {
    return fromRoot.map((d) => String(d).toLowerCase().replace(/\.$/, ""));
  }
  try {
    return [new URL(PRODUCTION_RELAY_URL).hostname.toLowerCase()];
  } catch {
    return [];
  }
}

function isHostedIdentity(identity) {
  const id = String(identity || "").toLowerCase().replace(/\.$/, "");
  if (!id) return false;
  return hostedDomains().some((domain) => id === domain || id.endsWith("." + domain));
}

/**
 * Prefer the identity host for hosted names so enroll/DAV/sessions do not
 * bounce to the operator apex. Loopback stored relays stay put — local tests
 * have no public DNS for `alice.poweur.net`.
 */
export function apiBaseForIdentity(identity, relayUrl) {
  const stored = String(relayUrl || "").replace(/\/+$/, "");
  if (!stored) return stored;
  const origin = identityOriginUrl(identity, stored);
  if (!origin) return stored;
  let storedHost;
  try {
    storedHost = new URL(stored).hostname.toLowerCase();
  } catch {
    return stored;
  }
  const id = String(identity || "").toLowerCase().replace(/\.$/, "");
  if (storedHost === id) return stored;
  if (isPrivateHost(storedHost)) return stored;
  // Only rewrite when the stored URL is this operator (apex or a sibling like
  // relay.poweur.net). A configured custom relay for a hosted-looking name is
  // left alone — E15-T1: the record's relay is source of truth across operators.
  const operator = hostedDomains().some(
    (domain) => storedHost === domain || storedHost.endsWith("." + domain),
  );
  if (operator && isHostedIdentity(id)) return origin;
  return stored;
}

/**
 * The relay this identity actually lives on. Identity records carry their own
 * relay, so one client can hold identities across several relays.
 */
export function relayUrlFor(identity) {
  const record = identity ? loadIdentityRecord(identity) : null;
  return apiBaseForIdentity(identity, record?.relay || defaultRelayUrl());
}

/**
 * Resolver options for a relay URL. A relay reachable over plain HTTP or on a
 * private address is a local development or test relay, and the SDK's SSRF
 * guard has to be told so explicitly — it is never relaxed by default.
 */
export function resolveOptionsFor(relayUrl) {
  const options = { relayUrl };
  let url;
  try {
    url = new URL(relayUrl);
  } catch {
    return options;
  }
  if (url.protocol === "http:") options.scheme = "http";
  if (isPrivateHost(url.hostname)) options.allowPrivate = true;
  return options;
}

function isPrivateHost(hostname) {
  if (hostname === "localhost" || hostname.endsWith(".localhost")) return true;
  if (/^127\./.test(hostname) || hostname === "::1" || hostname === "[::1]") return true;
  if (/^10\./.test(hostname) || /^192\.168\./.test(hostname)) return true;
  return /^172\.(1[6-9]|2\d|3[01])\./.test(hostname);
}

// ─── Config ───────────────────────────────────────────────────────────────────

const DEFAULT_CONFIG = {
  relayUrl: "",
  parentDomain: "poweur.net",
  dnsProvider: "cloudflare",
  dnsToken: "",
};

function readConfigRaw() {
  try {
    const raw = localStorage.getItem(CONFIG_KEY);
    return raw ? JSON.parse(raw) : {};
  } catch {
    return {};
  }
}

export function getConfig() {
  const stored = readConfigRaw();
  return { ...DEFAULT_CONFIG, ...stored, relayUrl: stored.relayUrl || defaultRelayUrl() };
}

export function saveConfig(config) {
  localStorage.setItem(CONFIG_KEY, JSON.stringify(config));
}

// ─── Identity Records ─────────────────────────────────────────────────────────

/**
 * Identity record shape:
 * {
 *   identity: string,           // FQDN (alice.poweur.net)
 *   publicKey: string,          // base64url Ed25519 public key
 *   encPublicKey: string,       // base64url X25519 public key
 *   credentialId: string,       // base64url WebAuthn credential ID
 *   encryptedKeys: {            // wrapped private keys
 *     kdf: "prf" | "native",
 *     iv: string,
 *     ciphertext: string,
 *     gate?: string,            // only for native
 *   },
 *   relay: string,              // relay URL this identity lives on
 *   userId: string,             // base64url random bytes (WebAuthn user ID)
 *   createdAt: string,          // ISO timestamp
 *   enrollmentId?: string,      // this browser's row in the relay keystore (EPIC-011)
 *   credentialPublicKey?: string, // SPKI DER, base64url — what the relay verifies
 *   credentialAlg?: number,     // COSE alg id for that key
 * }
 */
export function saveIdentityRecord(identity, record) {
  localStorage.setItem(IDENTITY_PREFIX + identity, JSON.stringify(record));
}

export function loadIdentityRecord(identity) {
  const raw = localStorage.getItem(IDENTITY_PREFIX + identity);
  return raw ? JSON.parse(raw) : null;
}

export function listIdentities() {
  const result = [];
  for (let i = 0; i < localStorage.length; i++) {
    const key = localStorage.key(i);
    if (key && key.startsWith(IDENTITY_PREFIX)) {
      result.push(key.slice(IDENTITY_PREFIX.length));
    }
  }
  return result.sort();
}

export function removeIdentity(identity) {
  localStorage.removeItem(IDENTITY_PREFIX + identity);
  if (getActiveIdentity() === identity) {
    localStorage.removeItem(ACTIVE_KEY);
  }
}

/**
 * The launcher (poweur.net) is neutral: it never holds or remembers an
 * identity — those live on their own hosts. Drops whatever a visit left here:
 * records, the active pointer, local avatars and sessions. Settings (theme,
 * relay config, device fingerprint) stay.
 */
export function scrubIdentityState() {
  const prefixes = [IDENTITY_PREFIX, SESSION_PREFIX, "poweur:avatar:"];
  for (const store of [globalThis.localStorage, globalThis.sessionStorage]) {
    try {
      if (!store) continue;
      for (const key of Object.keys(store)) {
        if (key === ACTIVE_KEY || prefixes.some((p) => key.startsWith(p))) store.removeItem(key);
      }
    } catch {
      /* storage blocked: nothing was kept either */
    }
  }
}

// ─── Active Identity ──────────────────────────────────────────────────────────

export function getActiveIdentity() {
  return localStorage.getItem(ACTIVE_KEY) || null;
}

export function setActiveIdentity(identity) {
  if (identity) {
    localStorage.setItem(ACTIVE_KEY, identity);
  } else {
    localStorage.removeItem(ACTIVE_KEY);
  }
}

// ─── Sessions ─────────────────────────────────────────────────────────────────

/**
 * The SDK's `SessionStore` over `sessionStorage`: sessions die with the tab,
 * which is the browser equivalent of the CLI's short-lived `~/.poweur/sessions`
 * entries. The record shape is the SDK's `StoredSession`, so `SessionManager`
 * owns registration, expiry and revocation instead of the app.
 */
export class BrowserSessionStore {
  async load(identity) {
    return loadSessionRecord(identity);
  }

  async save(session) {
    saveSessionRecord(session.identity, session);
  }

  async remove(identity) {
    removeSessionRecord(identity);
  }
}

export function saveSessionRecord(identity, record) {
  sessionStorage.setItem(SESSION_PREFIX + identity, JSON.stringify(record));
}

export function loadSessionRecord(identity) {
  const raw = sessionStorage.getItem(SESSION_PREFIX + identity);
  return raw ? JSON.parse(raw) : null;
}

export function removeSessionRecord(identity) {
  sessionStorage.removeItem(SESSION_PREFIX + identity);
}

// ─── Unlocked Keys (in-memory only) ──────────────────────────────────────────
// Never persisted to any storage: cleared on reload, on lock, and on tab close.

// The master seed rides along when there is one: the recovery kit is derived
// from it, and it is the one secret that must never be written down by us.
let _unlockedKeys = null; // { identity, signingJWK, encJWK, seed }

/**
 * The rp.id a stored credential was created with (EPIC-018 E18-T4).
 *
 * Records written before this existed were minted with the page host and must
 * keep being asserted against it — reading the registrable domain for them
 * would silently stop finding their credential.
 */
export function rpIdFor(identity) {
  const record = loadIdentityRecord(identity);
  if (record?.rpId) return record.rpId;
  return globalThis.location?.hostname ?? "";
}

export function setUnlockedKeys(identity, signingJWK, encJWK, seed) {
  _unlockedKeys = { identity, signingJWK, encJWK, seed };
}

export function getUnlockedKeys() {
  return _unlockedKeys;
}

export function clearUnlockedKeys() {
  _unlockedKeys = null;
}
