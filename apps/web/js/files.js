/**
 * WebDAV file client for the Poweur web app (EPIC-003 E03-T5).
 * Mints DAV bearer tokens with the identity key and speaks just enough
 * WebDAV (PROPFIND/PUT/GET/MKCOL/MOVE/DELETE) for the file browser.
 * Shared by the SPA and Vitest/Playwright tests — mirrors `poweur dav`.
 */

import { sign, now, randomNonce } from "./crypto.js";

/** Root folders and their audience badges (storage-model.md). */
export const ROOT_INFO = {
  "poweur-sys": { badge: "system", desc: "managed system data" },
  public: { badge: "any ID", desc: "readable by any Poweur identity" },
  shared: { badge: "grants", desc: "shared per explicit grant" },
  private: { badge: "private", desc: "only you" },
  apps: { badge: "apps", desc: "per-application data" },
};

function baseUrl(relayUrl) {
  return relayUrl.replace(/\/$/, "");
}

/**
 * Mint a WebDAV bearer token, signing the canonical dav-token string with
 * the identity key (CLI parity: `poweur dav token`).
 */
export async function mintDavToken(relayUrl, identity, signingJWK, { audience, scope } = {}) {
  const aud = audience || identity;
  const sc = scope || (aud === identity ? "dav:full" : "dav:read");
  const issuedAt = now();
  const nonce = randomNonce();
  const canonical = ["dav-token", identity, aud, sc, issuedAt, nonce].join("\n");
  const signature = await sign(signingJWK, canonical);
  const res = await fetch(`${baseUrl(relayUrl)}/auth/dav-token`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      identity, audience: aud, scope: sc,
      issued_at: issuedAt, nonce, signature,
    }),
  });
  if (!res.ok) {
    const data = await res.json().catch(() => ({}));
    throw new Error(data.detail || data.error || `dav token failed: HTTP ${res.status}`);
  }
  return res.json(); // { token, audience, scope, expires_at }
}

function davUrl(relayUrl, identity, path) {
  const clean = String(path || "").replace(/^\/+/, "");
  return `${baseUrl(relayUrl)}/dav/${encodeURIComponent(identity)}/${clean.split("/").map(encodeURIComponent).join("/")}`;
}

async function davFetch(relayUrl, identity, token, method, path, { body, headers } = {}) {
  const res = await fetch(davUrl(relayUrl, identity, path), {
    method,
    headers: { Authorization: `Bearer ${token}`, ...headers },
    body,
  });
  if (!res.ok && res.status !== 207) {
    const text = await res.text().catch(() => "");
    let detail = text;
    try { detail = JSON.parse(text).detail || JSON.parse(text).error || text; } catch { /* raw */ }
    const err = new Error(detail || `${method} ${path}: HTTP ${res.status}`);
    err.status = res.status;
    throw err;
  }
  return res;
}

function decodeXML(s) {
  return s
    .replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&quot;/g, '"')
    .replace(/&apos;/g, "'").replace(/&#(\d+);/g, (_, n) => String.fromCharCode(n))
    .replace(/&amp;/g, "&");
}

/**
 * Parse a WebDAV multistatus body without DOMParser (works in node tests).
 * Returns [{ name, path, dir, size, modified, etag }] excluding the
 * requested collection itself.
 */
export function parseMultistatus(xml, requestPath) {
  const entries = [];
  const responses = xml.split(/<\/D:response>/i).slice(0, -1);
  for (const chunk of responses) {
    const hrefMatch = chunk.match(/<D:href>([^<]*)<\/D:href>/i);
    if (!hrefMatch) continue;
    let href = decodeXML(hrefMatch[1]);
    try { href = decodeURIComponent(href); } catch { /* keep raw */ }
    // Strip /dav/<identity> prefix → tree path
    const treePath = href.replace(/^.*?\/dav\/[^/]+/, "").replace(/\/$/, "") || "/";
    const req = ("/" + String(requestPath || "").replace(/^\/+|\/+$/g, "")).replace(/\/$/, "") || "/";
    if (treePath === req) continue; // the collection itself
    const dir = /<D:resourcetype>\s*<D:collection[^>]*>/i.test(chunk);
    const sizeMatch = chunk.match(/<D:getcontentlength>(\d+)<\/D:getcontentlength>/i);
    const modMatch = chunk.match(/<D:getlastmodified>([^<]*)<\/D:getlastmodified>/i);
    const etagMatch = chunk.match(/<D:getetag>([^<]*)<\/D:getetag>/i);
    entries.push({
      name: treePath.split("/").filter(Boolean).pop() || treePath,
      path: treePath.replace(/^\//, ""),
      dir,
      size: sizeMatch ? Number(sizeMatch[1]) : 0,
      modified: modMatch ? decodeXML(modMatch[1]) : "",
      etag: etagMatch ? decodeXML(etagMatch[1]) : "",
    });
  }
  entries.sort((a, b) => (a.dir === b.dir ? a.name.localeCompare(b.name) : a.dir ? -1 : 1));
  return entries;
}

/** List a directory (PROPFIND depth 1). */
export async function listDir(relayUrl, identity, token, path = "") {
  const res = await davFetch(relayUrl, identity, token, "PROPFIND", path ? path + "/" : "", {
    headers: { Depth: "1" },
  });
  const xml = await res.text();
  return parseMultistatus(xml, path);
}

/** Upload a file (PUT). body may be a string, Blob, File, or ArrayBuffer. */
export async function uploadFile(relayUrl, identity, token, path, body) {
  await davFetch(relayUrl, identity, token, "PUT", path, { body });
}

/** Download a file; returns the Response (caller picks .text()/.blob()). */
export async function downloadFile(relayUrl, identity, token, path) {
  return davFetch(relayUrl, identity, token, "GET", path);
}

/** Create a directory (MKCOL). */
export async function makeDir(relayUrl, identity, token, path) {
  await davFetch(relayUrl, identity, token, "MKCOL", path);
}

/** Rename / move (MOVE). */
export async function moveEntry(relayUrl, identity, token, fromPath, toPath) {
  await davFetch(relayUrl, identity, token, "MOVE", fromPath, {
    headers: { Destination: davUrl(relayUrl, identity, toPath), Overwrite: "F" },
  });
}

/** Delete a file or directory subtree. */
export async function deleteEntry(relayUrl, identity, token, path) {
  await davFetch(relayUrl, identity, token, "DELETE", path);
}

/** Fetch storage usage: { used_bytes, quota_bytes, provider, change_id }. */
export async function fetchQuota(relayUrl, identity, token) {
  const res = await fetch(`${baseUrl(relayUrl)}/files/${encodeURIComponent(identity)}/quota`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!res.ok) throw new Error(`quota failed: HTTP ${res.status}`);
  return res.json();
}

/** Human-readable byte size. */
export function fmtBytes(n) {
  if (!Number.isFinite(n) || n <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.min(units.length - 1, Math.floor(Math.log2(n) / 10));
  const v = n / 2 ** (10 * i);
  return `${v >= 10 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}
