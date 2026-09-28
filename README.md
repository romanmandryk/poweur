# Poweur

**One name for identity, messages, files, apps, and agents.**

Poweur is an open protocol and self-hostable network built around a simple idea: a stable,
human-readable internet name should be useful everywhere. A Poweur ID such as
`alice.example.com` or `alice.poweur.net` is simultaneously a verifiable identity, a messaging
address, a sign-in credential, and a file-sharing target.

Poweur will provide hosted IDs and services, but the network is not meant to depend on one
operator. Anyone can run a relay, host identities, build a compatible client, or create an app
on the same identity, messaging, and storage primitives.

> [!NOTE]
> Poweur proves control of a name and its cryptographic keys. It does not by itself prove a
> legal identity, a unique human, or a real-world reputation. Those claims can be added through
> explicit attestations without changing the underlying ID.

## TL;DR

- **Your ID is an internet name.** Bring a domain you control or claim a hosted name under a
  relay's wildcard domain.
- **Your keys stay with you.** Clients sign, messages are encrypted end-to-end, and relays do
  not hold identity private keys.
- **The same ID works across services.** Use it for end-to-end encrypted messaging, contacts,
  file sharing, app sign-in, and scoped access to your data.
- **Your ID has a home.** Storage v2 is rebuilding that home as an encrypted drive
  with node sharing and explicit public/system files (EPIC-020).
- **Apps can compose instead of integrating pairwise.** Apps and agents can exchange typed
  messages and work on shared files using published conventions rather than bespoke APIs.
- **Hosting is a choice, not a boundary.** Poweur can operate a convenient hosted service while
  independent relays and self-hosted domains remain first-class participants.
- **The working substrate exists today.** Identity resolution, hosted registration, encrypted
  messaging, contacts and anti-spam controls, sign-in, web and
  CLI clients, and recovery foundations are implemented. Poweur is still pre-1.0; product UX,
  integrations, active websites, agent workflows, and storage v2 remain roadmap work.

Start with the [documentation](apps/docs/docs/intro.md), browse the
[roadmap](epics/README.md), or see [Contributing](CONTRIBUTING.md).

## The broader goal: an open, composable “everything app”

Poweur is not trying to put every feature into one closed super-app. It aims to make many apps
feel like one coherent environment because they share a few open primitives:

```text
Poweur ID
   ├── proves control of a stable name
   ├── receives signed, encrypted messages
   ├── owns a syncable file home
   ├── grants apps and people scoped access
   └── gives agents an address and shared workspace
```

An app can authenticate a user and, with consent, store its data in that user's home. Another
authorized app can read the same open convention. A person can share a folder with another
person, a group, or an agent. Typed messages and file-change events can coordinate work across
different vendors and independently hosted relays.

The intended result is an open distributed application layer for communication, publishing,
collaboration, payments, personal software, and agent workflows—composable like the web, but
with identity, reachability, trust controls, and user-controlled data built in.

## What works today

### Identity and discovery

- DNS names are canonical Poweur IDs.
- Public keys and relay information resolve web-first from
  `https://<id>/.well-known/poweur/id.json`, with DNS TXT fallback.
- Conflicting web and DNS identity material fails closed.
- Relays can offer hosted wildcard identities without one DNS write per user.
- Domain owners can self-host, rotate keys, export an identity, and move between relays.

See [Identity model](apps/docs/docs/protocol/identity-model.md),
[web identity](apps/docs/docs/protocol/web-identity.md), and
[EPIC-001](epics/EPIC-001-web-identity.md).

### Messaging, contacts, and trust

- Signed, end-to-end encrypted relay-to-relay messaging.
- Durable inbox spool; encrypted multi-device history is being restored on storage v2.
- Push notifications over SSE, delivery acknowledgements, expiry, threads, typed messages,
  and group messaging. Attachments are being restored on storage v2.
- Contact requests, pinned keys, blocking, inbox policies, relay-level abuse pressure, and
  opt-in anonymous messages protected by proof of work.

See [Messaging](apps/docs/docs/protocol/message-format.md),
[contacts and trust](apps/docs/docs/trust/contacts.md),
[EPIC-007](epics/EPIC-007-contacts-trust-antispam.md), and
[EPIC-009](epics/EPIC-009-messaging-upgrades.md).

### Files, sync, and sharing

V1 WebDAV, sync, path shares, links and the Files UI were removed on master.
Storage v2 is in progress: encrypted nodes/chunks, filesystem and S3 providers,
atomic replace/append commits and node shares. The temporary system-file API
preserves public profiles and relay settings while that drive is built.

See the [storage v2 specification](apps/docs/docs/files/storage-v2.md) and
[EPIC-020](epics/EPIC-020-storage-protocol-v2.md). **Do not deploy master until the
baseline and system-only production migration have passed.** Files UI, direct
shares, sync and the reference apps return after the baseline.

### Sign-in, clients, and recovery

- Stateless “Sign in with Poweur” challenge-response and verifier SDKs.
- Optional, consented, path-scoped grants to an app's own namespace.
- A mechanical `did:web` projection and a reference relying-party guestbook.
- Go CLI, browser app, and `@poweur/client` for browser, Node, Bun, and Deno.
- Seed-derived keys, multiple device enrollments, recovery kits, device removal, and a
  device-to-device enrollment ceremony.
- A Capacitor mobile shell with native key-custody seams and multi-relay support.

See [Sign-in](apps/docs/docs/auth/sign-in.md),
[client overview](apps/docs/docs/clients/overview.md),
[EPIC-008](epics/EPIC-008-sign-in.md), and
[EPIC-011](epics/EPIC-011-key-management-recovery.md).

## Roadmap

The detailed roadmap and task status live in [`epics/`](epics/README.md). Broadly, the work
moves from a usable substrate toward an interoperable app and agent ecosystem.

### 1. Finish the everyday product

Complete the host-aware web experience, desktop/tablet layout, conversation history UX, mobile
packaging and background delivery, and the remaining share-offer/mount flow.

- [Web app UX](epics/EPIC-015-web-app-ux.md)
- [Mobile app](epics/EPIC-019-mobile-app-capacitor.md)
- [Sharing and recipient mounts](epics/EPIC-005-sharing-acl.md)
- [Key management and recovery](epics/EPIC-011-key-management-recovery.md)

### 2. Make every identity a place to publish and interact

Let identities publish real websites and content from their homes, receive contact-form
messages under their inbox policy, advertise capabilities, and later expose payment methods or
recipient-priced sender challenges.

- [Identity websites](epics/EPIC-012-identity-websites.md)
- [Capabilities and conventions](epics/EPIC-006-poweur-sys-conventions.md)
- [Anonymous messaging](epics/EPIC-014-anonymous-messaging-challenges.md)
- [Pay-to-send and proof-of-work v2](epics/EPIC-016-pow-v2-and-pay-to-send.md)
- [Payment integrations](epics/integrations/INT-002-payments.md)

### 3. Turn the home into an app platform

Publish stable client packages, make app data portable by convention, and let third-party apps
use a Poweur ID for both authentication and narrowly scoped access to user-controlled storage.

- [TypeScript client SDK](epics/EPIC-017-typescript-client-sdk.md)
- [Sign in with Poweur](epics/EPIC-008-sign-in.md)
- [OAuth/OIDC and IndieAuth bridge](epics/EPIC-021-oauth-oidc-indieauth-bridge.md)
- [Integration program and bridge adoption](epics/integrations/INT-000-overview.md)
- [Identity and verification integrations](epics/integrations/INT-001-identity-verification.md)
- [Collaboration-tool integrations](epics/integrations/INT-004-collaboration-tools.md)

### 4. Build shared agent workflows

Give agents their own verifiable IDs and accountable operators, then use messages, scoped
grants, shared folders, and file-change events as a neutral handoff layer between agents and
people—even when their tools and hosting providers differ.

- [Agents and automation](epics/EPIC-010-agents-automation.md)
- [AI and agent-framework integrations](epics/integrations/INT-003-ai-agents.md)
- [Agent control-plane integrations](epics/integrations/INT-005-agent-control-planes.md)

### 5. Harden the distributed network

Separate identity/messaging control from storage services, add content-addressed chunking and
relay-blind encryption, improve delegation and share capabilities, and continue production and
federation hardening.

- [Storage protocol v2](epics/EPIC-020-storage-protocol-v2.md)
- [Production deployment and observability](epics/EPIC-013-prod-deployment-observability.md)
- [Relay registration, persistence, export, and migration](epics/EPIC-002-relay-registration-and-persistence.md)

Adoption work is tracked separately in the
[integration epics](epics/integrations/INT-000-overview.md), because those deliverables often
belong in upstream projects rather than this repository.

## Architecture

- **Relay (`apps/api`)** — Go HTTP service for identity hosting, message verification/routing,
  durable delivery, file homes, shares, and well-known endpoints.
- **Identity package (`packages/identity`)** — canonical Go wire formats, signatures, grants,
  policies, resolution, and conformance-vector generation.
- **Go CLI (`apps/cli`)** — complete scriptable client for people, operators, bots, and tests.
- **TypeScript SDK (`packages/client-ts`)** — the corresponding browser/Node implementation
  and interoperable `poweur` CLI.
- **Web app (`apps/web`)** — React + Tailwind client for identity creation, messaging, contacts,
  settings, sign-in approval, devices, and recovery.
- **Mobile (`apps/mobile`)** — Capacitor shell over the web client with native iOS and Android
  key-custody bridges.
- **Integration suite (`apps/integration`)** — real in-process relays, CLI journeys, fake DNS,
  restart tests, and cross-relay coverage.
- **Reference apps** — Tasks and Guestbook were removed with v1 storage; v2 headless scenarios are tracked in EPIC-031.
- **Docs and conventions (`apps/docs`, `conventions`)** — protocol documentation, schemas, and
  the Poweur Convention Proposal process.
- **Deployment (`deploy`)** — container, Caddy, Ansible, telemetry, dashboards, and operational
  material for a production relay.

## Development

### Prerequisites

- Go 1.25 or newer
- Node.js 18 or newer
- pnpm 9 or newer

Install JavaScript dependencies:

```bash
pnpm install
```

Run the Go relay:

```bash
cd apps/api
# Create .env first; see apps/api/README.md for the required relay address and local settings.
go run .
```

Run the Go CLI:

```bash
cd apps/cli
go run . --help
```

Build the web app with `pnpm web`, then serve it from the relay by setting `WEB_STATIC_DIR` to
`apps/web/dist` in the relay's environment and open `/app/`. `pnpm web:dev` runs Vite with hot
reload while working on the UI.

See [`apps/api/README.md`](apps/api/README.md) for a local relay setup.

Build and test the TypeScript client:

```bash
pnpm client:build
pnpm client:test
pnpm client:typecheck
```

Run the Go protocol and integration suites:

```bash
go test ./packages/identity/... ./apps/api/... ./apps/cli/...
cd apps/integration && go test ./... -count=1
```

See [AGENTS.md](AGENTS.md) for testing, protocol-conformance, vendoring, versioning, and epic
tracking requirements before changing code.

## Project status

Poweur is pre-1.0 and under active development. The protocol and main product paths are backed
by unit, conformance, live-relay, cross-language, browser, and end-to-end tests, but APIs and
storage formats may still evolve. Check each epic's progress table before relying on a planned
capability.

The code is developed in public with the goal of permitting independent clients, relays, apps,
and hosted services. See [License](#license).

## Contributing

Roadmap epics are the issue tracker until corresponding GitHub issues exist. Start with
[Contributing](CONTRIBUTING.md), choose an unblocked task from the [epic index](epics/README.md),
and keep protocol changes documented and covered by integration tests.

## License

Poweur uses two licenses. The services are copyleft, and the pieces other software builds on are
permissive:

| Part | License |
|------|---------|
| Relay (`apps/api`), OAuth/OIDC bridge (`apps/oauth`), web app (`apps/web`), mobile shell (`apps/mobile`), integration tests, deploy and everything else not listed below | [AGPL-3.0-only](LICENSE) |
| Identity package (`packages/identity`), TypeScript SDK and its CLI (`packages/client-ts`), Go CLI (`apps/cli`), conventions and schemas (`conventions`), docs (`apps/docs`) | [Apache-2.0](packages/identity/LICENSE) (a `LICENSE` file in each directory) |

A directory's own `LICENSE` file wins over the root one. If you run a modified relay, bridge or
web app as a network service, the AGPL requires you to offer your users its source. Building an
app, bot, agent or client on the SDK, the identity package or the protocol carries no such
obligation, whether your code is open or closed.

The Poweur name and logo are not covered by either license; see [TRADEMARKS.md](TRADEMARKS.md).

