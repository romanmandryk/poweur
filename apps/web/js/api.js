/**
 * Relay HTTP client for the Eurything web client.
 * Talks to the relay API and does DNS-over-HTTPS lookups via Cloudflare.
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
 * request: {
 *   identity, public_key, encryption_public_key,
 *   dns_provider, dns_token,
 *   issued_at, nonce, identity_signature
 * }
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
 * Fetch an identity's public key from the relay.
 */
export async function getIdentityKey(relayUrl, identity) {
  return apiRequest(relayUrl, "GET", `/identities/${encodeURIComponent(identity)}`);
}

// ─── Inbox ────────────────────────────────────────────────────────────────────

/**
 * Fetch a challenge for inbox authentication.
 * Returns { challenge: string }
 */
export async function getChallenge(relayUrl, identity) {
  return apiRequest(
    relayUrl, "GET",
    `/auth/challenge?identity=${encodeURIComponent(identity)}`
  );
}

/**
 * Fetch pending messages and acks for an identity.
 * signature: base64url signature of the challenge string.
 */
export async function fetchInbox(relayUrl, identity, challenge, signature) {
  return apiRequest(
    relayUrl, "GET",
    `/messages/${encodeURIComponent(identity)}`,
    undefined,
    {
      "X-Eurything-Identity": identity,
      "X-Eurything-Challenge": challenge,
      "X-Eurything-Signature": signature,
    }
  );
}

// ─── Messaging ────────────────────────────────────────────────────────────────

/**
 * Send an encrypted, signed message.
 * message: full Message object per relay types.
 */
export async function sendMessage(relayUrl, message) {
  return apiRequest(relayUrl, "POST", "/messages", message);
}

/**
 * Submit a delivery acknowledgement.
 */
export async function submitAck(relayUrl, ack) {
  return apiRequest(relayUrl, "POST", "/acks", ack);
}

// ─── Sessions ─────────────────────────────────────────────────────────────────

/**
 * Register a short-lived session key.
 * request: SessionCreateRequest
 */
export async function registerSession(relayUrl, request) {
  return apiRequest(relayUrl, "POST", "/sessions", request);
}

/**
 * Revoke a session.
 */
export async function revokeSession(relayUrl, sessionId, request) {
  return apiRequest(relayUrl, "DELETE", `/sessions/${encodeURIComponent(sessionId)}`, request);
}

// ─── Relay Health ─────────────────────────────────────────────────────────────

export async function checkHealth(relayUrl) {
  return apiRequest(relayUrl, "GET", "/health");
}

// ─── DNS-over-HTTPS (Cloudflare) ─────────────────────────────────────────────

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
 * Look up a recipient's signing public key via DNS TXT.
 * Record format: eurything-pubkey=ed25519:<base64url>
 * Returns base64url public key string or null.
 */
export async function resolveSigningKey(identity) {
  const records = await dohLookup(`_eurything.${identity}`, "TXT");
  for (const rec of records) {
    if (rec.startsWith("eurything-pubkey=")) {
      let val = rec.slice("eurything-pubkey=".length);
      if (val.startsWith("ed25519:")) val = val.slice("ed25519:".length);
      return val;
    }
  }
  return null;
}

/**
 * Look up a recipient's X25519 encryption public key via DNS TXT.
 * Record format: eurything-enckey=x25519:<base64url>
 * Returns base64url public key string or null.
 */
export async function resolveEncryptionKey(identity) {
  const records = await dohLookup(`_eurything-enc.${identity}`, "TXT");
  for (const rec of records) {
    if (rec.startsWith("eurything-enckey=")) {
      let val = rec.slice("eurything-enckey=".length);
      if (val.startsWith("x25519:")) val = val.slice("x25519:".length);
      return val;
    }
  }
  return null;
}

/**
 * Resolve recipient's relay address from DNS (A/CNAME for the identity FQDN).
 * Returns the relay base URL or null if not resolvable.
 */
export async function resolveRelay(identity) {
  // Try TXT first for explicit relay annotation (future extension)
  // For now, derive from the identity's FQDN host lookup:
  // The relay runs at the identity's domain.
  // In practice, we fallback to the home relay for cross-relay delivery.
  return null;
}
