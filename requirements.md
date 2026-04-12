# Eurything — MVP Requirements

## Overview

The goal of the MVP is to implement a decentralized, DNS-based identity and messaging system. Every participant in the system — whether a human, a bot, or an autonomous agent — is identified by a subdomain of a domain they control (e.g., `myname.example.com`). This subdomain serves as the participant's globally unique, human-readable identity. In the MVP, identities are used primarily for messaging, but the protocol and mobile app must already reserve a stable path for using the same DNS identity to sign up to and sign in to third-party websites and apps.

The system is composed of several applications and supporting packages within a pnpm monorepo:

- `apps/api` — a Go relay server
- `apps/ios` — native iOS app (Swift)
- `apps/android` — native Android app (Kotlin)
- `apps/cli` — a command-line interface for bots, scripts, and developers
- `apps/infra` — Hetzner Cloud infrastructure definition (Terraform)

---

## Identity Model

An identity is a fully qualified subdomain, such as `alice.example.com`. The owner of that subdomain controls the associated cryptographic key pair. The public key is the authoritative identifier for the identity; the subdomain is the human-readable handle that resolves to it.

Humans authenticate using passkeys on their mobile device. The passkey is tied to the identity's key pair and is used both to prove ownership of the identity and to sign outgoing messages. Bots and automated agents manage their own key pairs programmatically and interact with the system via the API or CLI rather than a mobile UI.

A signed message carries the sender's identity subdomain and a signature verifiable against the public key associated with that subdomain. Recipients and relays can verify authenticity without a central authority.

The same identity key pair must also be usable outside messaging. A third-party website or app must be able to issue a challenge to `alice.example.com`, have Alice approve that request in the mobile app, and verify the resulting signature against Alice's published public key. This makes the DNS identity a general-purpose authentication primitive rather than a messaging-only handle.

### DNS as Persistent Storage

DNS is not only the routing layer — it is the only durable store in the system. All state that must survive a relay restart lives in DNS records:

- An `A` or `CNAME` record on the identity subdomain points to the relay that handles that identity.
- A `TXT` record on the identity subdomain (e.g., `_eurything.alice.example.com`) stores the identity's public key.

Because DNS is the authoritative store, the relay itself holds no database and performs no disk writes for identity or routing data. Any relay that can resolve DNS can verify messages and route to any identity, without coordination with any central registry.

For the MVP, the relay writes DNS records on behalf of identities via a configurable DNS provider. Cloudflare and Hetzner DNS are both supported. The API token required to do so is supplied by the client at registration time and used ephemerally — the relay does not store it.

---

## Identity Beyond Messaging

The protocol must support using the same DNS identity for third-party sign-up and sign-in flows, not only for Eurything relay access. The mobile app is the primary signer and consent surface for human users. A relying party (website or app) can ask the user to prove control of `alice.example.com`, and the user confirms that request in the mobile app using the identity's passkey-backed private key.

### Third-Party Authentication Flow

The baseline flow is:

1. A website or app creates an authentication request containing at minimum: `domain`, `audience`, `nonce`, `issued_at`, `expires_at`, `request_id`, requested action (`signup` or `signin`), and an optional human-readable statement.
2. The relying party presents this request to the user via QR code, universal link, deep link, or an app-to-app handoff.
3. The mobile app resolves the relying party metadata, shows the request details to the user, and asks for explicit approval.
4. On approval, the mobile app signs the challenge payload with the identity's private key and returns the signed response to the relying party.
5. The relying party verifies the signature using the public key published in DNS for the claimed identity, or through a compatible DID document derived from that DNS identity.

This flow must be designed so that websites and apps can adopt it without running an Eurything relay themselves. The relay remains important for messaging and DNS management, but third-party authentication must work as a standalone verifier pattern.

### Mobile App as Signer

The mobile app must support a dedicated approval flow for external authentication requests:

- Display the relying party domain or app identifier clearly before approval.
- Show whether the action is sign-up, sign-in, account linking, or another future auth action.
- Require biometric or platform passkey confirmation before producing a signature.
- Keep the private key entirely on-device; only the signed response leaves the device.

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

To keep the envelope evolvable and easier to map to established signed-message formats, the protocol should reserve optional forward-compatible fields such as `id`, `type`, `nonce`, `thread_id`, `expires_at`, and `metadata`. These are not required for the first messaging milestone, but the protocol must treat unknown fields as ignorable unless they are explicitly defined as signed mandatory fields in a later version.

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

## Protocol Interoperability Requirements

The Eurything identifier remains the DNS name itself (for example `alice.example.com`). Protocol interop should be additive: Eurything must not require a DID or an external identity provider, but it should map cleanly to existing standards where that improves adoption.

### DID Mapping

- The canonical Eurything identifier is the FQDN.
- The protocol should support a deterministic DID projection for interoperability. `did:dns:<fqdn>` is the closest conceptual fit, but because the broader ecosystem around `did:dns` is still emerging, Eurything should also support publishing a compatible DID document through `did:web`-style hosting.
- Where HTTP hosting is available, an identity should be able to expose `https://<identity>/.well-known/did.json` so that external systems can consume the same key material and service metadata through a familiar DID resolution path.

### Messaging Interoperability

- The current signed JSON envelope is intentionally simple, but future revisions should move toward a more explicit envelope shape similar in spirit to DIDComm basic messages or Nostr-style signed events: message identifier, message type, creation time, expiry, optional threading metadata, and canonicalized signing input.
- Once optional fields become common, the signature base should evolve from newline concatenation toward a canonical JSON representation to reduce ambiguity and make bridge implementations easier.
- Relay-to-relay delivery remains HTTPS JSON, but the protocol should use explicit content types and version markers so bridges can translate Eurything messages into other signed-message ecosystems when needed.

### Well-Known HTTP Discovery

To integrate cleanly with browsers, mobile apps, and third-party services, the protocol should define well-known HTTP metadata endpoints:

- `/.well-known/eurything.json` — identity or relay metadata, supported protocol versions, relay endpoints, authentication capabilities, and mobile app handoff information.
- `/.well-known/did.json` — optional DID document representation for interoperability with DID-aware systems.
- `/.well-known/did-configuration.json` — optional domain-to-DID binding for ecosystems that already use DID configuration files.

The relay or identity host should be able to serve these endpoints using the identity hostname so that discovery works with standard web tooling.

### Third-Party Authentication Object

Authentication requests and responses should follow a structure close to established challenge-response login schemes:

- Verifier request fields: `request_id`, `domain`, `audience`, `nonce`, `issued_at`, `expires_at`, `action`, `statement`, and `redirect_uri` or `response_uri`.
- Signed response fields: `request_id`, `identity`, `public_key_hint` or `kid`, `issued_at`, `signature`, and optional disclosed profile attributes.
- The verifier must bind the response to the original `nonce`, intended audience, and expiry to prevent replay across sites or sessions.

---

## Relay (Go Server)

The relay is the core server component, implemented in Go (`apps/api`). Its responsibilities in the MVP are:

- **Message ingress**: Accept signed messages from senders (human clients, bots, or other relays).
- **Signature verification**: Verify that each inbound message is correctly signed by the claimed sender identity.
- **Message routing**: Resolve the recipient's subdomain via DNS to locate their relay, then forward the message to that relay over HTTPS.
- **Message delivery**: Accept inbound forwarded messages and deliver them to local identity inboxes.
- **Identity registration**: On behalf of a registering client, write the appropriate DNS records (public key `TXT`, routing `A`/`CNAME`) via the configured DNS provider (Cloudflare or Hetzner).

The relay exposes an HTTP/JSON API consumed by the mobile app, the CLI, and peer relays.

### Stateless Design

The relay is intentionally almost stateless. The only in-memory state it maintains is:

- **Rate limit counters** — per-sender sliding windows or token buckets (see Rate Limiting). These are ephemeral; losing them on restart is acceptable.
- **DNS routing cache** — resolved peer relay addresses, cached for the duration of the DNS TTL to avoid redundant lookups on every forwarded message.

There is no database and no disk I/O. All durable state (identities, public keys, routing) lives in DNS. This means the relay process can be restarted, replaced, or horizontally scaled without any data migration or coordination.

### DNS Management

When a client registers an identity, it supplies a DNS provider API token scoped to the relevant DNS zone. The relay uses this token to create the required DNS records on the client's behalf:

- A `TXT` record at `_eurything.<subdomain>` containing the identity's base64-encoded public key.
- An `A` or `CNAME` record at `<subdomain>` pointing to the relay's own address, making the identity reachable.

The API token is used in-process for the duration of the registration request and then discarded. The relay stores no write credentials at rest.

#### Supported DNS Providers

The relay's DNS management logic is abstracted behind a provider interface, making it straightforward to add new providers without touching core relay logic. The MVP supports two providers, selected via a relay config value or environment variable (`EURYTHING_DNS_PROVIDER`):

- **Cloudflare** — uses the [Cloudflare DNS API](https://developers.cloudflare.com/api/). The client supplies a Cloudflare API token scoped to the target zone. Supports `TXT`, `A`, and `CNAME` record creation and updates.
- **Hetzner DNS** — uses the [Hetzner DNS API](https://dns.hetzner.com/api-docs). The client supplies a Hetzner DNS API token. Supports `TXT`, `A`, and `CNAME` record creation and updates with equivalent capability to the Cloudflare integration.

Support for additional providers (Route 53, Porkbun, etc.) is a post-MVP concern but requires only a new implementation of the provider interface.

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

## Mobile Apps (`apps/ios` and `apps/android`)

### Why Native (Not React Native)

The mobile apps are built as two separate native applications — iOS (`apps/ios`, Swift) and Android (`apps/android`, Kotlin) — rather than a single React Native app.

The core functionality of these apps is security-sensitive and deeply platform-native: passkey registration and assertion (WebAuthn/FIDO2), private key storage in the hardware-backed secure enclave (iOS Secure Enclave / Android StrongBox or TEE), cryptographic signing operations, and biometric authentication. These are areas where native platform APIs are more reliable, more directly auditable, and less abstraction-layered than React Native bridge equivalents. The UI surface is relatively small and does not justify the cross-platform tradeoff when the security-critical paths would require native modules regardless.

The requirements below apply to both platforms unless noted otherwise.

### The App as a Vault

The app is the authoritative secure store for all user secrets. Nothing sensitive is held by the relay or any server. Specifically, the app stores and manages:

- **Private keys** — held in the platform's hardware-backed secure enclave (iOS Secure Enclave / Android StrongBox or TEE), accessed only via the passkey APIs (WebAuthn/FIDO2). Private keys never leave the device in plaintext.
- **DNS provider API token** — stored in the platform's credential store (iOS Keychain / Android Keystore) and passed to the relay only when a DNS write is required (e.g., during identity registration or relay migration). The relay receives the token for the duration of that request only.
- **Relay configuration** — relay URL and any associated auth tokens, stored in Keychain/Keystore.

This means a compromised relay cannot expose user secrets: it never holds any. Trust is rooted in the device's secure hardware, not in any server.

The app does not perform DNS routing itself; it delegates all relay-protocol network operations to its configured relay.

### Welcome Screen

Shown on first launch, before any identity has been created:

- Short explainer of what Eurything is: *"Create your DNS Identity. Use it for everything: messaging, receiving payments, signing up to services, sharing data securely."*
- Explain that IDs can belong to humans, agents, or bots.
- Emphasise spam and bot resistance as a core property of the system.
- Single CTA: **"Create your ID"** button.

### Identity Creation (Sign-Up)

- User chooses a unique handle — minimum 8 characters, DNS subdomain-safe characters only (`a–z`, `0–9`, hyphens; no leading or trailing hyphens).
- The resulting identity is `<handle>.poweur.net` (the parent domain is configurable in app settings).
- Optional profile fields at creation time: Display Name, Profile Picture URL, short bio (1–2 sentences), long bio (paragraph).
- On submission: generate a new passkey (WebAuthn/FIDO2) scoped to the identity's domain. The app extracts the public key and uploads it as a DNS `TXT` record at `_eurything.<handle>.poweur.net` via the relay, supplying the DNS provider token for the duration of the write.
- The app supports **multiple identities** — the user can create more than one handle. Each identity has its own passkey.

### Pending Registrations Screen

Shown when at least one identity has been submitted but DNS propagation has not yet been confirmed:

- Lists pending identities with their current status.
- **"Check DNS"** button per identity — triggers a live DNS `TXT` lookup for `_eurything.<handle>.poweur.net` and confirms whether the record is visible.
- Once verified, the identity moves into the active identity pool and the user is taken to the Dashboard.

### Active Identity Selector

Once at least one identity is verified, a persistent header appears at the top of the app showing the currently active identity (avatar, display name or handle):

- Tapping the header opens an identity picker listing all verified identities.
- Switching the active identity switches context across the entire app (messaging threads, contacts, settings).

### Dashboard (Home Screen)

The primary screen after an identity is verified. Shows the service modules available under the active identity:

- **Messaging** — active, MVP feature.
- **Authentication approvals** — active as soon as third-party auth is implemented; used to approve sign-up/sign-in requests from websites and apps.
- **Publishing** — listed, not yet active.
- **Receiving Payments** — listed; shows a placeholder with supported providers and currencies, not yet active.
- Footer: *"More capabilities coming soon."*
- Each module is displayed as a card indicating its status (active / coming soon).

### Contacts

- Contact list stored locally on-device (no server-side contact sync).
- Add a contact by entering their DNS identity (e.g., `alice.poweur.net`).
- Tapping a contact opens a detail sheet showing:
  - Parsed Eurything DNS records for that identity: handle, display name, profile picture (if set), short bio.
  - All advertised capabilities from DNS: messaging relay address, payment methods, ID verification proofs, and any future capability records.
  - This is a **live DNS lookup** on each open — not a cached read.

### Messaging

Classic messenger interface:

- Start a conversation by entering any valid DNS identity.
- Conversation list shows all threads, stored locally on the device.
- Thread view: chat bubbles, newest at bottom.
- Messages are signed with the sender's passkey-backed private key before dispatch.
- Messages are sent via the Eurything relay protocol (`POST /messages`).
- Received messages are fetched from the user's relay (polling for MVP; WebSocket is a stretch goal).
- **All message storage is local** — the relay is a forwarder only and holds no persistent message history.

### Vault (Architecture Requirement, Not a Screen)

The vault is not a visible screen but an architectural constraint enforced throughout both apps:

- Private keys: stored in iOS Secure Enclave / Android StrongBox via passkey APIs. Never extractable in plaintext.
- DNS provider token (Cloudflare or Hetzner): stored in iOS Keychain / Android Keystore.
- Relay URL and any relay auth tokens: stored in Keychain/Keystore.
- No secrets leave the device except when explicitly passed to the relay for an active operation, and only for its duration.

### External Authentication Approvals

The mobile apps must support external authentication approval flows for websites and apps:

- Accept inbound auth requests via QR scan, universal link, deep link, or clipboard import.
- Display verifier metadata from `/.well-known/eurything.json` or equivalent discovery metadata before asking for consent.
- Require biometric/passkey confirmation before signing any third-party auth challenge.
- Return the signed response using the verifier's requested callback mechanism (browser redirect, deep link, HTTPS callback, or QR handoff continuation).
- Keep a local approval history for the user, at least for the current device, so the user can review recent sign-in/sign-up approvals.

---

## CLI

The CLI (`apps/cli`) is the third application in the monorepo. It provides a scriptable interface to the relay API, intended for developers, bots, and automated agents. Key requirements:

- **Identity management**: Create and manage identity key pairs stored locally (e.g., in a config file or OS keychain).
- **Send messages**: Sign and send messages from a CLI-managed identity to any recipient identity.
- **Read inbox**: Retrieve and display messages delivered to a CLI-managed identity from the relay.
- **Sign verifier challenges**: Support signing third-party authentication requests for automated agents and headless environments.
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

The CLI should also reserve a future-compatible authentication command surface such as:

```
eurything auth sign <request-file-or-url>    # Sign a third-party auth request for bots and automated agents
eurything auth inspect <request-file-or-url> # Display verifier request metadata before signing
```

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

Register a new identity on this relay. The client supplies its public key, the DNS provider to use (`cloudflare` or `hetzner`), and an API token scoped to the DNS zone of the identity subdomain. The relay uses the token to write the required DNS records (public key `TXT` and routing `A`/`CNAME`), then discards the token. The relay stores no write credentials.

**Request body:**
```json
{
  "identity":      "alice.example.com",
  "public_key":    "<base64-encoded public key>",
  "dns_provider":  "cloudflare",
  "dns_token":     "<scoped DNS provider API token>"
}
```

**Responses:**
- `201 Created` — identity registered and DNS records written.
- `400 Bad Request` — malformed request or unsupported `dns_provider` value.
- `409 Conflict` — identity already registered on this relay.
- `502 Bad Gateway` — DNS provider write failed (token invalid, zone not found, etc.).

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
| DNS provider API token (Cloudflare or Hetzner) | iOS Keychain / Android Keystore | Passed to relay ephemerally for DNS writes only |
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

## Infrastructure (`apps/infra`)

The `apps/infra` directory contains the infrastructure definition for deploying a relay on Hetzner Cloud. The goal is a reproducible, version-controlled setup that a single operator can apply with minimal manual steps.

### Tooling

Both Terraform and Pulumi are viable options for this kind of infrastructure. **Terraform is recommended for the MVP** due to its larger ecosystem of Hetzner and ACME providers, broader community familiarity, and simpler state management for a small deployment. Pulumi is noted as an alternative if the team has a strong preference for writing infrastructure in a general-purpose language.

### Hetzner Cloud Resources

The Terraform configuration provisions the following resources:

- **Hetzner Cloud server(s)** — one or more VMs running the Go relay binary. The relay is stateless, so horizontal scaling requires no coordination; adding servers behind the load balancer is sufficient.
- **Hetzner Load Balancer** — sits in front of the relay server(s) and terminates incoming traffic. Handles health checks and distributes load across relay instances. TLS termination occurs here using the provisioned wildcard certificate.
- **DNS records** — `A`/`CNAME` records for the relay's own hostname (e.g., `relay.example.com`) pointing to the load balancer IP, provisioned via the Hetzner DNS Terraform provider.
- **Firewall rules** — restrict direct access to relay VMs; only the load balancer and operator IPs can reach them on non-public ports.

### TLS Certificate Provisioning

TLS certificate provisioning is automated using the **Terraform ACME provider** against **Let's Encrypt**. This keeps all infrastructure state in one place and avoids manual certificate management.

**Challenge type: DNS-01 is required.** HTTP-01 challenge is not used. DNS-01 is the only challenge type that supports wildcard certificates, and wildcard certificates are the correct strategy for this deployment (see below).

**Wildcard certificate strategy:** A single `*.example.com` wildcard certificate covers every first-level identity subdomain (`alice.example.com`, `bob.example.com`, etc.) hosted on the relay. This is appropriate because the operator controls the parent domain as a prerequisite for running a relay, and all identity subdomains are first-level. There is no need to provision or renew a certificate per identity — one cert covers all of them. The one-level wildcard limitation (i.e., `*.example.com` does not cover `deep.alice.example.com`) is a non-issue since the protocol does not use deeper subdomains.

DNS-01 challenge is automated via the Hetzner DNS API, using an API token held in Terraform (or passed via environment variable during `terraform apply`). This is the same Hetzner DNS API used for identity record management, so no additional provider account is needed.

**Certificate storage:** The provisioned certificate and private key are stored accessibly to the relay — either written to the server filesystem during provisioning or stored in Hetzner Object Storage and fetched at relay startup. The specific approach is left to the operator; both are documented in `apps/infra/README.md`.

**Renewal:** Two options are documented; Terraform ACME is recommended for the MVP:

- **Terraform ACME (recommended):** Re-running `terraform apply` (e.g., via a scheduled CI job) checks the certificate expiry and renews automatically when it falls within the renewal window. All state stays in Terraform.
- **On-server renewal (fallback):** `certbot` or `acme.sh` running on the relay server via a cron job. More self-contained but splits infrastructure state between Terraform and the server.

### Deployment

A minimal deployment script or CI hook should:

1. Build the Go relay binary.
2. Copy it to the Hetzner server(s) (e.g., via `scp` or a Hetzner snapshot).
3. Restart the relay service (e.g., `systemctl restart eurything-relay`).

A full CI/CD pipeline is a post-MVP concern; the MVP deployment process can be a documented manual script.

---

## Future Considerations

The following are explicitly out of scope for the MVP but should be kept in mind as the architecture evolves:

- **Capability advertisement via DNS**: Additional DNS record types (e.g., `TXT` records) can be used to advertise capabilities associated with an identity, such as supported protocols, service endpoints, or metadata. This allows the DNS layer to evolve from pure routing into a richer discovery mechanism.
- **Additional identity use cases**: Beyond messaging, identities could be used for richer authentication, authorization, payments, or social graph discovery. The core protocol should be designed so these additions reuse the same DNS identity and key material rather than introducing parallel identity systems.
- **OIDC / wallet bridge**: A bridge to OpenID-based wallet flows (such as SIOPv2 / OID4VP-style verifier requests) may be added later so Eurything identities can participate in ecosystems that already expect OpenID-style metadata and request objects.
- **Federation and relay peering**: Relays may eventually maintain persistent connections or trust relationships with known peer relays to improve reliability and reduce per-message DNS lookups.
- **Key rotation and revocation**: A mechanism for rotating or revoking the key pair associated with an identity without losing the subdomain will be necessary for production use.
- **Scalability and persistence**: The relay's storage and delivery model will need to be hardened for production workloads.
- **Push notifications**: The mobile app will require a push notification integration so users receive messages when the app is in the background.
