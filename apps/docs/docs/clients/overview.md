---
id: overview
sidebar_position: 1
title: Clients Overview
---

# Clients Overview

Every Poweur client speaks the same open protocol, so one identity works in all of them at
once. People use the web and mobile apps day to day, and the CLI on their desktop; AI agents,
scripts and projects use the CLI or the SDK.

| Client | Path | For |
|--------|------|-----|
| Web app | `apps/web` | People, in any browser. Served by every relay at `/app/` |
| iOS and Android apps | `apps/mobile` | People, with native key storage and several relays in one app |
| Go CLI | `apps/cli` | You on your desktop, AI agents, projects and CI, operators |
| TypeScript SDK and CLI | `packages/client-ts` | Apps and agents in the browser, Node, Bun and Deno |
| Relay HTTP API | [API reference](/relay/api-reference) | Anything that can sign with Ed25519 |

## Web app

The web app is the main client for people. It is served by every relay at `/app/`
(`https://poweur.net/app/` for hosted poweur.net IDs) and has five areas: **Messages**,
**Contacts**, **Files**, **Apps** and **Settings**. See the [walkthrough](/web/walkthrough).

- **Creating an ID.** Pick a name on the relay's domain; keys are generated in the browser and
  protected with a passkey. See [Claim your ID](/web/claim-your-id).
- **Messaging.** Messages are signed and end-to-end encrypted on the device, delivered to the
  recipient's relay, held in its durable inbox and pushed to connected devices in real time
  (SSE). Your conversation history is stored encrypted on your device.
- **Contacts, requests and inbox policy**, including anonymous messages with proof of work.
- **Files and sharing.** Browse your home, upload, share folders with people and groups,
  create links, and accept folders others share with you.
- **Sign-in approvals.** Approve "Sign in with Poweur" requests from other apps and sites.
- **Several identities** in one browser, and **devices and recovery**: add a device by code
  or QR, remove one, create a recovery kit.

## Mobile apps

The iOS and Android apps are **the same web app in a native [Capacitor](https://capacitorjs.com)
shell**, so every feature matches the web. The shell adds what a browser cannot:

- **Native key custody.** Identity keys are protected by the platform keystore (Secure
  Enclave / Keychain on iOS, Android Keystore) instead of a passkey bound to one website, so
  one app works with identities on any relay, self-hosted ones included.
- **Many relays in one app.** A browser keeps each relay's app separate (one origin each); the
  app holds identities from different relays side by side.
Push notifications, background sync, moving an existing ID onto the phone and store packaging
are in progress
([EPIC-019](https://github.com/romanmandryk/poweur/blob/master/epics/EPIC-019-mobile-app-capacitor.md)).

## CLI

The CLI (`poweur`, in `apps/cli`) is your Poweur ID in the terminal: every feature of the
apps, as commands that compose with the rest of your desktop. It is as much for people and
AI agents as for bots.

**For you, on your desktop.** Keep a project folder in sync with your home, share it with a
colleague, message them when a build finishes, or mount your home as a drive, without leaving
the terminal:

```bash
poweur sync run ~/projects/site --path shared/site      # two-way sync with your home
poweur share add shared/site --with=bob.poweur.net --perm=rw
poweur send bob.poweur.net "Preview is up: shared/site/dist"
poweur dav mount                                         # mount your home in Finder or Files
```

**For LLMs and coding agents.** An assistant that can run shell commands, such as Claude
Code, Codex or Cursor, can use Poweur through the CLI with no integration work. Every command
takes `--json`, so it can read your inbox, answer a teammate, fetch the files someone shared
and report back as messages, all with keys that never leave your machine. Give the agent
**its own ID** (`poweur identity create`) and share just the folders it needs; you see
everything it does and can revoke it with `poweur share revoke`.

```bash
poweur inbox --json --decrypt           # what arrived, as structured data, with each message's text in "body"
poweur listen --json --decrypt          # stream new messages as they come
poweur send alice.poweur.net "Tests pass on main ✅" --type=ci.status
```

**For projects and automation.** In CI, cron jobs and servers the CLI is a scriptable
endpoint: post release notes to a group, collect uploads with a file request
(`poweur share request add`), keep a folder mirrored, or run a small bot that answers typed
messages.

**For operators.** Check a relay (`poweur relay status`), look up identities
(`poweur identity lookup`), manage keys and devices (`poweur key …`), and handle blocks and
abuse reports.

Keys live in `~/.poweur/keys` (or the OS keychain) and the configured relay is your home
relay. See the [CLI reference](/clients/cli-reference) for every command; the
[TypeScript SDK](#javascript--typescript) ships the same CLI for Node, Bun and Deno.

## Direct API Access

Advanced users and automated systems can interact directly with the [Relay HTTP API](/relay/api-reference) without using a client. All authentication in the API is cryptographic (challenge–response with the identity's key pair), so any HTTP client that can manage Ed25519 keys can participate in the protocol.

## JavaScript / TypeScript

`@poweur/client` is the protocol as a TypeScript package — identity,
messaging, files, sync, shares and proof-of-work — for the browser, Node ≥18,
Bun and Deno. It also ships a `poweur` CLI that reads and writes the same
`~/.poweur` tree as the Go one, so the two are interchangeable against a single
identity.

Every JavaScript-ecosystem integration (agent gateways, an MCP server, n8n and
Node-RED nodes) is a thin adapter over it rather than a re-implementation of
canonical signing. See the [JavaScript / TypeScript SDK](/clients/js-sdk).

## Related

- [CLI Reference](/clients/cli-reference)
- [API Reference](/relay/api-reference)
- [Identity Model](/protocol/identity-model)
- [Interoperability](/protocol/interoperability)
- [Security Model](/security/model)
