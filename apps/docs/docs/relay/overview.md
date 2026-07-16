---
id: overview
sidebar_position: 1
title: Relay Overview
---

# Relay Overview

The Poweur ID relay (`apps/api`) is the core server component of the system. It is a Go HTTP server that implements the relay side of the protocol: accepting, verifying, routing, and delivering signed messages; ingesting delivery acks; registering identities (hosted or DNS); serving identity documents over `/.well-known/poweur/`; and hosting per-identity file trees over WebDAV when durable storage is enabled.

A relay is the **home** for the identities it locally hosts. It does **not** act as a generic open relay for unrelated parties — see the at-least-one-local rule in the [API Reference](/relay/api-reference#at-least-one-local-rule).

## Responsibilities

**Message ingress.** Accept signed, encrypted messages at `POST /messages`. Rate limiting, signature verification, and the at-least-one-local rule decide whether the relay accepts.

**Signature verification.** Verify envelopes against the sender's public key from the [resolver chain](/protocol/web-identity) (well-known first, DNS fallback), or against a cached session key / attached `session_proof`.

**Message routing.** For privacy-proxy / `--via-home-relay` sends where the sender is local and the recipient is remote, resolve the recipient's relay and forward the signed envelope.

**Message delivery.** Store messages for local identities until drained via inbox poll (in-memory today; durable inbox is a later epic).

**Ack ingestion.** Accept signed `delivered_client` acks at `POST /acks` under the same locality rules.

**Identity registration.**

- **Hosted** — no DNS token; identity under `HOSTED_DOMAINS`; persist signed `identity_document` under `POWEUR_DATA`.
- **DNS** — client-supplied provider token; relay writes `TXT` + routing records, then discards the token.

**Well-known + files.** When `POWEUR_DATA` is set, serve Host-routed `/.well-known/poweur/…`, WebDAV at `/dav/<identity>/`, and optional `/pub/` file sharing. See [File storage](/files/storage-model).

## Durable vs ephemeral state

The relay still holds **no identity private keys**. With `POWEUR_DATA` configured it *does* keep durable per-identity data on disk:

| State | Purpose | Survives restart? |
|-------|---------|:-----------------:|
| Identity documents (`poweur-sys/public/id.json`) | Hosted identity publication | Yes |
| File trees (`/public`, `/private`, …) | Per-identity home filesystem | Yes |
| Rate limit counters | Per-sender + global buckets | No |
| DNS / resolve caches | Peer addresses, identity resolve TTL | No |
| Identity inbox / ack store | Pending messages and acks | No (MVP) |
| Session + DAV token caches | Short-lived credentials | No |

Losing ephemeral state on restart is acceptable for messaging MVP behaviour; losing `POWEUR_DATA` is not — back it up (see `deploy/BACKUP.md`).

## Discovery and DNS

- **Verify a sender** → resolve via well-known, then DNS TXT if needed
- **Route to a recipient** → identity document `relay` field and/or DNS `A`/`CNAME`
- **Register (DNS mode)** → write TXT + A/CNAME via provider API with an ephemeral client token
- **Register (hosted)** → store document only; wildcard DNS already points the parent at this relay

## Security properties

- **No private keys at rest** for identities.
- **No long-lived DNS write credentials** required for hosted registration; DNS tokens in DNS mode are ephemeral.
- **Verification at every hop** using the resolver chain.
- **Payloads are end-to-end encrypted**; the relay sees ciphertext plus routing metadata.
- **Metadata visibility** depends on direct-send vs `--via-home-relay` — see [Security Model](/security/model#send-path-metadata-trade-off).

## Deployment

1. A domain for the relay hostname (and optionally a `HOSTED_DOMAINS` parent with a wildcard `A`/`CNAME`)
2. TLS covering the relay and wildcard identities (see [TLS](/security/tls))
3. `POWEUR_DATA` volume for durable identities and files
4. Environment variables from [Configuration](/relay/configuration)

The `apps/infra` directory contains Terraform examples for deploying on Hetzner Cloud.

## Related

- [API Reference](/relay/api-reference)
- [Configuration](/relay/configuration)
- [DNS Management](/relay/dns-management)
- [Web Identity](/protocol/web-identity)
- [File storage](/files/storage-model)
- [Security Model](/security/model)
