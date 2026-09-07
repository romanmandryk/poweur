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
| `POST /identities` | owner-only / admin | DNS-token (self-hosted) or invite (hosted) + identity-signed |
| `POST /identities/:identity/encryption-key` | owner-only / admin | DNS-token + identity-signed |
| `POST /identities/:identity/export` | owner-only | identity-signed export envelope → `application/gzip` |
| `POST /identities/:identity/rotate` | owner-only | old-key rotation signature + new signed document |
| `PUT /identities/:identity/keystore` | owner-only | identity-signed enrollment |
| `POST /identities/:identity/keystore/fetch` | **authenticator-only** | WebAuthn assertion — the one endpoint that does *not* require the identity key |
| `DELETE /identities/:identity/keystore/:enrollment` | owner-only | identity-signed removal |
| `POST /identities/:identity/keystore/list` | owner-only | identity-signed; metadata only |
| `POST /identities/:identity/enroll/offer` | **open** | new device has no key yet; capped per identity |
| `POST /identities/:identity/enroll/:rendezvous/fetch` | owner-only | identity-signed |
| `POST /identities/:identity/enroll/:rendezvous/deliver` | owner-only | identity-signed |
| `GET`/`DELETE /identities/:identity/enroll/:rendezvous` | bearer | rendezvous id; releases only ciphertext |

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

Register a new identity on this relay. Two modes:

### Hosted registration (no DNS writes)

Omit `dns_provider` / `dns_token`. The identity must be under a domain listed in
`HOSTED_DOMAINS`. Supply a signed `identity_document` (see [Web Identity](/protocol/web-identity)).
A single wildcard DNS A/CNAME for the hosted domain routes all identities; the
relay persists the document under `POWEUR_DATA` and serves it at
`/.well-known/poweur/id.json`.

### DNS registration (self-hosted)

Include `dns_provider` + `dns_token`. The relay writes zone records as before.
When `POWEUR_DATA` is set, a signed `identity_document` is also required so the
relay can serve well-known endpoints.

**Owner-only:** the request always includes an identity-signed admin envelope
(`issued_at`, `nonce`, `identity_signature`) so a DNS-token holder cannot
register a public key they do not control.

### Request body

```json
{
  "identity":              "alice.poweur.net",
  "public_key":            "<base64url-encoded Ed25519 public key>",
  "encryption_public_key": "<base64url-encoded X25519 public key>",
  "dns_provider":          "cloudflare",
  "dns_token":             "<scoped DNS provider API token>",
  "identity_document":     { "version": 1, "identity": "...", "signature": "..." },
  "invite_code":           "<optional; required when REGISTRATION_GATE=invite>",
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
| `dns_provider` | string | DNS provider — `cloudflare` or `hetzner`. Omit for hosted registration. |
| `dns_token` | string | Scoped API token for the target DNS zone. Omit for hosted registration. |
| `identity_document` | object | Signed Identity Document (required for hosted; required when `POWEUR_DATA` is set) |
| `issued_at` | string | RFC3339 UTC timestamp; relay enforces a recency window |
| `nonce` | string | Per-request nonce; included in the canonical string to bind the signature to this exact request |
| `identity_signature` | string | Base64-encoded Ed25519 signature over the canonical identity-registration string, verified against the `public_key` in this body |

Also: `GET /.well-known/poweur/id.json` (Host-routed) serves the stored document.

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

## POST /identities/:identity/export

Owner-signed export of the identity home directory as `application/gzip` (tar.gz).
Canonical string: `identity-export\n<identity>\n<issued_at>\n<nonce>`.

## POST /identities/:identity/rotate

Rotate the long-lived signing key. Body includes `identity_document` (signed by the **new**
key, with `previous_keys`), `new_public_key`, and `rotation_signature` from the **old** key
over `identity-rotation\n…`. See [Web identity — Key rotation](/protocol/web-identity).

## GET /health

Liveness check. Used by load balancers, monitoring systems, and client connectivity checks.
When `POWEUR_DATA` is set, includes storage health; `status` may be `degraded` if not writable.

### Response body

```json
{
  "status":  "ok",
  "version": "0.1.0",
  "storage": {
    "configured": true,
    "path": "/data",
    "writable": true,
    "free_bytes": 123456789
  }
}
```

### Responses

| Status | Meaning |
|--------|---------|
| `200 OK` | Relay is healthy (or degraded but still serving) |

---

## Keystore (EPIC-011)

Wrapped copies of an identity's master seed, one per enrolled authenticator. The relay stores
**ciphertext it cannot open** — wrapping secrets never leave the authenticator or device. See
[Key management & recovery](/security/key-management).

Writes are authenticated by the identity key; the read is authenticated by a WebAuthn assertion
instead. That asymmetry is deliberate: the read exists to recover an identity whose key you no
longer hold, so requiring that key would be circular.

The wrapped blobs deliberately live **outside** the DAV tree. They need a read path the identity
key cannot provide, and a blob inside the user's file tree would be one misplaced delete away
from destroying their recovery.

### PUT /identities/:identity/keystore

Store or replace one enrollment. Signed with `keystore-enroll`:

```
keystore-enroll\n<identity>\n<enrollment_id>\n<kind>\n<credential_id>\n<wrapped_digest>\n<issued_at>\n<nonce>
```

`wrapped_digest` is the base64url SHA-256 of the raw `wrapped` JSON as sent. It binds the
signature to the exact ciphertext, so a swapped blob under an otherwise valid authorization is
rejected.

```json
{
  "enrollment_id":         "enr-001",
  "kind":                  "passkey",
  "wrap":                  "prf",
  "payload":               "seed",
  "credential_id":         "<base64url>",
  "credential_public_key": "<SPKI DER, base64url>",
  "credential_alg":        -8,
  "wrapped":               { "iv": "...", "ciphertext": "..." },
  "label":                 "Laptop",
  "role":                  "device",
  "issued_at":             "2026-01-15T09:30:00Z",
  "nonce":                 "...",
  "identity_signature":    "..."
}
```

| Field | Values |
|-------|--------|
| `kind` | `passkey` \| `hardware-key` \| `cli-passphrase` \| `recovery-kit` \| `native` |
| `wrap` | `prf` \| `pin` \| `passphrase` \| `native` |
| `payload` | `seed` \| `legacy-keypair` (identities predating the seed model) |
| `credential_alg` | COSE id: `-7` ES256, `-8` EdDSA, `-257` RS256 |
| `role` | `device` (default) \| `recovery-master` |

`credential_public_key` is **SPKI DER**, exactly what WebAuthn's `getPublicKey()` returns — not
raw COSE. This keeps assertion verification inside the standard library rather than adding a
CBOR/COSE parser to the trusted path.

`credential_id` and `credential_public_key` must be supplied together: a passkey enrollment the
relay cannot verify could never satisfy the bootstrap read, so it is rejected rather than stored
as a dead entry.

**Responses:** `200` with `{enrollment_id, created_at}`; `400` invalid fields; `401` bad
signature; `404` unknown identity.

### POST /identities/:identity/keystore/fetch

The bootstrap read. Obtain a challenge from `GET /auth/challenge?identity=...`, sign it with an
enrolled authenticator, and post the assertion:

```json
{
  "assertion": {
    "credential_id":      "<base64url>",
    "client_data_json":   "<base64url>",
    "authenticator_data": "<base64url>",
    "signature":          "<base64url>"
  },
  "rp_id": "poweur.net"
}
```

Verified: `clientDataJSON.type` is `webauthn.get`; the challenge matches the relay-issued one;
`rpIdHash` matches an acceptable relying-party id; the user-present **and user-verified** flags
are set; and the signature verifies over `authenticatorData || SHA-256(clientDataJSON)`.

Acceptable `rp_id` values are the identity itself or its registrable domain when hosted — a
credential may legitimately be scoped to `poweur.net` while the request arrives at
`alice.poweur.net` (see EPIC-018 E18-T4).

**Responses:** `200` with `{identity, entries[]}` (ciphertext only); `401` for anything
rejected; `404` unknown identity.

:::note
The challenge is **single-use and consumed on every attempt**, so a captured assertion cannot be
replayed. An unenrolled credential id and a bad signature return byte-identical `401` responses:
the caller must not learn which credentials are enrolled. Combined with discoverable credentials
(the web client already sets `residentKey: "required"`), the relay never reveals credential ids
to an unverified caller.
:::

### POST /identities/:identity/keystore/list

Enumerate enrollments for the owner — the "Keys & devices" inventory. Signed with
`keystore-list\n<identity>\n<issued_at>\n<nonce>`.

Returns **metadata only**: `enrollment_id`, `kind`, `wrap`, `payload`, `label`, `role`,
`has_passkey`, `created_at`, `last_used_at`. Listing your devices needs no access to the
wrapped seed copies, so the ciphertext is not in the response at all.

### DELETE /identities/:identity/keystore/:enrollment

Remove an enrollment. Signed with `keystore-remove`:

```
keystore-remove\n<identity>\n<enrollment_id>\n<issued_at>\n<nonce>
```

**Responses:** `204`; `401` bad signature; `404` unknown identity or enrollment.

Optional fields:

| Field | Effect |
|-------|--------|
| `actor_assertion` | WebAuthn assertion proving the caller holds a `recovery-master` authenticator |
| `rp_id` | Relying party the actor assertion was scoped to |
| `allow_last` | Permit removing the final enrollment (refused by default) |
| `revoke_sessions` | Also end the removed device's live sessions |

**Recovery-master gating.** Once an identity has a `recovery-master` enrollment, every removal
must carry `actor_assertion` from it. This is what makes the role enforceable rather than
advisory: the identity key is shared by every device, so an identity signature alone says
nothing about *which* device is asking — and without the extra proof a stolen phone could evict
the very security key meant to revoke it.

Removing the last enrollment returns `409 last_enrollment` unless `allow_last` is set. Silently
stranding recovery is worse than an error the caller has to acknowledge.

**Responses:** `204`, or `200` with `{enrollment_id, sessions_revoked}` when sessions were
revoked; `401` bad signature; `403` recovery-master required; `404`; `409` last enrollment.

:::caution
Removal denies that authenticator the bootstrap read. It does **not** protect against an
attacker who already extracted the seed — that is what rotation is for.
:::

---

## Device enrollment ceremony (EPIC-011)

Moving a seed to a new device needs an **authentic** channel, not a secret one. The new device
generates an ephemeral X25519 keypair and displays a six-digit code derived from its public key;
the user types that code on a device that already holds the identity, which seals the seed to
the ephemeral key. See [Key management & recovery](/security/key-management).

The relay is a blind letterbox throughout: it sees an ephemeral public key and a sealed blob,
and can open neither.

:::note Why there is no PAKE
An earlier design had the code protect the payload, which would have made it a six-digit
password — brute-forceable offline by anyone holding the ciphertext, hence the usual SPAKE2
machinery. Having the *new* device generate the keypair removes the requirement entirely: the
code authenticates a public key and encrypts nothing, so there is no offline target. Forging it
means finding a colliding code on the first and only try. This is the numeric-comparison model
used by Bluetooth pairing and Signal safety numbers, and it needs no exotic primitive — which
matters, because no reviewed browser PAKE implementation exists.
:::

### POST /identities/:identity/enroll/offer

Opened by the **new** device. Unauthenticated by necessity — it has no key yet — so offers are
capped at 5 concurrent per identity (`429 too_many_offers`) and expire after 10 minutes.

```json
{ "ephemeral_public_key": "<32-byte x25519, base64url>", "label": "Firefox on Linux" }
```

Returns `201` with `{rendezvous_id, sas, expires_at}`. **Compute the SAS yourself** from the
ephemeral key rather than trusting the relay's copy:

```
sas = SHA-256("poweur/v1/enroll-sas\n" + ephemeral_public_key)[0:4] as uint32 mod 10^6, zero-padded to 6
```

### POST /identities/:identity/enroll/:rendezvous/fetch

The approving device asks what it is approving. Signed with
`enroll-fetch\n<identity>\n<rendezvous_id>\n<issued_at>\n<nonce>` — the rendezvous id is bound in,
so an approval cannot be redirected to a different offer. Returns the ephemeral public key, the
SAS and the device label.

### POST /identities/:identity/enroll/:rendezvous/deliver

Posts the sealed seed. Signed with `enroll-deliver\n<identity>\n<rendezvous_id>\n<issued_at>\n<nonce>`.
`sealed` is opaque to the relay. Delivering twice returns `409`.

### GET /identities/:identity/enroll/:rendezvous

Polled by the new device. Returns `{ready: false}` until approval, then `{ready: true, sealed}`
**once** — the rendezvous is consumed, so a captured id cannot be replayed. The id acts as a
bearer token, which is safe because it releases only ciphertext requiring the ephemeral private
key that never left the new device.

### DELETE /identities/:identity/enroll/:rendezvous

Abandon an offer, freeing its slot. Without this a user who backed out would occupy one of the
five slots until it expired.

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
