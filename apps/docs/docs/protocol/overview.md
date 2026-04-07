---
id: overview
sidebar_position: 1
title: Protocol Overview
---

# Protocol Overview

The Eurything Protocol defines the message format, signing scheme, verification procedure, and end-to-end routing model used across all system components. Every client, relay, and automated agent that participates in the Eurything network must conform to this protocol.

## Design Principles

**DNS is the only durable store.** No central registry, no relay database. Every piece of state that must survive a relay restart — identities, public keys, routing — lives in DNS records. This means a relay can be restarted, replaced, or horizontally scaled without data migration or coordination.

**The relay is an untrusted forwarder.** Relays route and verify messages but never hold private keys. A message's authenticity is proven by its signature, verifiable by anyone with access to DNS — not by trusting the relay that delivered it.

**Trust is rooted in device hardware.** Private keys live in the hardware secure enclave of the user's mobile device (iOS Secure Enclave / Android StrongBox). The relay and all server-side infrastructure are treated as untrusted.

**Signatures prevent forgery; DNS prevents impersonation.** A message cannot be forged because it must be signed by the identity's private key. An identity cannot be impersonated because the authoritative public key is in DNS — which only the domain owner controls.

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

Relays are stateless HTTP/JSON servers. Their responsibilities are:

1. Accept signed messages from senders
2. Verify message signatures against public keys from DNS
3. Resolve recipient relays via DNS and forward messages
4. Accept forwarded messages and hold them in memory for retrieval
5. Register new identities by writing DNS records on behalf of clients

Relays hold no database. The only in-memory state is rate limit counters and a DNS routing cache — both ephemeral and safe to lose on restart.

### Client Layer

Clients (mobile apps, CLI) are responsible for:

1. Generating and storing identity key pairs (in secure hardware)
2. Signing messages before dispatch
3. Communicating with their configured relay for send and receive

Clients never communicate directly with other clients. All message exchange passes through relays.

## Protocol Versioning

The current protocol version is **0.1** (MVP). The `version` field is included in relay health responses to allow clients to detect incompatible relays. Breaking changes will increment the major version.

## Related

- [Identity Model](/protocol/identity-model)
- [DNS Records](/protocol/dns-records)
- [Message Format](/protocol/message-format)
- [Routing](/protocol/routing)
