---
id: overview
sidebar_position: 1
title: Protocol Overview
---

# Protocol Overview

The Poweur ID Protocol defines the identity model, discovery chain, message format, signing scheme, verification procedure, authentication flow, and end-to-end routing used across all system components. Every client, relay, and automated agent that participates in the Poweur ID network must conform to this protocol.

## Design Principles

**The identity is a DNS name; discovery is web-first.** The canonical identifier is an FQDN the owner controls (`alice.com`, `bot.example.org`, or a hosted name like `alice.poweur.net`). Public keys and relay hints are resolved primarily from `https://<identity>/.well-known/poweur/`, with DNS `TXT` as a fallback. If both are present and disagree, verification **fails closed**.

**DNS naming without a central registry.** There is no global username database. Hosted relays may offer wildcard domains (`*.poweur.net`) so users get a name without operating DNS themselves; self-hosters point their own domain (and optionally publish DNS records).

**The relay is an untrusted forwarder (and optional host).** Relays route and verify messages and may store durable identity documents and file trees, but never hold identity private keys. Authenticity is proven by signatures against resolved public keys — not by trusting the relay that delivered a message.

**Trust is rooted in device hardware (or equivalent client custody).** Private keys live in the user's device (passkey / secure enclave on mobile; OS keychain or key files for CLI/web). Server-side infrastructure is treated as untrusted for key material.

**Signatures prevent forgery; name control prevents impersonation.** A message cannot be forged without the identity private key. An identity cannot be impersonated without controlling the name's publication channel (HTTPS origin and/or DNS zone).

**The same identity must work outside messaging.** A Poweur ID is not only a messaging address. The same key material should support third-party sign-up/sign-in, with the user's client as signer and approval surface.

**Wire format is JSON for the current protocol.** All messages are UTF-8 JSON. A future iteration may adopt a compact binary format while preserving field schema and signing semantics.

## Architecture Summary

```
┌────────────────────────────────────────────────────────────┐
│  Discovery (web-first, DNS fallback)                       │
│  https://alice.com/.well-known/poweur/id.json  ← preferred │
│  _poweur.alice.com TXT / alice.com A|CNAME     ← optional  │
└──────────────────────────┬─────────────────────────────────┘
                           │ resolve + verify
     ┌─────────────────────┴─────────────────────┐
     │                                           │
┌────┴──────┐       POST /messages        ┌──────┴────┐
│ Alice's   ├────────────────────────────>│  Bob's    │
│  Relay    │   (signed + encrypted)      │  Relay    │
└────┬──────┘                             └──────┬────┘
     │                                           │
┌────┴──────┐                             ┌──────┴────┐
│  Alice's  │                             │  Bob's    │
│  Device   │                             │  Device   │
└───────────┘                             └───────────┘
```

### Identity layer

Each identity is an FQDN. Hosted examples use a parent like `poweur.net` (`alice.poweur.net`); self-hosted identities use any domain the owner controls. Keys live in a signed [identity document](/protocol/web-identity); DNS `TXT` may mirror the same material. Relay routing uses the document's `relay` field and/or the name's `A`/`CNAME`.

### Relay layer

Relays are HTTP/JSON servers. Each relay is the **home** for identities it hosts. Responsibilities include:

1. Accept signed, encrypted messages at `POST /messages` (rate limits, at-least-one-local rule)
2. Verify signatures via the resolver chain (well-known, then DNS)
3. Store inboxes for local recipients; forward only on sanctioned privacy-proxy paths
4. Host durable identity documents and file trees when `POWEUR_DATA` is configured
5. Register identities: **hosted** (no DNS write) or **DNS** (client-supplied provider token)

Sessions and in-flight inboxes remain memory-oriented; identity documents and files are durable under `POWEUR_DATA` (see [Relay overview](/relay/overview)).

### Client layer

Clients (web, mobile, CLI) generate and store keys, sign/encrypt messages, talk to relays, and approve third-party auth challenges. They never exchange traffic peer-to-peer in the default model.

## Protocol Versioning

The current protocol version is **0.1**. The `version` field in relay health responses lets clients detect incompatible relays. Breaking changes increment the major version.

## Interoperability Direction

The FQDN stays the canonical identifier. Bridges (`did:web`, OIDC, etc.) are additive — see [Interoperability](/protocol/interoperability).

## Related

- [Web Identity](/protocol/web-identity)
- [Identity Model](/protocol/identity-model)
- [DNS Records](/protocol/dns-records)
- [Message Format](/protocol/message-format)
- [File storage](/files/storage-model)
- [Routing](/protocol/routing)
