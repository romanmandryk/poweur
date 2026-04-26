---
id: overview
sidebar_position: 1
title: Relay Overview
---

# Relay Overview

The Eurything relay (`apps/api`) is the core server component of the Eurything system. It is a Go HTTP server that implements the relay-side of the Eurything Protocol: accepting, verifying, routing, and delivering signed messages; ingesting signed delivery acks; and registering new identities by writing DNS records.

A relay is the **home** for the identities it locally hosts. It ingests messages **for** them (the default direct-send path; clients of any sender post directly to the recipient's home relay), and it optionally ingests messages **from** them when the user opts into routing outbound traffic via the home relay (`--via-home-relay`). It does **not** act as a generic open relay for unrelated parties — see the at-least-one-local rule in the [API Reference](/relay/api-reference#at-least-one-local-rule).

## Responsibilities

The relay has six core responsibilities in the MVP:

**Message ingress.** Accept signed messages submitted by any client at `POST /messages`. The signed envelope is the unit of trust; rate limiting, signature verification, and the at-least-one-local rule decide whether the relay accepts and where it stores the message.

**Signature verification.** Verify that every inbound message is correctly signed by the claimed sender identity. Signature verification uses the sender's public key fetched from DNS (`_eurything.<sender>` TXT record), or — for session-signed envelopes — a cached session key (or the attached `session_proof`).

**Message routing.** For messages where the **sender** is local but the **recipient** is remote (the privacy-proxy / `--via-home-relay` case), resolve the recipient's relay via DNS and forward the signed envelope. This is the only sanctioned forwarding path; all other "neither-local" forwards are rejected with `403 not_authorized`.

**Message delivery.** For messages addressed to local identities, store them in memory keyed by the client-assigned `id` until the recipient drains the inbox via `GET /messages/:identity`.

**Ack ingestion.** Accept signed `delivered_client` acks at `POST /acks`, subject to the same rate limits and at-least-one-local rule. Acks for local identities are surfaced alongside messages on the next inbox poll; acks where the ack-sender is local but the recipient is remote are forwarded to the recipient relay.

**Identity registration.** Accept `POST /identities` requests from clients, write the required DNS records (public key TXT record, routing A/CNAME record) using a client-supplied DNS provider token, and register the identity as locally hosted. This and other per-identity admin endpoints (`POST /identities/:identity/encryption-key`, `DELETE /sessions/:id`) require an `identity_signature` proving caller ownership.

## Stateless Design

The relay is intentionally almost stateless. It holds no database and performs no disk writes. All durable state lives in DNS:

- Identity existence and public keys are in DNS `TXT` records.
- Routing (which relay handles which identity) is in DNS `A`/`CNAME` records.
- No messages are persisted to disk.

The only in-memory state the relay maintains is:

| State | Purpose | Survives restart? |
|-------|---------|:-----------------:|
| Rate limit counters | Per-sender + global token buckets and sliding windows | No |
| DNS routing cache | Resolved peer relay addresses, keyed by identity | No |
| Identity inbox | Pending messages for locally hosted identities | No |
| Ack store | Pending `delivered_client` acks for locally hosted identities | No |
| Session cache | Short-lived signing keys registered via `POST /sessions` | No |

Losing all of this state on restart is acceptable for the MVP. Rate limit counters reset (briefly permitting a burst); the DNS cache rebuilds on-demand; messages in the inbox are lost (durability is a post-MVP concern).

This stateless design has several important properties:

- **Zero-downtime replacement.** A new relay process can take over from an old one without any data handoff.
- **Horizontal scaling.** Multiple relay instances can run behind a load balancer. Any instance can handle any request.
- **No backup or migration risk.** There is nothing to back up or migrate at the relay layer.

## DNS as Storage

DNS is the relay's only durable backend. When the relay needs to:

- **Verify a message** → fetch the sender's public key from `_eurything.<sender>` TXT
- **Route a message** → resolve the recipient's `A`/`CNAME` record
- **Register an identity** → write a TXT + A/CNAME record via the DNS provider API

All reads are standard DNS queries. All writes go through the DNS provider API (Cloudflare or Hetzner) using a client-supplied token that is discarded immediately after use.

## DNS Routing Cache

To avoid a DNS lookup on every outbound message, the relay maintains a short-lived in-memory cache of resolved relay addresses. Cache entries expire at the DNS record TTL. On a cache miss, a fresh DNS lookup is performed.

The cache is intentionally separate from the DNS write path — the relay never caches its own write operations, ensuring DNS records reflect the ground truth immediately after writes succeed.

## Security Properties

The relay's stateless, DNS-anchored design has important security implications:

- **No private keys at rest.** The relay never stores private keys. A compromised relay cannot exfiltrate user credentials.
- **No DNS write credentials at rest.** DNS provider tokens are used ephemerally during registration and discarded. A compromised relay cannot make further DNS changes.
- **Verification at every hop.** Every relay that receives a message verifies the signature independently, including the recipient relay's check on a privacy-proxy forward.
- **Payloads are end-to-end encrypted.** Every message on the wire is ChaCha20-Poly1305 ciphertext under an X25519-derived key; the relay only sees ciphertext plus routing metadata (id, sender, recipient, timestamp, session id). Plaintext payloads are rejected at ingress with `400 encryption_required`.
- **Metadata is visible to whichever relay handles a given hop.** By default each side's home relay sees only its own user's metadata: Bob's relay sees Alice's IP and the fact she messaged Bob; Alice's home relay sees only inbound acks. Opting into `--via-home-relay` shifts the trust: Bob's relay then sees only Alice's home relay's IP, and Alice's home relay also sees Alice's outbound metadata. See the [Security Model](/security/model#send-path-metadata-trade-off).

## Deployment

The relay is a single stateless Go binary. Deployment requires:

1. A domain for hosting identities (e.g. `poweur.net`)
2. A wildcard TLS certificate for `*.poweur.net` (see [TLS Configuration](/security/tls))
3. DNS A/CNAME records pointing the relay's hostname to its IP address
4. The relay binary running with the required environment variables set (see [Configuration](/relay/configuration))

The `apps/infra` directory contains Terraform configuration for deploying a production relay on Hetzner Cloud.

## Related

- [API Reference](/relay/api-reference)
- [Configuration](/relay/configuration)
- [DNS Management](/relay/dns-management)
- [Security Model](/security/model)
