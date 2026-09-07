/**
 * Poweur ID Web Client — SPA controller.
 *
 * UI only: the protocol lives in `@poweur/client` (EPIC-015 E15-T6), key
 * custody in `js/vault.js`, and every relay call is made through the
 * `PoweurClient` that `js/client.js` builds for the active identity.
 *
 * Three top-level pages: main | launcher | settings
 * Sub-pages (full-screen, back button): add-id | new-id | unlock | compose
 */

import {
  createIdentity, formatBytes, isSessionValid as sessionIsValid, ROOT_INFO,
} from "@poweur/client";

import {
  createPasskey, authenticatePasskey,
  wrapKeysWithPRF, unwrapKeysWithPRF, checkPasskeySupport,
} from "./passkey.js";

import {
  generateIdentityJwks, generateEncryptionJwk, keyBytesFromJwks,
  wrapKeysWithPin, unwrapKeysWithPin, toBase64url,
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

// ─── Router & State ───────────────────────────────────────────────────────────

const R = {
  page: "main",          // main | launcher | settings
  sub: null,             // null | add-id | new-id | unlock | compose
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
  files: { dav: null, davExp: 0, path: "", entries: [], quota: null, loading: false },
};

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

const iconHome = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M3 12l9-9 9 9M5 10v9a1 1 0 001 1h4v-5h4v5h4a1 1 0 001-1v-9"/></svg>`;
const iconHomeFill = `<svg viewBox="0 0 24 24" fill="currentColor"><path d="M12 2.1L3 10.5V21h6v-5h6v5h6V10.5L12 2.1z"/></svg>`;
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

function renderBottomNav() {
  const p = R.page;
  return `
    <nav class="bottom-nav">
      <button class="nav-tab${p==="main"?" active":""}" data-page="main">
        ${p==="main" ? iconHomeFill : iconHome}
        <span>Main</span>
      </button>
      <button class="nav-tab${p==="launcher"?" active":""}" data-page="launcher">
        ${p==="launcher" ? iconRocketFill : iconRocket}
        <span>Launcher</span>
      </button>
      <button class="nav-tab${p==="settings"?" active":""}" data-page="settings">
        ${p==="settings" ? iconGearFill : iconGear}
        <span>Settings</span>
      </button>
    </nav>`;
}

// ─── Top-level pages ──────────────────────────────────────────────────────────

function renderPage() {
  switch (R.page) {
    case "launcher": return renderLauncher();
    case "settings": return renderSettings();
    default:         return renderMain();
  }
}

// ─── Main page ────────────────────────────────────────────────────────────────

function renderMain() {
  if (!S.identity) return renderWelcome();
  if (!getUnlockedKeys()) return renderLockedMain();
  return renderMessagingMain();
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

function renderLockedMain() {
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

function renderMessagingMain() {
  const convs = buildConversations();
  return `
    ${convs.length ? `
      <div class="section-label">Messages</div>
      <div class="conv-list">
        ${convs.map(c => `
          <div class="conv-row" data-compose-to="${esc(c.contact)}">
            ${avatarHtml(c.contact, "md")}
            <div class="conv-info">
              <div class="conv-name">${esc(idHandle(c.contact))}</div>
              <div class="conv-preview">🔒 Encrypted message</div>
            </div>
            <div class="conv-meta">
              <span class="conv-time">${fmtRelative(c.lastMsg.timestamp)}</span>
              ${c.unread ? `<span class="conv-badge">${c.unread}</span>` : ""}
            </div>
          </div>`).join("")}
      </div>
    ` : `
      <div class="section-label">Messages</div>
      <div class="empty-conv">
        <div class="empty-conv-icon">💬</div>
        <p>No messages yet.<br>Tap ✏️ to send your first.</p>
      </div>
    `}

    <div class="section-label" style="margin-top:8px">More features</div>
    <div class="feature-grid">
      ${renderFeatureCards()}
    </div>

    <button class="fab" id="btn-compose" title="New message">✏️</button>`;
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
    .map(([contact, msgs]) => ({ contact, lastMsg: msgs.at(-1), unread: msgs.length }))
    .sort((a, b) => new Date(b.lastMsg.timestamp) - new Date(a.lastMsg.timestamp));
}

function renderFeatureCards() {
  const features = [
    { icon: "📁", name: "Files",        desc: "Your home filesystem", id: "feature-files" },
    { icon: "📞", name: "Voice calls",  desc: "Crystal-clear encrypted calls" },
    { icon: "🎥", name: "Video",        desc: "Face-to-face, end-to-end" },
    { icon: "👥", name: "Groups",       desc: "Encrypted group messaging" },
    { icon: "🔍", name: "Discover",     desc: "Find people by identity" },
    { icon: "💎", name: "Wallet",       desc: "Identity-native payments" },
  ];
  return features.map(f => f.id ? `
    <button class="feature-card active-card" id="${f.id}">
      <span class="feature-icon">${f.icon}</span>
      <span class="feature-name">${f.name}</span>
      <span class="feature-desc">${f.desc}</span>
    </button>` : `
    <div class="feature-card coming-soon">
      <span class="feature-icon">${f.icon}</span>
      <span class="feature-name">${f.name}</span>
      <span class="feature-desc">${f.desc}</span>
      <span class="soon-badge">Soon</span>
    </div>`).join("");
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
    case "files":   return renderFiles();
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

          <div class="option-card" style="opacity:.55;pointer-events:none;cursor:default">
            <div class="option-icon-wrap">📱</div>
            <div class="option-body">
              <div class="option-title">Add new device(key) to existing ID</div>
              <div class="option-desc">Transfer your identity from another device</div>
            </div>
            <span class="option-soon">Soon</span>
          </div>

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
          <label class="form-label" for="c-to">To</label>
          <input id="c-to" class="input" type="text" placeholder="alice.poweur.net"
            value="${esc(preset)}" autocomplete="off" spellcheck="false" />
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

// Files ───────────────────────────────────────────────────────────────────────

function renderFiles() {
  const F = S.files;
  const crumbs = F.path ? F.path.split("/") : [];
  const atRoot = !F.path;
  const quota = F.quota;
  const quotaPct = quota?.quota_bytes > 0
    ? Math.min(100, Math.round((quota.used_bytes / quota.quota_bytes) * 100))
    : 0;
  return `
    <div class="sub-page">
      <div class="sub-header">
        <button class="btn-back" id="btn-back">${svgBack}</button>
        <span class="sub-title">Files</span>
        <span style="flex:1"></span>
        ${atRoot ? "" : `
          <button class="btn btn-sm" id="btn-new-folder" title="New folder">📁+</button>
          <label class="btn btn-sm" for="ff-upload" style="cursor:pointer" title="Upload">⬆️
            <input id="ff-upload" type="file" multiple style="display:none" />
          </label>`}
      </div>
      <div class="sub-body">
        ${quota ? `
          <div class="small muted" style="display:flex;justify-content:space-between;margin-bottom:6px">
            <span>${formatBytes(quota.used_bytes)}${quota.quota_bytes > 0 ? ` of ${formatBytes(quota.quota_bytes)}` : ""} used</span>
            <span>${esc(quota.provider)}</span>
          </div>
          ${quota.quota_bytes > 0 ? `
          <div style="height:4px;border-radius:2px;background:var(--border,#ddd);margin-bottom:14px">
            <div style="height:100%;width:${quotaPct}%;border-radius:2px;background:${quotaPct > 90 ? "#FF3B30" : "#34C759"}"></div>
          </div>` : ""}` : ""}
        <div class="small" style="margin-bottom:10px;display:flex;gap:4px;flex-wrap:wrap;align-items:center">
          <button class="link-btn" data-nav-path="" style="font-weight:600">home</button>
          ${crumbs.map((c, i) => `
            <span class="muted">/</span>
            <button class="link-btn" data-nav-path="${esc(crumbs.slice(0, i + 1).join("/"))}">${esc(c)}</button>`).join("")}
        </div>
        ${F.loading ? `<p class="muted small">Loading…</p>` : `
        <div class="conv-list">
          ${F.entries.length === 0 ? `<p class="muted small" style="padding:12px">Empty folder</p>` : ""}
          ${F.entries.map(e => {
            const rootInfo = atRoot ? ROOT_INFO[e.name] : null;
            return `
            <div class="conv-row" style="align-items:center">
              <div style="font-size:22px;width:36px;text-align:center">${e.dir ? "📁" : "📄"}</div>
              <div class="conv-info" ${e.dir ? `data-open-dir="${esc(e.path)}"` : `data-download="${esc(e.path)}"`} style="cursor:pointer">
                <div class="conv-name">${esc(e.name)}
                  ${rootInfo ? `<span class="chip" style="margin-left:6px">${esc(rootInfo.badge)}</span>` : ""}
                </div>
                <div class="conv-preview">${rootInfo ? esc(rootInfo.desc) : e.dir ? "folder" : formatBytes(e.size)}</div>
              </div>
              ${atRoot ? "" : `
                <button class="btn btn-sm" data-rename="${esc(e.path)}" title="Rename">✏️</button>
                <button class="btn btn-sm" data-delete="${esc(e.path)}" title="Delete">🗑</button>`}
            </div>`;
          }).join("")}
        </div>`}
      </div>
    </div>`;
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
    clearUnlockedKeys();
    S.identity = id;
    setActiveIdentity(id);
    S.messages = []; S.acks = [];
    R.push("unlock");
  }));

  // Bottom nav
  qAll(".nav-tab[data-page]").forEach(t => t.addEventListener("click", () => {
    S.dropdownOpen = false;
    R.go(t.dataset.page);
  }));

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

  // Load inbox when unlocked and on main
  if (R.page === "main" && !R.sub && getUnlockedKeys()) {
    loadInbox();
  }

  // Add ID options
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
  q("#feature-files")?.addEventListener("click", () => openFiles(""));
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
  q("#row-rotate-enc")?.addEventListener("click", doRotateEncKey);
  q("#row-remove-id")?.addEventListener("click", doRemoveIdentity);
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

  const rec = loadIdentityRecord(fqdn);
  if (!rec) {
    toast("Identity not found on this device. Use 'Add new ID' to create one.", "warning", 5000);
    return;
  }

  clearUnlockedKeys();
  S.identity = fqdn;
  setActiveIdentity(fqdn);
  R.push("unlock");
}

async function doUnlock() {
  const id = S.identity;
  const rec = loadIdentityRecord(id);
  if (!rec) { toast("Identity record not found", "error"); return; }

  setLoading(true, "Authenticating…");
  try {
    let sigPriv, encPriv;
    if (rec.supportsPRF !== false && rec.encryptedKeys?.kdf === "prf") {
      const { prfOutput } = await authenticatePasskey(rec.credentialId);
      if (!prfOutput) throw new Error("PRF not available from this authenticator.");
      ({ signingJWK: sigPriv, encJWK: encPriv } = await unwrapKeysWithPRF(prfOutput, rec.encryptedKeys));
    } else {
      setLoading(false);
      const pin = await promptPin("Enter your PIN:");
      if (!pin) return;
      setLoading(true, "Unlocking…");
      ({ signingJWK: sigPriv, encJWK: encPriv } = await unwrapKeysWithPin(pin, rec.encryptedKeys));
    }

    setUnlockedKeys(id, sigPriv, encPriv);

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
    const { signingJWK: sigPriv, encJWK: encPriv, publicKey, encPublicKey } = await generateIdentityJwks();

    setLoading(true, "Creating passkey…");
    const userId = toBase64url(crypto.getRandomValues(new Uint8Array(16)));
    const { credentialId, prfOutput, supportsPRF } = await createPasskey(identity, userId);

    let encryptedKeys;
    if (supportsPRF) {
      setLoading(true, "Securing keys…");
      encryptedKeys = await wrapKeysWithPRF(prfOutput, sigPriv, encPriv);
    } else {
      // Drop overlay so the PIN sheet can receive clicks.
      setLoading(false);
      const pin = await promptPin("Set a PIN to protect your keys:", true);
      if (!pin) return;
      setLoading(true, "Securing keys…");
      encryptedKeys = await wrapKeysWithPin(pin, sigPriv, encPriv);
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
      credentialId, encryptedKeys, relay: relayUrl,
      userId, createdAt: created.document.updated_at, supportsPRF,
    });
    saveConfig({ ...S.config, relayUrl, parentDomain: domain, dnsProvider: provider });
    S.config = getConfig();

    setUnlockedKeys(identity, sigPriv, encPriv);
    S.identity = identity;
    setActiveIdentity(identity);

    setLoading(true, "Creating session…");
    await ensureSession(identity);

    setLoading(false);
    toast(`${identity} created! 🎉`, "success");
    R.sub = null; R.page = "main"; R.params = {};
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
      if (R.page === "main" && !R.sub) render();
    } catch (e) {
      console.warn("Inbox error:", e.message);
    } finally {
      inboxInFlight = null;
    }
  })();
  return inboxInFlight;
}

async function doSend() {
  const to   = q("#c-to")?.value.trim();
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
    setTimeout(() => { R.sub = null; R.page = "main"; render(); }, 1200);
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

async function openFiles(path) {
  if (!getUnlockedKeys()) { R.push("unlock"); return; }
  S.files.path = path;
  S.files.entries = [];
  S.files.loading = true;
  R.push("files");
  await loadFiles(path);
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
      encryptedKeys = await wrapKeysWithPRF(prfOutput, keys.signingJWK, encPrivNew);
    } else {
      setLoading(false);
      const pin = await promptPin("Re-enter PIN to save new key:");
      if (!pin) return;
      setLoading(true, "Securing keys…");
      encryptedKeys = await wrapKeysWithPin(pin, keys.signingJWK, encPrivNew);
    }

    saveIdentityRecord(id, { ...rec, encPublicKey, encryptedKeys });
    setUnlockedKeys(id, keys.signingJWK, encPrivNew);
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
      removeIdentity(S.identity);
      clearUnlockedKeys();
      removeSessionRecord(S.identity);
      S.identity = listIdentities()[0] || null;
      setActiveIdentity(S.identity);
      S.messages = []; S.acks = [];
      closePanel();
      R.go("main");
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
    R.page = "main";
  } else if (!getUnlockedKeys() && !sessionIsValid(loadSessionRecord(S.identity))) {
    // Auto-push unlock only if session is gone; otherwise session key in
    // sessionStorage lets us reload without re-auth.
    R.push("unlock");
  } else {
    R.page = "main";
  }

  render();
}

boot();
