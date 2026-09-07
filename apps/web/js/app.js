/**
 * Poweur ID Web Client — SPA controller.
 *
 * UI only: the protocol lives in `@poweur/client` (EPIC-015 E15-T6), key
 * custody in `js/vault.js`, and every relay call is made through the
 * `PoweurClient` that `js/client.js` builds for the active identity.
 *
 * Five destinations (E15-T1): messages | contacts | files | launcher | settings
 * Sub-pages (full-screen, back button): add-id | unlock | compose
 */

import {
  createIdentity, formatBytes, isSessionValid as sessionIsValid, ROOT_INFO,
  EnrollApi, RelayClient,
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
  loadSessionRecord, removeSessionRecord,
  setUnlockedKeys, getUnlockedKeys, clearUnlockedKeys,
  defaultRelayUrl, relayUrlFor,
} from "./storage.js";

import { clientFor, identityApiFor, lookup } from "./client.js";

import { resolveProfile, clearProfileCache } from "./profiles.js";
import { IdentityInput } from "./components/identity-input.js";
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
  files: { dav: null, davExp: 0, path: "", entries: [], quota: null, loading: false, loaded: false },
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

/** The Messages trays. Requests and anonymous are wired up in T2/T3. */
const TRAYS = [
  { id: "inbox",     label: "Inbox" },
  { id: "requests",  label: "Requests" },
  { id: "anonymous", label: "Anonymous" },
];

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
      ${TRAYS.map(t => `
        <button class="tray-tab${S.tray === t.id ? " active" : ""}" data-tray="${t.id}"
                role="tab" aria-selected="${S.tray === t.id}">${t.label}</button>`).join("")}
    </div>
    ${renderTray()}
    <button class="fab" id="btn-compose" title="New message" aria-label="New message">✏️</button>`;
}

function renderTray() {
  if (S.tray === "requests") {
    return emptyState("🤝", "No contact requests",
      "Requests to connect land here. Accepting one lets you message each other.");
  }
  if (S.tray === "anonymous") {
    return emptyState("🎭", "No anonymous messages",
      "Turn on anonymous messages in Settings to let strangers reach you behind a proof-of-work cost.");
  }

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
            <div class="conv-name">${esc(idHandle(c.contact))}</div>
            <div class="conv-preview">${esc(c.preview)}</div>
          </div>
          <div class="conv-meta">
            <span class="conv-time">${fmtRelative(c.lastMsg.timestamp)}</span>
            ${c.unread ? `<span class="conv-badge">${c.unread}</span>` : ""}
          </div>
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
      return {
        contact,
        lastMsg,
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
        ${filtered.map(c => slot(`contact-${encodeURIComponent(c.identity)}`, () =>
          ProfileCard({
            identity: c.identity,
            resolve: resolveForComponents,
            compact: true,
            action: { label: "Message", onSelect: (id) => R.push("compose", { to: id }) },
          }).el)).join("")}
      </div>` : ""}
    ${C.list.length && !filtered.length ? `<p class="muted small" style="padding:16px">No contact matches “${esc(C.filter)}”.</p>` : ""}`;
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
          <input id="ni-handle" class="input" type="text" placeholder="alice" autocomplete="off" spellcheck="false" />
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
        <div class="settings-row" id="row-switch-id">
          <span class="settings-row-icon">🔄</span>
          <span class="settings-row-label">Switch / Add identity</span>
          <span class="settings-row-arrow">›</span>
        </div>
        ${rec ? `
        <div class="settings-row" id="row-identity-keys">
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
        <div class="settings-row" id="row-keys-devices">
          <span class="settings-row-icon">📱</span>
          <span class="settings-row-label">Keys &amp; devices</span>
          <span class="settings-row-arrow">›</span>
        </div>
        <div class="settings-row" id="row-recovery-kit">
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
        <div class="settings-row no-action">
          <span class="settings-row-icon">🛡️</span>
          <span class="settings-row-label">Who can message you</span>
          <span class="settings-row-value muted">Soon</span>
        </div>
        <div class="settings-row no-action">
          <span class="settings-row-icon">🎭</span>
          <span class="settings-row-label">Anonymous &amp; proof-of-work</span>
          <span class="settings-row-value muted">Soon</span>
        </div>
      </div>
    </div>

    <div class="settings-group">
      <div class="settings-group-label">Network</div>
      <div class="settings-rows">
        <div class="settings-row" id="row-relay">
          <span class="settings-row-icon">🔗</span>
          <span class="settings-row-label">Relay URL</span>
          <span class="settings-row-value truncate">${esc(S.config.relayUrl)}</span>
          <span class="settings-row-arrow">›</span>
        </div>
        <div class="settings-row" id="row-lookup">
          <span class="settings-row-icon">🔎</span>
          <span class="settings-row-label">Lookup identity</span>
          <span class="settings-row-arrow">›</span>
        </div>
      </div>
    </div>

    <div class="settings-group">
      <div class="settings-group-label">Advanced</div>
      <div class="settings-rows">
        <div class="settings-row${sess ? "" : " no-action"}" id="row-session">
          <span class="settings-row-icon">🔑</span>
          <span class="settings-row-label">Session</span>
          <span class="settings-row-value ${sessOk ? "val-ok" : "val-warn"}">${sessOk ? "Active" : "None"}</span>
          ${sess ? `<span class="settings-row-arrow">›</span>` : ""}
        </div>
        <div class="settings-row" id="row-dns">
          <span class="settings-row-icon">🌐</span>
          <span class="settings-row-label">DNS provider</span>
          <span class="settings-row-value">${esc(S.config.dnsProvider || "Cloudflare")}</span>
          <span class="settings-row-arrow">›</span>
        </div>
        ${rec ? `
        <div class="settings-row" id="row-rotate-enc">
          <span class="settings-row-icon">🔄</span>
          <span class="settings-row-label">Rotate encryption key</span>
          <span class="settings-row-arrow">›</span>
        </div>
        <div class="settings-row" id="row-remove-id">
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
        <p id="c-status" class="compose-status"></p>
      </div>
      <div class="sub-footer">
        <button class="btn btn-primary" id="btn-send-msg">Send →</button>
      </div>
    </div>`;
}

// ─── Files destination ────────────────────────────────────────────────────────

function renderFilesDestination() {
  const F = S.files;
  const crumbs = F.path ? F.path.split("/") : [];
  const atRoot = !F.path;
  const quota = F.quota;
  const quotaPct = quota?.quota_bytes > 0
    ? Math.min(100, Math.round((quota.used_bytes / quota.quota_bytes) * 100))
    : 0;

  return `
    <div class="dest-header">
      <h1 class="dest-title">Files</h1>
      ${atRoot ? "" : `
        <button class="btn btn-sm" id="btn-new-folder" title="New folder" aria-label="New folder">📁+</button>
        <label class="btn btn-sm" for="ff-upload" style="cursor:pointer" title="Upload">⬆️
          <input id="ff-upload" type="file" multiple style="display:none" />
        </label>`}
    </div>

    ${quota ? `
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
      ${F.entries.length === 0
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
                  </div>
                  <div class="conv-preview">${rootInfo ? esc(rootInfo.desc) : e.dir ? "folder" : formatBytes(e.size)}</div>
                </div>
                ${atRoot ? "" : `
                  <button class="btn btn-sm" data-rename="${esc(e.path)}" aria-label="Rename ${esc(e.name)}">✏️</button>
                  <button class="btn btn-sm" data-delete="${esc(e.path)}" aria-label="Delete ${esc(e.name)}">🗑</button>`}
              </div>`;
            }).join("")}
          </div>`}`}`;
}

// ─── Event wiring ─────────────────────────────────────────────────────────────

function attachEvents() {
  q("#btn-theme")?.addEventListener("click", toggleTheme);

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
  q("#btn-add-contact")?.addEventListener("click", showAddContactPanel);
  q("#btn-add-contact-empty")?.addEventListener("click", showAddContactPanel);
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
  if (R.page === "contacts" && !R.sub && getUnlockedKeys()) loadContacts();

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

  // Load inbox when unlocked and on the Messages destination
  if (R.page === "messages" && !R.sub && getUnlockedKeys()) {
    loadInbox();
  }

  // Add ID options
  q("#opt-join-device")?.addEventListener("click", showJoinDevicePanel);
  q("#btn-signin-passkey")?.addEventListener("click", doSignInWithPasskey);
  q("#signin-id-input")?.addEventListener("keydown", e => { if (e.key === "Enter") doSignInWithPasskey(); });
  q("#opt-create-new")?.addEventListener("click", () => { R.sub = null; R.go("launcher"); });

  // Launcher step 1 → hosted stays here; DNS mode redirects to identity host
  q("#btn-next-id")?.addEventListener("click", doNextIdentityStep);
  q("#ni-handle")?.addEventListener("keydown", e => { if (e.key === "Enter") doNextIdentityStep(); });

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

  // Files
  if (R.page === "files" && !R.sub && getUnlockedKeys() && !S.files.loaded) {
    S.files.loaded = true;
    loadFiles(S.files.path);
  }
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
  q("#row-keys-devices")?.addEventListener("click", showKeysAndDevicesPanel);
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
  clearUnlockedKeys();
  clearProfileCache();
  S.identity = identity;
  setActiveIdentity(identity);
  S.messages = [];
  S.acks = [];
  S.contacts = { list: [], loading: false, loaded: false, error: null, filter: "" };
  S.files = { dav: null, davExp: 0, path: "", entries: [], quota: null, loading: false };
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
      const { prfOutput } = await authenticatePasskey(rec.credentialId);
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
  const { credentialId, prfOutput, supportsPRF, credentialPublicKey, credentialAlg } =
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
    credentialId, credentialPublicKey, credentialAlg,
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
    const { credentialId, prfOutput, supportsPRF, credentialPublicKey, credentialAlg } =
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
    });

    saveIdentityRecord(identity, {
      identity, publicKey, encPublicKey,
      credentialId, credentialPublicKey, credentialAlg,
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
    R.sub = null; R.page = "messages"; R.params = {};
    render(); // attachEvents starts the inbox fetch

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

function loadInbox() {
  const client = clientFor(S.identity);
  if (!client) return Promise.resolve();
  inboxInFlight ??= (async () => {
    try {
      // The SDK decrypts and emits tick-2 receipts for what actually opened.
      const { messages, acks } = await client.inboxAndAck();
      S.messages = messages;
      S.acks     = acks;
      if (R.page === "messages" && !R.sub) render();
    } catch (e) {
      console.warn("Inbox error:", e.message);
    } finally {
      inboxInFlight = null;
    }
  })();
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

  try {
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
  const connected = await client.dav({ force: true });
  S.files.dav = connected;
  // Tokens are short-lived; re-mint a minute before the relay stops honouring one.
  S.files.davExp = Date.now() + 55 * 60_000;
  return connected;
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
    setLoading(true, `Uploading ${files.length} file${files.length > 1 ? "s" : ""}…`);
    for (const f of files) {
      await client.write(`${S.files.path}/${f.name}`, f);
    }
    setLoading(false);
    toast(`Uploaded ${files.length} file${files.length > 1 ? "s" : ""}`, "success");
    await loadFiles(S.files.path);
  } catch (err) {
    setLoading(false);
    toast(err.status === 507 ? "Storage quota exceeded" : err.message, "error");
  }
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

// ─── Contacts actions ─────────────────────────────────────────────────────────

/**
 * Read `poweur-sys/relay/contacts.json` through the SDK.
 *
 * Read-only in T1 — requests, accept/block and key-pinning are E15-T2. Loading
 * it here is what makes the Contacts destination real rather than a stub, and
 * gives IdentityInput something to autocomplete against.
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
      pinnedKey: entry.public_key ?? null,
    }));
    C.loaded = true;
  } catch (error) {
    C.error = `Could not read contacts: ${error.message}`;
  } finally {
    C.loading = false;
    if (R.page === "contacts" && !R.sub) render();
  }
}

function showAddContactPanel() {
  let picked = null;
  showPanel("Add a contact", `
    <p class="muted small" style="margin-bottom:12px">
      Type a Poweur ID. We resolve it first, so a typo fails here rather than silently later.
    </p>
    <div id="add-contact-input"></div>
    <button class="btn btn-primary mt-md" id="btn-add-contact-go" disabled>Message them</button>
    <p class="muted small" style="margin-top:10px">
      Sending a contact <em>request</em> arrives with EPIC-015 E15-T2; for now this opens a message.
    </p>`,
  () => {
    const host = q("#add-contact-input");
    const go = q("#btn-add-contact-go");
    const input = IdentityInput({
      resolve: resolveForComponents,
      contacts: S.contacts.list,
      label: "Identity",
      onChange: (result) => { picked = result; if (go) go.disabled = !result; },
      onSubmit: () => go?.click(),
    });
    host?.replaceChildren(input.el);
    input.focus();
    go?.addEventListener("click", () => {
      if (!picked) return;
      closePanel();
      R.push("compose", { to: picked.identity });
    });
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
      const { prfOutput } = await authenticatePasskey(rec.credentialId);
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

function showPanel(title, bodyHtml, afterRender, onClose) {
  const backdrop = document.getElementById("panel-backdrop");
  const panel    = document.getElementById("panel-root");
  panel.innerHTML = `
    <div class="panel-handle"></div>
    <div class="panel-header">
      <span class="panel-title">${esc(title)}</span>
      <button class="btn-icon" id="panel-close-btn">✕</button>
    </div>
    <div class="panel-body">${bodyHtml}</div>`;
  backdrop.classList.remove("hidden");
  panel.classList.remove("hidden");

  const close = () => {
    panel.classList.add("hidden");
    backdrop.classList.add("hidden");
    onClose?.();
  };
  backdrop.onclick = close;
  q("#panel-close-btn")?.addEventListener("click", close);
  afterRender?.(close);
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

  if (parseCreateHash()) {
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
