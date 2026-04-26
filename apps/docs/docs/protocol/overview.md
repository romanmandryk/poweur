---
id: overview
sidebar_position: 1
title: Protocol Overview
---

# Protocol Overview

The Eurything Protocol defines the message format, signing scheme, verification procedure, authentication flow, and end-to-end routing model used across all system components. Every client, relay, and automated agent that participates in the Eurything network must conform to this protocol.

## Design Principles

**DNS is the only durable store.** No central registry, no relay database. Every piece of state that must survive a relay restart — identities, public keys, routing — lives in DNS records. This means a relay can be restarted, replaced, or horizontally scaled without data migration or coordination.

**The relay is an untrusted forwarder.** Relays route and verify messages but never hold private keys. A message's authenticity is proven by its signature, verifiable by anyone with access to DNS — not by trusting the relay that delivered it.

**Trust is rooted in device hardware.** Private keys live in the hardware secure enclave of the user's mobile device (iOS Secure Enclave / Android StrongBox). The relay and all server-side infrastructure are treated as untrusted.

**Signatures prevent forgery; DNS prevents impersonation.** A message cannot be forged because it must be signed by the identity's private key. An identity cannot be impersonated because the authoritative public key is in DNS — which only the domain owner controls.

**The same identity must work outside messaging.** An Eurything DNS identity is not only a messaging address. The same key material should be usable for third-party sign-up and sign-in flows, with the mobile app acting as the user's signer and approval surface.

**Wire format is JSON for MVP.** All messages are UTF-8 encoded JSON. A future iteration may adopt a compact binary format (Protocol Buffers, MessagePack) while preserving the field schema and signing semantics.

## Architecture Summary

```
┌─────────────────────────────────────────────────────────┐
│                    DNS (Global)                          │
│  _eurything.alice.poweur.net TXT "eurything-pubkey=..."  │
│  alice.poweur.net            A   <relay-ip>              │
└─────────────────────┬───────────────────────────────────┘
                      │ lookup
     ┌────────────────┴─────────────────┐
     │                                  │
┌────┴──────┐                    ┌──────┴────┐
│ Alice's   │ POST /messages     │  Bob's    │
│  Relay    ├───────────────────>│  Relay    │
│           │  (signed envelope) │           │
└────┬──────┘                    └──────┬────┘
     │ sign + send                      │ verify + deliver
     │                                  │
┌────┴──────┐                    ┌──────┴────┐
│  Alice's  │                    │  Bob's    │
│  Device   │                    │  Device   │
│ (vault)   │                    │  (inbox)  │
└───────────┘                    └───────────┘
```

### Identity Layer (DNS)

Each identity is a fully qualified subdomain (`alice.poweur.net`). The identity's public key is stored in a `TXT` record at `_eurything.<subdomain>`. The identity's relay is found by resolving an `A` or `CNAME` record on the subdomain itself.

Any party — another relay, a client, an auditor — can verify a message by fetching the sender's public key from DNS and checking the signature. No trusted third party is involved.

### Relay Layer

Relays are stateless HTTP/JSON servers. Each relay is the **home** for the identities it locally hosts. Its responsibilities are:

1. Accept signed messages directly from any client at `POST /messages`, subject to rate limits and the [at-least-one-local rule](/relay/api-reference#at-least-one-local-rule)
2. Verify message signatures against public keys from DNS (or against cached session keys when present)
3. Store accepted messages for local recipients; forward to the recipient relay only when the **sender** is locally hosted (privacy-proxy `--via-home-relay` mode)
4. Accept signed delivery acks at `POST /acks` and surface them on the next inbox poll (the second tick in the [two-tick delivery model](/protocol/delivery-acks))
5. Register new identities by writing DNS records on behalf of clients (owner-only / admin endpoints; identity-signed)

Relays hold no database. The in-memory state is per-identity inboxes, the ack store, the session cache, rate-limit counters (per-sender + global), and a DNS routing cache — all ephemeral and safe to lose on restart.

### Client Layer

Clients (mobile apps, CLI) are responsible for:

1. Generating and storing identity key pairs (in secure hardware)
2. Generating client-side message ids, signing messages, and persisting a per-identity pending journal
3. Sending each outbound message **directly** to the recipient's home relay (default), or routing through their own home relay for privacy (`--via-home-relay`)
4. Polling their own home relay for inbound messages and acks
5. Emitting signed `delivered_client` acks back to the original sender's home relay after successful decrypt
6. Approving third-party authentication challenges

Clients never communicate directly with each other. Every message exchange is mediated by a relay — but in the default model, a single message touches only the recipient's home relay; the sender's home relay only sees the resulting tick-2 ack.

## Protocol Versioning

The current protocol version is **0.1** (MVP). The `version` field is included in relay health responses to allow clients to detect incompatible relays. Breaking changes will increment the major version.

## Interoperability Direction

Eurything keeps the DNS name as the canonical identifier, but it should map cleanly into existing ecosystems:

- `did:dns:<fqdn>` is the closest conceptual DID mapping, but that method is still emerging.
- `did:web:<fqdn>` via `https://<identity>/.well-known/did.json` is the most practical bridge for existing DID-aware tooling.
- `/.well-known/eurything.json` should be the protocol's own discovery endpoint for relay metadata, supported capabilities, and mobile app auth handoff details.
- Signed message envelopes should evolve toward a more explicit structure, conceptually similar to DIDComm basic messages or Nostr-style events, while preserving DNS-native verification and routing.

## Related

- [Identity Model](/protocol/identity-model)
- [DNS Records](/protocol/dns-records)
- [Message Format](/protocol/message-format)
- [Interoperability](/protocol/interoperability)
- [Routing](/protocol/routing)
