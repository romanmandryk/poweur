/**
 * Poweur ID Web Client — SPA controller.
 *
 * UI only: the protocol lives in `@poweur/client` (EPIC-015 E15-T6), key
 * custody in `js/vault.js`, and every relay call is made through the
 * `PoweurClient` that `js/client.js` builds for the active identity.
 *
 * Five destinations (E15-T1): messages | contacts | files | launcher | settings
 * Sub-pages (full-screen, back button): add-id | unlock | compose | onboarding
 */

import {
  createIdentity, formatBytes, isSessionValid as sessionIsValid, ROOT_INFO,
  EnrollApi, RelayClient, sendAnonymous, clampPowBits,
  SHARE_ROOTS, DEFAULT_CHUNK_THRESHOLD, grantExpired, grantAllowsWrite, SyncClient,
  streamForever,
} from "@poweur/client";

import {
  createPasskey, authenticatePasskey,
  wrapKeysWithPRF, unwrapKeysWithPRF, checkPasskeySupport,
} from "./passkey.js";

import {
  generateSeedIdentityJwks, generateEncryptionJwk, jwksFromSeed, keyBytesFromJwks,
  publicKeyFromJwk, wrapKeysWithPin, unwrapKeysWithPin, toBase64url, fromBase64url,
} from "./vault.js";

import {
  getConfig, saveConfig,
  saveIdentityRecord, loadIdentityRecord, listIdentities, removeIdentity,
  getActiveIdentity, setActiveIdentity,
  loadSessionRecord, removeSessionRecord, rpIdFor,
  setUnlockedKeys, getUnlockedKeys, clearUnlockedKeys,
  defaultRelayUrl, relayUrlFor,
} from "./storage.js";

import { clientFor, identityApiFor, lookup, resolveOptionsForRelay } from "./client.js";

import { resolveProfile, clearProfileCache, primeProfile } from "./profiles.js";
import { IdentityInput } from "./components/identity-input.js";
import { PolicyControls, INBOX_MODES, describePowBits } from "./components/policy-controls.js";
import { AudiencePicker } from "./components/audience-picker.js";
import { ProfileCard } from "./components/profile-card.js";
import {
  listEnrollments, enrollThisBrowser, removeEnrollment, deviceLabel,
  recoveryKitEligibility, buildRecoveryKit, verifyRecoveryKit,
  recoverFromKeystore, rewrap,
} from "./keystore.js";

// ─── Router & State ───────────────────────────────────────────────────────────

/** The five primary destinations, in nav order. */
const DESTINATIONS = ["messages", "contacts", "files", "launcher", "settings"];

const R = {
  page: "messages",      // messages | contacts | files | launcher | settings
  sub: null,             // null | add-id | unlock | compose
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

const S = {
  config: getConfig(),
  identity: getActiveIdentity(),
  messages: [],
  acks: [],
  dropdownOpen: false,
  /** Messages destination: inbox | requests | anonymous, as distinct trays. */
  tray: "inbox",
  contacts: { list: [], loading: false, loaded: false, error: null, filter: "" },
  requests: { incoming: [], loading: false, loaded: false, error: null, fetchedAt: 0 },
  anon: { messages: [], loading: false, loaded: false, error: null, fetchedAt: 0 },
  policy: { doc: null, explicit: false, loading: false, loaded: false },
  profile: { doc: null, explicit: false, loaded: false, loading: false },
  /** null unless the first-run flow is on screen. */
  onboard: null,
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

function render() {
  const app = document.getElementById("app");
  if (!app) return;

  if (R.sub) {
    app.innerHTML = renderSubPage();
  } else {
    app.innerHTML = `
      ${renderHeader()}
      <div class="page-content" id="page-content">${renderPage()}</div>
      ${renderBottomNav()}`;
  }
  flushMounts();
  attachEvents();
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
  if (tray === "anonymous") return S.anon.messages.length;
  return 0;
}

/** The contact record for an identity, or null — the app's one lookup. */
function contactFor(identity) {
  const wanted = String(identity ?? "").toLowerCase();
  return S.contacts.list.find(c => c.identity.toLowerCase() === wanted) ?? null;
}

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
        🔑 Unlock with passkey
      </button>
      <p class="form-note small" style="max-width:220px">
        Your device will prompt for ${rec?.supportsPRF !== false ? "biometric" : "PIN"} authentication
      </p>
    </div>`;
}

function renderMessages() {
  return `
    <div class="dest-header">
      <h1 class="dest-title">Messages</h1>
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
        <div class="conv-row" data-compose-to="${esc(c.contact)}" role="button" tabindex="0">
          ${avatarHtml(c.contact, "md")}
          <div class="conv-info">
            <div class="conv-name">${esc(c.petname || idHandle(c.contact))}</div>
            <div class="conv-preview">${esc(c.preview)}</div>
          </div>
          <div class="conv-meta">
            <span class="conv-time">${fmtRelative(c.lastMsg.timestamp)}</span>
            ${c.unread ? `<span class="conv-badge">${c.unread}</span>` : ""}
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
    add({ sender: entry.sender, timestamp: entry.timestamp, intro: null, queued: true });
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

function buildConversations() {
  const byContact = {};
  for (const raw of S.messages) {
    const m = typeof raw === "string" ? JSON.parse(raw) : raw;
    const contact = m.sender === S.identity ? m.recipient : m.sender;
    if (!byContact[contact]) byContact[contact] = [];
    byContact[contact].push(m);
  }
  return Object.entries(byContact)
    .map(([contact, msgs]) => {
      const lastMsg = msgs.at(-1);
      const known = contactFor(contact);
      return {
        contact,
        lastMsg,
        petname: known?.petname ?? null,
        // Someone we have no entry for at all: adding them is one tap from
        // the message that made us want to.
        stranger: !known,
        unread: msgs.length,
        // The SDK decrypts in place, so show the message rather than a padlock
        // when we could actually read it.
        preview: lastMsg.plaintext ?? "🔒 Could not decrypt",
      };
    })
    .sort((a, b) => new Date(b.lastMsg.timestamp) - new Date(a.lastMsg.timestamp));
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

// ─── Launcher page ────────────────────────────────────────────────────────────

function parseCreateHash() {
  const hash = window.location.hash;
  if (!hash.startsWith("#create=")) return null;
  try { return JSON.parse(atob(hash.slice("#create=".length))); } catch { return null; }
}

function renderLauncher() {
  const cfg = S.config;
  const step2 = parseCreateHash();

  if (step2) {
    // Step 2 — on the identity's own domain, collect DNS credentials and create
    const { handle, domain } = step2;
    return `
      <div class="launcher-form-page">
        <div class="section-label">New identity — step 2 of 2</div>
        <div class="form-card">
          <div class="form-group">
            <label class="form-label">Handle</label>
            <input class="input" type="text" value="${esc(handle)}" disabled />
          </div>
          <div class="form-group">
            <label class="form-label">Domain</label>
            <input class="input" type="text" value="${esc(domain)}" disabled />
          </div>
          <div class="form-group">
            <label class="form-label" style="display:flex;align-items:center;gap:8px">
              <input type="checkbox" id="ni-hosted" checked />
              Hosted registration (no DNS token — identity under this relay's domain)
            </label>
          </div>
          <div class="form-group">
            <label class="form-label" for="ni-invite">Invite code (if required by relay)</label>
            <input id="ni-invite" class="input" type="text" placeholder="optional" autocomplete="off" />
          </div>
          <div id="ni-dns-fields" style="display:none">
            <div class="form-group">
              <label class="form-label" for="ni-provider">DNS provider (self-hosted only)</label>
              <select id="ni-provider" class="input select">
                <option value="cloudflare"${cfg.dnsProvider==="cloudflare"?" selected":""}>Cloudflare</option>
                <option value="hetzner"${cfg.dnsProvider==="hetzner"?" selected":""}>Hetzner</option>
              </select>
            </div>
            <div class="form-group">
              <label class="form-label" for="ni-token">DNS API token</label>
              <input id="ni-token" class="input" type="password" placeholder="Scoped API token" autocomplete="off" />
            </div>
          </div>
          <button class="btn btn-passkey" id="btn-create-id" style="width:100%;margin-top:4px">
            🔑 Create with passkey
          </button>
          <p class="form-note small" style="margin-top:10px;text-align:center">Keys are generated locally and never leave your device in plain form.</p>
        </div>
      </div>`;
  }

  // Step 1 — pick a handle; hosted stays on this relay, DNS mode redirects to identity host
  return `
    <div class="launcher-form-page">
      <div class="section-label">New identity — step 1 of 2</div>
      <div class="form-card">
        <div class="form-group">
          <label class="form-label" for="ni-handle">Handle</label>
          <input id="ni-handle" class="input" type="text" placeholder="alice" autocomplete="off" spellcheck="false"
                 aria-describedby="ni-availability" />
          <p class="idin-status small" id="ni-availability" role="status" aria-live="polite"></p>
        </div>
        <div class="form-group">
          <label class="form-label" for="ni-domain">Parent domain</label>
          <input id="ni-domain" class="input" type="text" value="${esc(cfg.parentDomain || "poweur.net")}" autocomplete="off" />
        </div>
        <div class="form-group">
          <label class="form-label" style="display:flex;align-items:center;gap:8px">
            <input type="checkbox" id="ni-hosted-step1" checked />
            Hosted on this relay (no DNS token)
          </label>
        </div>
        <button class="btn btn-primary" id="btn-next-id" style="width:100%;margin-top:4px">
          Next →
        </button>
        <p class="form-note small" style="margin-top:10px;text-align:center" id="ni-step1-note">
          Hosted: create passkey on this relay. Uncheck for self-hosted DNS (redirects to your subdomain).
        </p>
      </div>
    </div>`;
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
        <span class="chip ${rec.supportsPRF !== false ? "chip-green" : "chip-orange"}" style="margin-top:4px">
          ${rec.supportsPRF !== false ? "🔑 Passkey (PRF)" : "🔐 PIN-protected"}
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
        <div class="settings-row" role="button" tabindex="0" id="row-dns">
          <span class="settings-row-icon">🌐</span>
          <span class="settings-row-label">DNS provider</span>
          <span class="settings-row-value">${esc(S.config.dnsProvider || "Cloudflare")}</span>
          <span class="settings-row-arrow">›</span>
        </div>
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
        <div class="settings-row no-action">
          <span class="settings-row-icon">📋</span>
          <span class="settings-row-label">Protocol</span>
          <span class="settings-row-value">Poweur ID v1</span>
        </div>
        <div class="settings-row no-action">
          <span class="settings-row-icon">💻</span>
          <span class="settings-row-label">Client</span>
          <span class="settings-row-value">Web</span>
        </div>
      </div>
    </div>
    <div style="height:32px"></div>`;
}

// ─── Sub-pages ────────────────────────────────────────────────────────────────

function renderSubPage() {
  switch (R.sub) {
    case "add-id":  return renderAddId();
    case "unlock":  return renderUnlock();
    case "compose": return renderCompose();
    case "onboarding": return renderOnboarding();
    default:        return renderAddId();
  }
}

// Add ID ───────────────────────────────────────────────────────────────────────

function renderAddId() {
  return `
    <div class="sub-page">
      <div class="sub-header">
        <button class="btn-back" id="btn-back">${svgBack}</button>
        <span class="sub-title">Add identity</span>
      </div>
      <div class="sub-body">
        <p class="muted small" style="margin-bottom:20px">Connect or create a Poweur ID identity on this device.</p>
        <div class="option-list">
          <div class="option-card option-card-form-wrap">
            <div class="option-card-top">
              <div class="option-icon-wrap">🔑</div>
              <div class="option-body">
                <div class="option-title">Sign in with existing passkey</div>
                <div class="option-desc">Enter your identity to authenticate on this device</div>
              </div>
            </div>
            <div class="option-inline-form">
              <input id="signin-id-input" class="input" type="text" placeholder="alice.poweur.net"
                autocomplete="off" spellcheck="false" inputmode="url" />
              <button class="btn btn-primary" id="btn-signin-passkey">Sign in</button>
            </div>
          </div>

          <button class="option-card" id="opt-join-device">
            <div class="option-icon-wrap">📱</div>
            <div class="option-body">
              <div class="option-title">Add this device to an existing ID</div>
              <div class="option-desc">Show a code, approve it on a device you already use</div>
            </div>
            <span class="option-arrow">›</span>
          </button>

          <button class="option-card" id="opt-create-new">
            <div class="option-icon-wrap">✨</div>
            <div class="option-body">
              <div class="option-title">Add new ID</div>
              <div class="option-desc">Create a fresh identity with a passkey</div>
            </div>
            <span class="option-arrow">›</span>
          </button>
        </div>
      </div>
    </div>`;
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
          🔑 Authenticate with passkey
        </button>
        <p class="form-note small" style="max-width:220px">
          ${rec.supportsPRF !== false ? "Touch ID, Face ID, or Windows Hello" : "You'll be asked for your PIN"}
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
        await client.setPolicy(document.mode, document.anonymous);
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

  // FAB compose
  q("#btn-compose")?.addEventListener("click", () => R.push("compose"));

  // Conversation row → compose to that contact
  qAll(".conv-row[data-compose-to]").forEach(r =>
    r.addEventListener("click", () => R.push("compose", { to: r.dataset.composeTo })));

  // A push stream costs one connection and saves every poll after it.
  if (getUnlockedKeys()) startEventStream();

  // Load inbox when unlocked and on the Messages destination
  if (R.page === "messages" && !R.sub && getUnlockedKeys()) {
    loadInbox();
    // Always, not only on the Requests tray: an accept to a request *we* sent
    // arrives here, and the handshake only completes once we have read it.
    loadRequests();
    // The anonymous queue is drained here too, so its badge is honest before
    // the tray is opened — but only for the few who turned anonymous on, so
    // everyone else pays nothing for a queue that is always empty.
    loadPolicy();
    if (S.tray === "anonymous" || S.policy.doc?.anonymous?.allow) loadAnon();
  }
  q("#btn-anon-settings")?.addEventListener("click", () => R.go("settings"));

  // Add ID options
  q("#opt-join-device")?.addEventListener("click", showJoinDevicePanel);
  q("#btn-signin-passkey")?.addEventListener("click", doSignInWithPasskey);
  q("#signin-id-input")?.addEventListener("keydown", e => { if (e.key === "Enter") doSignInWithPasskey(); });
  q("#opt-create-new")?.addEventListener("click", () => { R.sub = null; R.go("launcher"); });

  // Launcher step 1 → hosted stays here; DNS mode redirects to identity host
  q("#btn-next-id")?.addEventListener("click", doNextIdentityStep);
  q("#ni-handle")?.addEventListener("keydown", e => { if (e.key === "Enter") doNextIdentityStep(); });
  attachAvailabilityCheck();

  // Launcher step 2 → create identity + toggle DNS fields
  q("#btn-create-id")?.addEventListener("click", doCreateIdentity);
  const hostedCb = q("#ni-hosted");
  const dnsFields = q("#ni-dns-fields");
  const syncDnsVisibility = () => {
    if (dnsFields) dnsFields.style.display = hostedCb?.checked ? "none" : "block";
  };
  hostedCb?.addEventListener("change", syncDnsVisibility);
  syncDnsVisibility();

  // Unlock sub-page
  q("#btn-do-unlock")?.addEventListener("click", doUnlock);

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
  q("#row-identity-keys")?.addEventListener("click", showIdentityKeysPanel);
  q("#row-relay")?.addEventListener("click", showRelayPanel);
  q("#row-lookup")?.addEventListener("click", showLookupPanel);
  q("#row-session")?.addEventListener("click", showSessionPanel);
  q("#row-dns")?.addEventListener("click", showDnsPanel);
  q("#row-profile")?.addEventListener("click", showProfilePanel);
  q("#row-policy")?.addEventListener("click", showPolicyPanel);
  q("#row-policy-anon")?.addEventListener("click", showPolicyPanel);
  q("#row-keys-devices")?.addEventListener("click", showKeysAndDevicesPanel);
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

async function doSignInWithPasskey() {
  const fqdn = q("#signin-id-input")?.value.trim().toLowerCase();
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
    if (rec.supportsPRF !== false && rec.encryptedKeys?.kdf === "prf") {
      const { prfOutput } = await authenticatePasskey(rec.credentialId, { rpId: rpIdFor(S.identity) });
      if (!prfOutput) throw new Error("PRF not available from this authenticator.");
      opened = await unwrapKeysWithPRF(prfOutput, rec.encryptedKeys);
    } else {
      setLoading(false);
      const pin = await promptPin("Enter your PIN:");
      if (!pin) return;
      setLoading(true, "Unlocking…");
      opened = await unwrapKeysWithPin(pin, rec.encryptedKeys);
    }

    setUnlockedKeys(id, opened.signingJWK, opened.encJWK, opened.seed ?? null);

    if (!sessionIsValid(loadSessionRecord(id))) {
      setLoading(true, "Creating session…");
      await ensureSession(id);
    }

    setLoading(false);
    toast("Unlocked", "success");
    R.sub = null; R.params = {};
    render(); // attachEvents starts the inbox fetch

  } catch (err) {
    setLoading(false);
    toast(err.message, "error");
  }
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

  setLoading(false);
  try {
    await adoptIdentity({
      identity,
      relayUrl,
      signingJWK: recovered.signingJWK,
      encJWK: recovered.encJWK,
      seed: recovered.seed,
      label: `${deviceLabel()} (restored)`,
    });
    toast(`${identity} restored on this device`, "success", 5000);
  } catch (error) {
    toast(error.message, "error", 9000);
  }
}

/**
 * Take ownership of key material this browser did not generate — from a
 * keystore restore or a device-enrollment ceremony.
 *
 * Creates a local passkey to wrap it, stores the record, and registers the new
 * enrollment so this browser becomes a recovery path in its own right rather
 * than a copy that only works until its site data is cleared.
 */
async function adoptIdentity({ identity, relayUrl, signingJWK, encJWK, seed, label }) {
  const support = await checkPasskeySupport();
  if (!support.available) throw new Error(`Passkey unavailable: ${support.reason}`);

  setLoading(true, "Creating a passkey on this device…");
  const userId = toBase64url(crypto.getRandomValues(new Uint8Array(16)));
  const { credentialId, prfOutput, supportsPRF, credentialPublicKey, credentialAlg, rpId: credentialScope } =
    await createPasskey(identity, userId);

  let encryptedKeys;
  if (supportsPRF) {
    setLoading(true, "Securing keys…");
    encryptedKeys = await rewrap({ prfOutput }, { signingJWK, encJWK, seed });
  } else {
    setLoading(false);
    const pin = await promptPin("Set a PIN to protect your keys:", true);
    if (!pin) throw new Error("Cancelled");
    setLoading(true, "Securing keys…");
    encryptedKeys = await rewrap({ pin }, { signingJWK, encJWK, seed });
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
 * The joining half of the enrollment ceremony (E11-T3), run on the **new**
 * device.
 *
 * This device generates the ephemeral keypair, so the six-digit code
 * authenticates a public key and protects no secret — which is why the
 * ceremony needs no PAKE. It polls until the other device approves.
 */
function showJoinDevicePanel() {
  let session = null;
  let joining = null;
  let polling = null;
  const relayUrl = defaultRelayUrl();
  const enroll = new EnrollApi(new RelayClient(relayUrl));

  const stop = () => { clearInterval(polling); polling = null; };

  showPanel("Add this device", `
    <p class="muted small" style="margin-bottom:12px">
      Enter your identity. This device will show a code to type on a device you already use.
    </p>
    <div class="form-group">
      <label class="form-label" for="join-identity">Your Poweur ID</label>
      <input id="join-identity" class="input" type="text" placeholder="alice.poweur.net"
             autocomplete="off" spellcheck="false" inputmode="url" />
    </div>
    <button class="btn btn-primary" id="btn-join-start" style="width:100%">Show my code</button>
    <div id="join-state"></div>`,
  () => {
    q("#btn-join-start")?.addEventListener("click", async () => {
      const identity = q("#join-identity")?.value.trim().toLowerCase();
      if (!identity) return toast("Enter your identity", "warning");

      setLoading(true, "Opening a secure channel…");
      try {
        // offer/claim/cancel are unauthenticated by necessity — this device has
        // no key yet — so they need the endpoint, not a signer.
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
          <p class="small muted" id="join-wait">Waiting for approval…</p>`;
        q("#btn-copy-rendezvous")?.addEventListener("click", async () => {
          try {
            await navigator.clipboard.writeText(session.rendezvousId);
            toast("Request code copied", "success", 2000);
          } catch {
            toast("Copy failed — select the code and copy it manually", "warning");
          }
        });

        polling = setInterval(async () => {
          let seedBytes;
          try {
            seedBytes = await enroll.claim(identity, session);
          } catch (error) {
            stop();
            toast(error.message, "error", 8000);
            return;
          }
          if (!seedBytes) return;
          stop();
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
        }, 2000);
      } catch (error) {
        setLoading(false);
        toast(error.message, "error", 8000);
      }
    });
  },
  () => {
    // Panel closed: free the rendezvous so the relay's per-identity cap does
    // not fill with abandoned ceremonies.
    stop();
    if (session && joining) {
      enroll.cancel(joining, session).catch(() => {});
      session = null;
    }
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
  const next = q("#btn-next-id");
  if (!input || !status || !next) return;

  let timer = null;
  const setStatus = (text, cls = "") => {
    status.textContent = text;
    status.className = `idin-status small ${cls}`;
  };

  const check = async () => {
    const handle = input.value.trim().toLowerCase();
    const domain = q("#ni-domain")?.value.trim().toLowerCase() || "";
    const hosted = q("#ni-hosted-step1")?.checked !== false;
    const token = ++availabilityToken;

    // Self-hosted names are not this relay's to give out.
    if (!hosted || !handle) {
      next.disabled = false;
      setStatus("");
      return;
    }
    next.disabled = true;
    setStatus("Checking…");
    try {
      const verdict = await identityApiFor(defaultRelayUrl()).availability(handle, domain);
      if (token !== availabilityToken) return; // a later keystroke won
      next.disabled = !verdict.available;
      setStatus(
        verdict.available ? `${verdict.identity} is available` : verdict.message,
        verdict.available ? "val-ok" : "val-warn",
      );
    } catch (error) {
      if (token !== availabilityToken) return;
      // A relay that cannot answer must not block the flow — registration
      // itself is still the authority, and it will refuse if this would have.
      next.disabled = false;
      setStatus("");
      console.warn("Availability check failed:", error.message);
    }
  };

  input.addEventListener("input", () => {
    clearTimeout(timer);
    next.disabled = true;
    setStatus("");
    timer = setTimeout(check, AVAILABILITY_DEBOUNCE_MS);
  });
  q("#ni-domain")?.addEventListener("input", () => { clearTimeout(timer); timer = setTimeout(check, AVAILABILITY_DEBOUNCE_MS); });
  q("#ni-hosted-step1")?.addEventListener("change", check);
  if (input.value.trim()) check();
}

function doNextIdentityStep() {
  const handle = q("#ni-handle")?.value.trim().toLowerCase();
  const domain = q("#ni-domain")?.value.trim().toLowerCase();
  const hosted = q("#ni-hosted-step1")?.checked !== false;
  if (!handle) return toast("Enter a handle", "warning");
  if (!domain) return toast("Enter a parent domain", "warning");
  if (handle.length < 3) return toast("Handle must be at least 3 characters", "warning");
  const encoded = btoa(JSON.stringify({ handle, domain, hosted }));
  if (hosted) {
    // Stay on this relay (wildcard / local) — same as CLI --hosted
    window.location.hash = `create=${encoded}`;
    render();
    return;
  }
  window.location.href = `https://${handle}.${domain}/app/#create=${encoded}`;
}

/** @deprecated use doNextIdentityStep */
function doRedirectToIdentityDomain() {
  doNextIdentityStep();
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

  let launcherHost = "";
  try {
    const root = await fetch(new URL("/", relayUrlFor(identity)).toString(), {
      headers: { Accept: "application/json" },
    }).then(r => (r.ok ? r.json() : null));
    launcherHost = String(root?.launcher_host ?? "").toLowerCase();
  } catch {
    return false;
  }
  if (!launcherHost || launcherHost !== host.toLowerCase()) return false;

  const payload = toBase64url(new TextEncoder().encode(JSON.stringify({ identity, record })));
  setLoading(true, `Taking you to ${identity}…`);
  globalThis.location.href = `https://${identity}/app/#claim=${payload}`;
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

async function doCreateIdentity() {
  const step2 = parseCreateHash();
  if (!step2) return;

  const handle   = step2.handle;
  const domain   = step2.domain;
  // A brand-new identity has no record yet, so this is the one moment the
  // relay is not read from one — see storage.defaultRelayUrl().
  const relayUrl = defaultRelayUrl();
  const provider = q("#ni-provider")?.value;
  const dnsToken = q("#ni-token")?.value.trim();
  const hosted = q("#ni-hosted")?.checked !== false;

  if (!hosted && !dnsToken) return toast("Enter a DNS API token, or enable hosted registration", "warning");

  const identity = `${handle}.${domain}`;
  if (loadIdentityRecord(identity)) return toast("Identity already exists on this device", "warning");

  const support = await checkPasskeySupport();
  if (!support.available) return toast(`Passkey unavailable: ${support.reason}`, "error", 7000);

  setLoading(true, "Generating keys…");
  try {
    // Seed-derived from the start (EPIC-011): one secret behind both keys, so
    // this identity can produce a 24-word recovery kit.
    const { signingJWK: sigPriv, encJWK: encPriv, seed, publicKey, encPublicKey } =
      await generateSeedIdentityJwks();

    setLoading(true, "Creating passkey…");
    const userId = toBase64url(crypto.getRandomValues(new Uint8Array(16)));
    const { credentialId, prfOutput, supportsPRF, credentialPublicKey, credentialAlg, rpId: credentialScope } =
      await createPasskey(identity, userId);

    let encryptedKeys;
    if (supportsPRF) {
      setLoading(true, "Securing keys…");
      encryptedKeys = await wrapKeysWithPRF(prfOutput, sigPriv, encPriv, seed);
    } else {
      // Drop overlay so the PIN sheet can receive clicks.
      setLoading(false);
      const pin = await promptPin("Set a PIN to protect your keys:", true);
      if (!pin) return;
      setLoading(true, "Securing keys…");
      encryptedKeys = await wrapKeysWithPin(pin, sigPriv, encPriv, seed);
    }

    setLoading(true, "Registering identity…");
    const created = await createIdentity(identityApiFor(relayUrl), identity, {
      hosted,
      keys: keyBytesFromJwks(identity, sigPriv, encPriv),
      ...(hosted ? {} : { dnsProvider: provider, dnsToken }),
      ...(q("#ni-invite")?.value.trim() ? { inviteCode: q("#ni-invite").value.trim() } : {}),
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

    // The `#create=` hash has done its job. Left in place it makes the
    // launcher reopen step 2 for a handle that already exists — including
    // after a reload, which drops the user into a stale creation flow.
    if (globalThis.location?.hash.startsWith("#create=")) {
      history.replaceState(null, "", globalThis.location.pathname + globalThis.location.search);
    }

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
    toast(err.message, "error", 8000);
    console.error(err);
  }
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
      if (event.type === "ready") return;  // nothing new by itself
      loadInbox();
      if (S.tray === "requests") loadRequests({ force: true });
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
      // The SDK decrypts and emits tick-2 receipts for what actually opened.
      const { messages, acks } = await client.inboxAndAck();
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

async function doSend() {
  // The component may still be debouncing a lookup; take the raw text so a
  // fast typist is never told "enter a recipient" for something they typed.
  const to   = composeInput?.raw() ?? "";
  const body = q("#c-body")?.value.trim();
  const statusEl = q("#c-status");
  const sendBtn  = q("#btn-send-msg");
  if (!to)   return toast("Enter a recipient", "warning");
  if (!body) return toast("Enter a message", "warning");

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

  try {
    setStatus("Checking their key…");
    if (!(await checkPinBeforeSend(client, to))) {
      setStatus("✕ Not sent — key not trusted", "err");
      return;
    }
    setStatus("Sending…");
    await client.send(to, body, { signWith: sessionIsValid(sess) ? "session" : "identity" });
    setStatus("✓ Sent", "ok");
    if (q("#c-body")) q("#c-body").value = "";
    toast("Message sent!", "success");
    setTimeout(() => { R.sub = null; R.page = "messages"; render(); }, 1200);
  } catch (err) {
    setStatus(`✕ ${err.message}`, "err");
    toast(err.message, "error");
  } finally {
    if (sendBtn) sendBtn.disabled = false;
  }
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
  if (Q.loading) return Promise.resolve();
  if (!force && Q.fetchedAt && Date.now() - Q.fetchedAt < REQUEST_DRAIN_INTERVAL_MS) {
    return Promise.resolve();
  }
  const client = clientFor(S.identity);
  if (!client) return Promise.resolve();

  Q.loading = true;
  return challengeSerial(async () => {
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
      if (R.page === "messages" && !R.sub && S.tray === "requests") render();
    }
  });
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
    <button class="btn btn-primary mt-md" id="btn-add-contact-go" disabled>Send request</button>
    <button class="btn mt-sm" id="btn-add-contact-msg" disabled>Just message them</button>`,
  (close) => {
    const host = q("#add-contact-input");
    const go = q("#btn-add-contact-go");
    const msg = q("#btn-add-contact-msg");
    const input = IdentityInput({
      resolve: resolveForComponents,
      contacts: S.contacts.list,
      value: preset,
      label: "Identity",
      onChange: (result) => {
        picked = result;
        if (go) go.disabled = !result;
        if (msg) msg.disabled = !result;
      },
      onSubmit: () => go?.click(),
    });
    host?.replaceChildren(input.el);
    input.focus();
    go?.addEventListener("click", () => {
      if (!picked) return;
      const intro = q("#ac-intro")?.value.trim();
      const petname = q("#ac-petname")?.value.trim();
      close();
      doRequestContact(picked.identity, { intro, petname });
    });
    msg?.addEventListener("click", () => {
      if (!picked) return;
      close();
      R.push("compose", { to: picked.identity });
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
        <span class="kv-value mono small" id="km-pinned">${esc(pinnedKey ?? "")}</span></div>
      <div class="kv-row"><span class="kv-label">Now</span>
        <span class="kv-value mono small" id="km-resolved">${esc(resolvedKey ?? "")}</span></div>
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
        await client.setPolicy(document.mode, document.anonymous);
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
  if (A.loading) return Promise.resolve();
  if (!force && A.fetchedAt && Date.now() - A.fetchedAt < REQUEST_DRAIN_INTERVAL_MS) {
    return Promise.resolve();
  }
  const client = clientFor(S.identity);
  if (!client) return Promise.resolve();
  A.loading = true;
  return challengeSerial(async () => {
    try {
      // `GET /anon/{id}` drains like the inbox: keep what we have been handed.
      mergeInto(A.messages, await client.anon());
      A.loaded = true;
      A.error = null;
    } catch (error) {
      A.error = `Could not read anonymous messages: ${error.message}`;
    } finally {
      A.loading = false;
      A.fetchedAt = Date.now();
      if (R.page === "messages" && !R.sub) render();
    }
  });
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

const ENROLLMENT_KIND_LABEL = {
  "passkey": "Passkey",
  "hardware-key": "Hardware key",
  "cli-passphrase": "CLI passphrase",
  "recovery-kit": "Recovery kit",
  "native": "Native keystore",
};

const ENROLLMENT_WRAP_LABEL = {
  prf: "passkey (PRF)",
  pin: "PIN",
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
  setLoading(false);

  const record = loadIdentityRecord(identity);
  const thisBrowserEnrolled = enrollments.some(e => e.current);

  showPanel("Keys & devices", `
    ${thisBrowserEnrolled ? "" : `
      <div class="notice notice-warn">
        <strong>This browser is not backed up.</strong> Its copy of your keys exists only here,
        so clearing site data would destroy this identity. Registering it stores an encrypted
        copy the relay cannot read.
        <button class="btn btn-sm btn-primary" id="btn-enroll-this" style="margin-top:10px">
          Back up this browser
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
    </p>`,
  () => {
    q("#btn-enroll-this")?.addEventListener("click", async () => {
      closePanel();
      setLoading(true, "Backing up this browser…");
      try {
        const { canBootstrap } = await enrollThisBrowser(clientFor(identity), identity);
        setLoading(false);
        toast(canBootstrap
          ? "This browser is backed up"
          : "Backed up — but a PIN-wrapped browser cannot restore itself; keep your recovery kit",
          canBootstrap ? "success" : "warning", canBootstrap ? 3500 : 8000);
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
             autocomplete="off" spellcheck="false" placeholder="paste it here" />
    </div>
    <button class="btn btn-primary" id="btn-enroll-lookup" style="width:100%">Continue</button>
    <div id="enroll-confirm"></div>`,
  () => {
    q("#btn-enroll-lookup")?.addEventListener("click", async () => {
      const rendezvousId = q("#enroll-rendezvous")?.value.trim();
      if (!rendezvousId) return toast("Enter the request code from the new device", "warning");

      const client = clientFor(identity);
      if (!client) return toast("Unlock your identity first", "warning");
      const keys = getUnlockedKeys();
      if (!keys?.seed) {
        return toast("Only seed-based identities can hand their keys to a new device — see Recovery kit", "warning", 8000);
      }

      setLoading(true, "Finding the new device…");
      try {
        const pending = await client.enroll.pending(client.signer, identity, rendezvousId);
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
            await client.enroll.approve(client.signer, identity, pending, fromBase64url(keys.seed));
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
      <span class="chip ${rec.supportsPRF !== false ? "chip-green" : "chip-orange"}">${rec.supportsPRF !== false ? "Passkey PRF" : "PIN (PBKDF2)"}</span>
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
    if (rec.supportsPRF !== false) {
      const { prfOutput } = await authenticatePasskey(rec.credentialId, { rpId: rpIdFor(S.identity) });
      encryptedKeys = await wrapKeysWithPRF(prfOutput, keys.signingJWK, encPrivNew, keys.seed);
    } else {
      setLoading(false);
      const pin = await promptPin("Re-enter PIN to save new key:");
      if (!pin) return;
      setLoading(true, "Securing keys…");
      encryptedKeys = await wrapKeysWithPin(pin, keys.signingJWK, encPrivNew, keys.seed);
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
      switchIdentity(listIdentities()[0] || null);
      closePanel();
      R.go("messages");
      toast("Identity removed from device", "info");
    });
    q("#panel-cancel-remove")?.addEventListener("click", closePanel);
  });
}

// ─── PIN prompt ───────────────────────────────────────────────────────────────

function promptPin(label, confirm = false) {
  return new Promise(resolve => {
    showPanel("Key protection PIN", `
      <p class="muted small" style="margin-bottom:14px">${esc(label)}</p>
      <div class="form-group">
        <input id="pin-input" class="input" type="password" placeholder="PIN (min 4 characters)" />
      </div>
      ${confirm ? `<div class="form-group"><input id="pin-confirm" class="input" type="password" placeholder="Confirm PIN" /></div>` : ""}
      <button class="btn btn-primary mt-sm" id="btn-pin-ok">OK</button>`,
    () => {
      const ok = () => {
        const pin = q("#pin-input")?.value ?? "";
        if (pin.length < 4) { toast("PIN must be ≥ 4 characters", "warning"); return; }
        if (confirm && pin !== (q("#pin-confirm")?.value ?? "")) { toast("PINs do not match", "warning"); return; }
        closePanel(); resolve(pin);
      };
      q("#btn-pin-ok")?.addEventListener("click", ok);
      q("#pin-input")?.addEventListener("keydown", e => { if (e.key === "Enter") ok(); });
    },
    () => resolve(null));
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
  const el = document.createElement("div");
  el.className = `toast ${type}`;
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

  // A hand-off from the launcher arrives as a fragment and is adopted before
  // anything else looks at storage (EPIC-018 E18-T3).
  const handedOver = adoptHandOff();

  if (handedOver) {
    // Locked on arrival: the keys are wrapped, and the passkey that opens them
    // works here because it is scoped to the domain both hosts share (E18-T4).
    R.page = "messages";
    R.push("unlock");
  } else if (parseCreateHash()) {
    R.page = "launcher";
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
}

boot();
