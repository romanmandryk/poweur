/**
 * Resolving *who someone is*, not just what their keys are.
 *
 * EPIC-006 serves an identity's public presentation from
 * `poweur-sys/public/`, reachable at `/.well-known/poweur/<file>` on the
 * identity's own host. Cached here so ProfileCard, IdentityInput and the
 * audience picker can all ask about the same person without re-fetching.
 *
 * Two honest limits, both handled by degrading rather than failing:
 *
 *  - the well-known route is **Host-routed**, and a browser cannot set Host.
 *    On an identity's own origin (production) the fetch works; against a
 *    shared dev relay it does not, and there is no non-Host path for another
 *    identity's `poweur-sys/public` — adding one is EPIC-006's call, not this
 *    epic's ("no new relay endpoints").
 *  - so the identity **document** is the floor. It always resolves, and it
 *    already carries `capabilities`, which is what E15-T5 shows read-only.
 */

import { resolveIdentity } from "@poweur/client";

import { resolveOptionsFor } from "./storage.js";

const PROFILE_PATH = "/.well-known/poweur/profile.json";
const CAPABILITIES_PATH = "/.well-known/poweur/capabilities.json";
const MAX_BYTES = 64 * 1024;
const TTL_MS = 5 * 60_000;

/** identity → { expiresAt, entry } */
const cache = new Map();
/** identity → in-flight promise, so N cards for one person make one request. */
const inFlight = new Map();

export function clearProfileCache() {
  cache.clear();
  inFlight.clear();
}

async function fetchJson(url, { timeoutMs = 5000 } = {}) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    const response = await fetch(url, {
      method: "GET",
      redirect: "error",
      headers: { Accept: "application/json" },
      signal: controller.signal,
    });
    if (!response.ok) return null;
    const body = await response.text();
    if (body.length > MAX_BYTES) return null;
    return JSON.parse(body);
  } catch {
    return null;
  } finally {
    clearTimeout(timer);
  }
}

/** `public/avatar.png` on alice's tree → the URL that actually serves it. */
export function avatarUrl(identity, avatarPath, { scheme = "https" } = {}) {
  if (!avatarPath || typeof avatarPath !== "string") return null;
  if (!avatarPath.startsWith("public/")) return null; // schema says tree path, never a URL
  const rest = avatarPath.slice("public/".length).split("/").map(encodeURIComponent).join("/");
  return `${scheme}://${identity}/pub/${rest}`;
}

/**
 * Everything worth showing about an identity, best-effort.
 *
 * Always resolves (or throws only when the identity itself cannot be
 * resolved): `profile` and `capabilities` are null when unreachable, which is
 * the normal case on a shared dev relay.
 */
export async function resolveProfile(identity, relayUrl, { force = false } = {}) {
  const name = String(identity || "").trim().toLowerCase();
  if (!name) throw new Error("identity is required");

  const cached = cache.get(name);
  if (!force && cached && cached.expiresAt > Date.now()) return cached.entry;
  if (!force && inFlight.has(name)) return inFlight.get(name);

  const load = (async () => {
    const options = resolveOptionsFor(relayUrl);
    const { document, source } = await resolveIdentity(name, options);
    const scheme = options.scheme ?? "https";
    const base = `${scheme}://${name}`;

    const [profile, capabilities] = await Promise.all([
      fetchJson(base + PROFILE_PATH),
      fetchJson(base + CAPABILITIES_PATH),
    ]);

    const entry = {
      identity: name,
      document,
      source,
      profile,
      // The document's own capability list is the fallback, and in practice
      // the only one available until an identity is on its own host.
      capabilities: capabilities ?? capabilitiesFromDocument(document),
      displayName: profile?.display_name || null,
      bio: profile?.bio || null,
      links: Array.isArray(profile?.links) ? profile.links : [],
      avatar: avatarUrl(name, profile?.avatar, { scheme }),
    };
    cache.set(name, { entry, expiresAt: Date.now() + TTL_MS });
    inFlight.delete(name);
    return entry;
  })().catch((error) => {
    inFlight.delete(name);
    throw error;
  });

  inFlight.set(name, load);
  return load;
}

function capabilitiesFromDocument(document) {
  const features = {};
  for (const capability of document?.capabilities ?? []) features[capability] = "1";
  return Object.keys(features).length ? { version: 1, features } : null;
}

/**
 * Seed the cache with a profile we just wrote ourselves.
 *
 * Our own profile does not need the well-known route — we read and write it
 * over DAV — but every card asks *this* module, and on a shared dev relay the
 * Host-routed fetch would answer null and blank the name the user just saved.
 */
export function primeProfile(identity, profile, relayUrl) {
  const name = String(identity || "").trim().toLowerCase();
  if (!name) return null;
  const scheme = resolveOptionsFor(relayUrl).scheme ?? "https";
  const existing = cache.get(name)?.entry ?? {};
  const entry = {
    ...existing,
    identity: name,
    profile,
    displayName: profile?.display_name || null,
    bio: profile?.bio || null,
    links: Array.isArray(profile?.links) ? profile.links : [],
    avatar: avatarUrl(name, profile?.avatar, { scheme }),
  };
  cache.set(name, { entry, expiresAt: Date.now() + TTL_MS });
  return entry;
}

/** A cache hit, or null — for synchronous first paint before the fetch lands. */
export function cachedProfile(identity) {
  const entry = cache.get(String(identity || "").trim().toLowerCase());
  return entry && entry.expiresAt > Date.now() ? entry.entry : null;
}
