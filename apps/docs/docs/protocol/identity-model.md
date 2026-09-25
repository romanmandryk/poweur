---
id: identity-model
sidebar_position: 2
title: Identity Model
---

# Identity Model

A Poweur ID identity is a **fully qualified domain name (FQDN)** that the owner controls. The name is the human-readable handle; the associated public key is the cryptographic identity.

Examples: `alice.com` (self-hosted apex), `bot.example.org` (self-hosted subdomain), or `alice.poweur.net` (hosted under a relay's wildcard parent). The protocol **always** identifies principals by FQDN — there is no domain-less ID. What is optional is *relay-hosted* names under a parent like `poweur.net`; owning your own domain works the same at the protocol layer.

Public keys are published via a signed [Identity Document](/protocol/web-identity) at
`/.well-known/poweur/id.json` (primary), and optionally via DNS TXT records (fallback).

## Identity Format

Identities are FQDNs:

- Must be a valid domain name, e.g. `alice.com` or `alice.poweur.net`
- Reserved leftmost labels (`www`, `admin`, `support`, `relay`, …) can't be **claimed** by self-service registration. They are still valid names: an operator can create one (`OPERATOR_TOKEN`, see [relay configuration](/relay/configuration)), and once it exists every client resolves and messages it like any other ID
- **Hosted** names under a parent (e.g. `*.poweur.net`) additionally require the leftmost label to meet a minimum length (currently 8 characters) to reduce squatting on short public handles
- Self-hosted apex names (`alice.com`) are not subject to that hosted-handle length rule beyond normal DNS label rules
- The parent domain for hosted registration is configured on the relay (`HOSTED_DOMAINS`)

Examples of valid identity addresses:

```
alice.com
alice.poweur.net
r2d2-bot.poweur.net
mycompany.example.org
agent-007.example.org
```

## Key Pairs

Each identity owns **three** cryptographic keys. The first two are long-lived; the third is short-lived and rotates on a 24-hour cadence.

### Long-lived identity key (Ed25519)

The authoritative signing key for the identity. Used to authorize sessions, sign auth challenges for third-party login, and (for clients that opt out of sessions) sign messages directly.

- **Private key** — never leaves the device. Stored in hardware-backed secure storage (iOS Secure Enclave / Android StrongBox on mobile; OS keychain or a key file under `~/.poweur/keys/` for the CLI).
- **Public key** — published in the identity document (`public_key`) at
  `/.well-known/poweur/id.json`, and optionally mirrored in DNS:

```
_poweur.alice.com.  300  IN  TXT  "poweur-pubkey=ed25519:<base64url-public-key>"
```

Because the private key never leaves the device and publication is under the owner's name control (HTTPS origin and/or DNS zone), there is no central authority that can forge the identity's signatures.

### Long-lived encryption key (X25519)

Enables senders to end-to-end encrypt messages to this identity. Generated alongside the identity key at registration time and stored on the same device with the same storage rules.

- **Public key** — in the identity document (`encryption_public_key`), and optionally in DNS:

```
_poweur-enc.alice.com.  300  IN  TXT  "poweur-enckey=x25519:<base64url-public-key>"
```

- **Private key** — stored locally and used only for decrypting incoming messages. The identity key and encryption key are separate so signing and encryption have independent lifetimes and threat models.

### Short-lived session key (Ed25519) {#sessions}

A per-device, per-relay ephemeral signing key with a **maximum lifetime of 24 hours**. All routine operations (sending messages, fetching the inbox) are signed by the session key; the long-lived identity key is only invoked once per session refresh. On mobile this means a passkey unlock happens at most once per session — typically once a day — rather than on every message.

Session flow:

1. **Generate.** The client generates a fresh Ed25519 keypair locally.
2. **Authorize.** The client signs a canonical session-registration string (`session-registration\n<identity>\n<session_public_key>\n<issued_at>\n<expires_at>\n<nonce>`) with the long-lived identity key. On mobile this is the passkey unlock step.
3. **Register.** The client POSTs the authorized registration to `POST /sessions` on its relay. The relay verifies the identity signature against the long-lived public key (resolved via the web-first chain, with DNS fallback), enforces the 24h TTL, issues a `session_id`, and caches the session in memory.
4. **Sign & send.** Subsequent `POST /messages` and `GET /messages/:identity` calls send `session_id` (plus an optional self-contained `session_proof` for cross-relay verification) and sign with the session key.
5. **Rotate or expire.** When the session nears expiry or the relay returns `401 session_expired`, the client silently re-runs the flow. Relay restarts also invalidate all sessions; clients re-register transparently.

Because sessions live in relay memory only, losing a session private key or losing a relay-side cache is not a protocol-level failure — the client just registers a fresh session.

A session can be revoked at any time with `DELETE /sessions/:id`.

### Third-party authentication

The long-lived identity key also backs third-party sign-up and sign-in flows. A relying party can challenge `alice.com` (or any Poweur ID), and Alice's device proves control by signing the challenge; the relying party verifies against the resolved public key (well-known or DNS). These flows always use the long-lived key (not a session key) because relying parties do not have access to the relay's session cache.

## Passkeys (Mobile)

On mobile, the **long-lived identity key** is managed through the **WebAuthn/FIDO2 passkey API**. When a user creates an identity:

1. The app calls the platform's passkey registration API, specifying the identity's domain as the relying party.
2. The platform generates an Ed25519 key pair in the hardware secure enclave.
3. The app also generates an X25519 encryption key pair in secure storage.
4. The app submits both public keys and a signed identity document via `POST /identities`.
5. **Hosted:** the relay persists the document under `POWEUR_DATA` (no DNS write). **Self-hosted DNS mode:** the relay writes `TXT` + routing records using a client-supplied DNS provider token.

During normal operation the passkey is used **only** to authorize new sessions — at most once per 24 hours. Every routine action (sending a message, reading the inbox, replying to a conversation) is signed by the short-lived session key stored locally in the app, which does **not** trigger a biometric prompt. The session key never leaves the device but does not require hardware-backed storage: losing it only invalidates an in-memory relay entry, not the identity.

Passkey-protected operations on mobile:

- Creating a new identity.
- Refreshing an expired or soon-to-expire session.
- Approving a third-party authentication challenge (signup/signin with an Poweur ID identity on an external site).
- Key rotation (post-MVP).

Everything else — sending, receiving, session check — proceeds without user friction.

## CLI Key Management

The CLI stores keys as files under `~/.poweur/keys/` (configurable via `keys_dir` in `~/.poweur/config.toml`):

- `<identity>.key` — the long-lived Ed25519 identity private key.
- `<identity>.enc` — the long-lived X25519 encryption private key.
- `~/.poweur/sessions/<identity>.toml` — the current short-lived session (session id, session private key, the raw `session_proof` inputs, and expiry).

The CLI runs the same session flow as the mobile app, but the "authorize a new session" step is not gated by biometrics — it just uses the identity key on disk. Headless agents that want to opt out of sessions entirely can do so (messages signed directly with the identity key are still accepted by relays), but the default CLI path uses sessions so CLI and mobile behave identically.

### When is the identity key used directly?

The long-lived identity key signs a message envelope — as opposed to the short-lived session key — in these cases:

- **Session registration.** Every call to `POST /sessions` is authorized by an identity signature over the registration canonical string.
- **`poweur auth sign`.** Third-party authentication challenges (signup/signin) are always signed with the identity key, because relying parties do not have access to any relay's session cache.
- **`poweur send --sign-with=identity`.** An explicit per-send opt-out of sessions. The CLI skips `POST /sessions`, leaves `session_id` and `session_proof` empty on the wire, and signs the canonical message with the identity Ed25519 key. Relays verify against the sender's resolved identity key (well-known / DNS, or via `GET /identities/<sender>` on a peer relay). See [Message Format → Choosing a signing key](/protocol/message-format#choosing-a-signing-key).

## Multiple Identities

A single user can hold multiple Poweur ID identities. Each identity has its own keys and publication (well-known document and/or DNS records). Common use cases for multiple identities:

- Separate personal and professional identities
- A human identity and one or more bot/agent identities
- Testing or staging identities

The mobile app provides an **Active Identity Selector** — a persistent header that shows the currently selected identity and allows switching context across all app screens.

## Third-Party Authentication

An Poweur ID identity should be usable as a portable login identity for websites and apps. The recommended flow is:

1. The verifier creates a challenge containing `domain`, `audience`, `nonce`, `issued_at`, `expires_at`, `request_id`, and the requested action such as `signup` or `signin`.
2. The request is handed to the mobile app via QR, universal link, or deep link.
3. The app shows the verifier identity and requested action to the user.
4. After approval, the app signs the challenge with the identity's private key.
5. The verifier resolves the public key (well-known / DNS, or a compatible DID document) and verifies the signature.

This keeps the Poweur ID FQDN as the canonical identifier while making it usable in login ecosystems that expect signed challenge-response proofs.

## Bots and Automated Agents

Bots and automated agents interact with the protocol through the CLI or the relay API directly, rather than through mobile passkeys. They manage their key pairs programmatically and can:

- Create identities via `POST /identities`
- Sign and send messages via `POST /messages`
- Retrieve inbox messages via `GET /messages/:identity` after completing the challenge–response flow

Bot identities are indistinguishable from human identities at the protocol level. Participants who wish to signal that an identity is automated can add a future capability record (e.g., `poweur-caps=bot`) — but that is not enforced in the MVP.

## Identity Lifecycle

### Registration (hosted)

1. Client generates Ed25519 identity + X25519 encryption key pairs.
2. Client builds and signs an identity document, then calls `POST /identities` **without** a DNS token (identity must be under `HOSTED_DOMAINS`).
3. Relay persists the document under `POWEUR_DATA` and serves it at `/.well-known/poweur/` via Host-routing.
4. On first use, the client registers a session via `POST /sessions`.

### Registration (self-hosted DNS)

1. Same key generation + signed identity document.
2. Client calls `POST /identities` with `dns_provider` + scoped DNS API token.
3. Relay writes `_poweur` / `_poweur-enc` `TXT` records and the routing `A`/`CNAME`, then discards the token.
4. The owner should also serve (or rely on DNS-only fallback for) the well-known document when possible.

### Propagation

Hosted identities are available as soon as the relay has stored the document (wildcard DNS already points at the relay). Self-hosted DNS changes may take up to the record TTL to propagate; clients can re-resolve to confirm.

### Key Rotation

Supported via signed rotation statements and `previous_keys` on the identity document (`poweur key rotate`, `POST /identities/{id}/rotate`). See [Web identity → Key rotation](/protocol/web-identity#key-rotation).

### Relay Migration

Update the document's `relay` field (and DNS `A`/`CNAME` if used), optionally set `moved_to`, and use export/migrate helpers (`poweur identity export`, `poweur relay set`).

## Related

- [Web Identity](/protocol/web-identity)
- [DNS Records](/protocol/dns-records)
- [Interoperability](/protocol/interoperability)
- [Message Format](/protocol/message-format)
- [Security Model](/security/model)
