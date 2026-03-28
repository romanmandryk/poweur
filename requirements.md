# Eurything — MVP Requirements

## Overview

The goal of the MVP is to implement a decentralized, DNS-based identity and messaging system. Every participant in the system — whether a human, a bot, or an autonomous agent — is identified by a subdomain of a domain they control (e.g., `myname.example.com`). This subdomain serves as the participant's globally unique, human-readable identity. In the MVP, identities are used primarily for messaging; the architecture is designed to accommodate additional capabilities over time.

The system is composed of three applications within a pnpm monorepo:

- `apps/api` — a Go relay server
- `apps/mobile` — a React Native mobile app for human users
- `apps/cli` — a command-line interface for bots, scripts, and developers

---

## Identity Model

An identity is a fully qualified subdomain, such as `alice.example.com`. The owner of that subdomain controls the associated cryptographic key pair. The public key is the authoritative identifier for the identity; the subdomain is the human-readable handle that resolves to it.

Humans authenticate using passkeys on their mobile device. The passkey is tied to the identity's key pair and is used both to prove ownership of the identity and to sign outgoing messages. Bots and automated agents manage their own key pairs programmatically and interact with the system via the API or CLI rather than a mobile UI.

A signed message carries the sender's identity subdomain and a signature verifiable against the public key associated with that subdomain. Recipients and relays can verify authenticity without a central authority.

### DNS as Persistent Storage

DNS is not only the routing layer — it is the only durable store in the system. All state that must survive a relay restart lives in DNS records:

- An `A` or `CNAME` record on the identity subdomain points to the relay that handles that identity.
- A `TXT` record on the identity subdomain (e.g., `_eurything.alice.example.com`) stores the identity's public key.

Because DNS is the authoritative store, the relay itself holds no database and performs no disk writes for identity or routing data. Any relay that can resolve DNS can verify messages and route to any identity, without coordination with any central registry.

For the MVP, the relay writes DNS records on behalf of identities via the Cloudflare DNS API. The Cloudflare API token required to do so is supplied by the client at registration time and used ephemerally — the relay does not store it.

---

## DNS Routing

DNS is the routing layer. Each identity subdomain must have an `A` or `CNAME` record pointing to the relay server that handles messages for that identity. For example:

- `alice.example.com` → Alice's relay IP or hostname
- `bob.example.org` → Bob's relay IP or hostname

When a relay needs to deliver a message to `bob.example.org`, it resolves the DNS record for that subdomain to find the destination relay and forwards the message there. This eliminates the need for a central routing registry: any relay can route to any identity by performing a standard DNS lookup.

Operators hosting multiple identities on a single relay point all of their subdomains to the same server. Identities on different relays are routed transparently via DNS resolution.

---

## Eurything Protocol

The Eurything Protocol defines the message format, signing scheme, verification procedure, and end-to-end routing model used across all system components. All clients, relays, and automated agents must conform to this protocol.

### Message Structure

A message is a JSON object with the following fields:

| Field       | Type   | Description |
|-------------|--------|-------------|
| `sender`    | string | Fully qualified identity subdomain of the sender (e.g., `alice.example.com`) |
| `recipient` | string | Fully qualified identity subdomain of the recipient (e.g., `bob.example.org`) |
| `timestamp` | string | ISO 8601 UTC timestamp of when the message was created |
| `payload`   | string | Message content — plaintext for MVP; encryption is a post-MVP concern |
| `signature` | string | Base64-encoded signature over the canonical fields (see below) |

**Minimal example:**
```json
{
  "sender":    "alice.example.com",
  "recipient": "bob.example.org",
  "timestamp": "2026-03-28T12:00:00Z",
  "payload":   "Hey Bob, are you around?",
  "signature": "MEUCIQDz...base64..."
}
```

### Signing

Before sending, the client constructs a canonical string by concatenating the following fields in order, separated by newlines:

```
alice.example.com
bob.example.org
2026-03-28T12:00:00Z
Hey Bob, are you around?
```

The sender signs this canonical string with their private key (Ed25519 in the MVP). The resulting signature is base64-encoded and placed in the `signature` field. Including the timestamp in the signed payload prevents trivial replay attacks.

### Verification

When a relay receives a message, it verifies the signature as follows:

1. Extract the `sender` field from the message.
2. Fetch the sender's public key. Public keys are stored in DNS `TXT` records at `_eurything.<sender-subdomain>`. The relay queries DNS for this record to retrieve the public key. As a fallback (e.g., if the TXT record is not yet propagated), the relay may call `GET /identities/:identity` on the peer relay resolved from the sender's subdomain DNS record.
3. Reconstruct the canonical string from `sender`, `recipient`, `timestamp`, and `payload` in the same order used during signing.
4. Verify the signature against the canonical string using the sender's public key.
5. If verification fails, reject the message with `401 Unauthorized`.

Relays forwarding messages on behalf of another relay do not re-sign; they forward the original signed envelope as-is. The receiving relay verifies against the original sender's public key.

### End-to-End Routing

The full delivery path for a message from Alice (`alice.example.com`) to Bob (`bob.example.org`) is:

1. **Sender client → sender's relay**: Alice's mobile app or CLI signs the message and submits it to Alice's configured relay via `POST /messages`.
2. **Sender relay resolves recipient**: Alice's relay inspects the `recipient` field. Since `bob.example.org` is not a locally hosted identity, it performs a DNS lookup for `bob.example.org` to discover Bob's relay IP or hostname.
3. **Sender relay forwards**: Alice's relay forwards the original signed message envelope to Bob's relay via `POST /messages` over HTTPS.
4. **Recipient relay delivers**: Bob's relay verifies the message signature, confirms `bob.example.org` is a locally hosted identity, and stores the message in Bob's inbox.
5. **Bob fetches**: Bob's client polls or subscribes via WebSocket to his relay, retrieves the message from his inbox, and displays it.

### Wire Format

The wire format is JSON for the MVP. All messages and API responses are UTF-8 encoded JSON. A future iteration may replace JSON with a compact binary format such as Protocol Buffers or MessagePack to reduce payload size and parsing overhead, but the field schema and signing semantics will remain the same.

---

## Relay (Go Server)

The relay is the core server component, implemented in Go (`apps/api`). Its responsibilities in the MVP are:

- **Message ingress**: Accept signed messages from senders (human clients, bots, or other relays).
- **Signature verification**: Verify that each inbound message is correctly signed by the claimed sender identity.
- **Message routing**: Resolve the recipient's subdomain via DNS to locate their relay, then forward the message to that relay over HTTPS.
- **Message delivery**: Accept inbound forwarded messages and deliver them to local identity inboxes.
- **Identity registration**: On behalf of a registering client, write the appropriate DNS records (public key `TXT`, routing `A`/`CNAME`) via the Cloudflare API.

The relay exposes an HTTP/JSON API consumed by the mobile app, the CLI, and peer relays.

### Stateless Design

The relay is intentionally almost stateless. The only in-memory state it maintains is:

- **Rate limit counters** — per-sender sliding windows or token buckets (see Rate Limiting). These are ephemeral; losing them on restart is acceptable.
- **DNS routing cache** — resolved peer relay addresses, cached for the duration of the DNS TTL to avoid redundant lookups on every forwarded message.

There is no database and no disk I/O. All durable state (identities, public keys, routing) lives in DNS. This means the relay process can be restarted, replaced, or horizontally scaled without any data migration or coordination.

### DNS Management

When a client registers an identity, it supplies a Cloudflare API token scoped to the relevant DNS zone. The relay uses this token to create the required DNS records on the client's behalf:

- A `TXT` record at `_eurything.<subdomain>` containing the identity's base64-encoded public key.
- An `A` or `CNAME` record at `<subdomain>` pointing to the relay's own address, making the identity reachable.

The Cloudflare API token is used in-process for the duration of the registration request and then discarded. The relay stores no write credentials at rest. Cloudflare is the only supported DNS provider for the MVP; support for additional providers (Route 53, Porkbun, etc.) is a post-MVP concern.

### Rate Limiting

The relay must defend against spam cheaply, before performing any expensive work (signature verification, storage, or DNS lookups). Rate limits are enforced per sender identity, keyed by the sender's public key or identity subdomain as presented in the incoming request.

Default limits (all configurable via environment variable or config file):

| Window     | Limit          |
|------------|----------------|
| Per minute | 20 messages    |
| Per hour   | 200 messages   |
| Per day    | 1,000 messages |

These thresholds are calibrated to slightly above average human messaging activity, making them permissive for normal use while blocking automated spam. Requests that exceed any limit are rejected with HTTP `429 Too Many Requests` **before** signature verification or any storage operation, keeping the rejection path cheap.

The implementation should use a stateless-friendly algorithm such as a sliding window counter or token bucket, backed by an in-memory store for the MVP. This keeps the rate limiter self-contained with no external dependencies while remaining straightforward to replace with a distributed store (e.g., Redis) in a production deployment.

### Max Message Size

The maximum message payload size is **512 KB**. This limit is enforced at ingress, before any parsing, signature verification, or routing logic runs. Requests exceeding this size are rejected immediately with HTTP `413 Content Too Large`.

---

## Mobile App

The mobile app (`apps/mobile`) is a React Native application targeting iOS and Android. It is the primary interface for human users. Key requirements:

- **Passkey-based authentication**: Users create and manage their identity using a device passkey. The passkey is bound to the user's key pair and is used to sign messages locally before they are sent to the relay.
- **Identity registration**: On first launch the user claims a subdomain (subject to availability on their chosen relay) and provisions their passkey.
- **Messaging**: Users can compose and send signed messages to other identities and view messages delivered to their own inbox.
- **Relay configuration**: The app must be configured with, or allow the user to select, a relay endpoint to connect to.

The mobile app does not perform DNS routing itself; it delegates sending to its configured relay.

### The App as a Vault

The mobile app is the secure store for all user secrets. Nothing sensitive is held by the relay or any server. Specifically, the app stores and manages:

- **Private key** — held in the device's secure enclave, accessed only via the passkey (WebAuthn). The private key never leaves the device in plaintext.
- **Cloudflare API token** — stored in the app (e.g., iOS Keychain / Android Keystore) and passed to the relay only when a DNS write is required (e.g., during identity registration or relay migration). The relay receives the token for the duration of that request only.
- **Relay configuration** — the endpoint URL of the relay the user has chosen.

This design means a compromised relay cannot expose user secrets: it never holds any. Trust is rooted in the device's secure enclave, not in any server.

### MVP Feature Set

The following features constitute the mobile app MVP:

- **Passkey registration and login**: Account creation and subsequent logins are handled entirely via WebAuthn / device biometrics (Face ID, Touch ID, fingerprint). No passwords are stored or transmitted.
- **Identity creation**: On first launch the user chooses a subdomain handle, provides their Cloudflare API token (scoped to their DNS zone), and associates the identity with their chosen relay. The app passes the token to the relay, which writes the DNS records; the token is then discarded by the relay.
- **Send a message**: Compose and send a signed message to any valid identity address (e.g., `bob.example.org`).
- **Receive and read messages**: Fetch messages delivered to the user's inbox, either by polling the relay on a configurable interval or via a persistent WebSocket connection for lower latency.
- **Conversation threads**: Messages are grouped by correspondent identity into conversation threads, displayed in chronological order.
- **Relay configuration**: The user can view and change which relay their identity is associated with, and the app endpoint used for API calls.

---

## CLI

The CLI (`apps/cli`) is the third application in the monorepo. It provides a scriptable interface to the relay API, intended for developers, bots, and automated agents. Key requirements:

- **Identity management**: Create and manage identity key pairs stored locally (e.g., in a config file or OS keychain).
- **Send messages**: Sign and send messages from a CLI-managed identity to any recipient identity.
- **Read inbox**: Retrieve and display messages delivered to a CLI-managed identity from the relay.
- **Relay interaction**: All network operations go through a configured relay endpoint (set via config file or environment variable).
- **Scriptability**: The CLI should support machine-readable output (e.g., JSON) to facilitate use in scripts and automated pipelines.

The CLI package is named `@eurything/cli` and lives at `apps/cli` within the monorepo.

### MVP Commands

```
eurything identity create <name>   # Generate a key pair and register the identity with the configured relay
eurything identity show            # Display the current identity's subdomain and public key
eurything send <to> <message>      # Sign and send a message to the given identity address
eurything inbox                    # Fetch and display messages from the relay inbox
eurything relay status             # Check relay connectivity, show configured endpoint and relay version
```

All commands accept a `--json` flag that produces machine-readable JSON output, suitable for use in scripts and automated pipelines.

Configuration is stored at `~/.eurything/config.toml`. The config file holds the relay endpoint, the identity subdomain, and the path to (or reference for) the private key. Individual settings can be overridden via environment variables (e.g., `EURYTHING_RELAY_URL`) or command-line flags.

---

## API

The relay exposes an HTTP/JSON API. All endpoints consume and produce `application/json`. The MVP API surface is:

### `POST /messages`

Submit a signed message for delivery. The relay inspects the recipient field, determines whether the recipient is local or remote, and either delivers locally or forwards to the appropriate peer relay via DNS resolution.

**Request body** (Eurything message envelope — see Eurything Protocol section):
```json
{
  "sender":    "alice.example.com",
  "recipient": "bob.example.org",
  "timestamp": "2026-03-28T12:00:00Z",
  "payload":   "Hello, Bob.",
  "signature": "<base64-encoded signature over canonical fields>"
}
```

**Responses:**
- `202 Accepted` — message accepted for delivery or forwarding.
- `400 Bad Request` — malformed message envelope.
- `401 Unauthorized` — signature verification failed.
- `413 Content Too Large` — payload exceeds 512 KB.
- `429 Too Many Requests` — sender has exceeded rate limits.

---

### `GET /messages/:identity`

Retrieve pending messages for a local identity. The requester must prove ownership of the identity via a challenge–response: the client signs a short-lived server-issued challenge with its private key, and the relay verifies the signature against the registered public key for that identity. This avoids passwords or bearer tokens while remaining consistent with the passkey model used in the mobile app.

**Authentication flow:**
1. Client calls `GET /auth/challenge?identity=alice.example.com` to obtain a short-lived challenge string.
2. Client signs the challenge with its private key and includes it as the `X-Eurything-Signature` request header, with the identity in `X-Eurything-Identity`.

**Response body:**
```json
{
  "messages": [
    {
      "id":        "msg_01j...",
      "sender":    "bob.example.org",
      "recipient": "alice.example.com",
      "timestamp": "2026-03-28T12:00:00Z",
      "payload":   "Hey Alice!",
      "signature": "<base64-encoded signature>"
    }
  ]
}
```

**Responses:**
- `200 OK` — array of pending messages (may be empty).
- `401 Unauthorized` — challenge signature invalid or expired.
- `404 Not Found` — identity is not hosted on this relay.

---

### `GET /auth/challenge`

Issue a short-lived challenge string for use in authenticated requests.

**Query parameters:** `identity` — the identity subdomain requesting a challenge.

**Response body:**
```json
{
  "challenge":  "eyJhbGci...",
  "expires_at": "2026-03-28T12:05:00Z"
}
```

Challenges expire after a short window (e.g., 60 seconds). The relay stores issued challenges in memory and invalidates them after use or expiry.

---

### `POST /identities`

Register a new identity on this relay. The client supplies its public key and a Cloudflare API token scoped to the DNS zone of the identity subdomain. The relay uses the token to write the required DNS records (public key `TXT` and routing `A`/`CNAME`), then discards the token. The relay stores no write credentials.

**Request body:**
```json
{
  "identity":          "alice.example.com",
  "public_key":        "<base64-encoded public key>",
  "cloudflare_token":  "<scoped Cloudflare API token>"
}
```

**Responses:**
- `201 Created` — identity registered and DNS records written.
- `400 Bad Request` — malformed request.
- `409 Conflict` — identity already registered on this relay.
- `502 Bad Gateway` — Cloudflare DNS write failed (token invalid, zone not found, etc.).

---

### `GET /identities/:identity`

Look up the public key registered for an identity on this relay. Used by other relays to fetch a sender's public key for signature verification when a DNS TXT record lookup is not available or not yet implemented.

**Response body:**
```json
{
  "identity":   "alice.example.com",
  "public_key": "<base64-encoded public key>"
}
```

**Responses:**
- `200 OK` — identity found, public key returned.
- `404 Not Found` — identity not hosted on this relay.

---

### `GET /health`

Liveness check. Returns a minimal response indicating the relay is running and reachable.

**Response body:**
```json
{
  "status":  "ok",
  "version": "0.1.0"
}
```

---

The API is consumed by the mobile app, the CLI, and peer relays performing message forwarding.

---

## Security Model

The key principle of the Eurything security model is: **trust is rooted in the device's secure enclave; the relay is an untrusted forwarder**.

### Trust Hierarchy

The device secure enclave (accessed via WebAuthn / passkeys) is the only trusted component. Everything else — including the relay — is treated as untrusted infrastructure that can be observed, replaced, or compromised without exposing user secrets or allowing message forgery.

**What the relay can and cannot do:**

| Can | Cannot |
|-----|--------|
| Forward messages between identities | Read encrypted message content (post-MVP; plaintext in MVP) |
| Verify message authenticity (via public key from DNS) | Forge a message from any identity |
| Rate-limit and reject spam | Impersonate a user (no private keys held) |
| Write DNS records (ephemerally, with client-supplied token) | Retain DNS write credentials after a registration request |
| Drop or delay messages | Prove that it delivered a message (no receipts in MVP) |

### What Lives Where

| Secret / Data | Stored in | Notes |
|---------------|-----------|-------|
| Private key | Device secure enclave | Never leaves the device in plaintext |
| Cloudflare API token | App (iOS Keychain / Android Keystore) | Passed to relay ephemerally for DNS writes only |
| Public key | DNS TXT record | Publicly readable; used for signature verification |
| Relay endpoint | App config | Not a secret; user-configurable |
| Rate limit counters | Relay in-memory | Ephemeral; lost on restart |
| DNS routing cache | Relay in-memory | Ephemeral; rebuilt from DNS on miss |
| Inbox messages | Relay in-memory | Ephemeral in MVP; durability is a post-MVP concern |

### Threat Model Notes

- A compromised relay can read plaintext message payloads (MVP limitation; end-to-end encryption is a post-MVP goal) and can drop or delay messages, but cannot forge signatures or impersonate identities.
- A compromised relay cannot exfiltrate private keys or DNS write credentials because it never holds them at rest.
- DNS records are the ground truth for public keys and routing. An attacker who can manipulate DNS records for an identity subdomain can redirect messages and substitute a public key. DNS zone security (DNSSEC, restricted API token scopes) is therefore important and should be documented in operator guidance.
- The MVP uses plaintext payloads. Until end-to-end encryption is implemented, relay operators and network observers can read message content. This should be clearly disclosed to users.

---

## Future Considerations

The following are explicitly out of scope for the MVP but should be kept in mind as the architecture evolves:

- **Capability advertisement via DNS**: Additional DNS record types (e.g., `TXT` records) can be used to advertise capabilities associated with an identity, such as supported protocols, service endpoints, or metadata. This allows the DNS layer to evolve from pure routing into a richer discovery mechanism.
- **Additional identity use cases**: Beyond messaging, identities could be used for authentication, authorization, payments, or social graph discovery.
- **Federation and relay peering**: Relays may eventually maintain persistent connections or trust relationships with known peer relays to improve reliability and reduce per-message DNS lookups.
- **Key rotation and revocation**: A mechanism for rotating or revoking the key pair associated with an identity without losing the subdomain will be necessary for production use.
- **Scalability and persistence**: The relay's storage and delivery model will need to be hardened for production workloads.
- **Push notifications**: The mobile app will require a push notification integration so users receive messages when the app is in the background.
