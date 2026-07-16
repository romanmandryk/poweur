---
id: intro
slug: /
sidebar_position: 1
title: Introduction
---

# What is the Poweur ID Protocol?

Poweur ID is an **open, DNS-named identity and messaging protocol**. Every participant — human, bot, or autonomous agent — is identified by a domain name they control (an FQDN), such as `alice.com` if they own that domain, or a hosted name like `alice.poweur.net` on a relay that offers wildcard hosting. That name is their globally unique, human-readable identity. The public key bound to that name is their cryptographic identity.

There is no central username database and no OAuth provider you must join. **DNS remains the naming layer** (your identity *is* a domain name), while keys and capabilities are discovered **web-first** at `https://<identity>/.well-known/poweur/` — with DNS `TXT` records as a fallback for self-hosted setups that prefer DNS publication.

## Why a DNS name — and why also the web?

DNS is already the internet's naming layer. Every device can resolve a domain. DNS is:

- **Decentralised by design** — names are controlled by the domain owner, not a platform.
- **Globally available** — any relay can look up any identity without calling home.
- **Already trusted** — decades of infrastructure protect and serve DNS reliably.
- **Human-readable** — `alice.com` or `alice.poweur.net` is as readable as an email address.

Poweur keeps the **DNS name as the canonical identifier**, but does **not** require every identity to publish keys only in DNS. Hosted identities on a wildcard domain (`*.poweur.net` → one relay) publish a signed [identity document](/protocol/web-identity) over HTTPS. Self-hosters may publish the same document from their own origin, and/or publish keys in DNS `TXT` records. Relays and clients resolve **web first, DNS second**, and fail closed if both are present and disagree.

`alice.poweur.net` in examples is a **hosted-service illustration**, not a requirement to use that parent. You still need *some* domain name: if you own `alice.com` (or `bot.example.org`), that FQDN can be your Poweur ID. There is no protocol path without a resolvable name.

## What can you do with a Poweur ID?

Identities are used for **end-to-end encrypted, cryptographically verified messaging**: signing and sending messages that any recipient can verify came from you, without trusting any server with your private key.

The same name is also a **home filesystem** on your relay (WebDAV, public/shared/private trees — see [File storage](/files/storage-model)), and is designed to grow into publishing, payments, and third-party sign-in without a separate account system.

Planned and in-progress capabilities include:

- **Messaging** — active
- **Files & WebDAV** — active (per-identity home on the relay)
- **Publishing** — signed content under your identity (see also [identity websites](/files/webdav#public-web-serving-pub) for file sharing; full sites are a later epic)
- **Receiving payments** — payment address advertisement via capability records
- **Authentication** — prove identity to third-party services without passwords

## How it works in one paragraph

You control a domain name. Your private key lives on your device (passkey / secure storage) — it never leaves in plaintext. Your public keys are published in a signed identity document at `/.well-known/poweur/id.json` (and optionally in DNS `TXT`). When you send a message, your client signs and encrypts it. The recipient (or their relay) resolves your identity over HTTPS or DNS, verifies the signature, and delivers the ciphertext. No central server holds your private key.

## System components

- **Relay (`apps/api`)** — Go server that routes and verifies messages, hosts identity documents and per-identity file trees (`POWEUR_DATA`), and optionally writes DNS records for self-hosted registration. Holds no identity private keys.
- **Web client (`apps/web`)** — browser SPA for hosted identity creation, messaging, and files.
- **CLI (`apps/cli`)** — scriptable client for developers, bots, and agents.
- **Mobile apps (`apps/ios`, `apps/android`)** — native clients; the device is the user's cryptographic vault.
- **Infrastructure (`apps/infra`)** — Terraform for deploying a relay (e.g. on Hetzner Cloud).

## Next steps

- [Protocol Overview](/protocol/overview) — design principles and architecture
- [Web Identity](/protocol/web-identity) — `/.well-known/poweur/` discovery (primary)
- [Identity Model](/protocol/identity-model) — how identities and keys work
- [DNS Records](/protocol/dns-records) — DNS publication format (fallback / self-host)
- [File storage](/files/storage-model) — per-identity home filesystem
- [Relay API Reference](/relay/api-reference) — HTTP API endpoints
