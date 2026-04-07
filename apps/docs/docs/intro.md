---
id: intro
slug: /
sidebar_position: 1
title: Introduction
---

# What is the Eurything Protocol?

Eurything is an **open, DNS-native identity and messaging protocol**. Every participant — human, bot, or autonomous agent — is identified by a subdomain they control, such as `alice.poweur.net`. That subdomain is their globally unique, human-readable identity. The public key associated with that subdomain is their cryptographic identity.

There is no central registry. No OAuth provider. No username database. Just DNS — the internet's existing, massively distributed lookup infrastructure — repurposed as the ground truth for identity and routing.

## Why DNS-native identity?

DNS is already the internet's naming layer. Every device on the internet can resolve a DNS record. DNS is:

- **Decentralised by design** — records are controlled by the domain owner, not a platform.
- **Globally available** — any relay anywhere can look up any identity without calling home.
- **Already trusted** — decades of infrastructure exist to protect and serve DNS records reliably.
- **Human-readable** — `alice.poweur.net` is as readable as an email address.

Eurything uses DNS not just for routing (as email does) but as the **authoritative data store** for identity. An identity's public key lives in a DNS `TXT` record. Its relay address lives in an `A` or `CNAME` record on the same subdomain. Any relay that can resolve DNS can verify any message and route to any identity — without coordination, without a central service.

## What can you do with an Eurything identity?

In the MVP, identities are used for **end-to-end verified messaging**: signing and sending messages that any recipient can verify came from you, without trusting any server in the middle.

The protocol is designed to grow. Because identity is expressed in DNS, new capabilities can be advertised by adding new DNS records. The same subdomain that routes your messages today can announce payment addresses, service endpoints, or capability flags tomorrow — all without modifying the core protocol or any central registry.

Planned capabilities include:

- **Messaging** (MVP — active)
- **Publishing** — signed content under your identity
- **Receiving payments** — payment address advertisement via DNS capability records
- **Authentication** — prove identity to third-party services without passwords

## How it works in one paragraph

You own a subdomain. Your private key lives in your phone's secure enclave — it never leaves your device. Your public key is published in a DNS `TXT` record on your subdomain. When you send a message, your phone signs it with your private key. The recipient's relay looks up your subdomain in DNS to find your public key, verifies the signature, and delivers the message. No central server ever holds your private key, and any message that claims to be from you can be verified by anyone who can resolve DNS.

## System components

The Eurything system is composed of:

- **Relay (`apps/api`)** — a stateless Go server that routes, verifies, and delivers messages. Writes DNS records on behalf of new identities. Holds no private keys and no durable storage.
- **Mobile apps (`apps/ios`, `apps/android`)** — native iOS and Android clients. The app is the user's cryptographic vault. Private keys are stored in the hardware secure enclave and never leave the device.
- **CLI (`apps/cli`)** — a scriptable command-line client for developers, bots, and automated agents.
- **Infrastructure (`apps/infra`)** — Terraform configuration for deploying a relay on Hetzner Cloud.

## Next steps

- [Protocol Overview](/protocol/overview) — design principles and architecture
- [Identity Model](/protocol/identity-model) — how identities work
- [DNS Records](/protocol/dns-records) — the full DNS record format specification
- [Relay API Reference](/relay/api-reference) — HTTP API endpoints
