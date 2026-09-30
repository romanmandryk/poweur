# Poweur

**One open ID to sign in, message, share data and collaborate.**

Almost everything you do online is one of three things: proving who you are, talking to someone,
and working on shared data. Poweur makes those three open, interoperable and yours. A Poweur ID
is an internet name such as `alice.poweur.net` or `alice.example.com`, backed by keys that only
your devices hold. It is your identity, your inbox and your drive in one, and you can use it on
our servers, on your own domain or on your own relay.

- 🌐 **Website:** [poweur.org](https://www.poweur.org)
- 🪪 **Claim your ID:** [poweur.net/app](https://poweur.net/app/)
- 📖 **Documentation:** [poweur.org/docs](https://www.poweur.org/docs/)
- 🖥️ **Run your own relay:** the [self-hosting guide](apps/docs/docs/relay/self-hosting.md)
- 🔐 **Report a vulnerability:** [SECURITY.md](SECURITY.md)

Poweur is open source and **pre-1.0**: it runs in production at poweur.net, but interfaces and
storage formats can still change. A Poweur ID proves control of a name and its keys; it does not by
itself prove a legal identity or a unique human.

---

The rest of this page is for people who build, run or contribute to Poweur.

## What is here

| Path | What it is | License |
|------|------------|---------|
| [`apps/api`](apps/api) | The relay: a Go service that hosts IDs, verifies and routes messages, keeps encrypted drives and serves the web app | AGPL-3.0 |
| [`apps/web`](apps/web) | The web app (React + Tailwind), served by the relay at `/app/` | AGPL-3.0 |
| [`apps/mobile`](apps/mobile) | iOS and Android shell (Capacitor) around the web app | AGPL-3.0 |
| [`apps/oauth`](apps/oauth) | OAuth 2.0 / OpenID Connect / IndieAuth bridge: "Sign in with Poweur" for any app | AGPL-3.0 |
| [`apps/cli`](apps/cli) | The `poweur` command line: scripting, bots, agents, operators | Apache-2.0 |
| [`packages/client-ts`](packages/client-ts) | `@poweur/client`, the TypeScript SDK for browsers, Node, Bun and Deno, with a matching CLI | Apache-2.0 |
| [`packages/identity`](packages/identity) | Canonical wire formats, signatures, grants and resolution, with the conformance vectors | Apache-2.0 |
| [`conventions`](conventions) | Poweur Convention Proposals and JSON schemas for typed messages and app data | Apache-2.0 |
| [`apps/docs`](apps/docs) | The documentation site (Docusaurus): protocol, relay, clients, security | Apache-2.0 |
| [`apps/site`](apps/site) | The poweur.org website | AGPL-3.0 |
| [`apps/integration`](apps/integration) | End-to-end tests with real in-process relays, the CLI, fake DNS and cross-relay scenarios | AGPL-3.0 |
| [`deploy`](deploy) | Ansible, Caddy, monitoring and backup material for running a relay, see [`deploy/README.md`](deploy/README.md) | AGPL-3.0 |
| [`design/brand`](design/brand) | Brand tokens, icon and logo generators | see [TRADEMARKS.md](TRADEMARKS.md) |
| [`epics`](epics) | The roadmap and task tracker | |

A directory's own `LICENSE` file wins over the root one; see [License](#license).

## How it fits together

```text
Poweur ID  alice.poweur.net
   ├── resolves web-first from https://alice.poweur.net/.well-known/poweur/id.json (DNS TXT fallback)
   ├── receives signed, end-to-end encrypted messages, relay to relay
   ├── owns an encrypted drive: folders shared with any ID, group or agent
   └── signs in to apps through the OAuth/OIDC bridge or "Sign in with Poweur"
```

Clients hold the keys and sign; relays verify, route and store ciphertext, and never see identity
private keys or file contents. Start with the [protocol overview](apps/docs/docs/protocol/overview.md),
the [identity model](apps/docs/docs/protocol/identity-model.md) and the
[security model](apps/docs/docs/security/model.md).

## Run a relay

The [self-hosting guide](apps/docs/docs/relay/self-hosting.md) takes you from a fresh Linux server to
a relay with TLS for every hosted name, and the
[configuration reference](apps/docs/docs/relay/configuration.md) lists every setting.
[`docker-compose.prod.yml`](docker-compose.prod.yml) shows a production shape and
[`deploy/`](deploy/README.md) holds the Ansible playbook, the monitoring stack and the backup job. To
use your own domain as an ID without running anything, see
[web identity](apps/docs/docs/protocol/web-identity.md).

## Develop

You need Go 1.25 or newer, Node.js 18 or newer and pnpm 9 or newer.

```bash
pnpm install
```

Run a relay (copy the settings from [`apps/api/README.md`](apps/api/README.md) into `apps/api/.env` first):

```bash
cd apps/api && go run .
```

Build the web app and let the relay serve it at `/app/` by pointing `WEB_STATIC_DIR` at
`apps/web/dist`, or run it with hot reload:

```bash
pnpm web        # build to apps/web/dist
pnpm web:dev    # Vite dev server
```

Try the CLI:

```bash
cd apps/cli && go run . --help
```

Tests:

```bash
go test ./packages/identity/... ./apps/api/... ./apps/cli/...   # Go units and protocol vectors
(cd apps/integration && go test ./... -count=1)                 # real relays, CLI journeys
pnpm client:test && pnpm web:test                               # TypeScript SDK and web app
```

[`AGENTS.md`](AGENTS.md) describes the conformance, vendoring and version-bump rules to follow before
changing code. It applies to people as much as to coding agents.

## Contribute

Start with [CONTRIBUTING.md](CONTRIBUTING.md). The roadmap lives in [`epics/`](epics/README.md): each
epic has a progress table and self-contained tasks, and is the issue tracker until issues are filed.
Protocol changes land with documentation in `apps/docs/docs/` and tests in `apps/integration`.

Good places to begin: the [integration epics](epics/integrations/INT-000-overview.md) (plugins and
connectors that live in other projects), the [web app UX](epics/EPIC-015-web-app-ux.md) and the
[TypeScript SDK](epics/EPIC-017-typescript-client-sdk.md).

## Status

Working today: hosted and own-domain IDs, end-to-end encrypted messaging with contacts and spam
control, an encrypted drive with sharing (storage v2, deployed on poweur.net), sign-in for apps with
an OAuth/OIDC/IndieAuth bridge, passkey-protected keys with device enrollment and recovery kits, and
the web, mobile, Go CLI and TypeScript clients. Still in progress: desktop and mobile polish, push
notifications, group and real-time collaboration, the email bridge and identity websites. Check an
epic's progress table before relying on a planned capability.

## License

Poweur uses two licenses. The services are copyleft, and the pieces other software builds on are
permissive:

| Part | License |
|------|---------|
| Relay (`apps/api`), OAuth/OIDC bridge (`apps/oauth`), web app (`apps/web`), mobile shell (`apps/mobile`), integration tests, deploy and everything else not listed below | [AGPL-3.0-only](LICENSE) |
| Identity package (`packages/identity`), TypeScript SDK and its CLI (`packages/client-ts`), Go CLI (`apps/cli`), conventions and schemas (`conventions`), docs (`apps/docs`) | [Apache-2.0](packages/identity/LICENSE) (a `LICENSE` file in each directory) |

If you run a modified relay, bridge or web app as a network service, the AGPL requires you to offer
your users its source. Building an app, bot, agent or client on the SDK, the identity package or the
protocol carries no such obligation, whether your code is open or closed.

The Poweur name and logo are not covered by either license; see [TRADEMARKS.md](TRADEMARKS.md).
