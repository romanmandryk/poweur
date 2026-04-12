---
id: overview
sidebar_position: 1
title: Clients Overview
---

# Clients Overview

Eurything is designed for two categories of client: **human users** interacting through mobile apps, and **automated agents** (bots, scripts, pipelines) using the CLI or the HTTP API directly.

## Mobile Apps

Two native mobile apps implement the Eurything Protocol for end-users:

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

**Identity creation.** The user chooses a handle (minimum 8 characters, DNS-safe). The app generates a passkey, extracts the public key, and registers the identity with the relay. The relay writes DNS records. The user sees a confirmation screen once DNS propagation is verified.

**Multiple identities.** A user can hold multiple Eurything identities. Each identity has its own passkey. An Active Identity Selector header persists across all screens.

**Contacts.** Contacts are stored locally. Adding a contact by DNS identity triggers a live DNS lookup to show their capability records, display name, and profile picture.

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

Authentication is not treated as a separate identity system. It reuses the same DNS identity, key material, and approval surface.

## CLI

The CLI (`apps/cli`) provides a scriptable interface to the relay API for developers, bots, and automated agents. See [CLI Reference](/clients/cli-reference) for full command documentation.

The CLI stores key pairs locally (OS keychain or `~/.eurything/keys/`) and communicates with a configured relay. It supports machine-readable JSON output (`--json`) for use in scripts and automated pipelines.

## Direct API Access

Advanced users and automated systems can interact directly with the [Relay HTTP API](/relay/api-reference) without using the CLI or mobile apps. All authentication in the API is cryptographic (challenge–response with the identity's key pair), so any HTTP client that can manage Ed25519 keys can participate in the protocol.

## Related

- [CLI Reference](/clients/cli-reference)
- [API Reference](/relay/api-reference)
- [Identity Model](/protocol/identity-model)
- [Interoperability](/protocol/interoperability)
- [Security Model](/security/model)
