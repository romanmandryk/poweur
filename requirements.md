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

---

## DNS Routing

DNS is the routing layer. Each identity subdomain must have an `A` or `CNAME` record pointing to the relay server that handles messages for that identity. For example:

- `alice.example.com` → Alice's relay IP or hostname
- `bob.example.org` → Bob's relay IP or hostname

When a relay needs to deliver a message to `bob.example.org`, it resolves the DNS record for that subdomain to find the destination relay and forwards the message there. This eliminates the need for a central routing registry: any relay can route to any identity by performing a standard DNS lookup.

Operators hosting multiple identities on a single relay point all of their subdomains to the same server. Identities on different relays are routed transparently via DNS resolution.

---

## Relay (Go Server)

The relay is the core server component, implemented in Go (`apps/api`). Its responsibilities in the MVP are:

- **Message ingress**: Accept signed messages from senders (human clients, bots, or other relays).
- **Signature verification**: Verify that each inbound message is correctly signed by the claimed sender identity.
- **Message routing**: Resolve the recipient's subdomain via DNS to locate their relay, then forward the message to that relay over HTTPS.
- **Message delivery**: Accept inbound forwarded messages and deliver them to local identity inboxes.
- **Identity hosting**: Associate one or more identity subdomains with the relay instance, making those identities reachable.

The relay exposes an HTTP/JSON API consumed by the mobile app, the CLI, and peer relays. In the MVP, persistence can be minimal (in-memory or simple file-based storage); durability and scalability are post-MVP concerns.

---

## Mobile App

The mobile app (`apps/mobile`) is a React Native application targeting iOS and Android. It is the primary interface for human users. Key requirements:

- **Passkey-based authentication**: Users create and manage their identity using a device passkey. The passkey is bound to the user's key pair and is used to sign messages locally before they are sent to the relay.
- **Identity registration**: On first launch the user claims a subdomain (subject to availability on their chosen relay) and provisions their passkey.
- **Messaging**: Users can compose and send signed messages to other identities and view messages delivered to their own inbox.
- **Relay configuration**: The app must be configured with, or allow the user to select, a relay endpoint to connect to.

The mobile app does not perform DNS routing itself; it delegates sending to its configured relay.

---

## CLI

The CLI (`apps/cli`) is the third application in the monorepo. It provides a scriptable interface to the relay API, intended for developers, bots, and automated agents. Key requirements:

- **Identity management**: Create and manage identity key pairs stored locally (e.g., in a config file or OS keychain).
- **Send messages**: Sign and send messages from a CLI-managed identity to any recipient identity.
- **Read inbox**: Retrieve and display messages delivered to a CLI-managed identity from the relay.
- **Relay interaction**: All network operations go through a configured relay endpoint (set via config file or environment variable).
- **Scriptability**: The CLI should support machine-readable output (e.g., JSON) to facilitate use in scripts and automated pipelines.

The CLI package is named `@eurything/cli` and lives at `apps/cli` within the monorepo.

---

## API

The relay exposes an HTTP/JSON API. The MVP API surface should include at minimum:

- `POST /messages` — submit a signed message for routing or local delivery.
- `GET /messages/:identity` — retrieve pending messages for a local identity (authenticated by the requester proving ownership of that identity).
- `GET /health` — liveness check.

Authentication for inbox access uses a challenge–response mechanism: the client signs a short-lived challenge with its private key, and the relay verifies the signature against the public key associated with the claimed identity subdomain. This avoids passwords or tokens while remaining consistent with the passkey model used in the mobile app.

The API is consumed by the mobile app, the CLI, and peer relays performing message forwarding.

---

## Future Considerations

The following are explicitly out of scope for the MVP but should be kept in mind as the architecture evolves:

- **Capability advertisement via DNS**: Additional DNS record types (e.g., `TXT` records) can be used to advertise capabilities associated with an identity, such as supported protocols, service endpoints, or metadata. This allows the DNS layer to evolve from pure routing into a richer discovery mechanism.
- **Additional identity use cases**: Beyond messaging, identities could be used for authentication, authorization, payments, or social graph discovery.
- **Federation and relay peering**: Relays may eventually maintain persistent connections or trust relationships with known peer relays to improve reliability and reduce per-message DNS lookups.
- **Key rotation and revocation**: A mechanism for rotating or revoking the key pair associated with an identity without losing the subdomain will be necessary for production use.
- **Scalability and persistence**: The relay's storage and delivery model will need to be hardened for production workloads.
- **Push notifications**: The mobile app will require a push notification integration so users receive messages when the app is in the background.
