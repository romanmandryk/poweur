/**
 * Poweur ID Web Client — SPA controller (complete rewrite)
 *
 * Three top-level pages: main | launcher | settings
 * Sub-pages (full-screen, back button): add-id | new-id | unlock | compose
 */

import {
  generateSigningKeypair, generateEncryptionKeypair, sign,
  encryptMessage, decryptMessage,
  wrapKeysWithPin, unwrapKeysWithPin,
  canonicalMessage, canonicalAck, canonicalIdentityRegistration,
  buildSignedIdentityDocument,
  canonicalSessionRegistration, canonicalSessionRevocation,
  generateMessageId, now, randomNonce, toBase64url,
} from "./crypto.js";

import {
  createPasskey, authenticatePasskey,
  wrapKeysWithPRF, unwrapKeysWithPRF, signChallenge, checkPasskeySupport,
} from "./passkey.js";

import {
  getConfig, saveConfig,
  saveIdentityRecord, loadIdentityRecord, listIdentities, removeIdentity,
  getActiveIdentity, setActiveIdentity,
  saveSessionRecord, loadSessionRecord, removeSessionRecord, isSessionValid,
  setUnlockedKeys, getUnlockedKeys, clearUnlockedKeys,
} from "./storage.js";

import {
  registerIdentity, updateEncryptionKey, getIdentityKey,
  getChallenge, fetchInbox, sendMessage, submitAck,
  registerSession, revokeSession, checkHealth, fetchRelayAddress,
  resolveEncryptionKey,
} from "./api.js";

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
    { icon: "📞", name: "Voice calls",  desc: "Crystal-clear encrypted calls" },
    { icon: "🎥", name: "Video",        desc: "Face-to-face, end-to-end" },
    { icon: "📁", name: "Files",        desc: "Send any file securely" },
    { icon: "👥", name: "Groups",       desc: "Encrypted group messaging" },
    { icon: "🔍", name: "Discover",     desc: "Find people by identity" },
    { icon: "💎", name: "Wallet",       desc: "Identity-native payments" },
  ];
  return features.map(f => `
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
          <div id="ni-dns-fields">
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

  // Step 1 — pick a handle, then redirect to the identity's domain for passkey creation
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
          <input id="ni-domain" class="input" type="text" value="${esc(cfg.parentDomain)}" autocomplete="off" />
        </div>
        <button class="btn btn-primary" id="btn-next-id" style="width:100%;margin-top:4px">
          Next →
        </button>
        <p class="form-note small" style="margin-top:10px;text-align:center">You'll be taken to your identity's domain to create a passkey.</p>
      </div>
    </div>`;
}

// ─── Settings page ────────────────────────────────────────────────────────────

function renderSettings() {
  const rec = S.identity ? loadIdentityRecord(S.identity) : null;
  const sess = S.identity ? loadSessionRecord(S.identity) : null;
  const sessOk = S.identity && isSessionValid(S.identity);

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

  // Launcher step 1 → redirect to identity domain
  q("#btn-next-id")?.addEventListener("click", doRedirectToIdentityDomain);
  q("#ni-handle")?.addEventListener("keydown", e => { if (e.key === "Enter") doRedirectToIdentityDomain(); });

  // Launcher step 2 → create identity
  q("#btn-create-id")?.addEventListener("click", doCreateIdentity);

  // Unlock sub-page
  q("#btn-do-unlock")?.addEventListener("click", doUnlock);

  // Compose send
  q("#btn-send-msg")?.addEventListener("click", doSend);

  // Settings rows
  q("#settings-add-id")?.addEventListener("click", () => R.push("add-id"));
  q("#row-switch-id")?.addEventListener("click", () => R.push("add-id"));
  q("#row-identity-keys")?.addEventListener("click", showIdentityKeysPanel);
  q("#row-relay")?.addEventListener("click", showRelayPanel);
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
      const pin = await promptPin("Enter your PIN:");
      if (!pin) { setLoading(false); return; }
      ({ signingJWK: sigPriv, encJWK: encPriv } = await unwrapKeysWithPin(pin, rec.encryptedKeys));
    }

    setUnlockedKeys(id, sigPriv, encPriv);

    if (!isSessionValid(id)) {
      setLoading(true, "Creating session…");
      await createSession(id, sigPriv, rec.relay || S.config.relayUrl);
    }

    setLoading(false);
    toast("Unlocked", "success");
    R.sub = null; R.params = {};
    render();
    loadInbox();
  } catch (err) {
    setLoading(false);
    toast(err.message, "error");
  }
}

function doRedirectToIdentityDomain() {
  const handle = q("#ni-handle")?.value.trim();
  const domain = q("#ni-domain")?.value.trim();
  if (!handle) return toast("Enter a handle", "warning");
  if (!domain) return toast("Enter a parent domain", "warning");
  const encoded = btoa(JSON.stringify({ handle, domain }));
  window.location.href = `https://${handle}.${domain}/app/#create=${encoded}`;
}

async function doCreateIdentity() {
  const step2 = parseCreateHash();
  if (!step2) return;

  const handle   = step2.handle;
  const domain   = step2.domain;
  const relayUrl = window.location.origin;
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
    const { publicKeyBytes: sigPub, privateKeyJWK: sigPriv } = await generateSigningKeypair();
    const { publicKeyBytes: encPub, privateKeyJWK: encPriv } = await generateEncryptionKeypair();
    const pubB64 = toBase64url(sigPub);
    const encB64 = toBase64url(encPub);

    setLoading(true, "Registering identity…");
    const relayAddr = await fetchRelayAddress(relayUrl);
    const rpId = deriveRpId(relayAddr);

    setLoading(true, "Creating passkey…");
    const userId = toBase64url(crypto.getRandomValues(new Uint8Array(16)));
    const { credentialId, prfOutput, supportsPRF } = await createPasskey(identity, userId, rpId);

    setLoading(true, "Securing keys…");
    let encryptedKeys;
    if (supportsPRF) {
      encryptedKeys = await wrapKeysWithPRF(prfOutput, sigPriv, encPriv);
    } else {
      const pin = await promptPin("Set a PIN to protect your keys:", true);
      if (!pin) { setLoading(false); return; }
      encryptedKeys = await wrapKeysWithPin(pin, sigPriv, encPriv);
    }
    const issuedAt = now();
    const nonce    = randomNonce();
    const canonical = canonicalIdentityRegistration(identity, pubB64, encB64, relayAddr, issuedAt, nonce);
    const identitySignature = await sign(sigPriv, canonical);

    const identityDocument = await buildSignedIdentityDocument(sigPriv, {
      identity, publicKey: pubB64, encPublicKey: encB64, relay: relayAddr, updatedAt: issuedAt,
    });

    const regReq = {
      identity, public_key: pubB64, encryption_public_key: encB64,
      issued_at: issuedAt, nonce, identity_signature: identitySignature,
      identity_document: identityDocument,
    };
    if (!hosted) {
      regReq.dns_provider = provider;
      regReq.dns_token = dnsToken;
    }

    await registerIdentity(relayUrl, regReq);

    saveIdentityRecord(identity, {
      identity, publicKey: pubB64, encPublicKey: encB64,
      credentialId, encryptedKeys, relay: relayUrl,
      userId, createdAt: issuedAt, supportsPRF,
    });
    saveConfig({ ...S.config, relayUrl, parentDomain: domain, dnsProvider: provider });
    S.config = getConfig();

    setUnlockedKeys(identity, sigPriv, encPriv);
    S.identity = identity;
    setActiveIdentity(identity);

    setLoading(true, "Creating session…");
    await createSession(identity, sigPriv, relayUrl);

    setLoading(false);
    toast(`${identity} created! 🎉`, "success");
    R.sub = null; R.page = "main"; R.params = {};
    render();
    loadInbox();
  } catch (err) {
    setLoading(false);
    toast(err.message, "error", 8000);
    console.error(err);
  }
}

async function createSession(identity, sigPriv, relayUrl) {
  const { publicKeyBytes: sesPub, privateKeyJWK: sesPriv } = await generateSigningKeypair();
  const sesPubB64  = toBase64url(sesPub);
  const issuedAt   = now();
  const expiresAt  = new Date(Date.now() + 23 * 3600_000).toISOString().replace(/\.\d{3}Z$/, "Z");
  const nonce      = randomNonce();
  const canonical  = canonicalSessionRegistration(identity, sesPubB64, issuedAt, expiresAt, nonce);
  const identitySignature = await sign(sigPriv, canonical);

  const res = await registerSession(relayUrl, {
    identity, session_public_key: sesPubB64,
    issued_at: issuedAt, expires_at: expiresAt,
    nonce, identity_signature: identitySignature,
  });
  saveSessionRecord(identity, {
    sessionId: res.session_id,
    sessionPublicKey: sesPubB64,
    sessionSigningJWK: sesPriv,
    issuedAt, expiresAt, nonce, identitySignature,
  });
}

async function loadInbox() {
  const id = S.identity;
  if (!id || !getUnlockedKeys()) return;
  const rec      = loadIdentityRecord(id);
  const relayUrl = rec?.relay || S.config.relayUrl;
  const keys     = getUnlockedKeys();
  try {
    const { challenge } = await getChallenge(relayUrl, id);
    const signature     = await signChallenge(keys.signingJWK, challenge);
    const { messages = [], acks = [] } = await fetchInbox(relayUrl, id, challenge, signature);
    S.messages = messages;
    S.acks     = acks;
    if (R.page === "main" && !R.sub) render();
  } catch (e) { console.warn("Inbox error:", e.message); }
}

async function doSend() {
  const to   = q("#c-to")?.value.trim();
  const body = q("#c-body")?.value.trim();
  const statusEl = q("#c-status");
  const sendBtn  = q("#btn-send-msg");
  if (!to)   return toast("Enter a recipient", "warning");
  if (!body) return toast("Enter a message", "warning");

  const id       = S.identity;
  const rec      = loadIdentityRecord(id);
  const relayUrl = rec?.relay || S.config.relayUrl;
  const sess     = loadSessionRecord(id);
  const keys     = getUnlockedKeys();
  if (!keys) return toast("Unlock your identity first", "warning");

  sendBtn.disabled = true;
  const setStatus = (msg, cls = "") => { if (statusEl) { statusEl.textContent = msg; statusEl.className = `compose-status ${cls}`; } };

  try {
    setStatus("Resolving keys…");
    let recipientEncKey = await resolveEncryptionKey(to);
    if (!recipientEncKey) {
      try { const r = await getIdentityKey(relayUrl, to); recipientEncKey = r.encryption_public_key; } catch {}
    }
    if (!recipientEncKey) throw new Error(`Cannot resolve encryption key for ${to}`);

    setStatus("Encrypting…");
    const { ciphertext, ephemeralPublicKey, nonce } = await encryptMessage(body, recipientEncKey);

    setStatus("Signing…");
    const msgId     = generateMessageId();
    const timestamp = now();
    const enc_      = { alg: "x25519-chacha20-poly1305", ephemeralPublicKey, nonce };
    const canonical = canonicalMessage(id, to, timestamp, ciphertext, msgId, sess?.sessionId, enc_);
    const sigJWK    = sess?.sessionSigningJWK || keys.signingJWK;
    const signature = await sign(sigJWK, canonical);

    const message = {
      id: msgId, sender: id, recipient: to, timestamp,
      payload: ciphertext, signature,
      ...(sess ? {
        session_id: sess.sessionId,
        session_proof: {
          session_public_key: sess.sessionPublicKey,
          issued_at: sess.issuedAt,
          expires_at: sess.expiresAt,
          nonce: sess.nonce,
          identity_signature: sess.identitySignature,
        },
      } : {}),
      encryption: { alg: enc_.alg, ephemeral_public_key: ephemeralPublicKey, nonce },
    };

    setStatus("Sending…");
    await sendMessage(relayUrl, message);
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
      try { await checkHealth(url); s.textContent = "✓ Connected"; s.style.color = "var(--green)"; }
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
  const valid = isSessionValid(S.identity);
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
      const rec = loadIdentityRecord(S.identity);
      setLoading(true, "Refreshing session…");
      try {
        await createSession(S.identity, getUnlockedKeys().signingJWK, rec?.relay || S.config.relayUrl);
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
  if (!getUnlockedKeys()) return toast("Unlock your identity first", "warning");
  const id  = S.identity;
  const rec = loadIdentityRecord(id);
  const relayUrl = rec?.relay || S.config.relayUrl;
  const keys = getUnlockedKeys();

  setLoading(true, "Rotating encryption key…");
  try {
    const { publicKeyBytes, privateKeyJWK: encPrivNew } = await generateEncryptionKeypair();
    const encPubB64 = toBase64url(publicKeyBytes);
    const issuedAt = now(); const nonce = randomNonce();
    const canonical = ["identity-encryption-key", id, encPubB64, issuedAt, nonce].join("\n");
    const signature = await sign(keys.signingJWK, canonical);

    await updateEncryptionKey(relayUrl, id, {
      encryption_public_key: encPubB64, issued_at: issuedAt, nonce, identity_signature: signature,
    });

    let encryptedKeys;
    if (rec.supportsPRF !== false) {
      const { prfOutput } = await authenticatePasskey(rec.credentialId);
      encryptedKeys = await wrapKeysWithPRF(prfOutput, keys.signingJWK, encPrivNew);
    } else {
      const pin = await promptPin("Re-enter PIN to save new key:");
      if (!pin) { setLoading(false); return; }
      encryptedKeys = await wrapKeysWithPin(pin, keys.signingJWK, encPrivNew);
    }

    saveIdentityRecord(id, { ...rec, encPublicKey: encPubB64, encryptedKeys });
    setUnlockedKeys(id, keys.signingJWK, encPrivNew);
    setLoading(false); toast("Encryption key rotated", "success"); render();
  } catch (e) { setLoading(false); toast(e.message, "error"); }
}

async function doRevokeSession() {
  const id   = S.identity;
  const rec  = loadIdentityRecord(id);
  const relayUrl = rec?.relay || S.config.relayUrl;
  const sess = loadSessionRecord(id);
  const keys = getUnlockedKeys();
  if (!sess || !keys) return;

  setLoading(true, "Revoking session…");
  try {
    const issuedAt = now(); const nonce = randomNonce();
    const canonical = canonicalSessionRevocation(id, sess.sessionId, issuedAt, nonce);
    const signature = await sign(keys.signingJWK, canonical);
    await revokeSession(relayUrl, sess.sessionId, { identity: id, session_id: sess.sessionId, issued_at: issuedAt, nonce, identity_signature: signature });
    removeSessionRecord(id);
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
  } else if (!getUnlockedKeys() && !isSessionValid(S.identity)) {
    // Auto-push unlock only if session is gone; otherwise session key in
    // sessionStorage lets us reload without re-auth.
    R.push("unlock");
  } else {
    R.page = "main";
  }

  render();
}

boot();
