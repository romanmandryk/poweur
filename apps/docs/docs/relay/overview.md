---
id: overview
sidebar_position: 1
title: Relay Overview
---

# Relay Overview

The Eurything relay (`apps/api`) is the core server component of the Eurything system. It is a Go HTTP server that implements the relay-side of the Eurything Protocol: accepting, verifying, routing, and delivering signed messages; and registering new identities by writing DNS records.

## Responsibilities

The relay has five core responsibilities in the MVP:

**Message ingress.** Accept signed messages submitted by clients (mobile apps, CLI, or other relays) via `POST /messages`.

**Signature verification.** Verify that every inbound message is correctly signed by the claimed sender identity. Signature verification uses the sender's public key fetched from DNS (`_eurything.<sender>` TXT record).

**Message routing.** For messages addressed to remote identities, resolve the recipient's relay via DNS and forward the signed envelope to that relay.

**Message delivery.** For messages addressed to local identities, accept inbound forwarded messages and hold them in memory until the recipient retrieves them.

**Identity registration.** Accept `POST /identities` requests from clients, write the required DNS records (public key TXT record, routing A/CNAME record) using a client-supplied DNS provider token, and register the identity as locally hosted.

## Stateless Design

The relay is intentionally almost stateless. It holds no database and performs no disk writes. All durable state lives in DNS:

- Identity existence and public keys are in DNS `TXT` records.
- Routing (which relay handles which identity) is in DNS `A`/`CNAME` records.
- No messages are persisted to disk.

The only in-memory state the relay maintains is:

| State | Purpose | Survives restart? |
|-------|---------|:-----------------:|
| Rate limit counters | Per-sender token buckets and sliding windows | No |
| DNS routing cache | Resolved peer relay addresses, keyed by identity | No |
| Identity inbox | Pending messages for locally hosted identities | No |

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
- **Verification at every hop.** Every relay that receives a message verifies the signature independently, including relays that receive forwarded messages.
- **Messages are readable.** In the MVP, message payloads are plaintext. A relay operator can read message content. End-to-end encryption is a post-MVP goal.

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
