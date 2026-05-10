---
id: api-reference
sidebar_position: 2
title: API Reference
---

# Relay API Reference

The relay exposes an HTTP/JSON API consumed by mobile clients, the CLI, and peer relays. All endpoints consume and produce `application/json`. All requests and responses use UTF-8 encoding.

**Base URL:** `https://<relay-host>`

## Endpoint classification {#endpoint-classes}

The relay exposes two distinct surfaces and authenticates them differently:

- **Open / messaging** — anyone may call. Authentication is per-message:
  every envelope (message or ack) is signature-verified before storage.
  Per-sender and per-relay rate limits run before signature verification
  to keep the cheap-rejection path fast.
- **Owner-only / admin** — must prove ownership of the identity in
  question via either a challenge–response (already-implemented for
  `GET /messages/:identity`) or an identity-signed admin envelope
  (`issued_at`, `nonce`, `identity_signature`). The DNS-token check on
  `POST /identities` is necessary but no longer sufficient on its own.

| Endpoint | Class | Notes |
|----------|-------|-------|
| `POST /messages` | open / messaging | Subject to the at-least-one-local rule (see below) |
| `POST /acks` | open / messaging | Same rule and rate limits as `/messages` |
| `GET /health` | public read | Global rate limit only |
| `GET /identities/:identity` | public read | Global rate limit only |
| `GET /auth/challenge` | public read | Issues short-lived owner-only auth material |
| `GET /messages/:identity` | owner-only / admin | Challenge–response authenticated |
| `POST /sessions` | owner-only / admin | Identity-signed |
| `DELETE /sessions/:id` | owner-only / admin | Identity-signed |
| `POST /identities` | owner-only / admin | DNS-token + identity-signed |
| `POST /identities/:identity/encryption-key` | owner-only / admin | DNS-token + identity-signed |

## At-least-one-local rule {#at-least-one-local-rule}

Both `POST /messages` and `POST /acks` enforce a single forwarding rule
that prevents the relay from being abused as an open forwarder for the
world:

```
senderLocal    = identityStore.Exists(envelope.sender)    || dns(envelope.sender)    points here
recipientLocal = identityStore.Exists(envelope.recipient) || dns(envelope.recipient) points here
accept iff senderLocal || recipientLocal
```

Three accepted cases follow:

1. **Recipient-local** (normal inbound): store in the local inbox.
2. **Sender-local, recipient-remote** (the only sanctioned forward, used
   by the `--via-home-relay` privacy proxy): forward over HTTP to the
   recipient relay (DNS-resolved).
3. **Both local** (note-to-self): store in the local inbox.

Anything else returns `403 not_authorized` *before* signature
verification, so the cheap reject path stays cheap.

---

## POST /messages

Submit a signed message for delivery. The relay verifies the sender's
signature, applies the at-least-one-local rule, and then either stores
the message for a local recipient or forwards it to the recipient's relay
over HTTP.

By default, **clients post directly to the recipient's relay**, resolved
via DNS (`A` / `CNAME` for `<recipient>` or `HOST:<recipient>` in the
fake-DNS test environment). The sender's home relay never sees the
outbound traffic. Clients that want to hide their IP from the recipient's
relay opt into the home-relay proxy mode (CLI flag `--via-home-relay`),
which posts to the sender's home relay; the home relay accepts because
the sender is local and forwards over HTTP to the recipient relay.

### Rate limiting

Rate limit checks run **before** signature verification. Both per-sender
and global per-relay buckets are evaluated; whichever fires first
produces the `429`. See [Rate Limiting](/protocol/rate-limiting) for the
default thresholds.

### Request body

```json
{
  "id":        "msg_01j9xkay7g000000000000000",
  "sender":    "alice.poweur.net",
  "recipient": "bob.example.org",
  "timestamp": "2026-03-28T12:00:00Z",
  "payload":   "<base64url AEAD ciphertext>",
  "signature": "<base64 Ed25519 signature over canonical fields>",
  "session_id": "sess_01j...",
  "session_proof": {
    "session_public_key":  "<base64url>",
    "issued_at":           "...",
    "expires_at":          "...",
    "nonce":               "...",
    "identity_signature":  "<base64>"
  },
  "encryption": {
    "alg":                  "x25519-chacha20-poly1305",
    "ephemeral_public_key": "<base64url>",
    "nonce":                "<base64url>"
  }
}
```

`id`, `sender`, `recipient`, `timestamp`, `payload`, `signature`, and `encryption` are always required. The client-assigned `id` is bound into the canonical signing string and used as the inbox storage key on the recipient relay. `session_id` and `session_proof` are optional — MVP clients include `session_id` on every routine (session-signed) send. Envelopes without `encryption.alg`, `encryption.ephemeral_public_key`, or `encryption.nonce` are rejected with `400 encryption_required` before signature verification or rate-limiting runs. See [Message Format](/protocol/message-format) for the canonical signing string and the signature verification rules.

### Responses

| Status | Meaning |
|--------|---------|
| `202 Accepted` | Message accepted for delivery or forwarding (corresponds to delivery tick 1 — see [Delivery Acks](/protocol/delivery-acks)) |
| `400 Bad Request` | Malformed message envelope (missing field, invalid JSON, invalid timestamp format). `encryption_required` when the envelope is missing the `encryption` block. |
| `401 Unauthorized` | Signature verification failed, or `session_expired` when `session_id` is unknown and no valid `session_proof` is attached |
| `403 Forbidden` | `not_authorized` — neither sender nor recipient is locally hosted on this relay (see [at-least-one-local rule](#at-least-one-local-rule)) |
| `413 Content Too Large` | Request body exceeds 512 KB |
| `429 Too Many Requests` | Sender or relay exceeded a rate-limit bucket. The `scope` field in the body is `sender` or `global`. |

When the relay returns `401 session_expired`, the client should silently register a new session via `POST /sessions` and retry the send.

**429 response body:**
```json
{
  "error":    "rate_limit_exceeded",
  "scope":    "sender",
  "window":   "minute",
  "limit":    20,
  "reset_at": "2026-03-28T12:01:00Z"
}
```

The `202 Accepted` body echoes the client-assigned `id` so the sender can
correlate the response with their pending journal entry:

```json
{
  "id": "msg_01j9xkay7g000000000000000"
}
```

---

## POST /acks

Submit a signed delivery acknowledgement. Open / messaging-class endpoint
that shares signature verification, rate limits, and the at-least-one-
local rule with `POST /messages`. In v1 the only valid `state` is
`delivered_client`, recorded by a recipient client immediately after a
successful decrypt.

### Request body

```json
{
  "type":       "ack",
  "id":         "ack_01j...",
  "message_id": "msg_01j...",
  "state":      "delivered_client",
  "sender":     "bob.example.org",
  "recipient":  "alice.poweur.net",
  "timestamp":  "2026-03-28T12:04:05Z",
  "signature":  "<base64 Ed25519 signature over canonical fields>",
  "session_id": "sess_...",
  "session_proof": { ... }
}
```

`sender` is the party that produced the ack (the recipient of the
original message). `recipient` is the party that cares whether tick 2
ever arrives (the original message's `sender`). See
[Delivery Acks](/protocol/delivery-acks) for the canonical signing string
and the journal semantics.

### Responses

| Status | Meaning |
|--------|---------|
| `202 Accepted` | Ack accepted; will be drained on the next `GET /messages/:identity` for the recipient |
| `400 Bad Request` | Malformed ack envelope or unsupported `state` value |
| `401 Unauthorized` | Signature verification failed |
| `403 Forbidden` | `not_authorized` — neither party is locally hosted (see [at-least-one-local rule](#at-least-one-local-rule)) |
| `413 Content Too Large` | Request body too large |
| `429 Too Many Requests` | Sender or relay exceeded a rate-limit bucket |

---

## GET /messages/:identity

Retrieve pending messages for a locally hosted identity. The requester must prove ownership via a challenge–response flow before messages are returned.

### Authentication

Before calling this endpoint, obtain a challenge from `GET /auth/challenge?identity=<identity>`. Sign the challenge and include the signature and identity in request headers.

| Header | Required | Value |
|--------|:--------:|-------|
| `X-Poweur-Identity`   | Yes | The identity subdomain (e.g. `alice.poweur.net`) |
| `X-Poweur-Signature`  | Yes | Base64-encoded signature of the challenge string |
| `X-Poweur-Session-Id` | No  | Session identifier when the challenge is signed with the session key. If omitted, the relay verifies with the long-lived identity key. |

When `X-Poweur-Session-Id` is present but the session is unknown or expired, the relay responds with `401 session_expired` so the client can re-register and retry.

### Path parameters

| Parameter | Description |
|-----------|-------------|
| `:identity` | Fully qualified identity subdomain (`alice.poweur.net`) |

### Response body

```json
{
  "messages": [
    {
      "id":        "msg_01j9xk7q2f000000000000000",
      "sender":    "bob.example.org",
      "recipient": "alice.poweur.net",
      "timestamp": "2026-03-28T12:00:00Z",
      "payload":   "<base64url AEAD ciphertext>",
      "signature": "<base64-encoded signature>",
      "session_id": "sess_01j...",
      "encryption": {
        "alg":                  "x25519-chacha20-poly1305",
        "ephemeral_public_key": "<base64url>",
        "nonce":                "<base64url>"
      }
    }
  ],
  "acks": [
    {
      "type":       "ack",
      "id":         "ack_01j...",
      "message_id": "msg_01j...",
      "state":      "delivered_client",
      "sender":     "bob.example.org",
      "recipient":  "alice.poweur.net",
      "timestamp":  "2026-03-28T12:04:05Z",
      "signature":  "<base64>"
    }
  ]
}
```

The relay forwards each envelope as it was signed. Clients should verify
the signature and, when `encryption` is set, decrypt with the recipient's
local X25519 private key before displaying the payload.

The `acks` array carries delivery acknowledgements for messages this
identity previously sent (tick 2 in the WhatsApp-style two-tick model —
see [Delivery Acks](/protocol/delivery-acks)). Both arrays are drained on
read, so a polling client gets exactly-one delivery of every pending
event in a single round-trip. If there is nothing pending, the
corresponding array is `[]`.

### Responses

| Status | Meaning |
|--------|---------|
| `200 OK` | Success — array of pending messages (may be empty) |
| `401 Unauthorized` | Challenge signature invalid, expired, or headers missing |
| `404 Not Found` | Identity is not hosted on this relay |

---

## GET /auth/challenge

Issue a short-lived challenge string for use in authenticated requests (specifically `GET /messages/:identity`).

### Query parameters

| Parameter | Required | Description |
|-----------|:--------:|-------------|
| `identity` | Yes | The identity subdomain requesting a challenge |

### Example request

```
GET /auth/challenge?identity=alice.poweur.net
```

### Response body

```json
{
  "challenge":  "eyJhbGciOiJub25lIn0.eyJpZGVudGl0eSI6ImFsaWNlLnBvd2V1ci5uZXQiLCJleHAiOjE3NDMxNjQ3MDB9.",
  "expires_at": "2026-03-28T12:05:00Z"
}
```

Challenges expire after **60 seconds** and are invalidated after first use. The relay stores issued challenges in memory; they are cleared on expiry or use, whichever comes first.

### Responses

| Status | Meaning |
|--------|---------|
| `200 OK` | Challenge issued |
| `400 Bad Request` | Missing or invalid `identity` parameter |

---

## POST /identities

Register a new identity on this relay. **Owner-only / admin endpoint:**
in addition to the DNS provider token (which authorises the zone write),
the request body must include an identity-signed admin envelope so the
relay can verify the caller actually holds the private key for the
`public_key` they are publishing. A hostile DNS-token holder cannot
register an arbitrary identity public key.

The DNS provider token is used during this request only and **discarded
immediately** after the DNS writes succeed or fail. The relay stores no
write credentials at rest.

### Request body

```json
{
  "identity":              "alice.poweur.net",
  "public_key":            "<base64url-encoded Ed25519 public key>",
  "encryption_public_key": "<base64url-encoded X25519 public key>",
  "dns_provider":          "cloudflare",
  "dns_token":             "<scoped DNS provider API token>",
  "issued_at":             "2026-03-28T12:00:00Z",
  "nonce":                 "<base64url random nonce>",
  "identity_signature":    "<base64 signature of canonical identity-registration string>"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `identity` | string | Fully qualified identity subdomain to register |
| `public_key` | string | Base64url-encoded Ed25519 identity public key (no padding) |
| `encryption_public_key` | string | Base64url-encoded X25519 encryption public key (no padding). Optional in the API but written for every identity by CLI and mobile clients in the MVP. |
| `dns_provider` | string | DNS provider to use — `cloudflare` or `hetzner` |
| `dns_token` | string | Scoped API token for the target DNS zone |
| `issued_at` | string | RFC3339 UTC timestamp; relay enforces a recency window |
| `nonce` | string | Per-request nonce; included in the canonical string to bind the signature to this exact request |
| `identity_signature` | string | Base64-encoded Ed25519 signature over the canonical identity-registration string, verified against the `public_key` in this body |

The canonical identity-registration string is:

```
identity-registration
<identity>
<public_key>
<encryption_public_key>
<relay_address>
<issued_at>
<nonce>
```

`<relay_address>` is the host[:port] of the relay the request is being
made against (the relay's `RelayAddress` config value). `<encryption_public_key>` is the empty string when not supplied.

### DNS records written

On success, the relay creates (or updates) up to three DNS records:

1. `TXT` at `_poweur.<identity>` — `poweur-pubkey=ed25519:<public_key>`
2. `TXT` at `_poweur-enc.<identity>` — `poweur-enckey=x25519:<encryption_public_key>` (only when `encryption_public_key` is supplied)
3. `A` (or `CNAME`) at `<identity>` — pointing to the relay's own address

### Responses

| Status | Meaning |
|--------|---------|
| `201 Created` | Identity registered and DNS records written |
| `400 Bad Request` | Malformed request, missing fields (including a missing admin envelope), or unsupported `dns_provider` value |
| `401 Unauthorized` | `identity_signature` failed to verify against the body's `public_key` |
| `409 Conflict` | Identity is already registered on this relay |
| `502 Bad Gateway` | DNS provider write failed (token invalid, zone not found, etc.) |

**201 response body:**
```json
{
  "identity":              "alice.poweur.net",
  "public_key":            "<base64url-encoded identity public key>",
  "encryption_public_key": "<base64url-encoded encryption public key>",
  "relay":                 "relay.poweur.net",
  "created_at":            "2026-03-28T12:00:00Z"
}
```

**502 response body:**
```json
{
  "error":   "dns_write_failed",
  "detail":  "Cloudflare API returned 403: token lacks zone:edit permission"
}
```

---

## POST /identities/:identity/encryption-key

Add or replace the X25519 encryption public key for a locally hosted
identity. This endpoint exists so identities registered before E2E
encryption was mandatory can be retrofitted without re-registering.
**Owner-only / admin endpoint:** the request must carry an identity-
signed admin envelope verified against the registered identity's
long-lived signing key. The DNS provider token is used for the one DNS
write and discarded immediately.

### Path parameters

| Parameter | Description |
|-----------|-------------|
| `:identity` | Fully qualified identity subdomain hosted on this relay |

### Request body

```json
{
  "encryption_public_key": "<base64url-encoded X25519 public key>",
  "dns_provider":          "cloudflare",
  "dns_token":             "<scoped DNS provider API token>",
  "issued_at":             "2026-03-28T12:00:00Z",
  "nonce":                 "<base64url random nonce>",
  "identity_signature":    "<base64 signature of canonical identity-encryption-key string>"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `encryption_public_key` | string | Base64url-encoded X25519 encryption public key (no padding) |
| `dns_provider` | string | DNS provider to use — `cloudflare` or `hetzner` |
| `dns_token` | string | Scoped API token for the target DNS zone |
| `issued_at` | string | RFC3339 UTC timestamp; relay enforces a recency window |
| `nonce` | string | Per-request nonce |
| `identity_signature` | string | Base64-encoded Ed25519 signature over the canonical identity-encryption-key string, verified against the registered identity's signing key |

The canonical identity-encryption-key string is:

```
identity-encryption-key
<identity>
<encryption_public_key>
<issued_at>
<nonce>
```

### DNS records written

1. `TXT` at `_poweur-enc.<identity>` — `poweur-enckey=x25519:<encryption_public_key>` (created or updated)

### Responses

| Status | Meaning |
|--------|---------|
| `200 OK` | Encryption key written to DNS |
| `400 Bad Request` | Malformed request, missing fields (including a missing admin envelope), or unsupported `dns_provider` value |
| `401 Unauthorized` | `identity_signature` failed to verify against the registered signing key |
| `404 Not Found` | Identity is not hosted on this relay |
| `502 Bad Gateway` | DNS provider write failed |

**200 response body:**
```json
{
  "identity":              "alice.poweur.net",
  "encryption_public_key": "<base64url-encoded encryption public key>"
}
```

The equivalent CLI command is `poweur identity add-encryption-key`.

---

## GET /identities/:identity

Look up the public key registered for an identity on this relay. Used by peer relays to fetch a sender's public key when a DNS `TXT` record lookup is unavailable or not yet propagated.

### Path parameters

| Parameter | Description |
|-----------|-------------|
| `:identity` | Fully qualified identity subdomain |

### Response body

```json
{
  "identity":   "alice.poweur.net",
  "public_key": "<base64url-encoded Ed25519 public key>"
}
```

### Responses

| Status | Meaning |
|--------|---------|
| `200 OK` | Identity found, public key returned |
| `404 Not Found` | Identity is not hosted on this relay |

---

## POST /sessions

Register a short-lived session key with the relay. The client generates a fresh Ed25519 keypair, signs a canonical session-registration string with the long-lived identity key, and submits it. The relay verifies the identity signature against the identity's DNS-published public key, enforces the 24-hour maximum TTL, issues a `session_id`, and caches the session in memory.

### Request body

```json
{
  "identity":            "alice.poweur.net",
  "session_public_key":  "<base64url Ed25519 session public key>",
  "issued_at":           "2026-03-28T08:00:00Z",
  "expires_at":          "2026-03-29T08:00:00Z",
  "nonce":               "<base64url random nonce>",
  "identity_signature":  "<base64 signature of canonical session-registration string>",
  "device_fingerprint":  "optional opaque device id"
}
```

The canonical session-registration string is:

```
session-registration
<identity>
<session_public_key>
<issued_at>
<expires_at>
<nonce>
```

signed with the long-lived identity key (Ed25519). See [Identity Model → Session & Passkey Flow](/protocol/identity-model#sessions).

### Responses

| Status | Meaning |
|--------|---------|
| `201 Created` | Session registered |
| `400 Bad Request` | Missing/invalid fields, TTL over 24h, `issued_at` too far in the future, malformed timestamps |
| `401 Unauthorized` | `identity_signature` failed to verify against the identity's long-lived public key |

**201 response body:**
```json
{
  "session_id":         "sess_01j9xk7q...",
  "identity":           "alice.poweur.net",
  "session_public_key": "<base64url session public key>",
  "issued_at":          "2026-03-28T08:00:00Z",
  "expires_at":         "2026-03-29T08:00:00Z"
}
```

Relay restarts invalidate all sessions. Clients should treat `401 session_expired` on subsequent calls as a signal to re-register.

---

## DELETE /sessions/:id

Revoke a session. **Owner-only / admin endpoint:** the request body must
carry an identity-signed admin envelope so an attacker who guesses a
session id cannot invalidate someone else's sessions. Idempotent —
deleting a session that does not exist still returns `204`, but only
after authentication succeeds.

### Path parameters

| Parameter | Description |
|-----------|-------------|
| `:id` | The session id returned by `POST /sessions` |

### Request body

```json
{
  "identity":           "alice.poweur.net",
  "issued_at":          "2026-03-28T12:00:00Z",
  "nonce":              "<base64url random nonce>",
  "identity_signature": "<base64 signature of canonical session-revocation string>"
}
```

The canonical session-revocation string is:

```
session-revocation
<identity>
<session_id>
<issued_at>
<nonce>
```

The relay verifies the signature against the long-lived signing key of
the claimed `identity` (resolved from the local identity cache, then DNS
on miss). If the session is known and its owning identity does not match
the claimed `identity`, the relay rejects with `401 unauthorized`.

### Responses

| Status | Meaning |
|--------|---------|
| `204 No Content` | Session removed (or already absent) |
| `400 Bad Request` | Missing id or missing admin envelope |
| `401 Unauthorized` | `identity_signature` failed to verify, or session belongs to a different identity |

---

## GET /health

Liveness check. Returns a minimal response indicating the relay is running and reachable. Used by load balancers, monitoring systems, and client connectivity checks.

### Response body

```json
{
  "status":  "ok",
  "version": "0.1.0"
}
```

### Responses

| Status | Meaning |
|--------|---------|
| `200 OK` | Relay is healthy |

The `version` field reflects the relay software version and can be used by clients to detect incompatible protocol versions.

---

## Error Format

All error responses use a consistent JSON envelope:

```json
{
  "error":  "<error_code>",
  "detail": "<human-readable explanation>"
}
```

The `detail` field is informational and should not be parsed programmatically. Use the HTTP status code and `error` code for error handling logic.

## Related

- [Message Format](/protocol/message-format)
- [Routing](/protocol/routing)
- [Rate Limiting](/protocol/rate-limiting)
- [Security Model](/security/model)
