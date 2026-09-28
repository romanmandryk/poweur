---
id: intro
slug: /
sidebar_position: 1
title: Introduction
---

# Poweur documentation

Poweur is an **open-source identity, messaging and data-sharing layer**. A Poweur ID is an
internet name, such as `alice.poweur.net` or `alice.com`, that is at once:

- **an identity**, backed by keys that stay on your devices;
- **an address** that people, apps and agents can send signed, end-to-end encrypted messages to;
- **a home** for your files, which you can share with any other ID, group or agent;
- **a login** for third-party apps, without passwords.

Anyone can run a relay, host IDs on their own domain, or build an app on the same three
primitives. The network does not depend on any one operator, including us.

:::note
A Poweur ID proves control of a name and its keys. It does not by itself prove a legal
identity or a unique human. Those can be added later as attestations without changing the ID.
:::

## Start here

| I want to… | Read |
|------------|------|
| **Get an ID and use it** | [Claim your ID](/web/claim-your-id), then the [web app walkthrough](/web/walkthrough) |
| **Run my own relay** for my family, team or community | [Self-hosting a relay](/relay/self-hosting) |
| **Use my own domain** as my ID, on any relay | [Web identity](/protocol/web-identity) and [DNS records](/protocol/dns-records) |
| **Add "Sign in with Poweur"** to my app | [Add sign-in to an app](/auth/add-sign-in), or the [OAuth/OIDC bridge](/auth/oauth-oidc-bridge) for any app that already speaks OpenID Connect |
| **Work from the terminal, or give an AI agent an ID** | [CLI](/clients/overview#cli) |
| **Build an app or an agent** | [JavaScript/TypeScript SDK](/clients/js-sdk), [CLI reference](/clients/cli-reference), [app-data conventions](/files/storage-v2) |
| **Understand the protocol** | [Protocol overview](/protocol/overview), [identity model](/protocol/identity-model), [message format](/protocol/message-format) |
| **Review the security** | [Security model](/security/model), [key management](/security/key-management) |

## What works today

Poweur is pre-1.0, and the core works end to end today:

- **Identity.** Hosted names under a relay's domain, or your own domain. Keys resolve web-first
  from `https://<id>/.well-known/poweur/id.json`, with DNS `TXT` as a fallback, and fail closed
  when the two disagree. Key rotation, export and moving between relays.
- **Messaging.** Signed, end-to-end encrypted messages between IDs on any relay. A durable
  inbox, real-time push (SSE), delivery and read receipts, threads, groups,
  expiring messages, and typed messages that apps and agents understand.
- **Contacts and spam control.** Contact requests, pinned keys, blocking, per-identity inbox
  policy, relay-level abuse limits, and opt-in anonymous messages protected by proof of work.
- **Files and sharing.** V1 has been removed on master. The encrypted drive,
  attachments and multi-device history are being restored in EPIC-020.
- **Sign-in.** "Sign in with Poweur" for apps, a
  `did:web` projection, and an OAuth 2.0 / OpenID Connect / IndieAuth bridge.
- **Devices and recovery.** Passkey-protected keys, several devices per ID, adding a device
  by code or QR, removing a lost one, and recovery kits.
- **Clients.** The web app, iOS and Android apps (the web app in a native shell), a Go CLI,
  and `@poweur/client` for browsers, Node, Bun and Deno.

Identity websites, the email bridge, collaborative spaces, the app platform and payments are
on the [roadmap](https://github.com/romanmandryk/poweur/tree/master/epics).

## How it works, in one paragraph

Your private keys live on your devices and never leave them in plaintext. Your public keys are
published in a signed identity document at `/.well-known/poweur/id.json` (and optionally in
DNS). To send a message, your client signs and encrypts it and hands it to the recipient's
relay. That relay looks up your keys the same way a browser finds a website, verifies the
signature, applies the recipient's inbox policy, stores the ciphertext and pushes it to their
devices. Relays route, store and enforce policy; they never hold identity private keys. See
<a href="../architecture.html" data-noBrokenLinkCheck={true}>Architecture</a> for diagrams.

## Components

All in [one repository](https://github.com/romanmandryk/poweur):

| Component | Path | What it is |
|-----------|------|------------|
| Relay | `apps/api` | Go server: identity hosting, message verification and routing, durable inbox, public/system files during the storage-v2 transition. Serves the web app at `/app/`. |
| Web app | `apps/web` | React client for creating an ID, messaging, contacts, files, sharing, sign-in approval, devices and recovery. |
| Mobile apps | `apps/mobile` | The web app in a Capacitor shell for iOS and Android, with native key storage. |
| Go CLI | `apps/cli` | Your ID in the terminal: sync and share project folders, message from scripts, and let AI coding agents use Poweur. Also for CI, bots and operators. |
| TypeScript SDK | `packages/client-ts` | `@poweur/client` and a `poweur` CLI that matches the Go one command for command. |
| Identity package | `packages/identity` | Canonical wire formats, signatures, grants and resolution in Go, with conformance vectors. |
| OAuth/OIDC bridge | `apps/oauth` | Lets any OpenID Connect or IndieAuth app accept Poweur IDs. |
| Deployment | `deploy/` | Docker Compose, Caddy, Ansible and the observability stack behind poweur.net. |
