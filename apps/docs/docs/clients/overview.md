---
id: overview
sidebar_position: 1
title: Clients Overview
---

# Clients Overview

Poweur ID is designed for two categories of client: **human users** interacting through mobile apps, and **automated agents** (bots, scripts, pipelines) using the CLI or the HTTP API directly.

## Mobile Apps

Two native mobile apps implement the Poweur ID Protocol for end-users:

- **iOS** (`apps/ios`) — built in Swift
- **Android** (`apps/android`) — built in Kotlin

Both apps are native (not React Native) because the core functionality is security-sensitive and deeply platform-specific: passkey registration and assertion via WebAuthn/FIDO2, private key storage in hardware secure enclaves, cryptographic signing, and biometric authentication. Native platform APIs are more directly auditable and less abstraction-layered than React Native equivalents for these operations.

### The App as a Vault

The app's primary security responsibility is key custody. Private keys live in the device's hardware secure enclave (iOS Secure Enclave / Android StrongBox or TEE) and are accessed only via the passkey APIs. Keys **never leave the device in plaintext**.

In addition to private keys, the app stores:

- **DNS provider API token** — in iOS Keychain / Android Keystore. Passed to the relay ephemerally when needed (identity registration, relay migration). The relay receives the token for that request duration only.
- **Relay configuration** — relay URL and any associated auth tokens, stored in Keychain/Keystore.

A compromised relay cannot expose user secrets because the relay never holds them.

### Key Features

**Identity creation.** The user chooses a handle and either **hosted** registration (no DNS
token — identity under the relay's wildcard domain, keys via well-known) or **self-hosted**
DNS registration (provider token; relay writes TXT/A). The web client (`apps/web`) supports
both; hosted is the default for `*.poweur.net`.

**Multiple identities.** A user can hold multiple Poweur ID identities. Each identity has its own passkey. An Active Identity Selector header persists across all screens.

**Contacts / lookup.** Resolving another identity is **web-first**: fetch
`https://<id>/.well-known/poweur/id.json`, fall back to DNS TXT / DoH. The web Settings
panel and CLI `poweur identity lookup` expose the same chain (see
[Web identity](/protocol/web-identity)).

**Messaging.** Messages are signed with the sender's passkey before dispatch and sent via the relay protocol. Received messages are fetched from the user's relay (polling for MVP; WebSocket is a stretch goal). All message storage is local — the relay is a forwarder only.

**Authentication approvals.** The same identity must also be usable to sign up to and sign in to third-party websites and apps. The mobile app acts as the signer: it receives an auth request via QR or deep link, shows the relying party details, asks the user to approve, and signs the challenge with the identity's passkey-backed private key.

### App Screens

| Screen | Shown when |
|--------|------------|
| Welcome | First launch, before any identity |
| Identity Creation | User initiates sign-up or adds a new identity |
| Pending Registrations | At least one identity submitted, awaiting DNS propagation |
| Dashboard | At least one identity verified |
| Authentication Approval | User opens an external sign-up/sign-in request |
| Contacts | User navigates to contact list |
| Messaging | User opens a conversation thread |

### Future Capabilities (Planned)

The Dashboard shows module cards for upcoming capabilities:

- **Publishing** — signed content under your identity
- **Receiving Payments** — payment address advertisement via DNS capability records

Authentication is not treated as a separate identity system. It reuses the same Poweur ID (FQDN), key material, and approval surface.

## CLI

The CLI (`apps/cli`) provides a scriptable interface to the relay API for developers, bots, and automated agents. See [CLI Reference](/clients/cli-reference) for full command documentation.

The CLI stores key pairs locally (OS keychain or `~/.poweur/keys/`) and communicates with a configured relay. It supports machine-readable JSON output (`--json`) for use in scripts and automated pipelines.

Key commands for web identity:

- `poweur identity create … --hosted` — relay-only registration
- `poweur identity lookup <id>` — web-first resolve
- `poweur key rotate` — rotate signing keys (identity document + `previous_keys`)
- `poweur identity export` / `poweur relay set` — migration helpers

## Direct API Access

Advanced users and automated systems can interact directly with the [Relay HTTP API](/relay/api-reference) without using the CLI or mobile apps. All authentication in the API is cryptographic (challenge–response with the identity's key pair), so any HTTP client that can manage Ed25519 keys can participate in the protocol.

## Related

- [CLI Reference](/clients/cli-reference)
- [API Reference](/relay/api-reference)
- [Identity Model](/protocol/identity-model)
- [Interoperability](/protocol/interoperability)
- [Security Model](/security/model)

## JavaScript / TypeScript

`@poweur/client` is the protocol as a published npm package — identity,
messaging, files, sync, shares and proof-of-work — for the browser, Node ≥18,
Bun and Deno. It also ships a `poweur` CLI that reads and writes the same
`~/.poweur` tree as the Go one, so the two are interchangeable against a single
identity.

```bash
npx @poweur/client inbox
```

Every JavaScript-ecosystem integration (agent gateways, an MCP server, n8n and
Node-RED nodes) is a thin adapter over it rather than a re-implementation of
canonical signing. See the [JavaScript / TypeScript SDK](/clients/js-sdk).
