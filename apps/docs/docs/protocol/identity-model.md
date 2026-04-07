---
id: identity-model
sidebar_position: 2
title: Identity Model
---

# Identity Model

An Eurything identity is a **fully qualified subdomain** that the owner controls. The subdomain is the human-readable handle; the associated public key is the cryptographic identity.

## Identity Format

Identities follow standard DNS subdomain rules:

- Must be a fully qualified domain name (FQDN), e.g. `alice.poweur.net`
- The handle portion (leftmost label) must be at least 8 characters
- Only lowercase letters (`a–z`), digits (`0–9`), and hyphens (`-`) are permitted in the handle
- No leading or trailing hyphens
- The parent domain is configurable; the default for `poweur.net`-hosted identities is `<handle>.poweur.net`

Examples of valid identity addresses:

```
alice.poweur.net
r2d2-bot.poweur.net
mycompany.example.org
agent-007.example.com
```

## Key Pairs

Each identity is associated with an **Ed25519 key pair**:

- **Private key** — never leaves the device. Stored in hardware-backed secure storage (iOS Secure Enclave / Android StrongBox on mobile; OS keychain or local key file for the CLI).
- **Public key** — published in DNS. Any party can retrieve it by resolving `_eurything.<identity>` as a `TXT` record.

The public key is base64-encoded and stored in DNS as:

```
_eurything.alice.poweur.net.  300  IN  TXT  "eurything-pubkey=ed25519:<base64-encoded-public-key>"
```

Because the public key is published in DNS and the private key never leaves the device, there is no central authority that can forge or revoke an identity's signatures. The owner of the DNS zone is the owner of the identity.

## Passkeys (Mobile)

On mobile, key pairs are managed through the **WebAuthn/FIDO2 passkey API**. When a user creates an identity:

1. The app calls the platform's passkey registration API, specifying the identity's domain as the relying party.
2. The platform generates an Ed25519 key pair in the hardware secure enclave.
3. The app extracts the public key and submits it to the relay via `POST /identities`.
4. The relay writes the public key to DNS as a `TXT` record using the client-supplied DNS provider token.

Signing a message uses the passkey assertion API, which triggers biometric authentication (Face ID, fingerprint) before the secure enclave performs the signing operation. The private key never materialises in app memory.

## CLI Key Management

The CLI stores keys locally, either in the OS keychain or as a key file at `~/.eurything/keys/<identity>/`. The private key is never transmitted over the network. Key generation uses the same Ed25519 algorithm as the mobile path, ensuring protocol compatibility.

## Multiple Identities

A single user can hold multiple Eurything identities. Each identity has its own passkey and its own DNS records. Common use cases for multiple identities:

- Separate personal and professional identities
- A human identity and one or more bot/agent identities
- Testing or staging identities

The mobile app provides an **Active Identity Selector** — a persistent header that shows the currently selected identity and allows switching context across all app screens.

## Bots and Automated Agents

Bots and automated agents interact with the protocol through the CLI or the relay API directly, rather than through mobile passkeys. They manage their key pairs programmatically and can:

- Create identities via `POST /identities`
- Sign and send messages via `POST /messages`
- Retrieve inbox messages via `GET /messages/:identity` after completing the challenge–response flow

Bot identities are indistinguishable from human identities at the protocol level. Participants who wish to signal that an identity is automated can add a future capability record (e.g., `eurything-caps=bot`) — but that is not enforced in the MVP.

## Identity Lifecycle

### Registration

1. Client generates a key pair.
2. Client calls `POST /identities` with the identity, public key, DNS provider type, and a scoped DNS provider API token.
3. Relay uses the token to write two DNS records: the `TXT` public key record and the `A`/`CNAME` routing record.
4. Relay discards the token immediately after the DNS writes succeed.
5. The identity is now resolvable from any relay or client that can reach public DNS.

### DNS Propagation

After registration, DNS changes may take up to the record TTL to propagate globally. The mobile app provides a **"Check DNS"** button that performs a live lookup of the `TXT` record to confirm propagation.

### Key Rotation

Key rotation is a **post-MVP** concern. The procedure will involve creating a new key pair, publishing the new public key in DNS, and (optionally) publishing a signed rotation statement from the old key to prove continuity. The protocol is designed to accommodate this without breaking changes.

### Relay Migration

Because routing is DNS-driven, migrating an identity to a new relay requires updating the `A` or `CNAME` record to point to the new relay. No relay coordination is needed.

## Related

- [DNS Records](/protocol/dns-records)
- [Message Format](/protocol/message-format)
- [Security Model](/security/model)
