/**
 * Relay HTTP client for the Poweur ID web client.
 * Talks to the relay API; resolves identities web-first (well-known / identities API)
 * with DNS-over-HTTPS as fallback.
 */

// ─── Low-level HTTP ───────────────────────────────────────────────────────────

async function apiRequest(relayUrl, method, path, body, headers = {}) {
  const url = relayUrl.replace(/\/$/, "") + path;
  const opts = {
    method,
    headers: { "Content-Type": "application/json", ...headers },
  };
  if (body !== undefined) opts.body = JSON.stringify(body);

  const res = await fetch(url, opts);
  const text = await res.text();

  let data;
  try { data = JSON.parse(text); } catch { data = { raw: text }; }

  if (!res.ok) {
    const msg = data?.detail || data?.error || text || `HTTP ${res.status}`;
    const err = new Error(msg);
    err.status = res.status;
    err.code = data?.error;
    throw err;
  }
  return data;
}

// ─── Identity ─────────────────────────────────────────────────────────────────

/**
 * Register a new identity on the relay.
 * Hosted: omit dns_provider/dns_token; include identity_document.
 * DNS: include dns_provider, dns_token; identity_document recommended.
 */
export async function registerIdentity(relayUrl, request) {
  return apiRequest(relayUrl, "POST", "/identities", request);
}

/**
 * Publish or rotate an encryption key for an identity.
 */
export async function updateEncryptionKey(relayUrl, identity, request) {
  return apiRequest(relayUrl, "POST", `/identities/${encodeURIComponent(identity)}/encryption-key`, request);
}

/**
 * Fetch an identity's public key / document from the relay API.
 */
export async function getIdentityKey(relayUrl, identity) {
  return apiRequest(relayUrl, "GET", `/identities/${encodeURIComponent(identity)}`);
}

// ─── Inbox ────────────────────────────────────────────────────────────────────

export async function getChallenge(relayUrl, identity) {
  return apiRequest(
    relayUrl, "GET",
    `/auth/challenge?identity=${encodeURIComponent(identity)}`
  );
}

export async function fetchInbox(relayUrl, identity, challenge, signature) {
  return apiRequest(
    relayUrl, "GET",
    `/messages/${encodeURIComponent(identity)}`,
    undefined,
    {
      "X-Poweur-Identity": identity,
      "X-Poweur-Challenge": challenge,
      "X-Poweur-Signature": signature,
    }
  );
}

// ─── Messaging ────────────────────────────────────────────────────────────────

export async function sendMessage(relayUrl, message) {
  return apiRequest(relayUrl, "POST", "/messages", message);
}

export async function submitAck(relayUrl, ack) {
  return apiRequest(relayUrl, "POST", "/acks", ack);
}

// ─── Sessions ─────────────────────────────────────────────────────────────────

export async function registerSession(relayUrl, request) {
  return apiRequest(relayUrl, "POST", "/sessions", request);
}

export async function revokeSession(relayUrl, sessionId, request) {
  return apiRequest(relayUrl, "DELETE", `/sessions/${encodeURIComponent(sessionId)}`, request);
}

// ─── Relay Health ─────────────────────────────────────────────────────────────

export async function checkHealth(relayUrl) {
  return apiRequest(relayUrl, "GET", "/health");
}

/** Fetch the relay's canonical address (host[:port]) for use in canonical strings. */
export async function fetchRelayAddress(relayUrl) {
  const { relay_address } = await apiRequest(relayUrl, "GET", "/");
  if (!relay_address) throw new Error("Relay did not return its address");
  return relay_address;
}

// ─── Identity resolution (web-first) ──────────────────────────────────────────

const DOH_URL = "https://cloudflare-dns.com/dns-query";

async function dohLookup(name, type) {
  const url = `${DOH_URL}?name=${encodeURIComponent(name)}&type=${type}`;
  const res = await fetch(url, { headers: { Accept: "application/dns-json" } });
  if (!res.ok) throw new Error(`DoH lookup failed: HTTP ${res.status}`);
  const data = await res.json();
  if (data.Status !== 0 || !data.Answer?.length) return [];
  return data.Answer.map(r => r.data?.replace(/^"|"$/g, "") ?? "");
}

/**
 * Fetch identity document via well-known or relay API.
 * @param {string} identity
 * @param {{ relayUrl?: string }} [opts] — when set, also try GET /identities/:id
 *   (needed when the browser cannot set Host for virtual hosting).
 */
export async function fetchIdentityDocument(identity, opts = {}) {
  const hostname = typeof window !== "undefined" ? window.location.hostname.toLowerCase() : "";
  const candidates = [];

  if (hostname && hostname === identity.toLowerCase()) {
    candidates.push("/.well-known/poweur/id.json");
  }
  if (opts.relayUrl) {
    // Same-origin style: ask the relay API (returns identity_document when hosted)
    candidates.push(`${opts.relayUrl.replace(/\/$/, "")}/identities/${encodeURIComponent(identity)}`);
  }
  candidates.push(`https://${identity}/.well-known/poweur/id.json`);
  // Local/dev HTTP well-known (Host must match — only works when already on that host)
  if (opts.relayUrl && opts.relayUrl.startsWith("http://")) {
    candidates.push(`${opts.relayUrl.replace(/\/$/, "")}/.well-known/poweur/id.json`);
  }

  let lastErr;
  for (const url of candidates) {
    try {
      const res = await fetch(url, { method: "GET", redirect: "error" });
      if (!res.ok) {
        lastErr = new Error(`well-known ${res.status}`);
        continue;
      }
      const data = await res.json();
      // GET /identities wraps the document
      if (data.identity_document) {
        return typeof data.identity_document === "string"
          ? JSON.parse(data.identity_document)
          : data.identity_document;
      }
      if (data.public_key && data.identity) {
        // May be IdentityResponse without nested document — synthesize view
        if (data.version || data.signature) return data;
        return {
          version: 1,
          identity: data.identity,
          public_key: data.public_key.startsWith("ed25519:")
            ? data.public_key
            : `ed25519:${data.public_key}`,
          encryption_public_key: data.encryption_public_key
            ? (data.encryption_public_key.startsWith("x25519:")
              ? data.encryption_public_key
              : `x25519:${data.encryption_public_key}`)
            : undefined,
          relay: data.relay,
        };
      }
      return data;
    } catch (e) {
      lastErr = e;
    }
  }
  throw lastErr || new Error("identity document not found");
}

/**
 * Resolve an identity like CLI `identity lookup`.
 * Returns { source: "web"|"dns"|"api", document }.
 */
export async function lookupIdentity(identity, opts = {}) {
  try {
    const document = await fetchIdentityDocument(identity, opts);
    const source = opts.relayUrl ? "web" : "web";
    return { source, document };
  } catch {
    /* fall through to DNS */
  }

  const pubRecords = await dohLookup(`_poweur.${identity}`, "TXT");
  let public_key = "";
  for (const rec of pubRecords) {
    if (rec.startsWith("poweur-pubkey=")) {
      public_key = rec.slice("poweur-pubkey=".length);
      if (!public_key.startsWith("ed25519:")) public_key = `ed25519:${public_key}`;
      break;
    }
  }
  if (!public_key) throw new Error("identity not found via web or DNS");

  let encryption_public_key = "";
  const encRecords = await dohLookup(`_poweur-enc.${identity}`, "TXT");
  for (const rec of encRecords) {
    if (rec.startsWith("poweur-enckey=")) {
      encryption_public_key = rec.slice("poweur-enckey=".length);
      if (!encryption_public_key.startsWith("x25519:")) {
        encryption_public_key = `x25519:${encryption_public_key}`;
      }
      break;
    }
  }

  return {
    source: "dns",
    document: {
      version: 1,
      identity,
      public_key,
      encryption_public_key: encryption_public_key || undefined,
      relay: identity,
    },
  };
}

/**
 * Look up a recipient's signing public key (web-first, then DNS TXT).
 * Returns base64url public key string or null.
 */
export async function resolveSigningKey(identity, opts = {}) {
  try {
    const { document } = await lookupIdentity(identity, opts);
    if (document?.public_key) {
      let val = document.public_key;
      if (val.startsWith("ed25519:")) val = val.slice("ed25519:".length);
      return val;
    }
  } catch { /* ignore */ }
  return null;
}

/**
 * Look up a recipient's X25519 encryption public key (web-first, then DNS TXT).
 * Returns base64url public key string or null.
 */
export async function resolveEncryptionKey(identity, opts = {}) {
  try {
    const { document } = await lookupIdentity(identity, opts);
    if (document?.encryption_public_key) {
      let val = document.encryption_public_key;
      if (val.startsWith("x25519:")) val = val.slice("x25519:".length);
      return val;
    }
  } catch { /* ignore */ }
  return null;
}

/**
 * Resolve recipient's relay address — unused in MVP (direct send uses recipient DNS).
 */
export async function resolveRelay(_identity) {
  return null;
}
