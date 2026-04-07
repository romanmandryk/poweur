---
id: api-reference
sidebar_position: 2
title: API Reference
---

# Relay API Reference

The relay exposes an HTTP/JSON API consumed by mobile clients, the CLI, and peer relays. All endpoints consume and produce `application/json`. All requests and responses use UTF-8 encoding.

**Base URL:** `https://<relay-host>`

---

## POST /messages

Submit a signed message for delivery. The relay inspects the `recipient` field, verifies the sender's signature, and either delivers the message to a local identity inbox or forwards it to the appropriate peer relay via DNS resolution.

### Rate limiting

Rate limit checks run **before** signature verification. Requests that exceed per-sender limits are rejected immediately without touching DNS or message content.

### Request body

```json
{
  "sender":    "alice.poweur.net",
  "recipient": "bob.example.org",
  "timestamp": "2026-03-28T12:00:00Z",
  "payload":   "Hello, Bob.",
  "signature": "<base64-encoded Ed25519 signature over canonical fields>"
}
```

All fields are required.

### Responses

| Status | Meaning |
|--------|---------|
| `202 Accepted` | Message accepted for delivery or forwarding |
| `400 Bad Request` | Malformed message envelope (missing field, invalid JSON, invalid timestamp format) |
| `401 Unauthorized` | Signature verification failed |
| `413 Content Too Large` | Request body exceeds 512 KB |
| `429 Too Many Requests` | Sender has exceeded rate limits |

**429 response body:**
```json
{
  "error": "rate_limit_exceeded",
  "window": "minute",
  "limit": 20,
  "reset_at": "2026-03-28T12:01:00Z"
}
```

---

## GET /messages/:identity

Retrieve pending messages for a locally hosted identity. The requester must prove ownership via a challenge–response flow before messages are returned.

### Authentication

Before calling this endpoint, obtain a challenge from `GET /auth/challenge?identity=<identity>`. Sign the challenge with the identity's private key and include the signature and identity in request headers.

| Header | Value |
|--------|-------|
| `X-Eurything-Identity` | The identity subdomain (e.g. `alice.poweur.net`) |
| `X-Eurything-Signature` | Base64-encoded signature of the challenge string |

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
      "payload":   "Hey Alice!",
      "signature": "<base64-encoded signature>"
    }
  ]
}
```

If there are no pending messages, `messages` is an empty array `[]`.

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

Register a new identity on this relay. The client provides its public key, the DNS provider to use, and a scoped API token for the identity's DNS zone. The relay writes the required DNS records and registers the identity as locally hosted.

The DNS provider token is used during this request only and **discarded immediately** after the DNS writes succeed or fail. The relay stores no write credentials at rest.

### Request body

```json
{
  "identity":     "alice.poweur.net",
  "public_key":   "<base64url-encoded Ed25519 public key>",
  "dns_provider": "cloudflare",
  "dns_token":    "<scoped DNS provider API token>"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `identity` | string | Fully qualified identity subdomain to register |
| `public_key` | string | Base64url-encoded Ed25519 public key (no padding) |
| `dns_provider` | string | DNS provider to use — `cloudflare` or `hetzner` |
| `dns_token` | string | Scoped API token for the target DNS zone |

### DNS records written

On success, the relay creates (or updates) two DNS records:

1. `TXT` at `_eurything.<identity>` — `eurything-pubkey=ed25519:<public_key>`
2. `A` (or `CNAME`) at `<identity>` — pointing to the relay's own address

### Responses

| Status | Meaning |
|--------|---------|
| `201 Created` | Identity registered and DNS records written |
| `400 Bad Request` | Malformed request, missing fields, or unsupported `dns_provider` value |
| `409 Conflict` | Identity is already registered on this relay |
| `502 Bad Gateway` | DNS provider write failed (token invalid, zone not found, etc.) |

**201 response body:**
```json
{
  "identity":    "alice.poweur.net",
  "public_key":  "<base64url-encoded public key>",
  "relay":       "relay.poweur.net",
  "created_at":  "2026-03-28T12:00:00Z"
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
