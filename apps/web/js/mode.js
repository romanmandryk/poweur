/**
 * Which front door is this? (EPIC-015 E15-T7)
 *
 * The same static tree is served from three places and they are not the same
 * product surface:
 *
 *   - `id.poweur.net` / `poweur.net` — a **launcher**. Nobody here has an
 *     identity yet; the job is to explain what this is and let them claim a
 *     name.
 *   - `bob.poweur.net` — an **identity** host. The subject is not a question:
 *     it is in the URL bar. Either Bob signs in, or the name is unclaimed and
 *     can be claimed — but only as `bob`.
 *   - `capacitor://localhost` — a **shell**. It is on nothing, so it has to
 *     ask which relay before it can ask anything else (EPIC-019).
 *
 * Before this module `boot()` branched on stored identity alone, so all three
 * rendered the same welcome card and the claim form asked for a parent domain
 * that the host already determined.
 *
 * Nothing here reads `location.origin` — the relay base URL stays
 * `storage.defaultRelayUrl()`'s job (E15-T1 constraint 2). What this reads is
 * the *host*, which is a different question: not "who do I call" but "who am I
 * standing in for".
 */

import { identityApiFor } from "./client.js";
// `isShellRuntime`, not a protocol test: the shell's hostname is a perfectly
// ordinary "localhost", and iOS's `capacitor://` is not the only shape a shell
// takes — Android serves the same bundle from `https://localhost`, which every
// syntactic test calls an ordinary web host.
import { defaultRelayUrl, isShellRuntime } from "./storage.js";

/** @typedef {"launcher" | "identity" | "shell" | "unknown"} AppMode */

/**
 * @typedef {object} ModeInfo
 * @property {AppMode} mode
 * @property {string}  host          lowercased hostname, no port, no root dot
 * @property {string}  subject       the identity this host stands for ("" unless mode==="identity")
 * @property {string}  handle        subject's first label ("" unless mode==="identity")
 * @property {string}  domain        subject's parent domain, or the launcher's claim suffix
 * @property {string[]} hostedDomains
 * @property {string[]} launcherHosts
 * @property {string}  launcherHost  the canonical one, for hand-offs
 * @property {boolean} resolved      false while this is still the synchronous guess
 * @property {boolean} probed        true once the relay has been asked, answer or not
 * @property {boolean} reachable     false when the relay could not be asked
 */

const SESSION_KEY = "poweur:root";

/** Cached for the page's lifetime; `resolveMode()` is called from every render path. */
let current = null;
let inFlight = null;

/** Hostname, lowercased, without a port or the DNS root dot. */
export function currentHost(location = globalThis.location) {
  const raw = String(location?.hostname ?? "");
  return raw.toLowerCase().replace(/\.$/, "");
}

/**
 * Classify a host against a relay's root document.
 *
 * Exported and pure so the host table can be unit-tested without a page or a
 * relay — the parsing is where this goes wrong quietly.
 *
 * @param {string} host
 * @param {import("@poweur/client").RelayRoot | null} root
 * @param {{ shell?: boolean }} [options]
 * @returns {ModeInfo}
 */
export function classifyHost(host, root, options = {}) {
  const hostedDomains = normalizeList(root?.hosted_domains);
  const launcherHost = normalizeHost(root?.launcher_host);
  // A relay older than E15-T7 sends only `launcher_host`; that is the whole
  // set there, and treating an absent list as "no launcher" would break it.
  const launcherHosts = normalizeList(root?.launcher_hosts);
  const launchers = launcherHosts.length ? launcherHosts : (launcherHost ? [launcherHost] : []);

  const base = {
    host,
    subject: "",
    handle: "",
    domain: "",
    hostedDomains,
    launcherHosts: launchers,
    launcherHost: launcherHost || launchers[0] || "",
    resolved: Boolean(root),
    // Set by resolveMode() once the relay has been asked. It is deliberately
    // *not* `resolved`: an unreachable relay never resolves, and a caller that
    // re-resolved until it did would spin forever.
    probed: false,
    reachable: Boolean(root),
  };

  if (options.shell) {
    return { ...base, mode: "shell", domain: hostedDomains[0] ?? "" };
  }
  if (!host) return { ...base, mode: "unknown" };

  if (launchers.includes(host)) {
    // The claim suffix: the hosted domain this launcher belongs to, so
    // `id.example.org` offers `.example.org` rather than the first entry.
    return { ...base, mode: "launcher", domain: claimDomainFor(host, hostedDomains) };
  }

  const parent = hostedParentOf(host, hostedDomains);
  if (parent) {
    return {
      ...base,
      mode: "identity",
      subject: host,
      handle: host.slice(0, host.length - parent.length - 1),
      domain: parent,
    };
  }

  return { ...base, mode: "unknown" };
}

/** The hosted domain a launcher host belongs to, for the claim suffix. */
function claimDomainFor(host, hostedDomains) {
  for (const domain of hostedDomains) {
    if (host === domain || host === `id.${domain}` || host.endsWith(`.${domain}`)) return domain;
  }
  return hostedDomains[0] ?? "";
}

/**
 * The hosted domain `host` is a direct child of, or "".
 *
 * Direct child only: `a.b.poweur.net` is not an identity host, because a
 * hosted identity is one label under the parent (`packages/identity`'s name
 * policy), and treating deeper names as identities would offer claims that
 * registration refuses.
 */
function hostedParentOf(host, hostedDomains) {
  for (const domain of hostedDomains) {
    if (!domain || !host.endsWith(`.${domain}`)) continue;
    const label = host.slice(0, host.length - domain.length - 1);
    if (label && !label.includes(".")) return domain;
  }
  return "";
}

function normalizeHost(value) {
  return String(value ?? "").trim().toLowerCase().replace(/\.$/, "");
}

function normalizeList(value) {
  if (!Array.isArray(value)) return [];
  const seen = new Set();
  const out = [];
  for (const entry of value) {
    const host = normalizeHost(entry);
    if (!host || seen.has(host)) continue;
    seen.add(host);
    out.push(host);
  }
  return out;
}

/**
 * The mode as far as we can tell without asking anyone.
 *
 * `sessionStorage` holds the last root document for this origin, so a reload
 * paints the right door immediately instead of flashing a generic welcome and
 * then replacing it. With no cached document this answers `unknown` — which
 * renders today's welcome, the safe thing to show while we find out.
 */
export function modeNow() {
  if (current) return current;
  current = classifyHost(currentHost(), readCachedRoot(), { shell: isShellRuntime() });
  return current;
}

/**
 * Ask the relay, then re-classify.
 *
 * Fails **open to `unknown`**, never to a guess: a landing page offering to
 * claim a name on a relay we cannot reach is worse than one that says it
 * cannot reach the relay. `reachable` carries that distinction to the UI.
 */
export function resolveMode({ force = false } = {}) {
  if (inFlight) return inFlight;
  // `probed`, not `resolved`. A cached root document makes `modeNow()`
  // resolved before anyone has asked anything, and short-circuiting on that
  // returned an already-settled promise whose `probed` stayed false — so a
  // caller written as `if (!modeNow().probed) resolveMode().then(render)`
  // re-armed itself in a microtask, forever, and the page never reached
  // DOMContentLoaded. Only a completed probe may skip the relay.
  if (current?.probed && !force) return Promise.resolve(current);

  const relayUrl = defaultRelayUrl();
  if (!relayUrl) {
    // No relay to ask. In a shell that is expected — it asks the user next.
    current = { ...classifyHost(currentHost(), readCachedRoot(), { shell: isShellRuntime() }), probed: true };
    return Promise.resolve(current);
  }

  inFlight = identityApiFor(relayUrl)
    .root()
    .then((root) => {
      writeCachedRoot(root);
      current = { ...classifyHost(currentHost(), root, { shell: isShellRuntime() }), probed: true };
      return current;
    })
    .catch(() => {
      const cached = readCachedRoot();
      current = {
        ...classifyHost(currentHost(), cached, { shell: isShellRuntime() }),
        probed: true,
        // A cached document still describes this relay, but we could not
        // confirm it now — the claim path must say so rather than proceed.
        reachable: false,
      };
      return current;
    })
    .finally(() => {
      inFlight = null;
    });

  return inFlight;
}

/** Test seam: drop the memoized mode and the cached root document. */
export function resetMode() {
  current = null;
  inFlight = null;
  try {
    globalThis.sessionStorage?.removeItem(SESSION_KEY);
  } catch {
    /* private mode */
  }
}

function readCachedRoot() {
  try {
    const raw = globalThis.sessionStorage?.getItem(SESSION_KEY);
    return raw ? JSON.parse(raw) : null;
  } catch {
    return null;
  }
}

function writeCachedRoot(root) {
  try {
    globalThis.sessionStorage?.setItem(SESSION_KEY, JSON.stringify(root));
  } catch {
    /* private mode; the mode is simply re-fetched next load */
  }
}
