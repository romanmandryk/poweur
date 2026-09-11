/**
 * Poweur ID Web Client — SPA controller.
 *
 * UI only: the protocol lives in `@poweur/client` (EPIC-015 E15-T6), key
 * custody in `js/vault.js`, and every relay call is made through the
 * `PoweurClient` that `js/client.js` builds for the active identity.
 *
 * Five destinations (E15-T1): messages | contacts | files | launcher | settings
 * Sub-pages (full-screen, back button): add-id | unlock | compose | onboarding | claim
 *
 * Before any of that there is a **front door** (E15-T7): with no identity on
 * this device the host decides what someone sees — a landing page that claims
 * a name, an identity host's sign-in, or the generic welcome. See `js/mode.js`.
 */

import {
  createIdentity, formatBytes, isSessionValid as sessionIsValid, ROOT_INFO,
  EnrollApi, RelayClient, sendAnonymous, clampPowBits, fingerprintOrKey,
  normalizeRendezvousId, resolveRecipientRelayUrl,
  SHARE_ROOTS, DEFAULT_CHUNK_THRESHOLD, grantExpired, grantAllowsWrite, SyncClient,
  streamForever, SDK_VERSION, SDK_BUILD_TIME,
  ACK_STATE_READ, sendsReadReceiptsTo, crypto as poweurCrypto,
} from "@poweur/client";

import { buildConversationRows, threadLabel } from "./threads.js";
import { isRetryableSendError, queueWebMessage, retryWebOutbox } from "./outbox.js";

import {
  createPasskey, authenticatePasskey,
  wrapKeysWithPRF, unwrapKeysWithPRF, checkPasskeySupport, PRF_UNAVAILABLE_MESSAGE,
} from "./passkey.js";

import {
  generateSeedIdentityJwks, generateEncryptionJwk, jwksFromSeed, keyBytesFromJwks,
  publicKeyFromJwk, toBase64url, fromBase64url,
} from "./vault.js";

import {
  getConfig, saveConfig,
  saveIdentityRecord, loadIdentityRecord, listIdentities, removeIdentity,
  getActiveIdentity, setActiveIdentity,
  loadSessionRecord, removeSessionRecord, rpIdFor,
  setUnlockedKeys, getUnlockedKeys, clearUnlockedKeys,
  defaultRelayUrl, hasRelayUrl, relayUrlFor, isShellRuntime,
  PRODUCTION_RELAY_URL, localDevRelayUrl, relayPresetFor, identityOriginUrl,
} from "./storage.js";

import {
  hasNativeKeystore, biometricAvailability, wrapKeysNative, unwrapKeysNative,
  forgetNativeSecret, GATE_BIOMETRIC,
} from "./native.js";

import { clientFor, identityApiFor, lookup, resolveOptionsForRelay } from "./client.js";
import { modeNow, resolveMode, addIdOptions } from "./mode.js";
import {
  appendBrowserConsent, deliverBrowserApproval, loadSignInConsent,
  readConnectedApps, readConsentLog, revokeConnectedApp, signBrowserApproval,
} from "./signin.js";

import { resolveProfile, clearProfileCache, primeProfile } from "./profiles.js";
import { IdentityInput } from "./components/identity-input.js";
import { PolicyControls, INBOX_MODES, describePowBits } from "./components/policy-controls.js";
import { AudiencePicker } from "./components/audience-picker.js";
import { ProfileCard } from "./components/profile-card.js";
import { describeDevice, deviceIcon } from "./components/devices.js";
import {
  listEnrollments, enrollThisBrowser, removeEnrollment, deviceLabel,
  recoveryKitEligibility, buildRecoveryKit, verifyRecoveryKit,
  recoverFromKeystore, restoreLocalRecord, rewrap,
} from "./keystore.js";
import { APP_VERSION, APP_BUILD_TIME } from "./build-info.js";
import { startJoinPoll, describeJoinError } from "./enroll-wait.js";

// ─── Router & State ───────────────────────────────────────────────────────────

/** The five primary destinations, in nav order. */
const DESTINATIONS = ["messages", "contacts", "files", "launcher", "settings"];

const R = {
  page: "messages",      // messages | contacts | files | launcher | settings
  sub: null,             // null | add-id | unlock | compose | onboarding | claim
  params: {},
  go(page, params = {}) {
    this.page = page; this.sub = null; this.params = params;
    render(); window.scrollTo(0, 0);
  },
  push(sub, params = {}) {
    this.sub = sub; this.params = params;
    render(); window.scrollTo(0, 0);
  },
  pop() {
    this.sub = null; this.params = {};
    render();
  },
};

/**
 * Sub-pages that are a *detail of the list behind them* rather than a gate
 * (E15-T11). On a phone they still cover the screen — the CSS decides — but on
 * a wide one they sit beside the list they came from, which is the whole
 * reason a 1440px window should not be one 1360px-wide column.
 *
 * Gates (unlock, add-id, onboarding, claim) are deliberately not here: they
 * are not a detail of anything, and showing an inbox behind an unlock prompt
 * would suggest it is reachable.
 */
const DETAIL_SUBS = new Set(["compose"]);

const S = {
  config: getConfig(),
  identity: getActiveIdentity(),
  messages: [],
  acks: [],
  dropdownOpen: false,
  /** Messages destination: inbox | requests | anonymous, as distinct trays. */
  tray: "inbox",
  contacts: { list: [], loading: false, loaded: false, error: null, filter: "" },
  /**
   * The durable archive (EPIC-009 E09-T1), read from
   * `poweur-sys/private/messages/`. `S.messages` is its in-memory view, so a
   * reload repopulates from the tree rather than starting empty — the relay
   * drains on pickup and has nothing left to re-serve.
   */
  history: { loading: false, loaded: false, error: null, readState: { conversations: {} } },
  requests: { incoming: [], loading: false, loaded: false, error: null, fetchedAt: 0 },
  anon: { messages: [], loading: false, loaded: false, error: null, fetchedAt: 0 },
  policy: { doc: null, explicit: false, loading: false, loaded: false },
  profile: { doc: null, explicit: false, loaded: false, loading: false },
  /** null unless the first-run flow is on screen. */
  onboard: null,
  /**
   * The identity host's own name (E15-T9): is it claimed, and if not may it
   * be claimed here? `state` is one of
   * `idle | checking | claimed | claimable | unavailable | offline`.
   */
  door: { subject: "", state: "idle", message: "", policy: null },
  /** `checkPasskeySupport()`, resolved once and told to the user up front. */
  passkey: null,
  /** `chooseCustody()`, likewise: what will hold the keys, said before the name. */
  custody: null,
  auth: { input: "", request: null, metadata: null, headline: "", scopes: [], loading: false, error: "", result: null },
  files: {
    dav: null, davExp: 0, path: "", entries: [], quota: null, loading: false, loaded: false,
    /** null = our own tree; an identity = browsing what they shared with us. */
    owner: null,
    /** True while choosing whose shared tree to open. */
    picking: false,
    grants: [], grantsLoaded: false,
    /** Changes-feed cursor for the auto-refresh (EPIC-004). */
    cursor: "", polling: false,
  },
};

/**
 * Components build DOM, the shell builds HTML strings. `mount()` bridges the
 * two: render an empty slot in the markup, then attach the live component to
 * it after the string lands. Kept in one place so the pattern is obvious.
 */
const pendingMounts = [];

function slot(id, build) {
  pendingMounts.push({ id, build });
  return `<div id="${id}"></div>`;
}

function flushMounts() {
  const queued = pendingMounts.splice(0, pendingMounts.length);
  for (const { id, build } of queued) {
    const host = document.getElementById(id);
    if (!host) continue;
    const node = build();
    if (node) host.replaceChildren(node);
  }
}

/** Resolve an identity for the components — the profile helper, curried. */
const resolveForComponents = (identity) => resolveProfile(identity, relayUrlFor(S.identity));

// ─── Helpers ──────────────────────────────────────────────────────────────────

const esc = s => String(s ?? "").replace(/&/g,"&amp;").replace(/</g,"&lt;").replace(/>/g,"&gt;").replace(/"/g,"&quot;");

function avatarColor(id) {
  const palette = ["#5856D6","#FF2D55","#FF9500","#34C759","#007AFF","#AF52DE","#FF3B30","#00C7BE"];
  let h = 0; for (const c of String(id)) h = (h * 31 + c.charCodeAt(0)) >>> 0;
  return palette[h % palette.length];
}

function idHandle(fqdn)  { return fqdn?.split(".")[0] ?? fqdn ?? ""; }
function idDomain(fqdn)  { return fqdn?.split(".").slice(1).join(".") ?? ""; }
function idInitial(fqdn) { return (idHandle(fqdn)[0] ?? "?").toUpperCase(); }
function shortFqdn(fqdn) {
  if (!fqdn) return "";
  return fqdn.length > 22 ? fqdn.slice(0, 10) + "…" + fqdn.slice(-8) : fqdn;
}

function fmtRelative(ts) {
  const d = Date.now() - new Date(ts).getTime();
  if (d < 60000) return "now";
  if (d < 3600000) return `${Math.floor(d/60000)}m`;
  if (d < 86400000) return `${Math.floor(d/3600000)}h`;
  return `${Math.floor(d/86400000)}d`;
}

function fmtTime(ts) { try { return new Date(ts).toLocaleString(); } catch { return ts; } }

function avatarHtml(fqdn, cls = "") {
  return `<div class="id-avatar ${cls}" style="background:${avatarColor(fqdn)}">${esc(idInitial(fqdn))}</div>`;
}

const svgBack = `<svg viewBox="0 0 10 18" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 1L1 9l8 8"/></svg>`;
const svgChevron = (cls="") => `<svg class="${cls}" viewBox="0 0 12 8" width="12" height="8" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M1 1.5l5 5 5-5"/></svg>`;
const svgCheck = `<svg viewBox="0 0 18 14" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" width="16" height="12"><path d="M1 7l5 5L17 1"/></svg>`;
const svgPlus = `<svg viewBox="0 0 18 18" fill="currentColor" width="16" height="16"><path d="M9 1a1 1 0 011 1v6h6a1 1 0 110 2h-6v6a1 1 0 11-2 0v-6H2a1 1 0 110-2h6V2a1 1 0 011-1z"/></svg>`;

const iconChat = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M21 11.5a8.4 8.4 0 01-9 8.3 9.1 9.1 0 01-3.9-.8L3 20.5l1.6-4.6A8.3 8.3 0 013.5 11 8.4 8.4 0 0112 3a8.4 8.4 0 019 8.5z"/></svg>`;
const iconChatFill = `<svg viewBox="0 0 24 24" fill="currentColor"><path d="M12 3a8.4 8.4 0 00-8.5 8.5c0 1.7.5 3.3 1.4 4.6L3 20.5l4.6-1.5c1.3.7 2.8 1.1 4.4 1.1a8.4 8.4 0 009-8.6A8.4 8.4 0 0012 3z"/></svg>`;
const iconPeople = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><circle cx="9" cy="8" r="3.2"/><path d="M2.5 20a6.5 6.5 0 0113 0"/><path d="M16.5 5.2a3.2 3.2 0 010 5.9M18 14.4a6.5 6.5 0 013.5 5.6"/></svg>`;
const iconPeopleFill = `<svg viewBox="0 0 24 24" fill="currentColor"><circle cx="9" cy="8" r="3.6"/><path d="M2 20.5a7 7 0 0114 0z"/><path d="M16.4 4.8a3.4 3.4 0 010 6.4 3.2 3.2 0 000-6.4zM17.6 13.6a7 7 0 014.4 6.9h-3.6a8.4 8.4 0 00-2.6-6.3z"/></svg>`;
const iconFolder = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M3 7.5A1.5 1.5 0 014.5 6h4l2 2.5h7A1.5 1.5 0 0119 10v7a1.5 1.5 0 01-1.5 1.5h-13A1.5 1.5 0 013 17z"/></svg>`;
const iconFolderFill = `<svg viewBox="0 0 24 24" fill="currentColor"><path d="M3 7.2A1.7 1.7 0 014.7 5.5h3.8L11 8.3h6.3A1.7 1.7 0 0119 10v7.3a1.7 1.7 0 01-1.7 1.7H4.7A1.7 1.7 0 013 17.3z"/></svg>`;

const iconRocket = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M12 2s-5 2.5-5 9a7 7 0 0014 0c0-6.5-5-9-5-9z"/><circle cx="12" cy="11" r="2"/><path d="M9 21l3-3 3 3"/><path d="M7 14l-3 3M17 14l3 3"/></svg>`;
const iconRocketFill = `<svg viewBox="0 0 24 24" fill="currentColor"><path d="M12 2C12 2 7 4.5 7 11a7 7 0 0010 6.32V21l-3-3-3 3v-3.68A7 7 0 0017 11c0-6.5-5-9-5-9zm0 11a2 2 0 110-4 2 2 0 010 4z"/><path d="M7 14L4 17M17 14l3 3" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" fill="none"/></svg>`;
const iconGear = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 00.33 1.82l.06.06a2 2 0 010 2.83 2 2 0 01-2.83 0l-.06-.06A1.65 1.65 0 0015 19.4a1.65 1.65 0 00-1 1.51V21a2 2 0 01-4 0v-.09A1.65 1.65 0 009 19.4a1.65 1.65 0 00-1.82.33l-.06.06a2 2 0 01-2.83-2.83l.06-.06A1.65 1.65 0 004.68 15a1.65 1.65 0 00-1.51-1H3a2 2 0 010-4h.09A1.65 1.65 0 004.6 9a1.65 1.65 0 00-.33-1.82l-.06-.06a2 2 0 012.83-2.83l.06.06A1.65 1.65 0 009 4.68a1.65 1.65 0 001-1.51V3a2 2 0 014 0v.09a1.65 1.65 0 001 1.51 1.65 1.65 0 001.82-.33l.06-.06a2 2 0 012.83 2.83l-.06.06A1.65 1.65 0 0019.4 9a1.65 1.65 0 001.51 1H21a2 2 0 010 4h-.09a1.65 1.65 0 00-1.51 1z"/></svg>`;
const iconGearFill = `<svg viewBox="0 0 24 24" fill="currentColor"><path d="M20.83 14.6a9 9 0 000-5.2l-2.18.36a7 7 0 00-.8-1.38l1.3-1.78A9 9 0 0015.4 3.2l-1.3 1.78a7 7 0 00-1.55-.44L12.2 2.1A9 9 0 009 2.1L8.65 4.6a7 7 0 00-1.55.44L5.8 3.2A9 9 0 003.05 6.6l1.3 1.78a7 7 0 00-.8 1.38L1.17 9.4a9 9 0 000 5.2l2.18-.36c.2.49.47.95.8 1.38l-1.3 1.78a9 9 0 002.75 3.4l1.3-1.78a7 7 0 001.55.44l.35 2.54a9 9 0 005.2 0l.35-2.54a7 7 0 001.55-.44l1.3 1.78a9 9 0 002.75-3.4l-1.3-1.78a7 7 0 00.8-1.38l2.18.36zM12 15a3 3 0 110-6 3 3 0 010 6z"/></svg>`;

// ─── Render orchestration ─────────────────────────────────────────────────────

/**
 * Find this node again after `innerHTML` replacement.
 *
 * The shell paints from strings, so a focused node is detached on the next
 * paint. Settings rows have ids; file and conversation rows have data
 * attributes. Without putting focus back, a DAV read that lands while
 * someone is on a row swallows the Enter that should have opened it —
 * which is what CI saw on the Files `shared` row (E15-T5).
 */
function focusSelector(el) {
  if (!el || el === document.body || el === document.documentElement) return null;
  if (el.id) return `#${CSS.escape(el.id)}`;
  if (el.dataset?.openDir != null) return `[data-open-dir="${CSS.escape(el.dataset.openDir)}"]`;
  if (el.dataset?.download != null) return `[data-download="${CSS.escape(el.dataset.download)}"]`;
  if (el.dataset?.composeTo != null) return `[data-compose-to="${CSS.escape(el.dataset.composeTo)}"]`;
  if (el.dataset?.contactOpen != null) return `[data-contact-open="${CSS.escape(el.dataset.contactOpen)}"]`;
  if (el.dataset?.openOwner != null) return `[data-open-owner="${CSS.escape(el.dataset.openOwner)}"]`;
  if (el.dataset?.page != null && el.classList.contains("nav-tab")) {
    return `.nav-tab[data-page="${CSS.escape(el.dataset.page)}"]`;
  }
  return null;
}

function render() {
  const app = document.getElementById("app");
  if (!app) return;
  const restore = app.contains(document.activeElement)
    ? focusSelector(document.activeElement)
    : null;

  if (R.sub && !DETAIL_SUBS.has(R.sub)) {
    app.innerHTML = renderSubPage();
  } else if (isFrontDoor()) {
    // No identity on this device: the destinations are not reachable yet, so
    // the nav would be five tabs that all say the same thing. The door is the
    // whole page (E15-T7).
    app.innerHTML = renderFrontDoor();
  } else {
    app.innerHTML = `
      ${renderHeader()}
      <div class="page-content" id="page-content">${renderPage()}</div>
      ${R.sub ? `<div class="detail-pane" id="detail-pane">${renderSubPage()}</div>` : ""}
      ${renderBottomNav()}`;
  }
  flushMounts();
  attachEvents();
  if (restore) app.querySelector(restore)?.focus();
}

// ─── Header ───────────────────────────────────────────────────────────────────

function renderHeader() {
  const ids = listIdentities();
  const themeIcon = document.documentElement.dataset.theme === "dark" ? "☀️" : "🌙";
  return `
    <header class="app-header">
      <span class="app-wordmark">Poweur ID</span>
      <div class="header-actions">
        <button class="btn-theme" id="btn-theme" aria-label="Toggle theme">${themeIcon}</button>
        <div class="id-pill-wrap" id="id-pill-wrap">
          ${S.identity ? `
            <button class="id-pill" id="id-pill" aria-expanded="${S.dropdownOpen}">
              ${avatarHtml(S.identity)}
              <span class="id-pill-handle">${esc(idHandle(S.identity))}</span>
              ${svgChevron("id-pill-chevron" + (S.dropdownOpen ? " open" : ""))}
            </button>
            ${S.dropdownOpen ? renderDropdown(ids) : ""}
          ` : `
            <button class="btn-add-id-pill" id="btn-add-id-header">
              ${svgPlus} Add ID
            </button>
          `}
        </div>
      </div>
    </header>`;
}

function renderDropdown(ids) {
  return `
    <div class="id-dropdown" id="id-dropdown">
      ${ids.map(id => `
        <button class="id-dr-row${id === S.identity ? " active" : ""}" data-switch="${esc(id)}">
          ${avatarHtml(id, "md")}
          <div class="id-dr-info">
            <div class="id-dr-name">${esc(idHandle(id))}</div>
            <div class="id-dr-domain">${esc(idDomain(id))}</div>
          </div>
          ${id === S.identity ? `<span class="id-dr-check">${svgCheck}</span>` : ""}
        </button>`).join("")}
      <div class="id-dr-sep"></div>
      <button class="id-dr-row id-dr-add" id="dd-add-id">
        <div class="id-avatar md" style="background:var(--accent-light);color:var(--accent)">${svgPlus}</div>
        <div class="id-dr-info">
          <div class="id-dr-name" style="color:var(--accent)">Add identity</div>
        </div>
      </button>
    </div>`;
}

const NAV = [
  { page: "messages", label: "Messages", icon: iconChat,   iconFill: iconChatFill },
  { page: "contacts", label: "Contacts", icon: iconPeople, iconFill: iconPeopleFill },
  { page: "files",    label: "Files",    icon: iconFolder, iconFill: iconFolderFill },
  { page: "launcher", label: "Apps",     icon: iconRocket, iconFill: iconRocketFill },
  { page: "settings", label: "Settings", icon: iconGear,   iconFill: iconGearFill },
];

function renderBottomNav() {
  return `
    <nav class="bottom-nav" role="tablist" aria-label="Primary">
      ${NAV.map(({ page, label, icon, iconFill }) => `
        <button class="nav-tab${R.page === page ? " active" : ""}" data-page="${page}"
                role="tab" aria-selected="${R.page === page}" aria-label="${label}">
          ${R.page === page ? iconFill : icon}
          <span>${label}</span>
        </button>`).join("")}
    </nav>`;
}

// ─── Top-level pages ──────────────────────────────────────────────────────────

function renderPage() {
  // Every destination but the launcher needs an identity first; the launcher
  // is where you make one, so it stays reachable.
  if (!S.identity && R.page !== "launcher") return renderWelcome();
  if (!getUnlockedKeys() && R.page !== "launcher" && R.page !== "settings") return renderLocked();

  switch (R.page) {
    case "contacts": return renderContacts();
    case "files":    return renderFilesDestination();
    case "launcher": return renderLauncher();
    case "settings": return renderSettings();
    default:         return renderMessages();
  }
}

// ─── Messages destination ─────────────────────────────────────────────────────

/** The Messages trays. The anonymous one is wired up in T3. */
const TRAYS = [
  { id: "inbox",     label: "Inbox" },
  { id: "requests",  label: "Requests" },
  { id: "anonymous", label: "Anonymous" },
];

const CONTACT_STATE_CHIP = {
  accepted:  { label: "Contact",   cls: "chip-green"  },
  requested: { label: "Requested", cls: "chip-orange" },
  blocked:   { label: "Blocked",   cls: "chip-red"    },
};

/**
 * How many things are waiting in a tray.
 *
 * Only the trays where a count is unambiguous: a request and an anonymous
 * message are both "something addressed to you that you have not dealt with".
 * The inbox gets no badge, because the app has no read state and a number
 * there would be counting messages held, not messages unread — a badge that
 * never clears teaches people to ignore badges.
 */
function trayCount(tray) {
  if (tray === "requests") return incomingRequests().length;
  // Unread, not held. `S.anon.messages.length` never reached zero, because
  // nothing in the app ever removed one — so the badge sat there for the life
  // of the session no matter how many times the tray was opened.
  if (tray === "anonymous") return unreadAnonymous();
  return 0;
}

/** The contact record for an identity, or null — the app's one lookup. */
function contactFor(identity) {
  const wanted = String(identity ?? "").toLowerCase();
  return S.contacts.list.find(c => c.identity.toLowerCase() === wanted) ?? null;
}

// ─── Front doors (E15-T7 / T8 / T9) ───────────────────────────────────────────

/**
 * True when this device holds no identity, so the app is a front door rather
 * than a client. The claim and add-id flows are sub-pages, which render
 * themselves — they are how you stop being at the door.
 */
function isFrontDoor() {
  return !S.identity && !R.sub;
}

/**
 * Which door, decided by the host.
 *
 * `unknown` — a dev server, a self-hoster's relay host, a shell that has not
 * been told its relay yet — keeps the generic welcome. It is the honest
 * answer: we do not know what this host stands for, so we do not pretend to.
 */
function renderFrontDoor() {
  const info = modeNow();
  switch (info.mode) {
    case "launcher": return renderLanding(info);
    case "shell":    return renderLanding(info);
    case "identity": return renderIdentityDoor(info);
    default:         return renderWelcome();
  }
}

/** The three sentences. Any longer and nobody reads them. */
const HOW_IT_WORKS = [
  ["🏷️", "A name you own", "Your ID is an address on the internet — like a domain, but for a person."],
  ["🔒", "Messages and files under it", "End-to-end encrypted mail and a synced drive, addressed to the name."],
  ["🔑", "Sign in with it", "No passwords to reuse: your device holds the key, and apps ask it."],
];

/**
 * The parent-domain landing (E15-T8).
 *
 * One dominant control: a name, with the domain as a fixed suffix *inside* the
 * field. The suffix is not a question — the host already answered it — so it
 * is not an input. Free text returns only where the domain genuinely is not
 * knowable: a relay that hosts nothing under a domain of its own.
 */
function renderLanding(info) {
  const relayPrompt = renderRelayPrompt();
  // A shell still shows the claim field: production is the default relay, so
  // the picker is a switch, not a gate. Without a relay there is nothing to
  // claim under, which is the original empty-shell case.
  const claim = !relayPrompt || hasRelayUrl() ? renderClaimCard(info) : "";
  return `
    <div class="landing" id="landing">
      <header class="landing-bar">
        <span class="app-wordmark">Poweur ID</span>
        <button class="btn-theme" id="btn-theme" aria-label="Toggle theme">${document.documentElement.dataset.theme === "dark" ? "☀️" : "🌙"}</button>
      </header>
      <div class="landing-body">
        <div class="landing-hero">
          <h1 class="landing-title">Your name. Your inbox. Your files.</h1>
          <p class="landing-sub">Claim an identity you own, and take it everywhere.</p>
        </div>
        ${relayPrompt ? `<div class="landing-card">${relayPrompt}</div>` : ""}
        ${claim}
        <ol class="landing-steps">
          ${HOW_IT_WORKS.map(([icon, title, body]) => `
            <li class="landing-step">
              <span class="landing-step-icon" aria-hidden="true">${icon}</span>
              <div>
                <div class="landing-step-title">${esc(title)}</div>
                <div class="landing-step-body">${esc(body)}</div>
              </div>
            </li>`).join("")}
        </ol>
        <div class="landing-alt">
          <button class="btn-link" id="opt-have-id">I already have an ID</button>
          <span class="landing-alt-sep" aria-hidden="true">·</span>
          <button class="btn-link" id="opt-own-domain">Use my own domain</button>
        </div>
      </div>
    </div>`;
}

/**
 * The name field, shared by the landing and the `claim` sub-page.
 *
 * `#ni-handle`, `#ni-availability` and `#btn-claim` are the contract the
 * availability wiring and the e2e specs use; the suffix beside them changes
 * shape with what the relay hosts, not with which screen this is on.
 */
function renderClaimCard(info) {
  // The field's shape depends on what the relay hosts, so drawing it from a
  // guess means redrawing it a moment later in a different shape. A skeleton
  // is the honest frame for "about to know" (E15-T12).
  if (!info.probed) {
    return `
      <div class="claim-card" id="claim-card-pending">
        <div class="skeleton-stack" aria-hidden="true">
          <div class="skeleton skeleton-btn"></div>
          <div class="skeleton skeleton-line"></div>
        </div>
        <p class="muted small" role="status">Connecting…</p>
      </div>`;
  }

  const domains = info.hostedDomains ?? [];
  let suffix;
  if (domains.length > 1) {
    suffix = `
      <select class="claim-suffix claim-suffix-select" id="ni-domain" aria-label="Domain">
        ${domains.map(d => `<option value="${esc(d)}"${d === info.domain ? " selected" : ""}>.${esc(d)}</option>`).join("")}
      </select>`;
  } else if (domains.length === 1) {
    suffix = `<span class="claim-suffix" id="ni-domain-fixed" data-domain="${esc(domains[0])}">.${esc(domains[0])}</span>`;
  } else {
    // Nothing hosted here (or the relay could not be asked). The domain is
    // genuinely unknown, so this is the one place it is still typed.
    suffix = `<input class="claim-suffix claim-suffix-input" id="ni-domain" type="text"
                     value="${esc(info.domain || S.config.parentDomain || "")}" placeholder="example.org"
                     autocapitalize="none" autocorrect="off" spellcheck="false" aria-label="Parent domain" />`;
  }

  return `
    <div class="claim-card" id="claim-card">
      <label class="claim-label" for="ni-handle">Choose your name</label>
      <div class="claim-field">
        <input id="ni-handle" class="claim-input" type="text" placeholder="yourname"
               autocomplete="off" spellcheck="false" autocapitalize="none" autocorrect="off"
               enterkeyhint="go" aria-describedby="ni-availability" />
        ${suffix}
      </div>
      <p class="idin-status small" id="ni-availability" role="status" aria-live="polite">${esc(policyHint(info))}</p>
      <button class="btn btn-primary claim-submit" id="btn-claim"${webCustodyBlocked() ? " disabled" : ""}>Create ID</button>
      ${renderClaimNote(info)}
    </div>`;
}

/**
 * What this browser can actually do, said before the name is typed (E15-T12).
 *
 * Native keystore (the mobile shell) or passkey PRF. Authenticators without
 * PRF are refused up front — a PIN is not a wrapping secret.
 */
function webCustodyBlocked() {
  if (S.custody?.kind === "native") return false;
  if (!S.passkey) return false;
  return S.passkey.available === false || S.passkey.prf === false;
}

function renderClaimNote(info) {
  if (info.reachable === false && info.resolved) {
    return `<p class="form-note small claim-note val-warn">
      Can't reach the relay right now — names can't be checked until it answers.
    </p>`;
  }
  if (S.custody?.kind === "native") {
    return `<p class="form-note small claim-note">
      This device's keystore will hold your key, and only your biometrics release it.
      Nothing leaves the device in plain form.
    </p>`;
  }
  if (webCustodyBlocked()) {
    return `<p class="form-note small claim-note val-warn" id="claim-prf-required">
      ${esc(S.passkey.reason || PRF_UNAVAILABLE_MESSAGE)}
    </p>`;
  }
  return `<p class="form-note small claim-note">
    Your keys are generated on this device and never leave it in plain form.
    This browser must support passkeys with PRF (Apple or Google passkeys in Safari or Chrome).
  </p>`;
}

/**
 * The rule, before the first keystroke rather than after it (E15-T12).
 *
 * The relay's policy is deployment configuration — a production relay asks for
 * six characters where the package default is three — so the only honest
 * source is the verdict document, which carries the policy it applied. Until
 * the first check answers, say nothing rather than guess.
 */
function policyHint(info) {
  const policy = S.door.policy;
  if (!info.reachable && info.resolved === false) return "";
  if (!policy) return "";
  const bits = [];
  if (policy.min_len && policy.max_len) bits.push(`${policy.min_len}–${policy.max_len} characters`);
  if (policy.charset) bits.push(policy.charset);
  return bits.join(", ");
}

/**
 * The identity host's own door (E15-T9).
 *
 * `bob.poweur.net` is not a place to ask who you are — the URL bar already
 * said. So there is no identity field here: either Bob signs in, or the name
 * is free and can be claimed, as `bob` and nothing else.
 */
function renderIdentityDoor(info) {
  const subject = info.subject;
  const door = S.door;
  const head = `
    <header class="landing-bar">
      <span class="app-wordmark">Poweur ID</span>
      <button class="btn-theme" id="btn-theme" aria-label="Toggle theme">${document.documentElement.dataset.theme === "dark" ? "☀️" : "🌙"}</button>
    </header>`;

  const shell = (body) => `<div class="landing door" id="door">${head}<div class="landing-body">${body}</div></div>`;

  if (door.state === "idle" || door.state === "checking") {
    return shell(`
      <div class="door-card">
        ${avatarHtml(subject, "xl")}
        <div class="door-name">${esc(subject)}</div>
        <div class="skeleton-stack" aria-hidden="true">
          <div class="skeleton skeleton-line"></div>
          <div class="skeleton skeleton-btn"></div>
        </div>
        <p class="muted small" role="status">Checking this name…</p>
      </div>`);
  }

  if (door.state === "claimed") {
    return shell(`
      <div class="door-card">
        ${avatarHtml(subject, "xl")}
        <div class="door-name">${esc(idHandle(subject))}</div>
        <div class="door-domain">${esc(idDomain(subject))}</div>
        <button class="btn btn-passkey door-primary" id="btn-door-signin">🔑 Sign in with passkey</button>
        <button class="btn btn-secondary door-secondary" id="opt-join-device"
                data-join-identity="${esc(subject)}">📱 Add this device</button>
        <p class="form-note small">
          This name is taken. If it is yours, your passkey opens it — on this device or a
          device you already use.
        </p>
      </div>
      ${renderDoorFooter(info)}`);
  }

  if (door.state === "claimable") {
    return shell(`
      <div class="door-card">
        ${avatarHtml(subject, "xl")}
        <div class="door-kicker">This name is free</div>
        <div class="door-name">${esc(idHandle(subject))}</div>
        <div class="door-domain">${esc(idDomain(subject))}</div>
        <button class="btn btn-primary door-primary" id="btn-door-claim">Claim ${esc(subject)}</button>
        <p class="form-note small">
          You are claiming <strong>${esc(subject)}</strong> — the name this page is served
          from. Your keys are generated here and never leave in plain form.
        </p>
      </div>
      ${renderDoorFooter(info)}`);
  }

  if (door.state === "offline") {
    return shell(`
      <div class="door-card">
        ${avatarHtml(subject, "xl")}
        <div class="door-name">${esc(subject)}</div>
        <p class="door-message">Can't reach the relay, so this name can't be checked.</p>
        <button class="btn btn-secondary door-primary" id="btn-door-retry">Try again</button>
        <button class="btn btn-passkey door-secondary" id="btn-door-signin">🔑 Sign in with passkey</button>
      </div>`);
  }

  // unavailable — reserved, blocked, or a policy refusal. Offering a claim
  // here would spend a passkey on a name registration is going to refuse.
  return shell(`
    <div class="door-card">
      ${avatarHtml(subject, "xl")}
      <div class="door-name">${esc(subject)}</div>
      <p class="door-message">${esc(door.message || "This name is not available.")}</p>
      ${renderDoorFooter(info)}
    </div>`);
}

/** The way out of an identity host: the launcher, where a name is chosen. */
function renderDoorFooter(info) {
  const launcher = info.launcherHost;
  if (!launcher) return "";
  return `
    <div class="landing-alt">
      <a class="btn-link" id="door-launcher-link"
         href="${esc(`${globalThis.location?.protocol ?? "https:"}//${launcher}/app/`)}">Claim a different name</a>
    </div>`;
}

/** The honest fallback: an unrecognised host, so no claim is offered. */
function renderWelcome() {
  return `
    <div class="welcome-wrap">
      <div class="welcome-icon">💬</div>
      <h1 class="welcome-title">Welcome to Poweur ID</h1>
      <p class="welcome-sub">Encrypted, identity-first messaging. No phone number required.</p>
      <div class="welcome-cta">
        <button class="btn btn-primary" id="btn-welcome-start">Get started</button>
      </div>
    </div>`;
}

/** Shown on any destination that needs keys, so unlocking is one tap from anywhere. */
function renderLocked() {
  const rec = loadIdentityRecord(S.identity);
  return `
    <div class="unlock-wrap">
      ${avatarHtml(S.identity, "xl unlock-avatar")}
      <div class="unlock-name">${esc(idHandle(S.identity))}</div>
      <div class="unlock-sub">${esc(idDomain(S.identity))}</div>
      <button class="btn btn-passkey mt-md" id="btn-unlock-main">
        🔓 ${CUSTODY_COPY[custodyOf(rec)].action}
      </button>
      <p class="form-note small" style="max-width:220px">
        ${CUSTODY_COPY[custodyOf(rec)].note}
      </p>
    </div>`;
}

function renderMessages() {
  return `
    <div class="dest-header">
      <h1 class="dest-title">Messages</h1>
      <button class="btn btn-sm btn-primary dest-action" id="btn-compose-top">${svgPlus} New message</button>
    </div>
    <div class="tray-bar" role="tablist" aria-label="Message trays">
      ${TRAYS.map(t => {
        const waiting = trayCount(t.id);
        return `
        <button class="tray-tab${S.tray === t.id ? " active" : ""}" data-tray="${t.id}"
                role="tab" aria-selected="${S.tray === t.id}"
                ${waiting ? `aria-label="${esc(t.label)}, ${waiting} waiting"` : ""}>${t.label}${
          waiting ? `<span class="tray-badge">${waiting > 99 ? "99+" : waiting}</span>` : ""
        }</button>`;
      }).join("")}
    </div>
    ${renderTray()}
    <button class="fab" id="btn-compose" title="New message" aria-label="New message">✏️</button>`;
}

function renderTray() {
  if (S.tray === "requests") return renderRequestsTray();
  if (S.tray === "anonymous") return renderAnonTray();

  const conversations = buildConversations();
  if (!conversations.length) {
    return emptyState("💬", "No messages yet", "Tap ✏️ to send your first.");
  }
  return `
    <div class="conv-list">
      ${conversations.map(c => `
        <div class="conv-row" data-compose-to="${esc(c.contact)}" data-thread="${esc(c.threadId)}"
             data-group="${c.group ? "true" : "false"}" role="button" tabindex="0">
          ${c.group ? `<div class="avatar avatar-md">👥</div>` : avatarHtml(c.contact, "md")}
          <div class="conv-info">
            <div class="conv-name">${esc(c.petname || idHandle(c.contact))}${c.group ? ` <span class="chip chip-green">Group</span>` : ""}${
              c.threaded ? ` <span class="conv-thread">#${esc(threadLabel(c.threadId))}</span>` : ""
            }</div>
            <div class="conv-preview">${esc(c.preview)}</div>
          </div>
          <div class="conv-meta">
            <span class="conv-time">${fmtRelative(c.lastMsg.timestamp)}</span>
            ${c.unread ? `<span class="conv-badge">${c.unread}</span>` : ""}
            ${c.lastMsg.type === "chat.attachment" && c.lastMsg.metadata ? `
              <button class="btn btn-sm" data-download-attachment="${esc(JSON.stringify(c.lastMsg.metadata))}">Open 📎</button>` : ""}
            ${c.stranger ? `
              <button class="btn btn-sm conv-add" data-add-contact="${esc(c.contact)}"
                      title="Add contact">${svgPlus} Add</button>` : ""}
          </div>
        </div>`).join("")}
    </div>`;
}

/**
 * Incoming requests, from both places one can arrive.
 *
 * Under `contacts_and_requests` the relay parks a stranger's first
 * `sys.contact.request` in the requests queue; under the default `open` policy
 * the very same envelope is delivered to the inbox as a typed message. Showing
 * only the queue would leave every default-policy user with an empty tray and
 * a contact request buried among their conversations.
 */
function incomingRequests() {
  const byRequester = new Map();
  const add = (entry) => {
    const state = contactFor(entry.sender)?.state;
    if (state === "accepted" || state === "blocked") return;
    const existing = byRequester.get(entry.sender);
    if (!existing || new Date(entry.timestamp) > new Date(existing.timestamp)) {
      byRequester.set(entry.sender, { ...existing, ...entry });
    }
  };
  for (const entry of S.requests.incoming) {
    // The queue also carries `sys.contact.accept` answers to requests we sent;
    // those are handled in the background, not shown as someone asking.
    if (entry.type && entry.type !== "sys.contact.request") continue;
    // The queued envelope is encrypted like any message and the SDK opens it,
    // so a request routed here reads the same as one routed to the inbox —
    // otherwise the recommended policy is the one that shows you least.
    add({ sender: entry.sender, timestamp: entry.timestamp, intro: entry.plaintext ?? null, queued: true });
  }
  for (const raw of S.messages) {
    const m = typeof raw === "string" ? JSON.parse(raw) : raw;
    if (m.type !== "sys.contact.request" || m.sender === S.identity) continue;
    add({ sender: m.sender, timestamp: m.timestamp, intro: m.plaintext ?? null, queued: false });
  }
  return [...byRequester.values()].sort((a, b) => new Date(b.timestamp) - new Date(a.timestamp));
}

function renderRequestsTray() {
  const incoming = incomingRequests();
  const outgoing = S.contacts.list.filter(c => c.state === "requested");

  if (!incoming.length && !outgoing.length) {
    return `${S.requests.error ? `<p class="small val-warn" style="padding:16px">${esc(S.requests.error)}</p>` : ""}
      ${emptyState("🤝", "No contact requests",
        "Requests to connect land here. Accepting one lets you message each other.")}`;
  }

  return `
    ${S.requests.error ? `<p class="small val-warn" style="padding:16px">${esc(S.requests.error)}</p>` : ""}
    ${incoming.length ? `
      <div class="tray-section-label">Waiting for you</div>
      <div class="conv-list">
        ${incoming.map(r => `
          <div class="request-row">
            ${slot(`req-${encodeURIComponent(r.sender)}`, () => ProfileCard({
              identity: r.sender,
              resolve: resolveForComponents,
              compact: true,
            }).el)}
            ${r.intro ? `<p class="request-intro">${esc(r.intro)}</p>` : ""}
            <div class="request-actions">
              <button class="btn btn-sm btn-primary" data-accept-contact="${esc(r.sender)}">Accept</button>
              <button class="btn btn-sm" data-block-contact="${esc(r.sender)}">Block</button>
            </div>
          </div>`).join("")}
      </div>` : ""}
    ${outgoing.length ? `
      <div class="tray-section-label">Sent by you</div>
      <div class="conv-list">
        ${outgoing.map(c => `
          <div class="request-row">
            ${slot(`out-${encodeURIComponent(c.identity)}`, () => ProfileCard({
              identity: c.identity,
              resolve: resolveForComponents,
              compact: true,
            }).el)}
            <div class="request-actions">
              <span class="chip chip-orange">Requested</span>
              <button class="btn btn-sm" data-cancel-request="${esc(c.identity)}">Cancel</button>
            </div>
          </div>`).join("")}
      </div>` : ""}`;
}

/**
 * The anonymous tray.
 *
 * These messages are **unauthenticated** — encrypted to us, but signed by
 * nobody — so they are rendered as a different kind of object entirely: no
 * sender, no avatar, and no reply affordance, because there is nobody to
 * reply to. That visual difference is a requirement of the spec, not styling.
 */
function renderAnonTray() {
  const A = S.anon;
  const anon = S.policy.doc?.anonymous;
  const off = S.policy.loaded && !anon?.allow;

  if (!A.messages.length) {
    return `
      ${A.error ? `<p class="small val-warn" style="padding:16px">${esc(A.error)}</p>` : ""}
      ${emptyState("🎭", "No anonymous messages",
        off
          ? "Anonymous messages are turned off. Strangers with no identity cannot reach you until you allow it in Settings."
          : "Messages from strangers with no identity land here. Nobody is identified, so there is nothing to reply to.",
        off ? `<button class="btn btn-primary" id="btn-anon-settings">Open inbox settings</button>` : "")}`;
  }

  return `
    <p class="anon-explainer small">
      Encrypted to you, signed by nobody. Anyone could have sent these, and there is no way to reply.
    </p>
    <div class="conv-list">
      ${A.messages.map(m => `
        <div class="anon-row">
          <div class="anon-row-head">
            <span class="chip chip-orange">Anonymous</span>
            <span class="conv-time">${fmtRelative(m.timestamp)}</span>
          </div>
          <div class="anon-body">${m.plaintext ? esc(m.plaintext) : "🔒 Could not decrypt"}</div>
        </div>`).join("")}
    </div>`;
}

function emptyState(icon, title, body, actionHtml = "") {
  return `
    <div class="empty-state">
      <div class="empty-state-icon">${icon}</div>
      <h2 class="empty-state-title">${esc(title)}</h2>
      <p class="empty-state-body">${esc(body)}</p>
      ${actionHtml}
    </div>`;
}

/**
 * Rows for the messages tray: one per (contact, thread).
 *
 * The grouping and the preview text live in `js/threads.js` — including the
 * generic "app message from …" line an unknown `type` gets, so a chat UI
 * never shows a user someone else's JSON. Here we only add what needs app
 * state: petnames and the unread mark.
 */
function buildConversations() {
  return buildConversationRows(S.messages, S.identity, unreadFor).map(row => {
    const known = contactFor(row.contact);
    return {
      ...row,
      petname: known?.petname ?? null,
      // Someone we have no entry for at all: adding them is one tap from
      // the message that made us want to.
      stranger: !known,
    };
  });
}

// ─── Contacts destination ─────────────────────────────────────────────────────

function renderContacts() {
  const C = S.contacts;
  const filtered = C.list.filter(c => {
    const needle = C.filter.trim().toLowerCase();
    return !needle || `${c.identity} ${c.petname ?? ""}`.toLowerCase().includes(needle);
  });

  return `
    <div class="dest-header">
      <h1 class="dest-title">Contacts</h1>
      <button class="btn btn-sm btn-primary" id="btn-add-contact">${svgPlus} Add</button>
    </div>
    ${C.list.length ? `
      <div class="dest-toolbar">
        <input id="contacts-filter" class="input" type="search" placeholder="Search contacts"
               value="${esc(C.filter)}" autocomplete="off" aria-label="Search contacts" />
      </div>` : ""}
    ${C.loading ? `<p class="muted small" style="padding:16px">Loading contacts…</p>` : ""}
    ${C.error ? `<p class="small val-warn" style="padding:16px">${esc(C.error)}</p>` : ""}
    ${!C.loading && !C.list.length ? emptyState(
      "👥", "No contacts yet",
      "Add someone by their Poweur ID and you can message them without either of you sharing a phone number.",
      `<button class="btn btn-primary" id="btn-add-contact-empty">Add a contact</button>`) : ""}
    ${filtered.length ? `
      <div class="conv-list">
        ${filtered.map(c => renderContactRow(c)).join("")}
      </div>` : ""}
    ${C.list.length && !filtered.length ? `<p class="muted small" style="padding:16px">No contact matches “${esc(C.filter)}”.</p>` : ""}`;
}

/**
 * One contact: the person (ProfileCard), their state, and the action that
 * state implies — with everything else behind the overflow, so the row stays
 * inside a 375px viewport.
 */
function renderContactRow(contact) {
  const chip = CONTACT_STATE_CHIP[contact.state] ?? CONTACT_STATE_CHIP.accepted;

  // No button on the card itself: at 375px a name, an ID, a state chip and an
  // action do not fit, and the row's own tap is the action people want
  // (message them; the overflow holds everything else).
  return `
    <div class="contact-row" data-contact-open="${esc(contact.identity)}"
         data-contact-state="${esc(contact.state)}" role="button" tabindex="0">
      ${slot(`contact-${encodeURIComponent(contact.identity)}`, () => ProfileCard({
        identity: contact.identity,
        resolve: resolveForComponents,
        compact: true,
        cached: contact.petname ? { displayName: contact.petname, links: [] } : null,
      }).el)}
      <div class="contact-row-meta">
        <span class="chip ${chip.cls}">${chip.label}</span>
        <button class="btn-icon contact-more" data-contact-menu="${esc(contact.identity)}"
                aria-label="More actions for ${esc(contact.identity)}">⋯</button>
      </div>
    </div>`;
}

// ─── Apps destination ─────────────────────────────────────────────────────────

/**
 * The Launcher used to *be* the create-identity form, so the "Apps" tab showed
 * "New identity — step 1 of 2" to someone who already had one. Claiming moved
 * to the front door (E15-T7/T8/T9); what is left here is the seam EPIC-010
 * fills, said plainly rather than filled with something else.
 */
function renderLauncher() {
  return `
    <div class="dest-header">
      <h1 class="dest-title">Apps</h1>
    </div>
    ${emptyState("🚀", "No apps yet",
      "Apps and automations that read and write your Poweur files will appear here.")}`;
}

// ─── Claim sub-page ───────────────────────────────────────────────────────────

/**
 * Claiming from somewhere that is not a launcher host: the "Add new ID" option
 * inside the app, or the self-hosted DNS path.
 *
 * `R.params.dns` picks the second one. It is a *different intent*, not a
 * checkbox on the first — which is why there is no "hosted?" toggle anywhere
 * any more (E15-T10). Whether a registration is hosted is derived from the
 * domain: hosted iff the relay says it hosts it.
 */
function renderClaim() {
  const info = modeNow();
  const dns = Boolean(R.params.dns);

  return `
    <div class="sub-page">
      <div class="sub-header">
        <button class="btn-back" id="btn-back">${svgBack}</button>
        <span class="sub-title">${dns ? "Use my own domain" : "New identity"}</span>
      </div>
      <div class="sub-body">
        ${dns ? renderDnsClaim(info) : renderClaimCard(info)}
      </div>
    </div>`;
}

/**
 * The self-hosted path, on one page.
 *
 * It used to redirect to `https://<handle>.<domain>/app/` to collect the DNS
 * token — a host that, in DNS mode, does not resolve until *after* the
 * registration being collected has written it. Nothing needed the hop: the
 * relay writing the zone is the one being asked here.
 */
function renderDnsClaim(info) {
  const cfg = S.config;
  return `
    <div class="form-card">
      <p class="muted small" style="margin-bottom:16px">
        Your identity lives under a domain you control. The relay writes the DNS records
        for you with a scoped API token, which is never stored.
      </p>
      <div class="form-group">
        <label class="form-label" for="ni-handle">Name</label>
        <input id="ni-handle" class="input" type="text" placeholder="yourname"
               autocomplete="off" spellcheck="false" autocapitalize="none" autocorrect="off" />
      </div>
      <div class="form-group">
        <label class="form-label" for="ni-domain">Your domain</label>
        <input id="ni-domain" class="input" type="text" placeholder="example.org"
               value="${esc(cfg.parentDomain || "")}"
               autocomplete="off" spellcheck="false" autocapitalize="none" autocorrect="off" />
      </div>
      <div class="form-group">
        <label class="form-label" for="ni-provider">DNS provider</label>
        <select id="ni-provider" class="input select">
          <option value="cloudflare"${cfg.dnsProvider === "cloudflare" ? " selected" : ""}>Cloudflare</option>
          <option value="hetzner"${cfg.dnsProvider === "hetzner" ? " selected" : ""}>Hetzner</option>
        </select>
      </div>
      <div class="form-group">
        <label class="form-label" for="ni-token">DNS API token</label>
        <input id="ni-token" class="input" type="password" placeholder="Scoped API token" autocomplete="off" />
      </div>
      <div class="form-group">
        <label class="form-label" for="ni-invite">Invite code (if this relay asks for one)</label>
        <input id="ni-invite" class="input" type="text" placeholder="optional" autocomplete="off" />
      </div>
      <p class="idin-status small" id="ni-availability" role="status" aria-live="polite"></p>
      <button class="btn btn-passkey" id="btn-claim" style="width:100%"${webCustodyBlocked() ? " disabled" : ""}>🔑 Create with passkey</button>
      <p class="form-note small" style="margin-top:10px;text-align:center">
        Keys are generated locally and never leave your device in plain form.
      </p>
    </div>`;
}

/**
 * What the user is asking for, gathered from whichever claim screen is up.
 *
 * `hosted` is **derived, never asked** (E15-T10): a registration is hosted iff
 * the relay hosts the domain, which the root document already published. The
 * checkbox that used to ask this made a fact into a prompt, and got it wrong
 * the moment someone unticked it on a relay that hosts nothing else.
 */
function readClaimIntent() {
  const info = modeNow();
  const handle = normalizeHandleInput(q("#ni-handle")?.value ?? "", info);
  const domain = (q("#ni-domain")?.value ?? q("#ni-domain-fixed")?.dataset.domain ?? info.domain ?? "")
    .trim().toLowerCase().replace(/^\.+/, "");
  return {
    handle,
    domain,
    hosted: info.hostedDomains.includes(domain),
    provider: q("#ni-provider")?.value,
    dnsToken: q("#ni-token")?.value.trim() ?? "",
    inviteCode: q("#ni-invite")?.value.trim() ?? "",
  };
}

/**
 * What someone types into a field that already shows its suffix.
 *
 * A visible `.poweur.net` invites pasting the whole address, and an ID copied
 * from somewhere else arrives as `@alice` or `Alice ` (E15-T12). All of those
 * mean the same handle, so none of them should be an error.
 */
export function normalizeHandleInput(raw, info = { domain: "", hostedDomains: [] }) {
  let value = String(raw ?? "").trim().toLowerCase().replace(/^@+/, "");
  const domains = [info.domain, ...(info.hostedDomains ?? [])].filter(Boolean);
  for (const domain of domains) {
    if (value.endsWith(`.${domain}`)) {
      value = value.slice(0, value.length - domain.length - 1);
      break;
    }
  }
  return value;
}

// ─── Settings page ────────────────────────────────────────────────────────────

function renderSettings() {
  const rec = S.identity ? loadIdentityRecord(S.identity) : null;
  const sess = S.identity ? loadSessionRecord(S.identity) : null;
  const sessOk = sessionIsValid(sess);

  return `
    <div class="dest-header">
      <h1 class="dest-title">Settings</h1>
    </div>
    ${rec ? `
      <div class="settings-id-card">
        ${avatarHtml(S.identity, "lg")}
        <div class="settings-id-name">${esc(idHandle(S.identity))}</div>
        <div class="settings-id-domain">${esc(idDomain(S.identity))}</div>
        <span class="chip ${CUSTODY_COPY[custodyOf(rec)].chipClass}" style="margin-top:4px">
          ${CUSTODY_COPY[custodyOf(rec)].chip}
        </span>
      </div>
    ` : `
      <div class="settings-id-card">
        <div style="font-size:48px;opacity:.3">👤</div>
        <p class="muted">No identity selected</p>
        <button class="btn btn-primary btn-sm mt-md" id="settings-add-id">Add identity</button>
      </div>
    `}

    <div class="settings-group">
      <div class="settings-group-label">Account</div>
      <div class="settings-rows">
        <div class="settings-row" role="button" tabindex="0" id="row-switch-id">
          <span class="settings-row-icon">🔄</span>
          <span class="settings-row-label">Switch / Add identity</span>
          <span class="settings-row-arrow">›</span>
        </div>
        ${rec ? `
        <div class="settings-row" role="button" tabindex="0" id="row-profile">
          <span class="settings-row-icon">🪞</span>
          <span class="settings-row-label">Your profile</span>
          <span class="settings-row-value truncate">${esc(S.profile.doc?.display_name ?? (S.profile.loaded ? "Not set" : "…"))}</span>
          <span class="settings-row-arrow">›</span>
        </div>
        <div class="settings-row" role="button" tabindex="0" id="row-identity-keys">
          <span class="settings-row-icon">🪪</span>
          <span class="settings-row-label">Identity keys</span>
          <span class="settings-row-arrow">›</span>
        </div>` : ""}
      </div>
    </div>

    ${rec ? `
    <div class="settings-group">
      <div class="settings-group-label">Security</div>
      <div class="settings-rows">
        <div class="settings-row" role="button" tabindex="0" id="row-keys-devices">
          <span class="settings-row-icon">📱</span>
          <span class="settings-row-label">Keys &amp; devices</span>
          <span class="settings-row-arrow">›</span>
        </div>
        <div class="settings-row" role="button" tabindex="0" id="row-connected-apps">
          <span class="settings-row-icon">🔐</span>
          <span class="settings-row-label">Connected apps</span>
          <span class="settings-row-arrow">›</span>
        </div>
        <div class="settings-row" role="button" tabindex="0" id="row-auth-request">
          <span class="settings-row-icon">✅</span>
          <span class="settings-row-label">Approve sign-in request</span>
          <span class="settings-row-arrow">›</span>
        </div>
        <div class="settings-row" role="button" tabindex="0" id="row-recovery-kit">
          <span class="settings-row-icon">🧾</span>
          <span class="settings-row-label">Recovery kit</span>
          <span class="settings-row-value ${rec.seedDerived ? "val-ok" : "val-warn"}">
            ${rec.seedDerived ? "Available" : "Not available"}
          </span>
          <span class="settings-row-arrow">›</span>
        </div>
      </div>
    </div>` : ""}

    <div class="settings-group">
      <div class="settings-group-label">Inbox</div>
      <div class="settings-rows">
        <div class="settings-row" role="button" tabindex="0" id="row-analytics">
          <span class="settings-row-icon">📊</span><span class="settings-row-label">Relay analytics</span><span class="settings-row-arrow">›</span>
        </div>
        <div class="settings-row" role="button" tabindex="0" id="row-policy">
          <span class="settings-row-icon">🛡️</span>
          <span class="settings-row-label">Who can message you</span>
          <span class="settings-row-value">${esc(policySummary().mode)}</span>
          <span class="settings-row-arrow">›</span>
        </div>
        <div class="settings-row" role="button" tabindex="0" id="row-policy-anon">
          <span class="settings-row-icon">🎭</span>
          <span class="settings-row-label">Anonymous &amp; proof-of-work</span>
          <span class="settings-row-value ${policySummary().anonOn ? "val-ok" : "muted"}">${esc(policySummary().anon)}</span>
          <span class="settings-row-arrow">›</span>
        </div>
      </div>
    </div>

    <div class="settings-group">
      <div class="settings-group-label">Network</div>
      <div class="settings-rows">
        <div class="settings-row" role="button" tabindex="0" id="row-relay">
          <span class="settings-row-icon">🔗</span>
          <span class="settings-row-label">Relay URL</span>
          <span class="settings-row-value truncate">${esc(S.config.relayUrl)}</span>
          <span class="settings-row-arrow">›</span>
        </div>
        <div class="settings-row" role="button" tabindex="0" id="row-lookup">
          <span class="settings-row-icon">🔎</span>
          <span class="settings-row-label">Lookup identity</span>
          <span class="settings-row-arrow">›</span>
        </div>
      </div>
    </div>

    <div class="settings-group">
      <div class="settings-group-label">Advanced</div>
      <div class="settings-rows">
        <div class="settings-row${sess ? "" : " no-action"}" ${sess ? `role="button" tabindex="0"` : ""} id="row-session">
          <span class="settings-row-icon">🔑</span>
          <span class="settings-row-label">Session</span>
          <span class="settings-row-value ${sessOk ? "val-ok" : "val-warn"}">${sessOk ? "Active" : "None"}</span>
          ${sess ? `<span class="settings-row-arrow">›</span>` : ""}
        </div>
        ${usesDnsPath(rec, S.identity) ? `
        <div class="settings-row" role="button" tabindex="0" id="row-dns">
          <span class="settings-row-icon">🌐</span>
          <span class="settings-row-label">DNS provider</span>
          <span class="settings-row-value">${esc(S.config.dnsProvider || "Cloudflare")}</span>
          <span class="settings-row-arrow">›</span>
        </div>` : ""}
        ${rec ? `
        <div class="settings-row" role="button" tabindex="0" id="row-rotate-enc">
          <span class="settings-row-icon">🔄</span>
          <span class="settings-row-label">Rotate encryption key</span>
          <span class="settings-row-arrow">›</span>
        </div>
        <div class="settings-row" role="button" tabindex="0" id="row-remove-id">
          <span class="settings-row-icon">🗑️</span>
          <span class="settings-row-label" style="color:var(--red)">Remove this identity</span>
          <span class="settings-row-arrow">›</span>
        </div>` : ""}
      </div>
    </div>

    <div class="settings-group">
      <div class="settings-group-label">About</div>
      <div class="settings-rows">
        ${aboutRow("📋", "Protocol", "Poweur ID v1")}
        ${aboutRow("💻", "App", APP_VERSION, `${buildStamp(APP_BUILD_TIME)}${isShellRuntime() ? " · Mobile" : " · Web"}`, { value: "about-app-version", meta: "about-app-build" })}
        ${aboutRow("📦", "SDK", SDK_VERSION, buildStamp(SDK_BUILD_TIME), { value: "about-sdk-version", meta: "about-sdk-build" })}
        ${aboutRelayRow()}
      </div>
    </div>
    <div style="height:32px"></div>`;
}

function buildStamp(time, extra) {
  const bits = [time, extra].filter(Boolean);
  return bits.join(" · ");
}

function aboutRow(icon, label, value, meta = "", ids = {}) {
  const valueId = ids.value ? ` id="${esc(ids.value)}"` : "";
  const metaId = ids.meta ? ` id="${esc(ids.meta)}"` : "";
  return `
        <div class="settings-row no-action settings-row-about">
          <span class="settings-row-icon">${icon}</span>
          <div class="settings-row-stack">
            <div class="settings-row-stack-top">
              <span class="settings-row-label">${esc(label)}</span>
              <span class="settings-row-value"${valueId}>${esc(value)}</span>
            </div>
            ${meta || ids.meta ? `<span class="settings-row-meta"${metaId}>${esc(meta)}</span>` : ""}
          </div>
        </div>`;
}

function aboutRelayRow() {
  const identity = S.identity || modeNow().subject || "";
  const relayUrl = identity ? relayUrlFor(identity) : defaultRelayUrl();
  const meta = [identity, relayUrl].filter(Boolean).join(" · ") || "…";
  return aboutRow("🔗", "Relay", "…", meta, {
    value: "about-relay-version",
    meta: "about-relay-meta",
  });
}

/**
 * Fill the connected relay's advertised version after Settings renders.
 * The URL is the active identity's home relay (or the identity-host subject
 * on the web when that is all we have).
 */
function fillAboutRelay() {
  const versionEl = q("#about-relay-version");
  const metaEl = q("#about-relay-meta");
  if (!versionEl || !metaEl) return;
  const identity = S.identity || modeNow().subject || "";
  const relayUrl = identity ? relayUrlFor(identity) : defaultRelayUrl();
  if (!relayUrl) {
    versionEl.textContent = "—";
    metaEl.textContent = "Not connected";
    return;
  }
  identityApiFor(relayUrl).root().then(root => {
    if (!q("#about-relay-version")) return;
    versionEl.textContent = root.version || "unknown";
    metaEl.textContent = [
      identity,
      relayUrl,
      root.buildTime,
      root.versionHash && String(root.versionHash).slice(0, 7),
    ].filter(Boolean).join(" · ");
  }).catch(() => {
    if (!q("#about-relay-version")) return;
    versionEl.textContent = "—";
    metaEl.textContent = [identity, relayUrl, "unreachable"].filter(Boolean).join(" · ");
  });
}

/**
 * Does this identity have DNS credentials to configure? (E15-T10)
 *
 * Records written from now on say so outright. Older ones do not, so they fall
 * back to the question the flag answers: is this identity's domain one the
 * relay hosts? A hosted user has no token and never will.
 */
function usesDnsPath(record, identity) {
  if (typeof record?.hosted === "boolean") return !record.hosted;
  const domain = idDomain(identity);
  const hosted = modeNow().hostedDomains;
  return Boolean(domain) && hosted.length > 0 && !hosted.includes(domain);
}

// ─── Sub-pages ────────────────────────────────────────────────────────────────

function renderSubPage() {
  switch (R.sub) {
    case "add-id":  return renderAddId();
    case "unlock":  return renderUnlock();
    case "compose": return renderCompose();
    case "onboarding": return renderOnboarding();
    case "claim":   return renderClaim();
    case "auth":    return renderSignInApproval();
    default:        return renderAddId();
  }
}

function renderSignInApproval() {
  const A = S.auth;
  if (A.loading) return `
    <div class="sub-page"><div class="sub-header"><span class="sub-title">Sign in with Poweur ID</span></div>
    <div class="sub-body"><p role="status">Verifying the app’s origin…</p></div></div>`;
  if (A.result) {
    const redirect = A.request?.response_uri
      ? `${A.request.response_uri}${A.request.response_uri.includes("?") ? "&" : "?"}response=${encodeURIComponent(A.result.encoded)}`
      : "";
    return `
      <div class="sub-page"><div class="sub-header"><span class="sub-title">Approved</span></div>
      <div class="sub-body">
        <div class="empty-icon">✓</div>
        <h2>${esc(A.metadata?.name || A.request?.domain || "App")}</h2>
        <p class="muted">${A.result.delivered ? "The signed approval was delivered." : "Copy this one-time response back to the app."}</p>
        <textarea class="input" id="auth-response" readonly rows="5">${esc(A.result.encoded)}</textarea>
        <div class="stack mt-md">
          <button class="btn btn-secondary" id="btn-auth-copy">Copy response</button>
          ${redirect ? `<a class="btn btn-primary" href="${esc(redirect)}">Continue to app</a>` : ""}
        </div>
      </div></div>`;
  }
  if (!A.request) return `
    <div class="sub-page"><div class="sub-header"><button class="btn-back" id="btn-back">${svgBack}</button>
      <span class="sub-title">Approve sign-in</span></div>
      <div class="sub-body">
        <p class="muted small">Paste the code or <code>poweur://auth</code> link shown by the app.</p>
        <textarea class="input" id="auth-request-input" rows="7" placeholder="Paste sign-in request"></textarea>
        ${A.error ? `<p class="compose-status err">${esc(A.error)}</p>` : ""}
        <button class="btn btn-primary mt-md" id="btn-auth-load">Check request</button>
      </div></div>`;
  const unlocked = Boolean(getUnlockedKeys());
  return `
    <div class="sub-page"><div class="sub-header"><button class="btn-back" id="btn-back">${svgBack}</button>
      <span class="sub-title">Approve sign-in</span></div>
      <div class="sub-body">
        <div class="settings-id-card">
          <div class="settings-id-name">${esc(A.metadata.name)}</div>
          <div class="settings-id-domain">${esc(A.request.audience)}</div>
          <span class="chip chip-green">Origin verified</span>
        </div>
        <h2>${esc(A.headline)}</h2>
        ${A.request.statement ? `<p>${esc(A.request.statement)}</p>` : ""}
        ${A.scopes.length ? `<div class="settings-group"><div class="settings-group-label">Permissions</div>
          <ul>${A.scopes.map(scope => `<li>${esc(scope)}</li>`).join("")}</ul></div>` : `<p class="muted">This app requests sign-in only, with no home access.</p>`}
        <p class="form-note small">Signing as <strong>${esc(S.identity || "no identity selected")}</strong>.</p>
        ${A.error ? `<p class="compose-status err">${esc(A.error)}</p>` : ""}
        ${unlocked
          ? `<button class="btn btn-primary mt-md" id="btn-auth-approve">Approve</button>`
          : `<button class="btn btn-passkey mt-md" id="btn-auth-unlock">Unlock to approve</button>`}
      </div></div>`;
}

// Add ID ───────────────────────────────────────────────────────────────────────

/**
 * Add an identity to this device.
 *
 * What it may ask depends on which door it was opened from (E15-T9). On
 * `bob.poweur.net` the subject is in the URL bar, so the typed-identity field
 * would be an invitation to sign into someone else's name from Bob's page —
 * and creating a *different* identity belongs on the launcher, not here.
 * Passkeys are a browser authenticator, so the shell does not offer them.
 */
function renderAddId() {
  const info = modeNow();
  const opts = addIdOptions(info);
  const subject = opts.joinSubject;
  const asksWhichIdentity = !subject;
  const here = isShellRuntime() ? "device" : "browser";

  return `
    <div class="sub-page">
      <div class="sub-header">
        <button class="btn-back" id="btn-back">${svgBack}</button>
        <span class="sub-title">${subject ? "Sign in" : "Add identity"}</span>
      </div>
      <div class="sub-body">
        <p class="muted small" style="margin-bottom:20px">
          ${subject
            ? `Sign in to <strong>${esc(subject)}</strong> on this ${here}.`
            : opts.create
              ? "Connect or create a Poweur ID identity on this device."
              : "Sign in with the passkey this browser already has for your identity."}
        </p>
        ${renderRelayPrompt()}
        <div class="option-list">
          ${opts.passkey && asksWhichIdentity ? `
          <div class="option-card option-card-form-wrap">
            <div class="option-card-top">
              <div class="option-icon-wrap">🔑</div>
              <div class="option-body">
                <div class="option-title">Sign in with existing passkey</div>
                <div class="option-desc">Enter your identity to authenticate in this browser</div>
              </div>
            </div>
            <div class="option-inline-form">
              <input id="signin-id-input" class="input" type="text" placeholder="alice.poweur.net"
                autocapitalize="none" autocorrect="off"
                autocomplete="off" spellcheck="false" inputmode="url" />
              <button class="btn btn-primary" id="btn-signin-passkey">Sign in</button>
            </div>
          </div>` : ""}
          ${opts.passkey && !asksWhichIdentity ? `
          <button class="option-card" id="btn-door-signin">
            <div class="option-icon-wrap">🔑</div>
            <div class="option-body">
              <div class="option-title">Sign in with passkey</div>
              <div class="option-desc">Unlock ${esc(subject)} in this browser</div>
            </div>
            <span class="option-arrow">›</span>
          </button>` : ""}

          ${opts.join ? `
          <button class="option-card" id="opt-join-device"${subject ? ` data-join-identity="${esc(subject)}"` : ""}>
            <div class="option-icon-wrap">📱</div>
            <div class="option-body">
              <div class="option-title">Add this ${here} to an existing ID</div>
              <div class="option-desc">${subject
                ? `Show a code, approve it on a device you already use`
                : "Enter your ID, show a code, approve it on a device you already use"}</div>
            </div>
            <span class="option-arrow">›</span>
          </button>` : ""}

          ${opts.create ? `
          <button class="option-card" id="opt-create-new">
            <div class="option-icon-wrap">✨</div>
            <div class="option-body">
              <div class="option-title">Add new ID</div>
              <div class="option-desc">${isShellRuntime()
                ? "Create a fresh identity on this device"
                : "Create a fresh identity with a passkey"}</div>
            </div>
            <span class="option-arrow">›</span>
          </button>` : ""}
        </div>
      </div>
    </div>`;
}

/**
 * Ask which relay to talk to, when nothing else can answer.
 *
 * A relay-served SPA knows: it is *on* the relay. A shell is not on anything —
 * it runs from its own bundle — so it has to name one. Production is the
 * default; local and a typed URL are the other two choices. The picker stays
 * visible in the shell after a default is known, because switching relays is
 * why the shell exists.
 */
function renderRelayPrompt() {
  if (!isShellRuntime() && hasRelayUrl()) return "";
  const current = defaultRelayUrl();
  const preset = relayPresetFor(current);
  const customValue = preset === "custom" ? current : "";
  const localHost = localDevRelayUrl().replace(/^https?:\/\//, "");
  return `
    <div class="form-card relay-prompt">
      <div class="form-group" style="margin-bottom:0">
        <label class="form-label" for="setup-relay-preset">Relay</label>
        <p class="muted small" style="margin-bottom:6px">
          Where new identities are created. poweur.net is the hosted relay; switch to a local or your own for testing.
        </p>
        <select id="setup-relay-preset" class="input select">
          <option value="production"${preset === "production" ? " selected" : ""}>poweur.net</option>
          <option value="local"${preset === "local" ? " selected" : ""}>Local emulator (${esc(localHost)})</option>
          <option value="custom"${preset === "custom" ? " selected" : ""}>Other…</option>
        </select>
        <input id="setup-relay" class="input" type="url" inputmode="url" autocapitalize="none" autocorrect="off"
               placeholder="https://relay.example.com" autocomplete="off" spellcheck="false"
               value="${esc(customValue)}"${preset === "custom" ? "" : " hidden"} />
        <p class="idin-status small" id="setup-relay-status" role="status" aria-live="polite"></p>
      </div>
    </div>`;
}

function relayUrlForPreset(preset, typed) {
  if (preset === "local") return localDevRelayUrl();
  if (preset === "custom") {
    const raw = (typed ?? "").trim();
    if (!raw) return "";
    return (/^https?:\/\//i.test(raw) ? raw : `https://${raw}`).replace(/\/+$/, "");
  }
  return PRODUCTION_RELAY_URL;
}

/**
 * Save the chosen relay once it answers.
 *
 * Custom URLs are checked rather than taken on faith: a typo here would
 * otherwise surface as an unexplained failure three screens later. Switching
 * to a preset hits `/health` before saving; the first paint of the default
 * does not, so the landing cannot loop on its own seed.
 */
function attachRelayPrompt() {
  const presetEl = q("#setup-relay-preset");
  const input = q("#setup-relay");
  const status = q("#setup-relay-status");
  if (!presetEl || !status) return;

  const showCustom = () => {
    if (!input) return;
    input.hidden = presetEl.value !== "custom";
  };

  let timer = null;
  const apply = async ({ probe } = { probe: true }) => {
    showCustom();
    const url = relayUrlForPreset(presetEl.value, input?.value);
    if (!url) { status.textContent = ""; return; }
    if (!probe) {
      status.textContent = `Using ${new URL(url).host}`;
      status.className = "idin-status small val-ok";
      return;
    }
    status.textContent = "Checking…";
    status.className = "idin-status small";
    try {
      const health = await fetch(new URL("/health", url).toString(), {
        headers: { Accept: "application/json" },
      }).then(r => (r.ok ? r.json() : null));
      if (!health?.status) throw new Error("that does not look like a relay");
      saveConfig({ ...getConfig(), relayUrl: url });
      S.config = getConfig();
      status.textContent = `Connected to ${new URL(url).host}`;
      status.className = "idin-status small val-ok";
      // A shell has no host of its own, so the relay it was just given is the
      // only thing that can say what it may claim under (E15-T7).
      resolveMode({ force: true }).then(() => render());
    } catch (error) {
      status.textContent = `Could not reach ${url} — ${error.message}`;
      status.className = "idin-status small val-warn";
    }
  };

  presetEl.addEventListener("change", () => {
    clearTimeout(timer);
    if (presetEl.value === "custom") {
      showCustom();
      status.textContent = "";
      input?.focus();
      return;
    }
    apply({ probe: true });
  });
  input?.addEventListener("input", () => {
    clearTimeout(timer);
    timer = setTimeout(() => apply({ probe: true }), 600);
  });
  input?.addEventListener("keydown", (event) => {
    if (event.key !== "Enter") return;
    clearTimeout(timer);
    apply({ probe: true });
  });

  showCustom();
  // First paint of the default: do not re-probe and re-render, or the landing
  // loops. Switching away from it still hits /health before saving.
  if (presetEl.value !== "custom" && defaultRelayUrl() === relayUrlForPreset(presetEl.value)) {
    apply({ probe: false });
  } else if (presetEl.value === "custom" && input?.value.trim()) {
    apply({ probe: false });
  }
}

// Unlock ───────────────────────────────────────────────────────────────────────

function renderUnlock() {
  const id = S.identity;
  const rec = id ? loadIdentityRecord(id) : null;
  if (!id || !rec) { R.push("add-id"); return renderAddId(); }
  return `
    <div class="sub-page">
      <div class="sub-header">
        <button class="btn-back" id="btn-back">${svgBack}</button>
        <span class="sub-title">Unlock</span>
      </div>
      <div class="sub-body" style="display:flex;flex-direction:column;align-items:center;justify-content:center;min-height:60dvh;gap:16px;text-align:center">
        ${avatarHtml(id, "xl unlock-avatar")}
        <div>
          <div class="unlock-name">${esc(idHandle(id))}</div>
          <div class="unlock-sub muted">${esc(idDomain(id))}</div>
        </div>
        <button class="btn btn-passkey" id="btn-do-unlock" style="max-width:280px">
          🔓 ${CUSTODY_COPY[custodyOf(rec)].action}
        </button>
        <p class="form-note small" style="max-width:220px">
          ${CUSTODY_COPY[custodyOf(rec)].note}
        </p>
      </div>
    </div>`;
}

// Compose ─────────────────────────────────────────────────────────────────────

/** The live IdentityInput on the compose page, so doSend can read it. */
let composeInput = null;

function renderCompose() {
  const preset = R.params.to || "";
  return `
    <div class="sub-page">
      <div class="sub-header">
        <button class="btn-back" id="btn-back">${svgBack}</button>
        <span class="sub-title">New message</span>
      </div>
      <div class="sub-body compose-body">
        <div class="form-group">
          ${slot("c-to-slot", () => {
            composeInput = IdentityInput({
              resolve: resolveForComponents,
              contacts: S.contacts.list,
              value: preset,
              defaultDomain: idDomain(S.identity),
              label: "To",
              onSubmit: () => q("#c-body")?.focus(),
            });
            return composeInput.el;
          })}
        </div>
        <div class="form-group" style="flex:1">
          <label class="form-label" for="c-body">Message</label>
          <textarea id="c-body" class="compose-textarea" placeholder="Write your message…"></textarea>
        </div>
        <div class="form-group">
          <label class="form-label" for="c-attachment">Attachment (up to 20 MB)</label>
          <input id="c-attachment" class="input" type="file" />
        </div>
        <label class="policy-toggle compose-anon" for="c-anon">
          <input type="checkbox" id="c-anon" class="policy-check" />
          <span>
            <div class="policy-toggle-label">Send anonymously</div>
            <div class="policy-toggle-detail muted small">
              Still encrypted to them, but unsigned and unattributed — they will not know it is you and
              cannot reply. Their policy decides whether it costs you proof-of-work.
            </div>
          </span>
        </label>
        <label class="policy-toggle compose-anon" for="c-group">
          <input type="checkbox" id="c-group" class="policy-check"${R.params.group ? " checked" : ""} />
          <span>
            <div class="policy-toggle-label">Send to a group identity</div>
            <div class="policy-toggle-detail muted small">
              Encrypts a separate copy for every current member and sends the batch through the group’s relay.
            </div>
          </span>
        </label>
        <p id="c-status" class="compose-status"></p>
      </div>
      <div class="sub-footer">
        <button class="btn btn-primary" id="btn-send-msg">Send →</button>
      </div>
    </div>`;
}

// Onboarding ──────────────────────────────────────────────────────────────────

const ONBOARD_STEPS = 3;

/**
 * First run, three skippable steps.
 *
 * The one question worth interrupting for is the inbox policy: it is the only
 * setting whose default (anyone may message you) is a decision the user never
 * knowingly made, and nobody goes looking for it in Settings before the first
 * unwanted message arrives.
 */
function renderOnboarding() {
  const step = S.onboard?.step ?? 1;
  return `
    <div class="sub-page">
      <div class="sub-header">
        <span class="sub-title">Set up ${esc(idHandle(S.identity))}</span>
        <button class="btn-back onboard-skip" id="btn-onboard-skip"
                aria-label="Skip setup">Skip</button>
      </div>
      <div class="sub-body">
        <div class="onboard-progress" role="progressbar" aria-valuemin="1"
             aria-valuemax="${ONBOARD_STEPS}" aria-valuenow="${step}">
          ${Array.from({ length: ONBOARD_STEPS }, (_, i) =>
            `<span class="onboard-dot${i < step ? " done" : ""}"></span>`).join("")}
        </div>
        ${step === 1 ? `
          <h2 class="onboard-title">Who can message you?</h2>
          <p class="muted small">You can change this any time in Settings.</p>
          <div id="onboard-policy"></div>` : ""}
        ${step === 2 ? `
          <h2 class="onboard-title">How should people see you?</h2>
          <p class="muted small">Optional, and public — this is what anyone who looks you up reads.</p>
          <div id="onboard-profile"></div>` : ""}
        ${step === 3 ? `
          <div class="onboard-done">
            <div class="empty-state-icon">🎉</div>
            <h2 class="onboard-title">You're set</h2>
            <p class="muted">
              ${esc(S.identity)} can send and receive encrypted messages, keep files, and
              share them. Add someone from Contacts to get started.
            </p>
          </div>` : ""}
      </div>
      <div class="sub-footer onboard-footer">
        ${step > 1 ? `<button class="btn" id="btn-onboard-back">Back</button>` : ""}
        <button class="btn btn-primary" id="btn-onboard-next">
          ${step === ONBOARD_STEPS ? "Start using Poweur" : "Continue"}
        </button>
      </div>
    </div>`;
}

/** The onboarding steps mount live components, so wiring lives with them. */
function attachOnboardingEvents() {
  const step = S.onboard?.step ?? 1;
  const client = clientFor(S.identity);

  q("#btn-onboard-skip")?.addEventListener("click", finishOnboarding);
  q("#btn-onboard-back")?.addEventListener("click", () => {
    S.onboard.step = Math.max(1, step - 1);
    render();
  });
  q("#btn-onboard-next")?.addEventListener("click", async () => {
    if (step >= ONBOARD_STEPS) return finishOnboarding();
    // Continue saves this step's answers, then moves on. A step whose save
    // fails stays put with the error next to the field, rather than silently
    // advancing past a choice that was never written.
    if (!(await (S.onboard.pending?.() ?? Promise.resolve(true)))) return;
    S.onboard.step = step + 1;
    render();
  });

  S.onboard.pending = null;

  if (step === 1 && client) {
    const host = q("#onboard-policy");
    if (!host) return;
    // Recommended, not imposed: the flow opens on the mode the docs call the
    // human default, and Skip leaves the relay's default in place.
    const controls = PolicyControls({
      policy: S.policy.doc ?? { version: 1, mode: "contacts_and_requests" },
      explicit: S.policy.explicit,
      showSave: false,
      onSave: async (document) => {
        await client.setPolicy(document.mode, document.anonymous, document.read_receipts);
        await loadPolicy({ force: true });
      },
    });
    host.replaceChildren(controls.el);
    S.onboard.pending = controls.save;
  }

  if (step === 2 && client) {
    const host = q("#onboard-profile");
    if (!host) return;
    const editor = ProfileEditor({ profile: S.profile.doc, showSave: false });
    host.replaceChildren(editor);
    // Nothing typed is nothing to write — an empty profile document would say
    // "this person set a profile" when they skipped past it.
    S.onboard.pending = async () => {
      const typed = q("#pe-name")?.value.trim() || q("#pe-bio")?.value.trim()
        || q("#pe-avatar")?.files?.length;
      return typed ? editor.save() : true;
    };
  }
}

function finishOnboarding() {
  S.onboard = null;
  R.sub = null;
  R.page = "messages";
  render();
}

// ─── Files destination ────────────────────────────────────────────────────────

/** A path a grant may cover: under a shareable root, and not the root itself. */
function isShareablePath(path) {
  const top = String(path ?? "").split("/")[0];
  return SHARE_ROOTS.includes(top) && path.includes("/");
}

/** Grants covering exactly this path (what the "Shared" badge reports). */
function grantsForPath(path) {
  return S.files.grants.filter(g => g.path === path && !grantExpired(g));
}

function describeAudience(grant) {
  return grant.audience
    .map(entry => entry.id || `group:${entry.group}`)
    .join(", ");
}

function renderFilesDestination() {
  const F = S.files;
  const crumbs = F.path ? F.path.split("/") : [];
  const atRoot = !F.path;
  const visiting = Boolean(F.owner);
  const picking = F.picking && !visiting;
  const quota = F.quota;
  const quotaPct = quota?.quota_bytes > 0
    ? Math.min(100, Math.round((quota.used_bytes / quota.quota_bytes) * 100))
    : 0;

  return `
    <div class="dest-header">
      <h1 class="dest-title">Files</h1>
      ${atRoot && !visiting ? `
        <button class="btn btn-sm" id="btn-shares" title="Shares you have made">🔗</button>` : ""}
      ${atRoot ? "" : `
        ${visiting ? "" : `
          <button class="btn btn-sm" id="btn-new-folder" title="New folder" aria-label="New folder">📁+</button>`}
        <label class="btn btn-sm" for="ff-upload" style="cursor:pointer" title="Upload">⬆️
          <input id="ff-upload" type="file" multiple style="display:none" />
        </label>`}
    </div>

    <div class="dest-toolbar file-sources" role="tablist" aria-label="Which files">
      <button class="tray-tab${visiting ? "" : " active"}" id="btn-files-mine"
              role="tab" aria-selected="${!visiting}">My files</button>
      <button class="tray-tab${visiting || picking ? " active" : ""}" id="btn-files-shared"
              role="tab" aria-selected="${visiting || picking}">Shared with me</button>
    </div>

    ${picking ? renderOwnerPicker() : ""}

    ${visiting ? `
      <div class="visitor-banner small">
        Browsing <strong>${esc(F.owner)}</strong>. You see only what they granted you;
        writing works where they allowed it.
        <button class="link-btn" id="btn-leave-owner">Leave</button>
      </div>` : ""}

    ${picking ? "" : `
    ${quota && !visiting ? `
      <div class="quota-bar">
        <div class="small muted quota-line">
          <span>${formatBytes(quota.used_bytes)}${quota.quota_bytes > 0 ? ` of ${formatBytes(quota.quota_bytes)}` : ""} used</span>
          <span>${esc(quota.provider ?? "")}</span>
        </div>
        ${quota.quota_bytes > 0 ? `
          <div class="quota-track" role="progressbar" aria-valuenow="${quotaPct}" aria-valuemin="0" aria-valuemax="100">
            <div class="quota-fill" style="width:${quotaPct}%;background:${quotaPct > 90 ? "var(--red)" : "var(--green)"}"></div>
          </div>` : ""}
      </div>` : ""}

    <nav class="breadcrumbs small" aria-label="Folder path">
      <button class="link-btn" data-nav-path="" style="font-weight:600">home</button>
      ${crumbs.map((c, i) => `
        <span class="muted">/</span>
        <button class="link-btn" data-nav-path="${esc(crumbs.slice(0, i + 1).join("/"))}">${esc(c)}</button>`).join("")}
    </nav>

    ${F.loading ? `<p class="muted small" style="padding:16px">Loading…</p>` : `
      ${F.entries.length === 0 && visiting
        ? emptyState("🔒", "Nothing shared here",
            `${F.owner} has not granted you anything under this folder, or the grant was revoked.`)
        : F.entries.length === 0
        ? emptyState("📂", atRoot ? "No roots yet" : "Empty folder",
            atRoot ? "Your storage roots appear once the relay provisions them."
                   : "Upload a file or create a folder to get started.")
        : `<div class="conv-list">
            ${F.entries.map(e => {
              const rootInfo = atRoot ? ROOT_INFO[e.name] : null;
              return `
              <div class="conv-row" style="align-items:center">
                <div class="file-icon">${e.dir ? "📁" : "📄"}</div>
                <div class="conv-info" ${e.dir ? `data-open-dir="${esc(e.path)}"` : `data-download="${esc(e.path)}"`}
                     role="button" tabindex="0" style="cursor:pointer">
                  <div class="conv-name">${esc(e.name)}
                    ${rootInfo ? `<span class="chip" style="margin-left:6px">${esc(rootInfo.badge)}</span>` : ""}
                    ${grantsForPath(e.path).length
                      ? `<span class="chip chip-accent" style="margin-left:6px">Shared</span>` : ""}
                  </div>
                  <div class="conv-preview">${rootInfo ? esc(rootInfo.desc) : e.dir ? "folder" : formatBytes(e.size)}</div>
                </div>
                ${atRoot || visiting ? "" : `
                  ${isShareablePath(e.path) ? `
                    <button class="btn btn-sm" data-share="${esc(e.path)}"
                            aria-label="Share ${esc(e.name)}">🔗</button>` : ""}
                  <button class="btn btn-sm" data-rename="${esc(e.path)}" aria-label="Rename ${esc(e.name)}">✏️</button>
                  <button class="btn btn-sm" data-delete="${esc(e.path)}" aria-label="Delete ${esc(e.name)}">🗑</button>`}
              </div>`;
            }).join("")}
          </div>`}`}`}`;
}

/**
 * Whose shared files to open.
 *
 * There is no "shares granted to me" endpoint — grants live in the *owner's*
 * tree and only they can list them (EPIC-005's offer/accept flow is what will
 * change that). So the visitor names the owner, exactly as the CLI does, and
 * the relay's grant engine decides what they can see: a visitor may traverse
 * the ancestors of anything granted to them, and listings filter out
 * everything else, so an owner who shared nothing simply looks empty.
 */
function renderOwnerPicker() {
  const contacts = S.contacts.list.filter(c => c.state === "accepted");
  return `
    <div class="owner-picker">
      <p class="muted small">
        Open someone's tree to see what they have shared with you.
      </p>
      ${slot("owner-picker-input", () => IdentityInput({
        resolve: resolveForComponents,
        contacts: S.contacts.list,
        label: "Whose files?",
        preview: false,
        onSubmit: (identity) => openOwnerTree(identity),
      }).el)}
      ${contacts.length ? `
        <div class="section-label">Contacts</div>
        <div class="conv-list">
          ${contacts.map(c => `
            <div class="conv-row" data-open-owner="${esc(c.identity)}" role="button" tabindex="0">
              ${avatarHtml(c.identity, "md")}
              <div class="conv-info">
                <div class="conv-name">${esc(c.petname || idHandle(c.identity))}</div>
                <div class="conv-preview">${esc(c.identity)}</div>
              </div>
            </div>`).join("")}
        </div>` : `<p class="muted small">No contacts yet — type an identity above.</p>`}
    </div>`;
}

// ─── Event wiring ─────────────────────────────────────────────────────────────

function attachEvents() {
  q("#btn-theme")?.addEventListener("click", toggleTheme);

  // A div with role="button" is a button to a screen reader and a dead end to
  // a keyboard unless it answers Enter and Space itself.
  qAll('[role="button"][tabindex="0"]').forEach(row => row.addEventListener("keydown", event => {
    if (event.key !== "Enter" && event.key !== " ") return;
    event.preventDefault();
    row.click();
  }));

  // Identity pill / dropdown
  q("#id-pill")?.addEventListener("click", e => {
    e.stopPropagation();
    S.dropdownOpen = !S.dropdownOpen;
    render();
  });
  q("#btn-add-id-header")?.addEventListener("click", () => { S.dropdownOpen = false; R.push("add-id"); });
  q("#dd-add-id")?.addEventListener("click", () => { S.dropdownOpen = false; R.push("add-id"); });

  // Close dropdown on outside click
  if (S.dropdownOpen) {
    setTimeout(() => document.addEventListener("click", closeDropdownOnce, { once: true }), 0);
  }

  // Switch identity from dropdown
  qAll("[data-switch]").forEach(btn => btn.addEventListener("click", () => {
    S.dropdownOpen = false;
    const id = btn.dataset.switch;
    if (id === S.identity) { render(); return; }
    switchIdentity(id);
    R.push("unlock");
  }));

  // Bottom nav
  qAll(".nav-tab[data-page]").forEach(t => t.addEventListener("click", () => {
    S.dropdownOpen = false;
    R.go(t.dataset.page);
  }));

  // Message trays
  qAll(".tray-tab[data-tray]").forEach(t => t.addEventListener("click", () => {
    S.tray = t.dataset.tray;
    render();
  }));

  // Contacts
  q("#btn-add-contact")?.addEventListener("click", () => showAddContactPanel());
  q("#btn-add-contact-empty")?.addEventListener("click", () => showAddContactPanel());
  qAll("[data-contact-menu]").forEach(b => b.addEventListener("click", e => {
    e.stopPropagation();
    showContactPanel(b.dataset.contactMenu);
  }));
  qAll("[data-contact-open]").forEach(row => row.addEventListener("click", () => {
    // Messaging a blocked contact is not the action they meant.
    if (row.dataset.contactState === "blocked") showContactPanel(row.dataset.contactOpen);
    else R.push("compose", { to: row.dataset.contactOpen });
  }));
  qAll("[data-accept-contact]").forEach(b => b.addEventListener("click", e => {
    e.stopPropagation();
    doAcceptContact(b.dataset.acceptContact);
  }));
  qAll("[data-block-contact]").forEach(b => b.addEventListener("click", e => {
    e.stopPropagation();
    doBlockContact(b.dataset.blockContact);
  }));
  qAll("[data-cancel-request]").forEach(b => b.addEventListener("click", e => {
    e.stopPropagation();
    doRemoveContact(b.dataset.cancelRequest);
  }));
  qAll("[data-add-contact]").forEach(b => b.addEventListener("click", e => {
    e.stopPropagation();
    doRequestContact(b.dataset.addContact);
  }));
  const contactsFilter = q("#contacts-filter");
  if (contactsFilter) {
    contactsFilter.addEventListener("input", () => {
      S.contacts.filter = contactsFilter.value;
      const caret = contactsFilter.selectionStart;
      render();
      const next = q("#contacts-filter");
      next?.focus();
      next?.setSelectionRange(caret, caret);
    });
  }
  if (!R.sub && getUnlockedKeys() && (R.page === "contacts" || R.page === "messages")) {
    // Messages needs contacts too: the requests tray filters on contact state
    // and the inbox marks strangers.
    loadContacts();
  }

  // Back button (sub-pages)
  q("#btn-back")?.addEventListener("click", () => {
    R.sub = null; R.params = {}; render();
  });

  // Welcome
  q("#btn-welcome-start")?.addEventListener("click", () => R.push("add-id"));

  // Main unlock banner
  q("#btn-unlock-main")?.addEventListener("click", () => R.push("unlock"));

  // Compose, from the thumb-reach button on a phone or the header on a wide
  // screen — one of the two is hidden by CSS, never both (E15-T11).
  q("#btn-compose")?.addEventListener("click", () => R.push("compose"));
  q("#btn-compose-top")?.addEventListener("click", () => R.push("compose"));

  // Conversation row → compose to that contact. Opening it is reading it, so
  // the badge clears here rather than waiting for a reply to be sent.
  qAll(".conv-row[data-compose-to]").forEach(r =>
    r.addEventListener("click", () => {
      const peer = r.dataset.composeTo;
      markConversationRead(peer).catch(() => {});
      // Replying from a threaded row stays in that thread; from the default
      // row it starts nothing, which is what an unthreaded reply has always
      // been.
      R.push("compose", { to: peer, thread: r.dataset.thread || "", group: r.dataset.group === "true" });
    }));
  qAll("[data-download-attachment]").forEach(button => button.addEventListener("click", async event => {
    event.stopPropagation();
    const client = clientFor(S.identity);
    if (!client) return;
    try {
      const metadata = JSON.parse(button.dataset.downloadAttachment);
      const { ref, bytes } = await client.downloadAttachment(metadata);
      const url = URL.createObjectURL(new Blob([bytes], { type: ref.mime }));
      if (ref.mime.startsWith("image/")) window.open(url, "_blank", "noopener");
      else {
        const anchor = document.createElement("a");
        anchor.href = url;
        anchor.download = ref.name;
        anchor.click();
      }
      setTimeout(() => URL.revokeObjectURL(url), 60_000);
    } catch (error) {
      toast(`Attachment failed: ${error.message}`, "error");
    }
  }));

  // A push stream costs one connection and saves every poll after it.
  if (getUnlockedKeys()) startEventStream();

  // Load inbox when unlocked and on the Messages destination
  if (R.page === "messages" && !R.sub && getUnlockedKeys()) {
    // The archive first: after a reload it is the only place the messages
    // still exist, and it also carries the read marks the badges count from.
    loadHistory();
    loadInbox();
    // Always, not only on the Requests tray: an accept to a request *we* sent
    // arrives here, and the handshake only completes once we have read it.
    loadRequests();
    // The anonymous queue is drained here too, so its badge is honest before
    // the tray is opened — but only for the few who turned anonymous on, so
    // everyone else pays nothing for a queue that is always empty.
    loadPolicy();
    if (S.tray === "anonymous" || S.policy.doc?.anonymous?.allow) loadAnon();
    // Looking at the anonymous tray is reading it: there is nothing to open,
    // so the tray itself is the conversation.
    if (S.tray === "anonymous" && unreadAnonymous() > 0) {
      markConversationRead("anonymous").catch(() => {});
    }
  }
  q("#btn-anon-settings")?.addEventListener("click", () => R.go("settings"));

  // Add ID options
  attachRelayPrompt();
  q("#opt-join-device")?.addEventListener("click", (event) => {
    showJoinDevicePanel(event.currentTarget.dataset.joinIdentity ?? "");
  });
  q("#btn-signin-passkey")?.addEventListener("click", () => doSignInWithPasskey());
  q("#signin-id-input")?.addEventListener("keydown", e => { if (e.key === "Enter") doSignInWithPasskey(); });
  q("#opt-create-new")?.addEventListener("click", () => R.push("claim"));

  // Front doors (E15-T7/T8/T9)
  q("#opt-have-id")?.addEventListener("click", () => R.push("add-id"));
  q("#opt-own-domain")?.addEventListener("click", () => R.push("claim", { dns: true }));
  q("#btn-door-signin")?.addEventListener("click", () => doSignInWithPasskey(modeNow().subject));
  q("#btn-door-claim")?.addEventListener("click", doClaimThisHost);
  q("#btn-door-retry")?.addEventListener("click", () => {
    S.door = { ...S.door, state: "idle" };
    resolveMode({ force: true }).then(() => { render(); });
  });
  // The identity host has to know whether its own name is taken before it can
  // show either half of its door.
  probeDoor();

  // Anything that draws from the mode repaints once the relay answers. The
  // guard is `probed`, not `resolved`: an unreachable relay never resolves,
  // and looping on that would spin the page.
  if (!modeNow().probed) resolveMode().then(() => render());

  // One claim button, wherever the field is
  if (q("#claim-card") && !S.passkey) {
    checkPasskeySupport().then((support) => { S.passkey = support; render(); });
  }
  if (q("#claim-card") && !S.custody) {
    chooseCustody().then((custody) => { S.custody = custody; render(); });
  }
  q("#btn-claim")?.addEventListener("click", doClaim);
  q("#ni-handle")?.addEventListener("keydown", e => { if (e.key === "Enter") doClaim(); });
  attachAvailabilityCheck();

  // Unlock sub-page
  q("#btn-do-unlock")?.addEventListener("click", doUnlock);

  q("#btn-auth-load")?.addEventListener("click", () => beginSignInApproval(q("#auth-request-input")?.value));
  q("#btn-auth-approve")?.addEventListener("click", doApproveSignIn);
  q("#btn-auth-unlock")?.addEventListener("click", () => R.push("unlock", { returnTo: "auth" }));
  q("#btn-auth-copy")?.addEventListener("click", async () => {
    const value = q("#auth-response")?.value || S.auth.result?.encoded || "";
    await navigator.clipboard?.writeText(value);
    toast("Response copied", "success");
  });

  // Compose send
  q("#btn-send-msg")?.addEventListener("click", doSend);

  // First-run flow
  if (R.sub === "onboarding") attachOnboardingEvents();

  // Files
  if (R.page === "files" && !R.sub && getUnlockedKeys()) {
    if (!S.files.loaded) {
      S.files.loaded = true;
      loadFiles(S.files.path);
    }
    loadContacts();      // the owner picker and the share dialog both need them
    pollChanges();       // no-op if a loop is already running
  }
  q("#btn-files-mine")?.addEventListener("click", () => {
    if (!S.files.owner && !S.files.picking) return;
    setFilesOwner(null);
    S.files.picking = false;
    loadFiles("");
  });
  q("#btn-files-shared")?.addEventListener("click", () => {
    if (S.files.owner) return;
    S.files.picking = true;
    render();
  });
  q("#btn-leave-owner")?.addEventListener("click", () => {
    setFilesOwner(null);
    S.files.picking = true;
    render();
  });
  qAll("[data-open-owner]").forEach(row =>
    row.addEventListener("click", () => openOwnerTree(row.dataset.openOwner)));
  q("#btn-shares")?.addEventListener("click", showSharesPanel);
  qAll("[data-share]").forEach(b => b.addEventListener("click", e => {
    e.stopPropagation();
    showSharePanel(b.dataset.share);
  }));
  qAll("[data-nav-path]").forEach(b => b.addEventListener("click", () => loadFiles(b.dataset.navPath)));
  qAll("[data-open-dir]").forEach(b => b.addEventListener("click", () => loadFiles(b.dataset.openDir)));
  qAll("[data-download]").forEach(b => b.addEventListener("click", () => doDownloadEntry(b.dataset.download)));
  qAll("[data-rename]").forEach(b => b.addEventListener("click", e => { e.stopPropagation(); doRenameEntry(b.dataset.rename); }));
  qAll("[data-delete]").forEach(b => b.addEventListener("click", e => { e.stopPropagation(); doDeleteEntry(b.dataset.delete); }));
  q("#btn-new-folder")?.addEventListener("click", doNewFolder);
  q("#ff-upload")?.addEventListener("change", e => doUploadFiles(e.target.files));

  // Settings rows
  q("#settings-add-id")?.addEventListener("click", () => R.push("add-id"));
  q("#row-switch-id")?.addEventListener("click", () => R.push("add-id"));
  fillAboutRelay();
  q("#row-identity-keys")?.addEventListener("click", showIdentityKeysPanel);
  q("#row-relay")?.addEventListener("click", showRelayPanel);
  q("#row-lookup")?.addEventListener("click", showLookupPanel);
  q("#row-session")?.addEventListener("click", showSessionPanel);
  q("#row-dns")?.addEventListener("click", showDnsPanel);
  q("#row-profile")?.addEventListener("click", showProfilePanel);
  q("#row-analytics")?.addEventListener("click", showAnalyticsPanel);
  q("#row-policy")?.addEventListener("click", showPolicyPanel);
  q("#row-policy-anon")?.addEventListener("click", showPolicyPanel);
  q("#row-keys-devices")?.addEventListener("click", showKeysAndDevicesPanel);
  q("#row-connected-apps")?.addEventListener("click", showConnectedAppsPanel);
  q("#row-auth-request")?.addEventListener("click", () => {
    S.auth = { input: "", request: null, metadata: null, headline: "", scopes: [], loading: false, error: "", result: null };
    R.push("auth");
  });
  if (R.page === "settings" && !R.sub && getUnlockedKeys()) {
    loadPolicy();
    loadProfile();
  }
  q("#row-recovery-kit")?.addEventListener("click", showRecoveryKitPanel);
  q("#row-rotate-enc")?.addEventListener("click", doRotateEncKey);
  q("#row-remove-id")?.addEventListener("click", doRemoveIdentity);
}

/**
 * Make `identity` active and drop everything scoped to the previous one.
 *
 * State is keyed by identity (E15-T1) precisely so this is a reset rather than
 * a merge: messages, contacts, the DAV token and the profile cache all belong
 * to whoever was signed in.
 */
function switchIdentity(identity) {
  stopEventStream();
  clearUnlockedKeys();
  clearProfileCache();
  S.identity = identity;
  setActiveIdentity(identity);
  S.messages = [];
  S.acks = [];
  S.contacts = { list: [], loading: false, loaded: false, error: null, filter: "" };
  S.history = { loading: false, loaded: false, error: null, readState: { conversations: {} } };
  S.requests = { incoming: [], loading: false, loaded: false, error: null, fetchedAt: 0 };
  S.anon = { messages: [], loading: false, loaded: false, error: null, fetchedAt: 0 };
  S.policy = { doc: null, explicit: false, loading: false, loaded: false };
  S.profile = { doc: null, explicit: false, loaded: false, loading: false };
  S.files = {
    dav: null, davExp: 0, path: "", entries: [], quota: null, loading: false, loaded: false,
    owner: null, picking: false, grants: [], grantsLoaded: false, cursor: "", polling: false,
  };
}

function closeDropdownOnce() {
  S.dropdownOpen = false;
  render();
}

const q = sel => document.querySelector(sel);
const qAll = sel => document.querySelectorAll(sel);

// ─── Actions ──────────────────────────────────────────────────────────────────

/**
 * @param {string} [identity] the subject, when the host already named it
 *        (E15-T9). Falls back to the typed field, which survives only where
 *        the identity genuinely is not knowable — a shell, an unknown host.
 */
async function doSignInWithPasskey(identity) {
  const fqdn = (identity || q("#signin-id-input")?.value || "").trim().toLowerCase();
  if (!fqdn) return toast("Enter your identity (e.g. alice.poweur.net)", "warning");

  if (!loadIdentityRecord(fqdn)) {
    // Nothing stored here — but the relay may hold a copy this browser's
    // passkey can open. That is the "I cleared site data" path, and it is the
    // whole point of the keystore (EPIC-011 E11-T1).
    return doRecoverFromKeystore(fqdn);
  }

  switchIdentity(fqdn);
  R.push("unlock");
}

async function doUnlock() {
  const id = S.identity;
  const rec = loadIdentityRecord(id);
  if (!rec) { toast("Identity record not found", "error"); return; }

  setLoading(true, "Authenticating…");
  try {
    let opened;
    if (rec.encryptedKeys?.kdf === "native") {
      // The prompt is the platform's own, so the overlay would sit on top of
      // it saying "Authenticating…" about a dialog the user is already reading.
      setLoading(false);
      opened = await unwrapKeysNative(id, rec.encryptedKeys, {
        reason: `Unlock ${id.split(".")[0]}`,
      });
      setLoading(true, "Unlocking…");
    } else if (rec.encryptedKeys?.kdf === "prf") {
      const { prfOutput } = await authenticatePasskey(rec.credentialId, { rpId: rpIdFor(S.identity) });
      if (!prfOutput) throw new Error(PRF_UNAVAILABLE_MESSAGE);
      opened = await unwrapKeysWithPRF(prfOutput, rec.encryptedKeys);
    } else {
      throw new Error(PRF_UNAVAILABLE_MESSAGE);
    }

    setUnlockedKeys(id, opened.signingJWK, opened.encJWK, opened.seed ?? null);

    if (!sessionIsValid(loadSessionRecord(id))) {
      setLoading(true, "Creating session…");
      await ensureSession(id);
    }

    setLoading(false);
    toast("Unlocked", "success");
    const returnTo = R.params?.returnTo;
    R.sub = returnTo || null; R.params = {};
    render(); // attachEvents starts the inbox fetch

  } catch (err) {
    setLoading(false);
    toast(err.message, "error");
  }
}

async function beginSignInApproval(input) {
  S.auth = { ...S.auth, input: String(input ?? "").trim(), loading: true, error: "", result: null };
  R.sub = "auth";
  render();
  try {
    const consent = await loadSignInConsent(S.auth.input);
    S.auth = { ...S.auth, ...consent, loading: false, error: "" };
  } catch (error) {
    S.auth = { ...S.auth, request: null, metadata: null, loading: false, error: error.message };
  }
  render();
}

async function doApproveSignIn() {
  const client = clientFor(S.identity);
  if (!client || !S.auth.request || !S.auth.metadata) {
    return toast("Unlock your identity before approving", "warning");
  }
  S.auth = { ...S.auth, loading: true, error: "" };
  render();
  try {
    const signed = await signBrowserApproval(S.auth.request, S.identity, client.signer);
    const dav = await client.dav();
    await appendBrowserConsent(dav, signed.response, S.auth.metadata);
    const delivered = await deliverBrowserApproval(S.auth.request, signed.encoded);
    S.auth = { ...S.auth, loading: false, result: { ...signed, delivered } };
  } catch (error) {
    S.auth = { ...S.auth, loading: false, error: error.message };
  }
  render();
}

/**
 * Restore an identity onto a browser that holds nothing for it.
 *
 * Authorized by a WebAuthn assertion alone: there is no identity key here to
 * sign with, which is exactly the circularity the keystore endpoint breaks.
 */
async function doRecoverFromKeystore(identity) {
  const relayUrl = defaultRelayUrl();
  setLoading(true, "Looking for a stored copy…");
  let recovered;
  try {
    recovered = await recoverFromKeystore(identity, { relayUrl });
  } catch (error) {
    setLoading(false);
    toast(`Could not restore ${identity}: ${error.message}`, "error", 9000);
    return;
  }

  try {
    restoreLocalRecord(identity, recovered, { relayUrl });
    switchIdentity(identity);
    setUnlockedKeys(identity, recovered.signingJWK, recovered.encJWK, recovered.seed);
    S.config = getConfig();

    setLoading(true, "Creating session…");
    await ensureSession(identity);

    setLoading(false);
    toast(`${identity} restored on this device`, "success", 5000);
    R.sub = null; R.page = "messages"; R.params = {};
    render();
  } catch (error) {
    setLoading(false);
    toast(error.message, "error", 9000);
  }
}

/**
 * How this device holds a record's keys, for the copy that tells the user what
 * to expect when they unlock. Three custodies, three different prompts — and
 * telling someone to expect a passkey when the phone is about to draw a Face ID
 * sheet for its own keystore is exactly the kind of small lie that makes people
 * distrust the whole screen.
 */
function custodyOf(rec) {
  if (rec?.encryptedKeys?.kdf === "native") return "native";
  return "prf";
}

const CUSTODY_COPY = {
  native: {
    action: "Unlock",
    note: "Your device will ask for Face ID, Touch ID or your passcode",
    chip: "🛡️ Device keystore",
    chipClass: "chip-green",
  },
  prf: {
    action: "Unlock with passkey",
    note: "Touch ID, Face ID, or Windows Hello",
    chip: "🔑 Passkey (PRF)",
    chipClass: "chip-green",
  },
};

/**
 * Why the keys are about to be protected by something weaker than this device
 * could manage. Naming the actual obstacle is the difference between advice a
 * user can act on ("set a passcode") and a shrug ("no passkey here").
 */
/**
 * Which custody this device should use for a *new* record (EPIC-019 E19-T2).
 *
 * The order is not a preference, it is a ranking by what an attacker has to
 * defeat. A hardware keystore gated on biometrics beats a passkey because it
 * beats it on both ends: the secret is held by the secure element rather than
 * by the browser profile, and it carries no origin model — no `rp.id`, no
 * relying party, no associated domain — which is exactly what lets one shell
 * build work against any relay on any domain. A passkey with PRF comes next.
 * Authenticators without PRF are refused; a PIN is not a wrapping secret.
 *
 * A device with no enrolled biometrics is an ordinary device: the caller
 * tries a PRF passkey, and says so if that is missing too.
 */
async function chooseCustody() {
  if (!hasNativeKeystore()) return { kind: "passkey", reason: "no_native_keystore" };
  const biometrics = await biometricAvailability();
  return biometrics.available
    ? { kind: "native", gate: GATE_BIOMETRIC, biometryKind: biometrics.kind ?? null }
    : { kind: "passkey", reason: biometrics.reason ?? "unavailable" };
}

/**
 * Take ownership of key material this browser did not generate — a new
 * device joining via the enrollment ceremony, or a recovery-kit restore.
 *
 * Creates a local passkey to wrap it and registers a *new* enrollment. The
 * cleared-site-data path does not come here: that authenticator already
 * exists and `restoreLocalRecord` reuses it.
 */
async function adoptIdentity({ identity, relayUrl, signingJWK, encJWK, seed, label }) {
  const custody = await chooseCustody();
  if (custody.kind !== "native") {
    const support = await checkPasskeySupport();
    if (!support.available || support.prf === false) {
      throw new Error(support.reason || PRF_UNAVAILABLE_MESSAGE);
    }
  }

  const userId = toBase64url(crypto.getRandomValues(new Uint8Array(16)));
  let credentialId = "", prfOutput = null, supportsPRF = false;
  let credentialPublicKey = null, credentialAlg = null, credentialScope = null;
  let encryptedKeys;
  if (custody.kind === "native") {
    setLoading(false);
    encryptedKeys = await wrapKeysNative(identity, signingJWK, encJWK, seed, {
      gate: custody.gate,
      reason: `Protect ${identity.split(".")[0]}`,
    });
    setLoading(true, "Securing keys…");
  } else {
    setLoading(true, "Creating a passkey on this device…");
    ({ credentialId, prfOutput, supportsPRF, credentialPublicKey, credentialAlg, rpId: credentialScope } =
      await createPasskey(identity, userId));
    setLoading(true, "Securing keys…");
    encryptedKeys = await rewrap({ prfOutput }, { signingJWK, encJWK, seed });
  }

  saveIdentityRecord(identity, {
    identity,
    publicKey: publicKeyFromJwk(signingJWK),
    encPublicKey: publicKeyFromJwk(encJWK),
    credentialId, credentialPublicKey, credentialAlg, rpId: credentialScope,
    encryptedKeys,
    relay: relayUrl,
    userId,
    createdAt: new Date().toISOString(),
    supportsPRF,
    seedDerived: Boolean(seed),
    // A fresh passkey is a fresh enrollment; reusing the restored one would
    // overwrite a copy another authenticator still needs.
    enrollmentId: null,
  });

  switchIdentity(identity);
  setUnlockedKeys(identity, signingJWK, encJWK, seed);
  S.config = getConfig();

  setLoading(true, "Creating session…");
  await ensureSession(identity);

  setLoading(true, "Registering this device…");
  await enrollThisBrowser(clientFor(identity), identity, { label }).catch((error) => {
    console.warn("Keystore enrollment failed:", error.message);
    toast("Restored, but this device is not backed up — see Settings → Keys & devices", "warning", 8000);
  });

  setLoading(false);
  R.sub = null; R.page = "messages"; R.params = {};
  render();
}

/**
 * The identity this new device is asking to join.
 *
 * On an identity host the URL already named it — asking again is how people
 * type `alice` when the door said `alice.poweur.net`, and the offer then
 * lands under a name the approving device will never look up. Elsewhere we
 * still need a typed name, and a bare handle is completed with the host's
 * domain so it matches what the unlocked device holds.
 */
function joinIdentityFromForm() {
  const info = modeNow();
  if (info.mode === "identity" && info.subject) return info.subject;
  const raw = (q("#join-identity")?.value ?? "").trim().toLowerCase();
  if (!raw) return "";
  if (raw.includes(".")) return raw;
  const domain = (info.domain || getConfig().parentDomain || "").replace(/^\./, "");
  return domain ? `${raw}.${domain}` : raw;
}

/** API base for an identity this device does not yet hold. */
async function enrollApiForJoin(identity) {
  const fallback = defaultRelayUrl();
  const origin = identityOriginUrl(identity, fallback);
  let fallbackHost = "";
  try {
    fallbackHost = new URL(fallback).hostname.toLowerCase();
  } catch {
    /* ignore */
  }
  // Already on this identity's host — stay there (web at alice.poweur.net/app/).
  if (origin && fallbackHost === String(identity).toLowerCase()) {
    return { enroll: new EnrollApi(new RelayClient(origin)), relayUrl: origin };
  }
  // Hosted identity hosts *are* the relay (Caddy wildcard). Probe GET / so a
  // self-hosted website that only serves well-known is not treated as one.
  if (origin) {
    try {
      const root = await identityApiFor(origin, { timeoutMs: 4000 }).root();
      if (root?.service === "poweur-relay") {
        return { enroll: new EnrollApi(new RelayClient(origin)), relayUrl: origin };
      }
    } catch {
      /* identity host is a website, not a relay */
    }
  }
  try {
    const relayUrl = await resolveRecipientRelayUrl(
      identity,
      fallback.startsWith("http://") ? "http" : "https",
      resolveOptionsForRelay(fallback),
    );
    return { enroll: new EnrollApi(new RelayClient(relayUrl)), relayUrl };
  } catch {
    return { enroll: new EnrollApi(new RelayClient(fallback)), relayUrl: fallback };
  }
}

function showJoinDevicePanel(knownIdentity = "") {
  // Joining half of E11-T3: this device has no key yet. It opens a rendezvous
  // and polls until a device that already holds the seed approves.
  let session = null;
  let joining = null;
  let poller = null;
  let enroll = null;
  let relayUrl = defaultRelayUrl();
  // Prefer the identity the door already named (the URL bar, or a
  // `data-join-identity` on the button) over re-reading mode — a first visit
  // to `bob.poweur.net` has no cached root for a moment, and asking Bob to
  // type `bob.poweur.net` is the bug this exists to close (E15-T9).
  const known = String(knownIdentity || "").trim().toLowerCase()
    || (modeNow().mode === "identity" ? modeNow().subject : "");
  const here = isShellRuntime() ? "device" : "browser";

  const stop = () => { poller?.stop(); poller = null; };

  const abandon = () => {
    stop();
    if (session && joining) {
      enroll?.cancel(joining, session).catch(() => {});
      session = null;
    }
  };

  const showJoinRetry = (message, again) => {
    const state = q("#join-state");
    if (!state) return;
    state.innerHTML = `
      <p class="form-note small val-warn">${esc(message)}</p>
      <button class="btn btn-primary" id="btn-join-start" style="width:100%">Try again</button>`;
    q("#btn-join-start")?.addEventListener("click", again);
  };

  showPanel("Add this device", `
    <p class="muted small" style="margin-bottom:12px">
      ${known
        ? `This ${here} will show a code to type on a device that already has <strong>${esc(known)}</strong>.`
        : "Enter your identity. This device will show a code to type on a device you already use."}
    </p>
    ${known ? "" : `
    <div class="form-group">
      <label class="form-label" for="join-identity">Your Poweur ID</label>
      <input id="join-identity" class="input" type="text" placeholder="alice.poweur.net"
             autocapitalize="none" autocorrect="off"
             autocomplete="off" spellcheck="false" inputmode="url" />
    </div>
    <button class="btn btn-primary" id="btn-join-start" style="width:100%">Show my code</button>`}
    <div id="join-state">${known ? `<p class="small muted" id="join-wait">Opening a secure channel…</p>` : ""}</div>`,
  () => {
    const start = async () => {
      const identity = known || joinIdentityFromForm();
      if (!identity) return toast("Enter your identity", "warning");
      // A second tap (or Try again) must not leave the previous interval
      // firing — that is how a stack of identical error toasts starts.
      abandon();

      setLoading(true, "Opening a secure channel…");
      try {
        // offer/claim/cancel are unauthenticated by necessity — this device has
        // no key yet. Prefer the identity host when it is a relay, so a phone
        // adding johnjohn.poweur.net talks to that host rather than poweur.net.
        // Self-hosted websites fall through to the document's `relay`.
        ({ enroll, relayUrl } = await enrollApiForJoin(identity));
        session = await enroll.offer(identity, deviceLabel());
        joining = identity;
        setLoading(false);

        q("#join-state").innerHTML = `
          <div class="notice notice-info" style="margin-top:14px">
            <p>On a device you already use, open
               <strong>Settings → Keys &amp; devices → Add a device</strong> and enter:</p>
            <p class="rendezvous-code mono">${esc(session.rendezvousId)}</p>
            <button class="btn btn-sm btn-ghost" id="btn-copy-rendezvous">Copy request code</button>
            <p style="margin-top:14px">Then check it shows these six digits — they confirm it is
               really this device:</p>
            <p class="sas-code">${esc(session.sas)}</p>
          </div>
          <p class="small muted" id="join-wait">Waiting for approval…</p>
          <p class="small muted" id="join-expiry"></p>
          <button class="btn btn-sm btn-ghost" id="btn-join-check" style="margin-top:8px">Check now</button>`;
        q("#btn-copy-rendezvous")?.addEventListener("click", async () => {
          try {
            await navigator.clipboard.writeText(session.rendezvousId);
            toast("Request code copied", "success", 2000);
          } catch {
            toast("Copy failed — select the code and copy it manually", "warning");
          }
        });
        q("#btn-join-check")?.addEventListener("click", () => poller?.checkNow());

        const deadlineMs = Date.parse(session.expiresAt)
          ? Math.max(0, Date.parse(session.expiresAt) - Date.now())
          : undefined;
        poller = startJoinPoll({
          claim: () => enroll.claim(identity, session),
          deadlineMs,
          onTick: (text) => {
            const expiry = q("#join-expiry");
            if (expiry) expiry.textContent = text;
          },
          onSeed: async (seedBytes) => {
            session = null;
            closePanel();
            try {
              const derived = jwksFromSeed(seedBytes);
              await adoptIdentity({
                identity, relayUrl,
                signingJWK: derived.signingJWK,
                encJWK: derived.encJWK,
                seed: toBase64url(seedBytes),
                label: deviceLabel(),
              });
              toast(`${identity} is set up on this device`, "success", 5000);
            } catch (error) {
              toast(error.message, "error", 9000);
            }
          },
          onError: (error, { terminal }) => {
            const detail = describeJoinError(error);
            const wait = q("#join-wait");
            if (!terminal) {
              if (wait) wait.textContent = detail;
              return;
            }
            toast(detail, "error", 9000);
            showJoinRetry(detail, start);
          },
        });
      } catch (error) {
        setLoading(false);
        const detail = error.message || "Could not open a secure channel";
        toast(detail, "error", 8000);
        if (known) showJoinRetry(detail, start);
      }
    };

    q("#btn-join-start")?.addEventListener("click", start);
    // The identity host already named the subject — skip the form and the
    // extra tap, and just show the code (E15-T9).
    if (known) start();
  },
  () => {
    // Panel closed: free the rendezvous so the relay's per-identity cap does
    // not fill with abandoned ceremonies.
    abandon();
  });
}

/**
 * Is this identity host's own name claimed — and if not, may it be? (E15-T9)
 *
 * "Absent" and "claimable" are not the same answer. `admin.poweur.net` and
 * `www.poweur.net` have no identity document and can never be claimed, so a
 * door that keyed off a 404 would walk the user through a WebAuthn ceremony
 * that registration then refuses. `GET /hosted/availability` answers both
 * halves in one call, and carries the policy the claim field renders.
 *
 * Outside `hosted_domains` availability answers `domain_not_hosted`, which is
 * the honest answer — claimability there is not this relay's to decide — so
 * that case asks `GET /identities/{host}` instead and offers no claim either
 * way.
 */
let doorProbe = null;

function probeDoor() {
  const info = modeNow();
  if (info.mode !== "identity" || S.identity) return;
  if (S.door.subject === info.subject && S.door.state !== "idle") return;
  if (doorProbe) return;

  S.door = { subject: info.subject, state: "checking", message: "", policy: null };
  const relayUrl = defaultRelayUrl();
  if (!relayUrl) {
    S.door = { ...S.door, state: "offline" };
    return;
  }

  const api = identityApiFor(relayUrl);
  const hosted = info.hostedDomains.includes(info.domain);

  doorProbe = (hosted
    ? api.availability(info.handle, info.domain).then((verdict) => {
        if (verdict.reason === "taken") return { state: "claimed", message: "", policy: verdict.policy };
        if (verdict.available) return { state: "claimable", message: "", policy: verdict.policy };
        // reserved / blocked / too_short / charset — a name registration will
        // refuse, so no claim is offered and the relay's own words explain it.
        return { state: "unavailable", message: verdict.message, policy: verdict.policy };
      })
    : api.get(info.subject).then(
        () => ({ state: "claimed", message: "", policy: null }),
        (error) => {
          if (error?.status === 404) {
            return {
              state: "unavailable",
              message: "This relay does not host names under this domain.",
              policy: null,
            };
          }
          throw error;
        },
      )
  )
    .then((next) => { S.door = { subject: info.subject, ...next }; })
    .catch(() => { S.door = { subject: info.subject, state: "offline", message: "", policy: null }; })
    .finally(() => {
      doorProbe = null;
      render();
    });
}

/**
 * Check the handle against the relay as the user types (EPIC-018 E18-T3).
 *
 * The whole reason this endpoint exists is ordering: without it, "that name is
 * taken" arrives *after* a WebAuthn ceremony the user cannot get back. So the
 * Next button stays disabled until the relay has said yes, and the reason for
 * every no is shown under the field in the relay's own words — its policy is
 * deployment configuration this client cannot know.
 */
const AVAILABILITY_DEBOUNCE_MS = 350;
let availabilityToken = 0;

function attachAvailabilityCheck() {
  const input = q("#ni-handle");
  const status = q("#ni-availability");
  const submit = q("#btn-claim");
  if (!input || !status || !submit) return;

  let timer = null;
  const setStatus = (text, cls = "") => {
    status.textContent = text;
    status.className = `idin-status small ${cls}`;
  };

  const check = async () => {
    const info = modeNow();
    const intent = readClaimIntent();
    const token = ++availabilityToken;

    // Self-hosted names are not this relay's to give out, and an empty field
    // is not a question.
    if (!intent.hosted || !intent.handle) {
      submit.disabled = webCustodyBlocked();
      setStatus(intent.handle ? "" : policyHint(info));
      return;
    }
    submit.disabled = true;
    setStatus("Checking…");
    try {
      const verdict = await identityApiFor(defaultRelayUrl()).availability(intent.handle, intent.domain);
      if (token !== availabilityToken) return; // a later keystroke won
      // Keep the policy: it is what the field's hint renders from, and it is
      // deployment configuration this client cannot know (E15-T12).
      if (verdict.policy) S.door = { ...S.door, policy: verdict.policy };
      submit.disabled = webCustodyBlocked() || !verdict.available;
      setStatus(
        verdict.available ? `${verdict.identity} is available` : verdict.message,
        verdict.available ? "val-ok" : "val-warn",
      );
    } catch (error) {
      if (token !== availabilityToken) return;
      // A relay that cannot answer must not block the flow — registration
      // itself is still the authority, and it will refuse if this would have.
      // But it must say so: silently enabling the button teaches the user the
      // check passed when nothing was checked (E15-T12).
      submit.disabled = webCustodyBlocked();
      console.warn("Availability check failed:", error.message);
    }
  };

  const schedule = () => {
    clearTimeout(timer);
    timer = setTimeout(check, AVAILABILITY_DEBOUNCE_MS);
  };

  input.addEventListener("input", () => {
    // A pasted `alice.poweur.net` or `@alice` means the same handle as
    // `alice`; a field that shows its own suffix invites exactly that.
    const cleaned = normalizeHandleInput(input.value, modeNow());
    if (cleaned !== input.value) input.value = cleaned;
    submit.disabled = true;
    setStatus("");
    schedule();
  });
  q("#ni-domain")?.addEventListener("input", schedule);
  q("#ni-domain")?.addEventListener("change", schedule);
  if (input.value.trim()) check();
}

function doClaim() {
  if (webCustodyBlocked()) return toast(S.passkey.reason || PRF_UNAVAILABLE_MESSAGE, "error", 9000);
  const intent = readClaimIntent();
  if (!intent.handle) {
    focusHandleField();
    return toast("Choose a name", "warning");
  }
  if (!intent.domain) return toast("Enter the domain your identity lives under", "warning");
  return doCreateIdentity(intent);
}

/** The identity host's own name, claimed as itself and nothing else (E15-T9). */
function doClaimThisHost() {
  const info = modeNow();
  if (!info.handle || !info.domain) return;
  return doCreateIdentity({
    handle: info.handle,
    domain: info.domain,
    hosted: info.hostedDomains.includes(info.domain),
  });
}

/**
 * Move a freshly claimed identity from the launcher host to its own origin.
 *
 * Storage is per-origin, so the record has to travel. It goes in the URL
 * *fragment*, which browsers never send to a server, and what it carries is
 * the same AES-GCM blob localStorage held — the wrapping secret comes from the
 * passkey and is not in it. The passkey itself needs no second ceremony: E18-T4
 * scoped it to the registrable domain both hosts share.
 *
 * Returns false when there is nothing to hand off (self-hosted flow, an
 * unknown launcher host, or the app is already on the identity's origin), and
 * the caller carries on where it is.
 */
async function handOffToIdentityOrigin(identity) {
  const record = loadIdentityRecord(identity);
  const host = globalThis.location?.hostname ?? "";
  if (!record || !host || host === identity) return false;

  // Any launcher host hands off, not only the canonical one: the apex claims
  // names too now (E15-T7), and an identity claimed there needs the same hop.
  const info = await resolveMode();
  if (info.mode !== "launcher") return false;

  const payload = toBase64url(new TextEncoder().encode(JSON.stringify({ identity, record })));
  setLoading(true, `Taking you to ${identity}…`);
  // The port travels with the hop. It is empty in production, where identity
  // hosts are on 443 — and it is the difference between a working hand-off and
  // a dead host anywhere the relay is not (a dev server, the e2e harness).
  const port = globalThis.location?.port ? `:${globalThis.location.port}` : "";
  globalThis.location.href =
    `${globalThis.location?.protocol ?? "https:"}//${identity}${port}/app/#claim=${payload}`;
  return true;
}

/**
 * The receiving half: adopt an identity handed over by the launcher.
 *
 * The fragment is cleared immediately — it has served its purpose, and leaving
 * a wrapped key blob in the address bar and in history is needless exposure.
 */
function adoptHandOff() {
  const hash = globalThis.location?.hash ?? "";
  if (!hash.startsWith("#claim=")) return false;
  try {
    const decoded = JSON.parse(new TextDecoder().decode(fromBase64url(hash.slice("#claim=".length))));
    if (!decoded?.identity || !decoded?.record) return false;
    saveIdentityRecord(decoded.identity, decoded.record);
    setActiveIdentity(decoded.identity);
    S.identity = decoded.identity;
    return true;
  } catch {
    return false;
  } finally {
    history.replaceState(null, "", globalThis.location.pathname + globalThis.location.search);
  }
}

/**
 * Create an identity from an explicit intent (E15-T7).
 *
 * It used to read the handle out of a `#create=` hash and the mode out of a
 * checkbox, which is why the same form could be opened for a name that had
 * nothing to do with the page it was on. The caller now says what is being
 * claimed, and every caller gets that from the host.
 *
 * @param {{handle: string, domain: string, hosted: boolean,
 *          provider?: string, dnsToken?: string, inviteCode?: string}} intent
 */
async function doCreateIdentity(intent) {
  if (!intent?.handle || !intent?.domain) return toast("Choose a name first", "warning");

  const { handle, domain, hosted } = intent;
  // A brand-new identity has no record yet, so this is the one moment the
  // relay is not read from one — see storage.defaultRelayUrl().
  const relayUrl = defaultRelayUrl();
  const provider = intent.provider;
  const dnsToken = intent.dnsToken ?? "";

  if (!relayUrl) return toast("Choose a relay first", "warning");
  if (!hosted && !dnsToken) {
    return toast(`This relay does not host ${domain} — enter a DNS API token for it`, "warning", 7000);
  }

  const identity = `${handle}.${domain}`;
  if (loadIdentityRecord(identity)) return toast("Identity already exists on this device", "warning");

  const custody = await chooseCustody();
  if (custody.kind !== "native") {
    const support = await checkPasskeySupport();
    if (!support.available || support.prf === false) {
      return toast(support.reason || PRF_UNAVAILABLE_MESSAGE, "error", 9000);
    }
  }

  setLoading(true, "Generating keys…");
  try {
    // Seed-derived from the start (EPIC-011): one secret behind both keys, so
    // this identity can produce a 24-word recovery kit.
    const { signingJWK: sigPriv, encJWK: encPriv, seed, publicKey, encPublicKey } =
      await generateSeedIdentityJwks();

    const userId = toBase64url(crypto.getRandomValues(new Uint8Array(16)));
    let credentialId = "", prfOutput = null, supportsPRF = false;
    let credentialPublicKey = null, credentialAlg = null, credentialScope = null;
    let encryptedKeys;
    if (custody.kind === "native") {
      setLoading(false);
      encryptedKeys = await wrapKeysNative(identity, sigPriv, encPriv, seed, {
        gate: custody.gate,
        reason: `Protect ${handle}`,
      });
      setLoading(true, "Securing keys…");
    } else {
      setLoading(true, "Creating passkey…");
      ({ credentialId, prfOutput, supportsPRF, credentialPublicKey, credentialAlg, rpId: credentialScope } =
        await createPasskey(identity, userId));
      setLoading(true, "Securing keys…");
      encryptedKeys = await wrapKeysWithPRF(prfOutput, sigPriv, encPriv, seed);
    }

    setLoading(true, "Registering identity…");
    const created = await createIdentity(identityApiFor(relayUrl), identity, {
      hosted,
      keys: keyBytesFromJwks(identity, sigPriv, encPriv),
      ...(hosted ? {} : { dnsProvider: provider, dnsToken }),
      ...(intent.inviteCode ? { inviteCode: intent.inviteCode } : {}),
      // A relay may gate signup behind proof-of-work (EPIC-014). Solving it
      // silently is a signup screen that looks stuck, so say what is happening.
      onRegistrationChallenge: ({ bits }) =>
        setLoading(true, `This relay asks for proof of work (${bits} bits)…`),
      onRegistrationProgress: (attempts) =>
        setLoading(true, `Proof of work: ${attempts.toLocaleString()} attempts…`),
    });

    saveIdentityRecord(identity, {
      identity, publicKey, encPublicKey,
      credentialId, credentialPublicKey, credentialAlg, rpId: credentialScope,
      encryptedKeys, relay: relayUrl,
      // Which registration path this identity took. Settings shows DNS
      // credentials only to someone who has any (E15-T10); before this it was
      // shown to every hosted user, none of whom will ever hold a token.
      hosted,
      userId, createdAt: created.document.updated_at, supportsPRF,
      seedDerived: true,
    });
    saveConfig({ ...S.config, relayUrl, parentDomain: domain, dnsProvider: provider });
    S.config = getConfig();

    setUnlockedKeys(identity, sigPriv, encPriv, seed);
    S.identity = identity;
    setActiveIdentity(identity);

    setLoading(true, "Creating session…");
    await ensureSession(identity);

    // Put a wrapped copy on the relay now. Without it this identity lives in
    // exactly one localStorage, and clearing site data destroys it.
    setLoading(true, "Registering this device…");
    await enrollThisBrowser(clientFor(identity), identity).catch((error) => {
      console.warn("Keystore enrollment failed:", error.message);
      toast("Identity created, but this device is not backed up yet — see Settings → Keys & devices", "warning", 8000);
    });

    setLoading(false);
    toast(`${identity} created! 🎉`, "success");

    // Claimed on the launcher host? The identity's own origin is where it
    // lives, so hand it over rather than leaving the user on `id.…` with
    // storage that the identity's own pages cannot read (EPIC-018 E18-T3).
    if (await handOffToIdentityOrigin(identity)) return;

    // A configured app beats an empty inbox: every step is skippable, but the
    // policy question is one nobody thinks to go looking for in Settings.
    S.onboard = { step: 1 };
    R.page = "messages"; R.params = {};
    R.push("onboarding");

  } catch (err) {
    setLoading(false);
    console.error(err);
    // Availability is advisory and fails open, so the relay can still refuse
    // here — after a passkey ceremony. Say what became of that credential and
    // put the cursor back on the name, rather than leaving a toast to be
    // dismissed over a form that still looks fine (E15-T12).
    if (err?.relayCode === "handle_unavailable" || /taken|reserved|not available/i.test(err?.message ?? "")) {
      S.door = { ...S.door, state: "claimable", message: err.message };
      toast(`${err.message} The passkey you just created is unused — pick another name.`, "error", 9000);
      focusHandleField(handle);
      return;
    }
    toast(err.message, "error", 8000);
  }
}

/** Put the user back where the fix is, with the attempt still in the field. */
function focusHandleField(value = "") {
  const field = q("#ni-handle");
  if (!field) return;
  field.value = value;
  field.focus();
  field.select?.();
}

/**
 * Register (or reuse) a relay session. `SessionManager.ensure` re-registers
 * only when the stored one is expired or bound to another relay.
 */
async function ensureSession(identity) {
  const client = clientFor(identity);
  if (!client) return null;
  return client.sessions.ensure(client.signer);
}

/**
 * Fetch the inbox, at most one request in flight.
 *
 * The relay keeps a single outstanding challenge per identity, so two
 * overlapping authenticated GETs invalidate each other's signature. Rendering
 * triggers a fetch (see attachEvents) and callers ask for one explicitly after
 * unlocking, so without this guard those two collide every time.
 */
let inboxInFlight = null;

/**
 * Fold a poll response into the message store.
 *
 * `GET /inbox` **drains**: the relay hands each message over exactly once and
 * forgets it. A tray that rendered straight off the last response would
 * therefore empty itself on the next render, so everything fetched is kept
 * here instead — which is also the seam EPIC-009's push channel folds into.
 * (Durability across reloads is EPIC-009's; this store lives for the session.)
 */
function messageKey(message) {
  return message.id || `${message.sender}:${message.timestamp}`;
}

function mergeInto(store, incoming, key = (entry) => entry.id) {
  const byKey = new Map(store.map(entry => [key(entry), entry]));
  for (const entry of incoming ?? []) byKey.set(key(entry), entry);
  store.length = 0;
  store.push(...byKey.values());
  store.sort((a, b) => new Date(a.timestamp) - new Date(b.timestamp));
  return store;
}

function mergeMessages(incoming) {
  const parsed = (incoming ?? []).map(m => (typeof m === "string" ? JSON.parse(m) : m));
  mergeInto(S.messages, parsed, messageKey);
}

/**
 * Run challenge-signed reads one at a time.
 *
 * Same constraint as the single-flight inbox above, across *different* calls:
 * the inbox drain and the requests drain each fetch a challenge, and whichever
 * lands second invalidates the first one's signature.
 */
let challengeChain = Promise.resolve();

function challengeSerial(task) {
  const next = challengeChain.then(task, task);
  challengeChain = next.catch(() => {});
  return next;
}

/**
 * Hold the relay's push stream open while an identity is unlocked
 * (EPIC-009 E09-T2).
 *
 * The stream never carries a message — it says "there is something", and the
 * inbox fetch that follows is where delivery happens. That is what makes
 * reconnecting free: a gap costs one extra read, not a lost message, so the
 * polling that renders already do stays as the fallback.
 */
let streamAbort = null;

function startEventStream() {
  const client = clientFor(S.identity);
  if (!client || streamAbort) return;
  const identity = S.identity;
  streamAbort = new AbortController();
  streamForever(client.relay, client.signer, {
    signal: streamAbort.signal,
    onEvent: (event) => {
      if (identity !== S.identity) return; // the user switched identities
      // `ready` is the catch-up cue: the stream carries no backlog, so a
      // client that connected (or reconnected) after something was queued
      // has to read from its cursor rather than wait for the next event.
      // streamForever's contract is the same — ignore this and a request
      // parked while the socket was down stays invisible until a click.
      if (event.type === "ready") {
        retryBrowserOutbox();
        loadInbox();
        loadRequests({ force: true });
        if (S.tray === "anonymous" || S.policy.doc?.anonymous?.allow) loadAnon({ force: true });
        return;
      }
      // Each queue has its own event, because each is read by a different
      // call: told the wrong one, the app fetches an empty inbox and leaves
      // the tray that actually has something silent until the user happens to
      // open it.
      if (event.type === "request") loadRequests({ force: true });
      else if (event.type === "anon") loadAnon({ force: true });
      else loadInbox();
    },
    onError: (error) => console.warn("Push stream dropped, retrying:", error.message),
  }).catch(() => {});
}

function stopEventStream() {
  streamAbort?.abort();
  streamAbort = null;
}

function loadInbox() {
  const client = clientFor(S.identity);
  if (!client) return Promise.resolve();
  inboxInFlight ??= challengeSerial(async () => {
    try {
      // Read, receipt and *keep* in one step. Any gap between the pickup and
      // the archive write is a window where the relay has forgotten a message
      // and nothing has written it down — and a drain gives no second chance.
      const { messages, acks, lost } = await client.inboxAndArchive();
      if (lost) {
        toast(`${lost} message${lost === 1 ? "" : "s"} could not be saved to your history`, "warning", 8000);
      }
      await verifyGroupInboxMessages(client, messages);
      mergeMessages(messages);
      mergeInto(S.acks, acks);
      if (R.page === "messages" && !R.sub) render();
      processContactAccepts().catch(e => console.warn("Accept processing failed:", e.message));
    } catch (e) {
      console.warn("Inbox error:", e.message);
    } finally {
      inboxInFlight = null;
    }
  });
  return inboxInFlight;
}

/**
 * A remote relay can forward any correctly signed direct message carrying a
 * group label; only a member can read the group's signed roster. Verify that
 * hint before using it to file a message into a group conversation.
 */
async function verifyGroupInboxMessages(client, messages) {
  const byGroup = new Map();
  for (const message of messages ?? []) {
    const group = String(message.metadata?.group ?? "").toLowerCase();
    if (group && !byGroup.has(group)) byGroup.set(group, []);
    if (group) byGroup.get(group).push(message);
  }
  for (const [group, candidates] of byGroup) {
    try {
      const { document } = await client.groups.roster(client.signer, group);
      const members = new Set((document.members ?? []).map(member => member.toLowerCase()));
      for (const message of candidates) {
        message.group_verified = members.has(String(message.sender).toLowerCase()) &&
          String(message.metadata?.epoch) === String(document.epoch ?? 0);
      }
    } catch {
      for (const message of candidates) message.group_verified = false;
    }
  }
}

/**
 * Load the archive into the message store.
 *
 * This is what makes a reload show yesterday's conversation. `GET /messages`
 * drains — the relay hands each message over exactly once and forgets it — so
 * everything on screen after a refresh comes from here, not from the relay's
 * spool. Runs once per unlocked identity; `loadInbox` keeps it current after.
 */
let historyInFlight = null;

function loadHistory({ force = false } = {}) {
  const H = S.history;
  if (H.loading) return historyInFlight ?? Promise.resolve();
  if (H.loaded && !force) return Promise.resolve();
  const client = clientFor(S.identity);
  if (!client) return Promise.resolve();

  H.loading = true;
  historyInFlight = (async () => {
    try {
      const store = await client.history();
      const [records, readState] = await Promise.all([store.load(), store.readState()]);
      H.readState = readState;
      // Signed conversations and the anonymous queue are different objects to
      // this app — one has someone to reply to and one does not — so they are
      // restored into the trays that render them, not into one list.
      const signed = records.filter(r => r.queue !== "anonymous");
      const anonymous = records.filter(r => r.queue === "anonymous");
      mergeMessages(signed.map(recordToMessage));
      mergeInto(S.anon.messages, anonymous.map(recordToMessage), messageKey);
      H.loaded = true;
      H.error = null;
    } catch (error) {
      H.error = `Could not load your message history: ${error.message}`;
      console.warn("History load failed:", error.message);
    } finally {
      H.loading = false;
      historyInFlight = null;
      if (R.page === "messages" && !R.sub) render();
    }
  })();
  return historyInFlight;
}

/** An archived record in the shape the trays already render. */
function recordToMessage(record) {
  return {
    id: record.id,
    sender: record.sender || "",
    recipient: record.recipient,
    timestamp: record.timestamp,
    type: record.type ?? "",
    // Carried so a reload regroups the conversation into the same threads
    // the live inbox showed.
    thread_id: record.thread_id ?? "",
    expires_at: record.expires_at ?? "",
    metadata: record.metadata,
    queue: record.queue,
    plaintext: record.body,
  };
}

/** The reverse, for archiving something the app produced itself. */
function messageToRecord(message, queue) {
  return {
    id: message.id,
    ...(message.sender ? { sender: message.sender } : {}),
    recipient: message.recipient || S.identity,
    timestamp: message.timestamp,
    ...(message.type ? { type: message.type } : {}),
    ...(message.thread_id ? { thread_id: message.thread_id } : {}),
    ...(message.expires_at ? { expires_at: message.expires_at } : {}),
    ...(message.metadata ? { metadata: message.metadata } : {}),
    queue,
    body: message.plaintext ?? "",
  };
}

/**
 * Unread per conversation, from the read marks rather than from how much we
 * happen to be holding.
 *
 * The old count was `messages.length`, which is a number that can never reach
 * zero — a badge that never clears teaches people to ignore badges. A mark is
 * a *position* (timestamp and id): message timestamps are RFC3339 to the
 * second, so two messages a moment apart share one, and a timestamp-only mark
 * would silently swallow the second.
 */
function unreadFor(peer) {
  const wanted = String(peer ?? "").toLowerCase();
  const mark = S.history.readState?.conversations?.[wanted] ?? null;
  let count = 0;
  for (const raw of S.messages) {
    const m = typeof raw === "string" ? JSON.parse(raw) : raw;
    if (!m.sender || m.sender.toLowerCase() === String(S.identity).toLowerCase()) continue;
    const conversation = String(m.group_verified ? m.metadata?.group : m.sender).toLowerCase();
    if (conversation !== wanted) continue;
    if (mark && markCovers(mark, m.timestamp, m.id ?? "")) continue;
    count += 1;
  }
  return count;
}

/** Unread anonymous messages — the same rule, for the tray that has no sender. */
function unreadAnonymous() {
  const mark = S.history.readState?.conversations?.["anonymous"] ?? null;
  return S.anon.messages.filter(m => !(mark && markCovers(mark, m.timestamp, m.id ?? ""))).length;
}

function markCovers(mark, timestamp, id) {
  if (!mark?.timestamp) return false;
  if (mark.timestamp !== timestamp) return new Date(mark.timestamp) > new Date(timestamp);
  return (mark.id ?? "") >= id;
}

/**
 * Record that a conversation has been read, and repaint so the badge clears
 * at the moment the user would expect it to.
 */
async function markConversationRead(peer) {
  const client = clientFor(S.identity);
  if (!client) return;
  const wanted = String(peer ?? "").toLowerCase();
  const records = (wanted === "anonymous" ? S.anon.messages : S.messages)
    .map(raw => (typeof raw === "string" ? JSON.parse(raw) : raw))
    .filter(m => wanted === "anonymous"
      ? true
      : m.sender && m.sender.toLowerCase() === wanted)
    .map(m => messageToRecord(m, wanted === "anonymous" ? "anonymous" : "inbox"));
  if (!records.length) return;
  try {
    const store = await client.history();
    S.history.readState = await store.markConversationRead(wanted, records);
    // Opening the conversation is the semantic read boundary. Delivery
    // acks were emitted at decrypt time; this distinct signed state is tick 3.
    let policy = S.policy.doc;
    if (!policy) ({ policy } = await client.policy());
    if (wanted !== "anonymous" && sendsReadReceiptsTo(policy, wanted)) {
      const inbound = S.messages
        .map(raw => (typeof raw === "string" ? JSON.parse(raw) : raw))
        .filter(message => message.sender?.toLowerCase() === wanted && message.id && message.plaintext != null);
      await Promise.allSettled(inbound.map(message => client.messages.ack(client.signer, message, { state: ACK_STATE_READ })));
    }
    if (R.page === "messages") render();
  } catch (error) {
    console.warn("Could not save read marks:", error.message);
  }
}

async function doSend() {
  // The component may still be debouncing a lookup; take the raw text so a
  // fast typist is never told "enter a recipient" for something they typed.
  const to   = composeInput?.raw() ?? "";
  const body = q("#c-body")?.value.trim();
  const attachment = q("#c-attachment")?.files?.[0] ?? null;
  const statusEl = q("#c-status");
  const sendBtn  = q("#btn-send-msg");
  if (!to)   return toast("Enter a recipient", "warning");
  if (!body && !attachment) return toast("Enter a message or choose a file", "warning");

  if (q("#c-anon")?.checked && q("#c-group")?.checked) {
    return toast("A group message must be signed; turn off anonymous sending", "warning");
  }
  if (attachment && (q("#c-anon")?.checked || q("#c-group")?.checked)) {
    return toast("Attachments currently require a direct signed message", "warning");
  }

  const client = clientFor(S.identity);
  if (!client) return toast("Unlock your identity first", "warning");
  const sess = loadSessionRecord(S.identity);

  sendBtn.disabled = true;
  const setStatus = (msg, cls = "") => { if (statusEl) { statusEl.textContent = msg; statusEl.className = `compose-status ${cls}`; } };

  if (q("#c-anon")?.checked) {
    try {
      await doSendAnonymous(to, body, setStatus);
    } finally {
      if (sendBtn) sendBtn.disabled = false;
    }
    return;
  }

  if (q("#c-group")?.checked) {
    try {
      setStatus("Reading the signed group roster…");
      const sent = await client.sendGroupAndArchive(to, body, {
        ...(R.params.thread ? { thread: R.params.thread } : {}),
      });
      const first = sent.envelopes[0];
      mergeMessages([{
        id: first.id,
        sender: S.identity,
        recipient: sent.group.group,
        timestamp: first.timestamp,
        queue: "sent",
        plaintext: body,
        thread_id: first.thread_id,
        metadata: first.metadata,
        group_verified: true,
      }]);
      const delivered = sent.response.delivered?.length ?? 0;
      const total = sent.envelopes.length;
      setStatus(`✓ ${delivered} of ${total} delivered`, delivered === total ? "ok" : "err");
      if (sent.lost) toast("Sent, but not saved to your history", "warning", 6000);
      if (q("#c-body")) q("#c-body").value = "";
      toast(`Group message delivered to ${delivered} of ${total}`, delivered === total ? "success" : "warning");
      setTimeout(() => { R.sub = null; R.page = "messages"; render(); }, 1200);
    } catch (err) {
      setStatus(`✕ ${err.message}`, "err");
      toast(err.message, "error");
    } finally {
      if (sendBtn) sendBtn.disabled = false;
    }
    return;
  }

  try {
    setStatus("Checking their key…");
    if (!(await checkPinBeforeSend(client, to))) {
      setStatus("✕ Not sent — key not trusted", "err");
      return;
    }
    setStatus("Sending…");
    const sent = attachment
      ? await client.sendAttachment(to, new Uint8Array(await attachment.arrayBuffer()), {
          name: attachment.name,
          mime: attachment.type || "application/octet-stream",
          caption: body || attachment.name,
          ...(R.params.thread ? { threadId: R.params.thread } : {}),
        })
      : await client.sendAndArchive(to, body, {
          signWith: sessionIsValid(sess) ? "session" : "identity",
          ...(R.params.thread ? { threadId: R.params.thread } : {}),
        });
    setStatus("✓ Sent", "ok");
    // Keep our own copy on screen too: the relay never hands a sender their
    // own message back, so without this the conversation shows only one side
    // until someone replies.
    mergeMessages([{
      id: sent.message.id,
      sender: S.identity,
      recipient: sent.message.recipient,
      timestamp: sent.message.timestamp,
      queue: "sent",
      plaintext: body || attachment?.name,
      ...(attachment ? { type: "chat.attachment", metadata: sent.message.metadata } : {}),
      ...(R.params.thread ? { thread_id: R.params.thread } : {}),
    }]);
    if (sent.lost) toast("Sent, but not saved to your history", "warning", 6000);
    if (q("#c-body")) q("#c-body").value = "";
    toast("Message sent!", "success");
    setTimeout(() => { R.sub = null; R.page = "messages"; render(); }, 1200);
  } catch (err) {
    if (!attachment && client.decryptor && isRetryableSendError(err)) {
      const sealed = poweurCrypto.encryptMessage(client.decryptor.encryptionPublicKey, body);
      queueWebMessage(S.identity, to, sealed, {
        signWith: "identity",
        ...(R.params.thread ? { threadId: R.params.thread } : {}),
      }, err.message);
      setStatus("· Queued — will retry when online", "ok");
      toast("Message queued until the relay is reachable", "success");
      if (q("#c-body")) q("#c-body").value = "";
      return;
    }
    setStatus(`✕ ${err.message}`, "err");
    toast(err.message, "error");
  } finally {
    if (sendBtn) sendBtn.disabled = false;
  }
}

async function retryBrowserOutbox() {
  const client = clientFor(S.identity);
  if (!client?.decryptor) return;
  const privateKey = await client.decryptor.privateKeyBytes();
  const result = await retryWebOutbox(S.identity, entry => {
    const plaintext = poweurCrypto.decryptMessage(privateKey, entry.payload, entry.encryption);
    return client.sendAndArchive(entry.recipient, plaintext, entry.options);
  });
  if (result.sent) toast(`${result.sent} queued message${result.sent === 1 ? "" : "s"} sent`, "success");
}

/**
 * Send with no identity attached (EPIC-014).
 *
 * Nothing here touches the signer: the point is that the message carries no
 * sender. What it can carry is a *cost* — if the recipient's policy demands
 * proof-of-work the relay answers 428 and the browser mines the solution,
 * which at the difficulties people actually set is seconds of work. So it
 * reports progress and stays cancellable; a frozen tab is how a user learns to
 * distrust the feature.
 */
async function doSendAnonymous(to, body, setStatus) {
  const cancel = new AbortController();
  let bits = 0;
  setStatus("Sending anonymously…");
  try {
    const relayUrl = relayUrlFor(S.identity);
    const resolve = resolveOptionsForRelay(relayUrl);
    await sendAnonymous(to, body, {
      resolve,
      // The recipient's relay is resolved from their document, not assumed to
      // be ours — a stranger's home relay is usually somewhere else.
      ...(resolve.scheme ? { scheme: resolve.scheme } : {}),
      signal: cancel.signal,
      onChallenge: ({ type, bits: demanded }) => {
        bits = demanded;
        if (type !== "pow") return;
        setStatus(`${to} asks for proof of work (${bits} bits). Working…`);
        if (bits > 20) {
          toast(`${bits} bits is a big ask — this can take minutes in a browser`, "warning", 6000);
        }
      },
      onSolveProgress: (attempts) => {
        setStatus(`Proof of work (${bits} bits): ${attempts.toLocaleString()} attempts…`);
      },
    });
    setStatus("✓ Sent anonymously", "ok");
    if (q("#c-body")) q("#c-body").value = "";
    toast("Anonymous message sent", "success");
    setTimeout(() => { R.sub = null; R.page = "messages"; render(); }, 1200);
  } catch (error) {
    setStatus(`✕ ${error.message}`, "err");
    toast(error.message, "error");
  }
}

// ─── Files actions ────────────────────────────────────────────────────────────

/**
 * A `DavClient` for the active identity, cached on `S.files` because minting a
 * token costs a signature. `PoweurClient.dav()` reuses its own token too, but
 * the client itself is rebuilt per call, so the cache lives here.
 */
async function dav() {
  const client = clientFor(S.identity);
  if (!client) { toast("Unlock your identity first", "warning"); return null; }
  if (S.files.dav && S.files.davExp > Date.now() + 60_000) return S.files.dav;
  const owner = S.files.owner;
  const connected = await client.dav(owner
    // A visitor asks for `dav:full` and lets the grant engine decide: the
    // token scope is not the permission, the owner's signed grant is, and a
    // read-scoped token would refuse a write the owner *did* allow.
    ? { audience: owner, scope: "dav:full", force: true }
    : { force: true });
  S.files.dav = connected;
  // Tokens are short-lived; re-mint a minute before the relay stops honouring one.
  S.files.davExp = Date.now() + 55 * 60_000;
  return connected;
}

/**
 * A `SyncClient` over whichever tree is open — chunked upload + changes feed.
 *
 * Built from the cached DAV token rather than `client.sync()`, which would
 * mint a fresh one: `clientFor()` returns a new `PoweurClient` each call, so
 * its own token cache is empty every time, and the changes poll would sign a
 * new token every five seconds.
 */
async function syncFor() {
  const client = clientFor(S.identity);
  const davClient = await dav();
  if (!client || !davClient) return null;
  return new SyncClient(client.relay, davClient.identity, davClient.token);
}

/** Switch between our tree and someone else's; everything cached is per-tree. */
function setFilesOwner(owner) {
  S.files.owner = owner;
  S.files.picking = !owner && S.files.picking;
  S.files.dav = null;
  S.files.davExp = 0;
  S.files.path = "";
  S.files.entries = [];
  S.files.quota = null;
  S.files.cursor = "";
}

function openOwnerTree(identity) {
  setFilesOwner(identity.trim().toLowerCase());
  S.files.picking = false;
  loadFiles("");
}

async function loadFiles(path) {
  try {
    const client = await dav();
    if (!client) return;
    S.files.loading = true;
    render();
    const [entries, quota] = await Promise.all([
      client.list(path),
      client.quota().catch(() => S.files.quota),
    ]);
    S.files.path = path;
    S.files.entries = entries;
    S.files.quota = quota;
    if (!S.files.owner) loadGrants();
  } catch (err) {
    toast(err.message, "error");
  } finally {
    S.files.loading = false;
    render();
  }
}

async function doUploadFiles(fileList) {
  const files = Array.from(fileList || []);
  if (!files.length) return;
  try {
    const client = await dav();
    if (!client) return;
    let sync = null;
    setLoading(true, `Uploading ${files.length} file${files.length > 1 ? "s" : ""}…`);
    for (const f of files) {
      const path = `${S.files.path}/${f.name}`;
      if (f.size >= DEFAULT_CHUNK_THRESHOLD) {
        // Above the threshold a single PUT is one all-or-nothing request over
        // whatever connection a phone happens to have; the resumable endpoint
        // (E04/E14) uploads in chunks the relay can pick up again.
        setLoading(true, `Uploading ${f.name} in chunks…`);
        sync ??= await syncFor();
        await sync.uploadChunked(path, new Uint8Array(await f.arrayBuffer()));
      } else {
        await client.write(path, f);
      }
    }
    setLoading(false);
    toast(`Uploaded ${files.length} file${files.length > 1 ? "s" : ""}`, "success");
    await loadFiles(S.files.path);
  } catch (err) {
    setLoading(false);
    toast(uploadErrorMessage(err), "error");
  }
}

/** Say which of the two "no" answers this was. */
function uploadErrorMessage(error) {
  if (error.status === 507) return "Storage quota exceeded";
  if (error.status === 403 && S.files.owner) {
    return `${S.files.owner} granted you read-only access here`;
  }
  return error.message;
}

async function doDownloadEntry(path) {
  try {
    const client = await dav();
    if (!client) return;
    const bytes = await client.readBytes(path);
    const a = document.createElement("a");
    a.href = URL.createObjectURL(new Blob([bytes]));
    a.download = path.split("/").pop();
    a.click();
    URL.revokeObjectURL(a.href);
  } catch (err) {
    toast(err.message, "error");
  }
}

async function doNewFolder() {
  const name = prompt("Folder name:");
  if (!name?.trim()) return;
  try {
    const client = await dav();
    if (!client) return;
    await client.mkdir(`${S.files.path}/${name.trim()}`);
    await loadFiles(S.files.path);
  } catch (err) {
    toast(err.message, "error");
  }
}

async function doRenameEntry(path) {
  const oldName = path.split("/").pop();
  const name = prompt("Rename to:", oldName);
  if (!name?.trim() || name.trim() === oldName) return;
  const parent = path.split("/").slice(0, -1).join("/");
  try {
    const client = await dav();
    if (!client) return;
    await client.move(path, `${parent}/${name.trim()}`);
    await loadFiles(S.files.path);
  } catch (err) {
    toast(err.message, "error");
  }
}

async function doDeleteEntry(path) {
  if (!confirm(`Delete ${path.split("/").pop()}?`)) return;
  try {
    const client = await dav();
    if (!client) return;
    await client.remove(path);
    await loadFiles(S.files.path);
  } catch (err) {
    toast(err.message, "error");
  }
}

// ─── Sharing (EPIC-005) ──────────────────────────────────────────────────────

async function loadGrants({ force = false } = {}) {
  const F = S.files;
  if (F.grantsLoaded && !force) return;
  const client = clientFor(S.identity);
  if (!client || F.owner) return;
  try {
    const shares = await client.shares();
    F.grants = await shares.list();
    F.grantsLoaded = true;
    if (R.page === "files" && !R.sub) render();
  } catch (error) {
    console.warn("Share list failed:", error.message);
  }
}

/**
 * Grant access to one path.
 *
 * The grant is signed *here*, with the identity key, and stored in our own
 * tree — the relay verifies that signature before honouring it, so a relay
 * that rewrote the file could not widen the audience. That is why this dialog
 * cannot be a server call.
 */
function showSharePanel(path) {
  const client = clientFor(S.identity);
  if (!client) return toast("Unlock your identity first", "warning");

  let audience = [];
  const existing = grantsForPath(path);

  showPanel(`Share ${path.split("/").pop()}`, `
    <p class="muted small" style="margin-bottom:4px">/${esc(path)}</p>
    ${existing.length ? `
      <p class="small">Already shared with ${esc(existing.map(describeAudience).join("; "))}.</p>` : ""}
    <div id="share-audience"></div>
    <div class="section-label mt-md">They may</div>
    <div class="policy-challenges" id="share-perms">
      <button class="chip policy-challenge selected" data-perm="read">Read</button>
      <button class="chip policy-challenge" data-perm="rw">Read and write</button>
    </div>
    <div class="form-group">
      <label class="form-label" for="share-expiry">Stop working on (optional)</label>
      <input id="share-expiry" class="input" type="date" />
    </div>
    <button class="btn btn-primary mt-md" id="btn-share-go" disabled>Share</button>`,
  (close) => {
    let permissions = "read";
    const go = q("#btn-share-go");
    const picker = AudiencePicker({
      resolve: resolveForComponents,
      contacts: S.contacts.list,
      groups: [],
      onChange: (selection) => {
        audience = selection;
        if (go) go.disabled = selection.length === 0;
      },
    });
    q("#share-audience")?.replaceChildren(picker.el);

    qAll("#share-perms [data-perm]").forEach(button => button.addEventListener("click", () => {
      permissions = button.dataset.perm;
      qAll("#share-perms [data-perm]").forEach(b => b.classList.toggle("selected", b === button));
    }));

    go?.addEventListener("click", async () => {
      const expiry = q("#share-expiry")?.value;
      close();
      setLoading(true, "Signing the grant…");
      try {
        const shares = await client.shares();
        await shares.add(client.signer, path, {
          with: audience,
          permissions,
          // A date input gives a day; the grant wants an instant, and the end
          // of the chosen day is what "until the 5th" means to a person.
          ...(expiry ? { expiresAt: `${expiry}T23:59:59Z` } : {}),
        });
        toast(`Shared /${path} with ${audience.length} ${audience.length === 1 ? "person" : "people"}`, "success");
        await loadGrants({ force: true });
      } catch (error) {
        toast(error.message, "error");
      } finally {
        setLoading(false);
        render();
      }
    });
  });
}

/** Everything we have shared, and the one button that takes it back. */
function showSharesPanel() {
  const client = clientFor(S.identity);
  if (!client) return toast("Unlock your identity first", "warning");

  const body = () => {
    const grants = S.files.grants;
    if (!grants.length) {
      return `<p class="muted small">You have not shared anything yet. Open a folder under
              /shared or /apps and tap 🔗.</p>`;
    }
    return grants.map(grant => `
      <div class="share-row">
        <div class="share-row-body">
          <div class="share-path mono small">/${esc(grant.path)}</div>
          <div class="muted small">${esc(describeAudience(grant))}</div>
          <div class="small">
            <span class="chip ${grantAllowsWrite(grant) ? "chip-orange" : "chip-accent"}">
              ${grantAllowsWrite(grant) ? "read + write" : "read"}</span>
            ${grant.expires_at ? `<span class="chip ${grantExpired(grant) ? "chip-red" : ""}">
              ${grantExpired(grant) ? "expired" : `until ${esc(grant.expires_at.slice(0, 10))}`}</span>` : ""}
          </div>
        </div>
        <button class="btn btn-sm" data-revoke="${esc(grant.share_id)}">Revoke</button>
      </div>`).join("");
  };

  showPanel("Shared by you", `<div id="shares-list">${body()}</div>`, (close) => {
    const wire = () => qAll("#shares-list [data-revoke]").forEach(button =>
      button.addEventListener("click", async () => {
        button.disabled = true;
        try {
          const shares = await client.shares();
          // Revocation is a file delete: the relay reloads grants per request,
          // so the next thing the grantee tries is already refused.
          await shares.revoke(button.dataset.revoke);
          await loadGrants({ force: true });
          const list = q("#shares-list");
          if (list) { list.innerHTML = body(); wire(); }
          toast("Access revoked", "success");
          render();
        } catch (error) {
          button.disabled = false;
          toast(error.message, "error");
        }
      }));
    wire();
  });
}

/**
 * Live-update the open folder from the changes feed (EPIC-004 E04-T5).
 *
 * Polling, not pushing: the relay has no change socket yet (EPIC-009). The
 * loop runs only while the Files destination is on screen, and only reloads
 * when a change actually touches the folder being looked at — a feed full of
 * someone else's uploads should not make the list flicker.
 */
async function pollChanges() {
  const F = S.files;
  if (F.polling) return;
  F.polling = true;
  try {
    while (R.page === "files" && !R.sub && getUnlockedKeys()) {
      const sync = await syncFor();
      if (!sync) break;
      const { changes, cursor, fullResync } = await sync.changes(F.cursor);
      F.cursor = fullResync ? "" : cursor;
      const prefix = F.path ? `${F.path}/` : "";
      const touched = changes.some(change => {
        const path = change.path ?? "";
        if (!path.startsWith(prefix)) return false;
        // Only this folder's own entries — a change deep inside a subfolder
        // does not change what this listing shows.
        return !path.slice(prefix.length).includes("/");
      });
      if (touched && !F.loading) await loadFiles(F.path);
      await new Promise(resolve => setTimeout(resolve, 5000));
    }
  } catch (error) {
    console.warn("Changes feed stopped:", error.message);
  } finally {
    F.polling = false;
  }
}

// ─── Contacts actions ─────────────────────────────────────────────────────────

/**
 * Read `poweur-sys/relay/contacts.json` through the SDK.
 *
 * Every write below goes back through the same file API, so contacts sync
 * across devices (and to the CLI) with no web-only state anywhere.
 */
async function loadContacts({ force = false } = {}) {
  const C = S.contacts;
  if (C.loading || (C.loaded && !force)) return;
  const client = clientFor(S.identity);
  if (!client) return;

  C.loading = true;
  C.error = null;
  try {
    const contacts = await client.contacts();
    const file = await contacts.load();
    C.list = (file.contacts ?? []).map(entry => ({
      identity: entry.identity,
      petname: entry.petname ?? null,
      state: entry.state ?? "accepted",
      pinnedKey: entry.pinned_key ?? null,
    }));
    C.loaded = true;
  } catch (error) {
    C.error = `Could not read contacts: ${error.message}`;
  } finally {
    C.loading = false;
    if (!R.sub) render();
  }
}

/**
 * Drain the relay's pending contact-request queue.
 *
 * Serialized with the inbox fetch: the relay keeps one outstanding challenge
 * per identity, so two overlapping challenge-signed GETs invalidate each
 * other's signature.
 */
const REQUEST_DRAIN_INTERVAL_MS = 2000;

function loadRequests({ force = false } = {}) {
  const Q = S.requests;
  // Unlike a document read, this is a *drain*: what matters is whether the
  // queue has anything new, so "already loaded once" is not a reason to skip
  // it — an acceptance we never re-fetch is a handshake that never completes.
  // A short floor keeps a burst of renders from becoming a burst of requests.
  if (Q.loading) {
    // A push that lands mid-drain must not be dropped: the in-flight GET
    // may have left the relay before the item was queued.
    if (force) Q.pendingForce = true;
    return Q.inFlight ?? Promise.resolve();
  }
  if (!force && Q.fetchedAt && Date.now() - Q.fetchedAt < REQUEST_DRAIN_INTERVAL_MS) {
    return Promise.resolve();
  }
  const client = clientFor(S.identity);
  if (!client) return Promise.resolve();

  Q.loading = true;
  Q.inFlight = challengeSerial(async () => {
    try {
      // `GET /requests/{id}` drains the same way the inbox does.
      mergeInto(Q.incoming, await client.requests());
      Q.loaded = true;
      Q.fetchedAt = Date.now();
      Q.error = null;
      processContactAccepts().catch(e => console.warn("Accept processing failed:", e.message));
    } catch (error) {
      Q.error = `Could not read requests: ${error.message}`;
    } finally {
      Q.loading = false;
      Q.fetchedAt = Date.now();
      Q.inFlight = null;
      const again = Q.pendingForce;
      Q.pendingForce = false;
      // Repaint for the whole destination, not just this tray: the count is
      // on the tray *bar*, which is visible from every tray. Repainting only
      // when the Requests tray was open meant a request that arrived by push
      // was fetched and then not shown.
      if (R.page === "messages" && !R.sub) render();
      // Schedule outside this serial task: chaining another challenge-signed
      // drain from here waits on a promise that cannot resolve until we do.
      if (again) queueMicrotask(() => loadRequests({ force: true }));
    }
  });
  return Q.inFlight;
}

/** Refresh everything a contact write invalidates, then repaint. */
async function refreshContacts() {
  S.contacts.loaded = false;
  await loadContacts({ force: true });
  if (S.tray === "requests") await loadRequests({ force: true });
}

/**
 * Complete the handshake when someone answers our request.
 *
 * Their relay let our `sys.contact.request` through and they accepted; without
 * this, *our* contacts still say `requested`, so our own policy refuses their
 * first message and the two sides sit in a stalemate neither can see. EPIC-007
 * deferred this "auto-pin-on-accept" to the web pass.
 *
 * It promotes only someone we ourselves asked, and only while the key we
 * pinned when we asked is still their key — an accept is not a reason to
 * re-pin, it is a reason to finish what we started.
 */
async function processContactAccepts() {
  const client = clientFor(S.identity);
  if (!client) return;
  await loadContacts();

  // Under `contacts_and_requests` the relay routes an accept into the requests
  // queue, not the inbox (it is the answer to a request, not a message); under
  // `open` it lands in the inbox like anything else. Both, therefore.
  const senders = new Set([...S.messages, ...S.requests.incoming]
    .filter(entry => entry.type === "sys.contact.accept" && entry.sender !== S.identity)
    .map(entry => entry.sender)
    .filter(sender => contactFor(sender)?.state === "requested"));
  if (!senders.size) return;

  const contacts = await client.contacts();
  let promoted = 0;
  for (const sender of senders) {
    const pin = await contacts.checkPin(sender).catch(() => null);
    if (pin && pin.status !== "ok" && pin.status !== "unpinned") {
      toast(`${sender} accepted, but their key changed — review it in Contacts`, "warning", 8000);
      continue;
    }
    await contacts.set(sender, "accepted", {});
    promoted += 1;
  }
  if (promoted) {
    await refreshContacts();
    render();
  }
}

async function doRequestContact(identity, { intro, petname } = {}) {
  const client = clientFor(S.identity);
  if (!client) return toast("Unlock your identity first", "warning");
  setLoading(true, `Requesting ${identity}…`);
  try {
    await client.requestContact(identity, {
      ...(intro ? { intro } : {}),
      ...(petname ? { petname } : {}),
    });
    toast(`Contact request sent to ${identity}`, "success");
    await refreshContacts();
    return true;
  } catch (error) {
    toast(error.message, "error");
    return false;
  } finally {
    setLoading(false);
    render();
  }
}

/**
 * Accept (or unblock): pin their key now and tell them. `silent` is the
 * unblock case — there is no request to answer, so no notification is due.
 */
async function doAcceptContact(identity, { silent = false, petname } = {}) {
  const client = clientFor(S.identity);
  if (!client) return toast("Unlock your identity first", "warning");
  setLoading(true, `Accepting ${identity}…`);
  try {
    if (silent) {
      const contacts = await client.contacts();
      await contacts.set(identity, "accepted", petname ? { petname } : {});
      toast(`${identity} unblocked`, "success");
    } else {
      const { notified } = await client.acceptContact(identity, petname ? { petname } : {});
      toast(notified
        ? `${identity} is now a contact`
        : `${identity} is now a contact — they could not be notified`,
        notified ? "success" : "warning");
    }
    await refreshContacts();
  } catch (error) {
    toast(error.message, "error");
  } finally {
    setLoading(false);
    render();
  }
}

async function doBlockContact(identity) {
  const client = clientFor(S.identity);
  if (!client) return toast("Unlock your identity first", "warning");
  setLoading(true, `Blocking ${identity}…`);
  try {
    await client.blockContact(identity);
    toast(`${identity} blocked`, "success");
    await refreshContacts();
  } catch (error) {
    toast(error.message, "error");
  } finally {
    setLoading(false);
    render();
  }
}

async function doRemoveContact(identity) {
  const client = clientFor(S.identity);
  if (!client) return toast("Unlock your identity first", "warning");
  setLoading(true, `Removing ${identity}…`);
  try {
    const contacts = await client.contacts();
    await contacts.remove(identity);
    toast(`Removed ${identity}`, "success");
    await refreshContacts();
  } catch (error) {
    toast(error.message, "error");
  } finally {
    setLoading(false);
    render();
  }
}

async function doSetPetname(identity, petname) {
  const client = clientFor(S.identity);
  if (!client) return;
  const existing = contactFor(identity);
  try {
    const contacts = await client.contacts();
    await contacts.set(identity, existing?.state ?? "accepted", { petname });
    await refreshContacts();
    render();
  } catch (error) {
    toast(error.message, "error");
  }
}

/** Everything you can do to one contact, off the row's overflow button. */
function showContactPanel(identity) {
  const contact = contactFor(identity);
  if (!contact) return;
  const chip = CONTACT_STATE_CHIP[contact.state] ?? CONTACT_STATE_CHIP.accepted;

  showPanel(contact.petname || idHandle(identity), `
    <div class="kv-row"><span class="kv-label">Identity</span>
      <span class="kv-value mono small">${esc(identity)}</span></div>
    <div class="kv-row"><span class="kv-label">State</span>
      <span class="kv-value"><span class="chip ${chip.cls}">${chip.label}</span></span></div>
    <div class="kv-row"><span class="kv-label">Safety number</span>
      <span class="kv-value mono small">${contact.pinnedKey ? esc(fingerprintOrKey(contact.pinnedKey)) : "not pinned"}</span></div>
    <div class="kv-row"><span class="kv-label">Pinned key</span>
      <span class="kv-value mono small">${contact.pinnedKey ? esc(contact.pinnedKey) : "not pinned"}</span></div>

    <div class="form-group mt-md">
      <label class="form-label" for="cp-petname">Petname</label>
      <input id="cp-petname" class="input" type="text" value="${esc(contact.petname ?? "")}"
             placeholder="What you call them" autocomplete="off" />
    </div>
    <button class="btn btn-primary" id="cp-save-petname">Save petname</button>

    <div class="panel-actions mt-md">
      <button class="btn" id="cp-message">Message</button>
      ${contact.state === "blocked"
        ? `<button class="btn" id="cp-unblock">Unblock</button>`
        : `<button class="btn" id="cp-block">Block</button>`}
      <button class="btn btn-danger" id="cp-remove">Remove</button>
    </div>`,
  (close) => {
    q("#cp-save-petname")?.addEventListener("click", () => {
      const value = q("#cp-petname")?.value.trim() ?? "";
      close();
      doSetPetname(identity, value);
    });
    q("#cp-message")?.addEventListener("click", () => { close(); R.push("compose", { to: identity }); });
    q("#cp-block")?.addEventListener("click", () => { close(); doBlockContact(identity); });
    q("#cp-unblock")?.addEventListener("click", () => { close(); doAcceptContact(identity, { silent: true }); });
    q("#cp-remove")?.addEventListener("click", () => { close(); doRemoveContact(identity); });
  });
}

function showAddContactPanel(preset = "") {
  let picked = null;
  showPanel("Add a contact", `
    <p class="muted small" style="margin-bottom:12px">
      Type a Poweur ID. We resolve it first, so a typo fails here rather than silently later —
      and the key we resolve now is the one we pin.
    </p>
    <div id="add-contact-input"></div>
    <div class="form-group mt-md">
      <label class="form-label" for="ac-intro">Say hello (optional)</label>
      <input id="ac-intro" class="input" type="text" placeholder="contact request" autocomplete="off" />
    </div>
    <div class="form-group">
      <label class="form-label" for="ac-petname">Petname (optional)</label>
      <input id="ac-petname" class="input" type="text" placeholder="What you call them" autocomplete="off" />
    </div>
    <button class="btn btn-primary mt-md" id="btn-add-contact-go">Send request</button>
    <button class="btn mt-sm" id="btn-add-contact-msg">Just message them</button>`,
  (close) => {
    const host = q("#add-contact-input");
    const go = q("#btn-add-contact-go");
    const msg = q("#btn-add-contact-msg");
    const input = IdentityInput({
      resolve: resolveForComponents,
      contacts: S.contacts.list,
      value: preset,
      // On a hosted relay everyone shares a domain, so "alice" is what people
      // type and `alice.poweur.net` is what they mean.
      defaultDomain: idDomain(S.identity),
      label: "Identity",
      onChange: (result) => { picked = result; },
      onSubmit: () => go?.click(),
    });
    host?.replaceChildren(input.el);
    input.focus();

    /**
     * Resolve on the way out rather than gating the button on it.
     *
     * The buttons used to stay disabled until a debounced lookup came back,
     * so someone who typed a name and hit the button they were already
     * looking at got nothing at all — no error, no send, just a dead control.
     * Pressing it now forces the lookup it was waiting for.
     */
    const resolveNow = async (button) => {
      if (picked) return picked;
      if (button) button.disabled = true;
      try {
        return await input.lookup();
      } finally {
        if (button) button.disabled = false;
      }
    };

    go?.addEventListener("click", async () => {
      const target = await resolveNow(go);
      if (!target) return toast("Enter a Poweur ID we can find", "warning");
      const intro = q("#ac-intro")?.value.trim();
      const petname = q("#ac-petname")?.value.trim();
      close();
      doRequestContact(target.identity, { intro, petname });
    });
    msg?.addEventListener("click", async () => {
      const target = await resolveNow(msg);
      if (!target) return toast("Enter a Poweur ID we can find", "warning");
      close();
      R.push("compose", { to: target.identity });
    });
  });
}

/**
 * The known-hosts moment (EPIC-007 E07-T4), as a dialog rather than a toast.
 *
 * A pinned contact whose key changed with no rotation statement is what a
 * compromised relay or registrar looks like, so this blocks the send and makes
 * the user say the new key is fine — the web twin of the CLI's
 * `--accept-new-key`. Resolves true only on an explicit "trust".
 */
function showKeyMismatchDialog({ recipient, pinnedKey, resolvedKey }) {
  return new Promise((resolve) => {
    let trusted = false;
    showPanel("Key changed", `
      <p class="val-warn" style="font-weight:600;margin-bottom:8px">
        ${esc(recipient)}'s key does not match the one you pinned.
      </p>
      <p class="muted small" style="margin-bottom:12px">
        No rotation statement covers this change. It can mean a compromised relay or
        registrar impersonating your contact. Verify with them out of band before you trust it.
      </p>
      <div class="kv-row"><span class="kv-label">Pinned</span>
        <span class="kv-value mono small" id="km-pinned">${esc(pinnedKey ? fingerprintOrKey(pinnedKey) : "")}</span></div>
      <div class="kv-row"><span class="kv-label">Now</span>
        <span class="kv-value mono small" id="km-resolved">${esc(resolvedKey ? fingerprintOrKey(resolvedKey) : "")}</span></div>
      <p class="muted small" style="margin-top:8px">
        These are safety numbers — read them to ${esc(recipient)} over a channel you already
        trust. They match on both sides when nothing has been tampered with.
      </p>
      <div class="panel-actions mt-md">
        <button class="btn btn-primary" id="km-cancel">Don't send</button>
        <button class="btn btn-danger" id="km-trust">Trust new key</button>
      </div>`,
    (close) => {
      q("#km-cancel")?.addEventListener("click", () => close());
      q("#km-trust")?.addEventListener("click", () => { trusted = true; close(); });
    },
    () => resolve(trusted));
  });
}

/**
 * Gate a send on the recipient's pin. Returns false only when the user was
 * shown a mismatch and declined; every other failure fails *open*, because
 * pinning is client-side defence in depth and not the security boundary.
 */
async function checkPinBeforeSend(client, recipient) {
  let contacts, pin;
  try {
    contacts = await client.contacts();
    pin = await contacts.checkPin(recipient);
  } catch {
    return true;
  }
  if (pin.status === "ok" || pin.status === "unpinned") return true;

  if (pin.status === "rotated" && pin.resolvedKey) {
    // Covered by a signed rotation (E01-T5): re-pin and say so, don't block.
    await contacts.repin(recipient, pin.resolvedKey).catch(() => {});
    toast(`${recipient} rotated their key — re-pinned`, "info");
    await refreshContacts();
    return true;
  }

  const trusted = await showKeyMismatchDialog({
    recipient, pinnedKey: pin.pinnedKey, resolvedKey: pin.resolvedKey,
  });
  if (!trusted) return false;
  if (pin.resolvedKey) {
    await contacts.repin(recipient, pin.resolvedKey).catch(() => {});
    await refreshContacts();
  }
  return true;
}

// ─── Inbox policy, anonymous & PoW (E15-T3) ──────────────────────────────────

/** One-line summaries for the Settings rows. */
function policySummary() {
  const doc = S.policy.doc;
  if (!doc) return { mode: S.policy.loading ? "…" : "—", anon: "—", anonOn: false };
  const mode = INBOX_MODES.find(m => m.id === doc.mode)?.label ?? doc.mode;
  const anon = doc.anonymous?.allow
    ? (doc.anonymous.challenge === "pow"
        ? `On · ${clampPowBits(doc.anonymous.pow_bits ?? 0)} bits`
        : "On")
    : "Off";
  return { mode, anon, anonOn: Boolean(doc.anonymous?.allow) };
}

async function loadPolicy({ force = false } = {}) {
  const P = S.policy;
  if (P.loading || (P.loaded && !force)) return;
  const client = clientFor(S.identity);
  if (!client) return;
  P.loading = true;
  try {
    const { policy, explicit } = await client.policy();
    P.doc = policy;
    P.explicit = explicit;
    P.loaded = true;
  } catch (error) {
    console.warn("Policy read failed:", error.message);
  } finally {
    P.loading = false;
    if (!R.sub) render();
  }
}

function showPolicyPanel() {
  const client = clientFor(S.identity);
  if (!client) return toast("Unlock your identity first", "warning");

  showPanel("Inbox", `<div id="policy-host"></div>`, async (close) => {
    const host = q("#policy-host");
    if (!host) return;
    host.textContent = "Loading…";
    await loadPolicy({ force: true });
    const controls = PolicyControls({
      policy: S.policy.doc ?? {},
      explicit: S.policy.explicit,
      onSave: async (document) => {
        // One write of the whole document: mode and the anonymous block live
        // together, and `setPolicy` is the same call `poweur policy set` makes.
        await client.setPolicy(document.mode, document.anonymous, document.read_receipts);
        await loadPolicy({ force: true });
        S.anon.loaded = false;
        toast("Inbox policy saved", "success");
        close();
      },
    });
    host.replaceChildren(controls.el);
  });
}

/** Drain the anonymous queue — challenge-signed, so serialized like the rest. */
function loadAnon({ force = false } = {}) {
  const A = S.anon;
  if (A.loading) {
    if (force) A.pendingForce = true;
    return A.inFlight ?? Promise.resolve();
  }
  if (!force && A.fetchedAt && Date.now() - A.fetchedAt < REQUEST_DRAIN_INTERVAL_MS) {
    return Promise.resolve();
  }
  const client = clientFor(S.identity);
  if (!client) return Promise.resolve();
  A.loading = true;
  A.inFlight = challengeSerial(async () => {
    try {
      // `GET /anon/{id}` drains like the inbox: what we are handed here is
      // handed here once, so it is archived in the same step.
      const { messages, lost } = await client.anonAndArchive();
      mergeInto(A.messages, messages, messageKey);
      if (lost) toast("Anonymous messages could not be saved to your history", "warning", 6000);
      A.loaded = true;
      A.error = null;
    } catch (error) {
      A.error = `Could not read anonymous messages: ${error.message}`;
    } finally {
      A.loading = false;
      A.fetchedAt = Date.now();
      A.inFlight = null;
      const again = A.pendingForce;
      A.pendingForce = false;
      if (R.page === "messages" && !R.sub) render();
      if (again) queueMicrotask(() => loadAnon({ force: true }));
    }
  });
  return A.inFlight;
}

// ─── Profile (EPIC-006 E06-T2) ───────────────────────────────────────────────

/**
 * Read our own profile document.
 *
 * Repaints itself when the answer lands, and never asks the caller to — a
 * render() in the caller's `.then()` re-runs `attachEvents`, which calls this
 * again, which resolves immediately once loaded: a render loop that starves
 * the page. `loading` guards the same thing for the in-flight case.
 */
async function loadProfile({ force = false } = {}) {
  const P = S.profile;
  if (P.loading || (P.loaded && !force)) return P.doc;
  const client = clientFor(S.identity);
  if (!client) return null;
  P.loading = true;
  try {
    const { profile, explicit } = await client.profile();
    P.doc = profile;
    P.explicit = explicit;
  } catch (error) {
    console.warn("Profile read failed:", error.message);
  } finally {
    // Loaded either way: a failed read that left this false would be retried
    // on every render, which is the same loop by a slower route.
    P.loaded = true;
    P.loading = false;
    if (!R.sub) render();
  }
  return P.doc;
}

/**
 * The profile editor, as DOM so it can sit in a panel or in the onboarding
 * flow without being rendered twice.
 *
 * `onSaved` is how the caller finds out; the editor owns the write because
 * the avatar upload and the document write have to happen in that order —
 * the document points at a tree path, so the bytes must exist first.
 */
function ProfileEditor({ profile, onSaved = () => {}, saveLabel = "Save profile", showSave = true }) {
  const client = clientFor(S.identity);
  const doc = profile ?? { version: 1 };
  let avatarPath = doc.avatar ?? null;
  let pendingAvatar = null;

  const host = document.createElement("div");
  host.className = "profile-editor";
  host.innerHTML = `
    <div class="form-group">
      <label class="form-label" for="pe-name">Display name</label>
      <input id="pe-name" class="input" type="text" maxlength="256"
             value="${esc(doc.display_name ?? "")}" placeholder="${esc(idHandle(S.identity))}" />
    </div>
    <div class="form-group">
      <label class="form-label" for="pe-bio">Bio</label>
      <textarea id="pe-bio" class="input pe-bio" maxlength="4096"
                placeholder="A line or two about you">${esc(doc.bio ?? "")}</textarea>
    </div>
    <div class="form-group">
      <label class="form-label" for="pe-avatar">Avatar</label>
      <p class="muted small" style="margin-bottom:6px">
        Stored in your own <code>/public</code> folder — a profile can never point at
        someone else's server.
      </p>
      <input id="pe-avatar" class="input" type="file" accept="image/*" />
      <p class="small" id="pe-avatar-current">${avatarPath ? `Current: ${esc(avatarPath)}` : ""}</p>
    </div>
    <div class="form-group">
      <label class="form-label" for="pe-link-url">Link</label>
      <input id="pe-link-label" class="input" type="text" placeholder="Label (optional)"
             value="${esc(doc.links?.[0]?.label ?? "")}" />
      <input id="pe-link-url" class="input mt-sm" type="url" placeholder="https://example.org"
             value="${esc(doc.links?.[0]?.url ?? "")}" />
    </div>
    ${showSave ? `<button class="btn btn-primary mt-md" id="pe-save">${esc(saveLabel)}</button>` : ""}
    <p class="idin-status small" id="pe-status" role="status" aria-live="polite"></p>`;

  host.querySelector("#pe-avatar")?.addEventListener("change", (event) => {
    pendingAvatar = event.target.files?.[0] ?? null;
  });

  async function submit() {
    const status = host.querySelector("#pe-status");
    const button = host.querySelector("#pe-save");
    if (!client) { toast("Unlock your identity first", "warning"); return false; }
    if (button) button.disabled = true;
    status.className = "idin-status small";
    try {
      if (pendingAvatar) {
        status.textContent = "Uploading avatar…";
        const extension = (pendingAvatar.name.split(".").pop() || "png").toLowerCase().slice(0, 5);
        const path = `public/avatar.${extension.replace(/[^a-z0-9]/g, "") || "png"}`;
        const dav = await client.dav();
        await dav.write(path, pendingAvatar);
        avatarPath = path;
      }
      status.textContent = "Saving…";
      const saved = await client.setProfile({
        version: 1,
        display_name: host.querySelector("#pe-name").value,
        bio: host.querySelector("#pe-bio").value,
        ...(avatarPath ? { avatar: avatarPath } : {}),
        links: [{
          label: host.querySelector("#pe-link-label").value,
          url: host.querySelector("#pe-link-url").value,
        }],
      });
      S.profile = { doc: saved, explicit: true, loaded: true };
      // Everywhere a card shows us should show the new name immediately.
      primeProfile(S.identity, saved, relayUrlFor(S.identity));
      status.textContent = "Saved";
      status.className = "idin-status small val-ok";
      onSaved(saved);
      return true;
    } catch (error) {
      status.textContent = error.message;
      status.className = "idin-status small val-warn";
      return false;
    } finally {
      if (button) button.disabled = false;
    }
  }

  host.querySelector("#pe-save")?.addEventListener("click", submit);
  host.save = submit;
  return host;
}

function showProfilePanel() {
  const client = clientFor(S.identity);
  if (!client) return toast("Unlock your identity first", "warning");

  showPanel("Your profile", `<div id="profile-host">Loading…</div>`, async (close) => {
    const doc = await loadProfile({ force: true });
    const host = q("#profile-host");
    if (!host) return;
    host.replaceChildren(ProfileEditor({
      profile: doc,
      onSaved: () => { toast("Profile saved", "success"); close(); render(); },
    }));

    // Capabilities are read-only: what this identity speaks, not a preference.
    const entry = await resolveForComponents(S.identity).catch(() => null);
    const features = Object.keys(entry?.capabilities?.features ?? {});
    if (features.length && q("#profile-host")) {
      const caps = document.createElement("div");
      caps.className = "profile-caps";
      caps.innerHTML = `<div class="section-label">This identity speaks</div>
        <div class="profile-card-caps">${features.map(f => `<span class="chip">${esc(f)}</span>`).join("")}</div>`;
      host.append(caps);
    }
  });
}

// ─── Keys & devices (EPIC-011) ───────────────────────────────────────────────

async function showConnectedAppsPanel() {
  const identity = S.identity;
  const client = clientFor(identity);
  if (!client) return toast("Unlock your identity first", "warning");
  showPanel("Connected apps", `<div id="connected-apps-host" role="status">Loading…</div>`, async close => {
    const host = q("#connected-apps-host");
    try {
      const davClient = await client.dav();
      const [doc, consentLog] = await Promise.all([readConnectedApps(davClient), readConsentLog(davClient)]);
      if (!host?.isConnected || S.identity !== identity) return;
      const apps = [...doc.apps].sort((a, b) => String(a.app_id).localeCompare(String(b.app_id)));
      const recent = consentLog.slice(-10).reverse();
      host.innerHTML = `${apps.length ? `
        <div class="settings-rows">${apps.map(app => `
          <div class="settings-row">
            <span class="settings-row-icon">🧩</span>
            <span class="settings-row-label"><strong>${esc(app.name || app.app_id)}</strong><br>
              <span class="small muted">${esc(app.audience)}<br>${esc((app.scopes || []).join(", "))}</span></span>
            ${app.revoked_at
              ? `<span class="settings-row-value muted">Revoked</span>`
              : `<button class="btn btn-sm" data-revoke-app="${esc(app.app_id)}">Revoke</button>`}
          </div>`).join("")}</div>`
        : `<p class="muted">No apps have access to your home.</p>`}
        <div class="section-label mt-md">Recent approvals</div>
        ${recent.length ? `<div class="settings-rows">${recent.map(record => `
          <div class="settings-row">
            <span class="settings-row-icon">✓</span>
            <span class="settings-row-label"><strong>${esc(record.app_name || record.app_id || record.audience)}</strong><br>
              <span class="small muted">${esc(record.at || "")} · ${esc((record.scopes || []).join(", ") || "sign-in only")}</span></span>
          </div>`).join("")}</div>` : `<p class="muted small">No approvals recorded yet.</p>`}`;
      qAll("[data-revoke-app]").forEach(button => button.addEventListener("click", async () => {
        if (!confirm(`Revoke ${button.dataset.revokeApp}? Its current tokens stop working immediately.`)) return;
        button.disabled = true;
        try {
          await revokeConnectedApp(davClient, button.dataset.revokeApp);
          toast("App access revoked", "success");
          close();
          showConnectedAppsPanel();
        } catch (error) {
          toast(error.message, "error");
          button.disabled = false;
        }
      }));
    } catch (error) {
      if (host?.isConnected) host.textContent = `Could not load connected apps: ${error.message}`;
    }
  });
}

const ENROLLMENT_KIND_LABEL = {
  "passkey": "Passkey",
  "hardware-key": "Hardware key",
  "cli-passphrase": "CLI passphrase",
  "recovery-kit": "Recovery kit",
  "native": "Native keystore",
};

const ENROLLMENT_WRAP_LABEL = {
  prf: "passkey (PRF)",
  passphrase: "passphrase",
  native: "the OS keystore",
};

async function showKeysAndDevicesPanel() {
  const identity = S.identity;
  const client = clientFor(identity);
  if (!client) return toast("Unlock your identity first", "warning");

  setLoading(true, "Reading your devices…");
  let enrollments;
  try {
    enrollments = await listEnrollments(client, identity);
  } catch (error) {
    setLoading(false);
    return toast(`Could not read your devices: ${error.message}`, "error", 7000);
  }
  // The device registry (EPIC-004 E04-T6) is a different list from the key
  // enrollments above and belongs beside them: enrollments are who can
  // *unlock* this identity, the registry is who is *using* it — a phone that
  // syncs, a laptop with an app password, a headless agent. Losing it is
  // never fatal to this panel, so a failure costs the section, not the page.
  let registry = [];
  try {
    registry = (await (await dav()).devices()).devices ?? [];
  } catch {
    registry = null;
  }
  setLoading(false);

  const record = loadIdentityRecord(identity);
  const thisBrowserEnrolled = enrollments.some(e => e.current);
  const here = isShellRuntime() ? "device" : "browser";
  const loseHow = isShellRuntime() ? "clearing app data" : "clearing site data";

  showPanel("Keys & devices", `
    ${thisBrowserEnrolled ? "" : `
      <div class="notice notice-warn">
        <strong>This ${here} is not backed up.</strong> Its copy of your keys exists only here,
        so ${loseHow} would destroy this identity. Registering it stores an encrypted
        copy the relay cannot read.
        <button class="btn btn-sm btn-primary" id="btn-enroll-this" style="margin-top:10px">
          Back up this ${here}
        </button>
      </div>`}
    ${enrollments.length ? `
      <div class="enrollment-list">
        ${enrollments.map(e => `
          <div class="enrollment-row">
            <span class="enrollment-icon">${e.kind === "hardware-key" ? "🔐" : e.kind === "recovery-kit" ? "🧾" : "📱"}</span>
            <div class="enrollment-body">
              <div class="enrollment-label">
                ${esc(e.label || ENROLLMENT_KIND_LABEL[e.kind] || e.kind)}
                ${e.current ? `<span class="chip chip-green">this device</span>` : ""}
                ${e.role === "recovery-master" ? `<span class="chip chip-orange">recovery master</span>` : ""}
              </div>
              <div class="enrollment-meta small muted">
                ${esc(ENROLLMENT_KIND_LABEL[e.kind] ?? e.kind)}
                · unlocked by ${esc(ENROLLMENT_WRAP_LABEL[e.wrap] ?? e.wrap)}
                ${e.payload === "legacy-keypair" ? " · pre-seed keys" : ""}
                ${e.created_at ? ` · added ${esc(fmtTime(e.created_at))}` : ""}
              </div>
            </div>
            <button class="btn btn-sm" data-remove-enrollment="${esc(e.enrollment_id)}"
                    ${e.current ? "disabled" : ""} aria-label="Remove ${esc(e.label || e.enrollment_id)}">Remove</button>
          </div>`).join("")}
      </div>
    ` : `<p class="muted small">No devices registered yet.</p>`}

    <button class="btn btn-primary mt-md" id="btn-enroll-device" style="width:100%">Add a device</button>
    <p class="muted small" style="margin-top:10px">
      Removing a device stops it reading your stored keys and ends its sessions. It does not
      protect against someone who already copied them — that needs a key rotation.
    </p>

    <h3 class="panel-subhead">Devices using this identity</h3>
    <p class="muted small">
      What the relay has seen: apps and machines holding sessions, app passwords or sync
      cursors. Only you can see this list.
    </p>
    ${registry === null
      ? `<p class="muted small">Could not read the device registry.</p>`
      : registry.length
        ? `<div class="enrollment-list">
            ${registry.map(d => `
              <div class="enrollment-row${d.revoked ? " is-revoked" : ""}">
                <span class="enrollment-icon">${deviceIcon(d.kind)}</span>
                <div class="enrollment-body">
                  <div class="enrollment-label">
                    ${esc(d.name || "Unnamed device")}
                    ${d.revoked ? `<span class="chip chip-orange">revoked</span>` : ""}
                  </div>
                  <div class="enrollment-meta small muted">${esc(describeDevice(d))}</div>
                </div>
                ${d.revoked ? "" : `<button class="btn btn-sm" data-revoke-device="${esc(d.id)}"
                        aria-label="Revoke ${esc(d.name || d.id)}">Revoke</button>`}
              </div>`).join("")}
          </div>`
        : `<p class="muted small">No devices recorded yet.</p>`}
    <p class="muted small" style="margin-top:10px">
      Revoking ends that device's sessions, DAV tokens and app passwords. A device that still
      holds your identity key can enrol again — that case needs a key rotation.
    </p>`,
  () => {
    q("#btn-enroll-this")?.addEventListener("click", async () => {
      closePanel();
      setLoading(true, `Backing up this ${here}…`);
      try {
        await enrollThisBrowser(clientFor(identity), identity);
        setLoading(false);
        toast(`This ${here} is backed up`, "success", 3500);
        showKeysAndDevicesPanel();
      } catch (error) {
        setLoading(false);
        toast(error.message, "error", 8000);
      }
    });

    q("#btn-enroll-device")?.addEventListener("click", () => {
      closePanel();
      showApproveDevicePanel();
    });

    qAll("[data-remove-enrollment]").forEach(button =>
      button.addEventListener("click", async () => {
        const id = button.dataset.removeEnrollment;
        if (!confirm("Remove this device? It will lose access to your stored keys and its sessions end.")) return;
        closePanel();
        setLoading(true, "Removing device…");
        try {
          await removeEnrollment(clientFor(identity), identity, id);
          setLoading(false);
          toast("Device removed", "success");
        } catch (error) {
          setLoading(false);
          toast(error.message, "error", 8000);
        }
        showKeysAndDevicesPanel();
      }));

    qAll("[data-revoke-device]").forEach(button =>
      button.addEventListener("click", async () => {
        const id = button.dataset.revokeDevice;
        if (!confirm("Revoke this device? Its sessions, tokens and app passwords stop working.")) return;
        closePanel();
        setLoading(true, "Revoking device…");
        try {
          const result = await (await dav()).revokeDevice(id);
          setLoading(false);
          toast(`Revoked: ${result.sessions_revoked} session(s), ` +
                `${result.dav_tokens_revoked} token(s), ${result.app_passwords_revoked} app password(s)`,
                "success", 5000);
        } catch (error) {
          setLoading(false);
          toast(error.message, "error", 8000);
        }
        showKeysAndDevicesPanel();
      }));

    if (record && !record.seedDerived) {
      // Say it here too: this is where someone comes looking for recovery.
      q("#btn-enroll-device")?.insertAdjacentHTML("afterend",
        `<p class="muted small" style="margin-top:10px">This identity predates recovery kits — see Settings → Recovery kit.</p>`);
    }
  });
}

/**
 * The approving half of the enrollment ceremony (E11-T3).
 *
 * The new device shows a six-digit code; the user types it here. The code
 * authenticates the new device's ephemeral public key — it protects nothing,
 * which is exactly why no PAKE is needed. Comparing the two codes *is* the
 * authentication step, so the confirmation below is not a formality.
 */
function showApproveDevicePanel() {
  const identity = S.identity;
  showPanel("Add a device", `
    <ol class="steps small">
      <li>Open this app on the new device and choose <strong>Add this device</strong>.</li>
      <li>It shows a <strong>request code</strong> and a six-digit number.</li>
      <li>Enter the request code below, then check the six digits match before approving.</li>
    </ol>
    <div class="form-group">
      <label class="form-label" for="enroll-rendezvous">Request code from the new device</label>
      <input id="enroll-rendezvous" class="input mono" type="text"
             autocapitalize="none" autocorrect="off" autocomplete="off"
             spellcheck="false" inputmode="text" placeholder="paste it here" />
    </div>
    <button class="btn btn-primary" id="btn-enroll-lookup" style="width:100%">Continue</button>
    <div id="enroll-confirm"></div>`,
  () => {
    q("#btn-enroll-lookup")?.addEventListener("click", async () => {
      const rendezvousId = normalizeRendezvousId(q("#enroll-rendezvous")?.value ?? "");
      if (!rendezvousId) return toast("Enter the request code from the new device", "warning");

      const client = clientFor(identity);
      if (!client) return toast("Unlock your identity first", "warning");
      const keys = getUnlockedKeys();
      if (!keys?.seed) {
        return toast("Only seed-based identities can hand their keys to a new device — see Recovery kit", "warning", 8000);
      }

      setLoading(true, "Finding the new device…");
      try {
        const { enroll } = await enrollApiForJoin(identity);
        const pending = await enroll.pending(client.signer, identity, rendezvousId);
        setLoading(false);
        const host = q("#enroll-confirm");
        host.innerHTML = `
          <div class="notice notice-info" style="margin-top:14px">
            <p>Confirm this matches the code on the new device:</p>
            <p class="sas-code">${esc(pending.sas)}</p>
            ${pending.label ? `<p class="small muted">${esc(pending.label)}</p>` : ""}
          </div>
          <button class="btn btn-primary" id="btn-enroll-approve" style="width:100%">
            Codes match — send my keys
          </button>`;
        q("#btn-enroll-approve")?.addEventListener("click", async () => {
          setLoading(true, "Sending keys…");
          try {
            await enroll.approve(client.signer, identity, pending, fromBase64url(keys.seed));
            setLoading(false);
            closePanel();
            toast("The new device can now finish setting up", "success", 6000);
          } catch (error) {
            setLoading(false);
            toast(error.message, "error", 8000);
          }
        });
      } catch (error) {
        setLoading(false);
        toast(`No pending device for that request code: ${error.message}`, "error", 8000);
      }
    });
  });
}

// ─── Recovery kit ─────────────────────────────────────────────────────────────

function showRecoveryKitPanel() {
  const identity = S.identity;
  const { eligible, reason } = recoveryKitEligibility(identity);
  const keys = getUnlockedKeys();

  if (!eligible) {
    return showPanel("Recovery kit", `
      <p class="muted small" style="margin-bottom:12px">
        A recovery kit is your identity's master secret written as 24 words. With it you can
        rebuild this identity anywhere — no relay, no email, nothing else to remember.
      </p>
      <div class="notice notice-warn">
        <strong>Not available for this identity.</strong>
        ${reason === "legacy-keypair" ? `
          It was created with two independent keys rather than from a single seed, so there is
          no seed to write down. Identities created from now on have one. Converting this one
          means rotating your keys, which asks every contact to re-pin them — worth it for some
          people, not for others, so it is offered rather than done for you.`
        : `No local record for this identity.`}
      </div>`);
  }

  if (!keys?.seed) {
    return showPanel("Recovery kit", `
      <div class="notice notice-warn">Unlock this identity to see its recovery kit.</div>`);
  }

  const kit = buildRecoveryKit(identity, keys.seed);
  const words = kit.mnemonic.split(" ");

  showPanel("Recovery kit", `
    <p class="muted small" style="margin-bottom:12px">
      Write these 24 words down and keep them somewhere safe and offline. Anyone who has them
      <strong>is</strong> you — and without them, losing every device loses this identity.
    </p>
    <ol class="mnemonic">
      ${words.map(w => `<li class="mnemonic-word">${esc(w)}</li>`).join("")}
    </ol>
    <button class="btn btn-primary mt-md" id="btn-verify-kit" style="width:100%">
      I've written it down — check it
    </button>
    <div id="kit-verify"></div>`,
  () => {
    q("#btn-verify-kit")?.addEventListener("click", () => {
      const host = q("#kit-verify");
      host.innerHTML = `
        <div class="form-group" style="margin-top:16px">
          <label class="form-label" for="kit-input">Type the 24 words back</label>
          <textarea id="kit-input" class="compose-textarea" rows="4"
                    placeholder="word1 word2 …" spellcheck="false"></textarea>
        </div>
        <button class="btn btn-primary" id="btn-check-kit" style="width:100%">Check</button>
        <p id="kit-result" class="small" style="margin-top:10px;min-height:20px"></p>`;
      q("#btn-check-kit")?.addEventListener("click", () => {
        const result = q("#kit-result");
        // Verified against the seed we just rendered, so a kit that "looks
        // right" but decodes to something else is caught here, not in a year.
        if (verifyRecoveryKit(q("#kit-input").value, keys.seed)) {
          result.textContent = "✓ That's your kit. Store it somewhere safe.";
          result.className = "small val-ok";
        } else {
          result.textContent = "✕ That doesn't match. Check the spelling and the order.";
          result.className = "small val-warn";
        }
      });
    });
  });
}

// ─── Settings actions ─────────────────────────────────────────────────────────

function showIdentityKeysPanel() {
  const rec = loadIdentityRecord(S.identity);
  if (!rec) return;
  showPanel("Identity keys", `
    <div class="kv-row"><span class="kv-label">Identity</span><span class="kv-value mono small">${esc(rec.identity)}</span></div>
    <div class="kv-row"><span class="kv-label">Signing key</span><span class="kv-value mono small truncate">${esc(rec.publicKey.slice(0,24))}…</span></div>
    <div class="kv-row"><span class="kv-label">Enc key</span><span class="kv-value mono small truncate">${esc(rec.encPublicKey.slice(0,24))}…</span></div>
    <div class="kv-row"><span class="kv-label">Relay</span><span class="kv-value small">${esc(rec.relay)}</span></div>
    <div class="kv-row"><span class="kv-label">Created</span><span class="kv-value small">${esc(fmtTime(rec.createdAt))}</span></div>
    <div class="kv-row"><span class="kv-label">Protection</span>
      <span class="chip chip-green">${rec.encryptedKeys?.kdf === "native" ? "Device keystore" : "Passkey PRF"}</span>
    </div>
    <div class="kv-row"><span class="kv-label">DNS</span>
      <code class="kv-value small" style="font-size:11px;line-height:1.6">_poweur.${esc(rec.identity)}<br>_poweur-enc.${esc(rec.identity)}</code>
    </div>`);
}

function showLookupPanel() {
  showPanel("Lookup identity", `
    <p class="muted small" style="margin-bottom:12px">Resolve keys via well-known / relay API (then DNS), like <code>poweur identity lookup</code>.</p>
    <div class="form-group">
      <label class="form-label" for="lookup-id">Identity</label>
      <input id="lookup-id" class="input" type="text" placeholder="bob.poweur.net" autocomplete="off" spellcheck="false" />
    </div>
    <button class="btn btn-primary mt-sm" id="btn-lookup-run">Lookup</button>
    <pre id="lookup-result" class="mono small" style="margin-top:14px;white-space:pre-wrap;word-break:break-all"></pre>`,
  () => {
    q("#btn-lookup-run")?.addEventListener("click", async () => {
      const id = q("#lookup-id")?.value.trim().toLowerCase();
      const out = q("#lookup-result");
      if (!id) return toast("Enter an identity", "warning");
      if (out) out.textContent = "Looking up…";
      try {
        const { source, document } = await lookup(id, relayUrlFor(S.identity));
        if (out) {
          out.textContent = [
            `identity: ${document.identity}`,
            `source: ${source}`,
            `public_key: ${document.public_key || ""}`,
            `safety number: ${document.public_key ? fingerprintOrKey(document.public_key) : ""}`,
            `encryption_public_key: ${document.encryption_public_key || ""}`,
            `relay: ${document.relay || ""}`,
            `capabilities: ${(document.capabilities || []).join(", ")}`,
          ].join("\n");
        }
      } catch (e) {
        if (out) out.textContent = `Error: ${e.message}`;
        toast(e.message, "error");
      }
    });
  });
}

function showRelayPanel() {
  showPanel("Relay", `
    <div class="form-group">
      <label class="form-label">Relay URL</label>
      <input id="panel-relay-url" class="input" type="url" value="${esc(S.config.relayUrl)}" />
    </div>
    <div style="display:flex;gap:10px;margin-top:4px">
      <button class="btn btn-ghost btn-sm" id="panel-test-relay" style="flex:1">Test</button>
      <button class="btn btn-primary btn-sm" id="panel-save-relay" style="flex:1">Save</button>
    </div>
    <p id="panel-relay-status" style="font-size:13px;color:var(--t2);margin-top:10px;min-height:18px"></p>`,
  () => {
    q("#panel-test-relay")?.addEventListener("click", async () => {
      const url = q("#panel-relay-url").value.trim();
      const s = q("#panel-relay-status");
      s.textContent = "Testing…"; s.style.color = "var(--t2)";
      try { await identityApiFor(url).health(); s.textContent = "✓ Connected"; s.style.color = "var(--green)"; }
      catch (e) { s.textContent = `✕ ${e.message}`; s.style.color = "var(--red)"; }
    });
    q("#panel-save-relay")?.addEventListener("click", () => {
      const url = q("#panel-relay-url").value.trim();
      S.config = { ...S.config, relayUrl: url };
      saveConfig(S.config);
      toast("Saved", "success");
      closePanel();
    });
  });
}

function showSessionPanel() {
  const sess = loadSessionRecord(S.identity);
  const valid = sessionIsValid(sess);
  if (!sess) return;
  showPanel("Session", `
    <div class="kv-row"><span class="kv-label">Status</span>
      <span class="chip ${valid ? "chip-green" : "chip-red"}">${valid ? "Active" : "Expired"}</span>
    </div>
    <div class="kv-row"><span class="kv-label">Session ID</span><span class="kv-value mono small truncate">${esc(sess.sessionId)}</span></div>
    <div class="kv-row"><span class="kv-label">Issued</span><span class="kv-value small">${esc(fmtTime(sess.issuedAt))}</span></div>
    <div class="kv-row"><span class="kv-label">Expires</span><span class="kv-value small">${esc(fmtTime(sess.expiresAt))}</span></div>
    <div class="stack mt-md">
      <button class="btn btn-primary" id="panel-refresh-sess">Refresh session</button>
      <button class="btn btn-ghost" id="panel-revoke-sess" style="color:var(--red)">Revoke session</button>
    </div>`,
  () => {
    q("#panel-refresh-sess")?.addEventListener("click", async () => {
      if (!getUnlockedKeys()) return toast("Unlock first", "warning");
      closePanel();
      setLoading(true, "Refreshing session…");
      try {
        await clientFor(S.identity).sessions.refresh(clientFor(S.identity).signer);
        setLoading(false); toast("Session refreshed", "success"); render();
      } catch (e) { setLoading(false); toast(e.message, "error"); }
    });
    q("#panel-revoke-sess")?.addEventListener("click", async () => {
      if (!getUnlockedKeys()) return toast("Unlock first", "warning");
      closePanel();
      await doRevokeSession();
    });
  });
}

function showDnsPanel() {
  showPanel("DNS provider", `
    <div class="form-group">
      <label class="form-label">Provider</label>
      <select id="panel-dns-provider" class="input select">
        <option value="cloudflare"${S.config.dnsProvider==="cloudflare"?" selected":""}>Cloudflare</option>
        <option value="hetzner"${S.config.dnsProvider==="hetzner"?" selected":""}>Hetzner</option>
      </select>
    </div>
    <div class="form-group">
      <label class="form-label">Parent domain</label>
      <input id="panel-parent-domain" class="input" type="text" value="${esc(S.config.parentDomain)}" placeholder="poweur.net" />
    </div>
    <button class="btn btn-primary mt-sm" id="panel-save-dns">Save</button>`,
  () => {
    q("#panel-save-dns")?.addEventListener("click", () => {
      S.config = { ...S.config,
        dnsProvider: q("#panel-dns-provider").value,
        parentDomain: q("#panel-parent-domain").value.trim(),
      };
      saveConfig(S.config); toast("Saved", "success"); closePanel();
    });
  });
}

async function doRotateEncKey() {
  const client = clientFor(S.identity);
  if (!client) return toast("Unlock your identity first", "warning");
  const id  = S.identity;
  const rec = loadIdentityRecord(id);
  const keys = getUnlockedKeys();

  setLoading(true, "Rotating encryption key…");
  try {
    const { encJWK: encPrivNew, encPublicKey } = await generateEncryptionJwk();
    await client.identity.publishEncryptionKey(client.signer, encPublicKey);

    let encryptedKeys;
    if (rec.encryptedKeys?.kdf === "native") {
      encryptedKeys = await wrapKeysNative(id, keys.signingJWK, encPrivNew, keys.seed, {
        gate: rec.encryptedKeys.gate,
        reason: `Protect ${id.split(".")[0]}`,
      });
    } else {
      const { prfOutput } = await authenticatePasskey(rec.credentialId, { rpId: rpIdFor(S.identity) });
      if (!prfOutput) throw new Error(PRF_UNAVAILABLE_MESSAGE);
      encryptedKeys = await wrapKeysWithPRF(prfOutput, keys.signingJWK, encPrivNew, keys.seed);
    }

    // The encryption key no longer derives from the seed, so a kit rebuilt from
    // it would restore the *old* one. Say so rather than hand out a stale kit.
    saveIdentityRecord(id, { ...rec, encPublicKey, encryptedKeys, seedDerived: false });
    setUnlockedKeys(id, keys.signingJWK, encPrivNew, keys.seed);
    setLoading(false); toast("Encryption key rotated", "success"); render();
  } catch (e) { setLoading(false); toast(e.message, "error"); }
}

async function doRevokeSession() {
  const client = clientFor(S.identity);
  if (!client || !loadSessionRecord(S.identity)) return;

  setLoading(true, "Revoking session…");
  try {
    await client.sessions.revoke(client.signer);
    setLoading(false); toast("Session revoked", "success"); render();
  } catch (e) { setLoading(false); toast(e.message, "error"); }
}

function doRemoveIdentity() {
  showPanel("Remove identity", `
    <p style="color:var(--t1);margin-bottom:12px">Remove <strong>${esc(S.identity)}</strong> from this device?</p>
    <p class="muted small">This only removes the local record. Your identity remains registered on the relay and in DNS.</p>
    <div class="stack mt-md">
      <button class="btn btn-primary" id="panel-confirm-remove" style="background:var(--red)">Remove from device</button>
      <button class="btn btn-ghost" id="panel-cancel-remove">Cancel</button>
    </div>`,
  () => {
    q("#panel-confirm-remove")?.addEventListener("click", () => {
      const removed = S.identity;
      removeIdentity(removed);
      removeSessionRecord(removed);
      // The wrapped blob is gone; leaving its hardware secret behind would be
      // an entry nothing can ever open, kept for the life of the install.
      forgetNativeSecret(removed);
      switchIdentity(listIdentities()[0] || null);
      closePanel();
      R.go("messages");
      toast("Identity removed from device", "info");
    });
    q("#panel-cancel-remove")?.addEventListener("click", closePanel);
  });
}

// ─── Bottom sheet panel ───────────────────────────────────────────────────────

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), ' +
  'textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

/**
 * The slide-up panel, which is a modal dialog and has to behave like one:
 * focus moves in, Tab stays inside, Escape closes, and focus goes back where
 * it came from. Without that a keyboard user tabs into the page *behind* the
 * panel — and every dialog in this app is a decision (share, block, trust a
 * changed key), so losing the user's place in it is not cosmetic.
 */
function showPanel(title, bodyHtml, afterRender, onClose) {
  const backdrop = document.getElementById("panel-backdrop");
  const panel    = document.getElementById("panel-root");
  // Remember *which* control opened this, not the node: the shell re-renders
  // from strings, so any data the panel loads replaces the element that was
  // focused, and a node reference would be detached by the time we close.
  const returnToId = document.activeElement instanceof HTMLElement
    ? document.activeElement.id
    : "";

  panel.innerHTML = `
    <div class="panel-handle"></div>
    <div class="panel-header">
      <span class="panel-title" id="panel-title">${esc(title)}</span>
      <button class="btn-icon" id="panel-close-btn" aria-label="Close">✕</button>
    </div>
    <div class="panel-body">${bodyHtml}</div>`;
  panel.setAttribute("role", "dialog");
  panel.setAttribute("aria-modal", "true");
  panel.setAttribute("aria-labelledby", "panel-title");
  backdrop.classList.remove("hidden");
  panel.classList.remove("hidden");

  const onKeydown = (event) => {
    if (event.key === "Escape") { event.preventDefault(); close(); return; }
    if (event.key !== "Tab") return;
    const focusable = [...panel.querySelectorAll(FOCUSABLE)].filter(el => el.offsetParent !== null);
    if (!focusable.length) return;
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault(); last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault(); first.focus();
    }
  };

  const close = () => {
    document.removeEventListener("keydown", onKeydown, true);
    panel.classList.add("hidden");
    backdrop.classList.add("hidden");
    panel.removeAttribute("role");
    panel.removeAttribute("aria-modal");
    // Back where it came from — or nowhere in particular, rather than parked
    // on a control inside a panel the user can no longer see.
    const returnTo = returnToId ? document.getElementById(returnToId) : null;
    if (returnTo) returnTo.focus();
    else if (panel.contains(document.activeElement)) document.activeElement.blur();
    onClose?.();
  };

  document.addEventListener("keydown", onKeydown, true);
  backdrop.onclick = close;
  q("#panel-close-btn")?.addEventListener("click", close);
  afterRender?.(close);

  // Focus the first thing worth typing into, else the close button — never
  // nothing, which is what leaves the focus ring behind the backdrop.
  const target = panel.querySelector("input:not([type=file]), textarea, .btn-primary")
    ?? q("#panel-close-btn");
  target?.focus();
}

function closePanel() {
  document.getElementById("panel-root")?.classList.add("hidden");
  document.getElementById("panel-backdrop")?.classList.add("hidden");
}

// ─── Toast ────────────────────────────────────────────────────────────────────

function toast(msg, type = "info", duration = 3500) {
  const root = document.getElementById("toast-root");
  if (!root) return;
  // A backgrounded join poller used to dump the same 404 a dozen times when
  // the phone woke up. Same message + kind already on screen stays one toast.
  const key = `${type}:${msg}`;
  if ([...root.querySelectorAll(".toast")].some((el) => el.dataset.toastKey === key)) return;
  const el = document.createElement("div");
  el.className = `toast ${type}`;
  el.dataset.toastKey = key;
  el.innerHTML = `<span class="toast-icon"></span><span>${esc(msg)}</span>`;
  root.appendChild(el);
  setTimeout(() => el.remove(), duration + 400);
}

// ─── Loading ──────────────────────────────────────────────────────────────────

function setLoading(active, text = "Working…") {
  const el = document.getElementById("loading-root");
  const tx = document.getElementById("loading-text");
  if (!el) return;
  el.classList.toggle("hidden", !active);
  if (tx) tx.textContent = text;
}

// ─── Theme ────────────────────────────────────────────────────────────────────

function initTheme() {
  const saved = localStorage.getItem("poweur:theme");
  const theme = saved || (matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light");
  document.documentElement.dataset.theme = theme;
}

function toggleTheme() {
  const next = document.documentElement.dataset.theme === "dark" ? "light" : "dark";
  document.documentElement.dataset.theme = next;
  localStorage.setItem("poweur:theme", next);
  render();
}

// ─── Boot ─────────────────────────────────────────────────────────────────────

function boot() {
  initTheme();
  S.identity = getActiveIdentity();
  S.config   = getConfig();
  const authInput = new URL(globalThis.location?.href ?? "http://localhost/").searchParams.get("auth") || "";

  // A hand-off from the launcher arrives as a fragment and is adopted before
  // anything else looks at storage (EPIC-018 E18-T3).
  const handedOver = adoptHandOff();

  if (authInput) {
    R.page = "settings";
    R.sub = "auth";
    S.auth.input = authInput;
  } else if (handedOver) {
    // Locked on arrival: the keys are wrapped, and the passkey that opens them
    // works here because it is scoped to the domain both hosts share (E18-T4).
    R.page = "messages";
    R.push("unlock");
  } else if (!S.identity) {
    R.page = "messages";
  } else if (!getUnlockedKeys() && !sessionIsValid(loadSessionRecord(S.identity))) {
    // Auto-push unlock only if session is gone; otherwise session key in
    // sessionStorage lets us reload without re-auth.
    R.push("unlock");
  } else {
    R.page = "messages";
  }

  render();
  if (authInput) beginSignInApproval(authInput);

  // The door depends on the relay's answer, and the first paint above came
  // from the cached one (or from `unknown`, which renders the generic welcome
  // — the safe thing to show while we find out). Correct it when it lands.
  resolveMode().then((info) => {
    setDocumentIdentity(info);
    if (!S.identity) render();
  });
}

/**
 * Title and description per door (E15-T12).
 *
 * `poweur.net` is a landing page a search engine will index and a tab someone
 * keeps open; `bob.poweur.net` is neither. Before this every host claimed to
 * be "Poweur ID".
 */
function setDocumentIdentity(info) {
  const title = {
    launcher: "Poweur ID — claim your name",
    identity: info.subject ? `${info.subject} — Poweur ID` : "Poweur ID",
    shell: "Poweur ID",
  }[info.mode] ?? "Poweur ID";
  const description = {
    launcher: "Claim an identity you own: encrypted messages, a synced drive, and sign-in — under your own name.",
    identity: `Sign in to ${info.subject || "this identity"} with a passkey.`,
  }[info.mode] ?? "Encrypted, identity-first messaging.";

  document.title = title;
  let meta = document.querySelector('meta[name="description"]');
  if (!meta) {
    meta = document.createElement("meta");
    meta.setAttribute("name", "description");
    document.head.appendChild(meta);
  }
  meta.setAttribute("content", description);
}

boot();


function showAnalyticsPanel() {
  const identity = S.identity;
  const client = clientFor(identity);
  if (!client) return toast("Unlock your identity first", "warning");
  showPanel("Relay analytics", `<p>When your relay exports analytics, timestamps and actions are recorded. With detailed analytics off, your identity is hashed and your IP is omitted. Turning it on includes your raw identity and IP. Message contents and keys are never included.</p><div id="analytics-host" role="status">Loading…</div>`, async (close) => {
    const host = q("#analytics-host");
    try {
      const pref = await client.analyticsPreference();
      if (!host?.isConnected || S.identity !== identity) return;
      host.innerHTML = `<label><input id="analytics-consent" type="checkbox" ${pref?.granted ? "checked" : ""}> Allow detailed relay analytics for ${esc(identity)}</label><p class="small muted">Changes affect future exports. Existing records expire under the relay's retention settings.</p><button class="btn btn-primary" id="analytics-save">Save</button>`;
      q("#analytics-save").addEventListener("click", async (event) => {
        if (S.identity !== identity) return;
        const button = event.currentTarget;
        button.disabled = true;
        try {
          await client.setAnalyticsConsent(q("#analytics-consent").checked);
          toast("Analytics preference saved", "success");
          close();
        } catch (error) {
          toast(error.message, "error");
          button.disabled = false;
        }
      });
    } catch (error) { if (host?.isConnected) host.textContent = `Could not load preference: ${error.message}`; }
  });
}
