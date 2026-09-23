# AGENTS.md — guidance for coding agents

This repo is a **pnpm + Go workspace** for Poweur: DNS/web identity, relay messaging, CLI, and web client. Roadmap work lives in [`epics/`](epics/README.md). Treat epic markdown as the issue tracker until GitHub issues exist.

## Before you change code

1. Read the relevant epic (`epics/EPIC-0NN-*.md`) and mark/check progress there when you finish or defer work.
2. Prefer extending existing packages (`packages/identity`, `apps/api`, `apps/cli`, `apps/web`) over new top-level apps.
3. Do not invent parallel crypto or message formats — match canonical strings and wire types already used by the relay and CLI.
4. Do not commit secrets (`.env`, DNS tokens, private keys). `.env` examples stay local.
5. **Bump a patch version** only when the change implements or fixes shipped behavior (see [Version bumps](#version-bumps-required)). Test-only and config-only changes keep the current version.

## Testing requirements (non-negotiable)

### Unit tests

- Every new package or non-trivial function gets `_test.go` (Go) coverage for happy path + failure modes.
- Crypto, identity documents, name policy, storage path sanitization, and resolvers **must** have unit tests.
- Prefer table-driven tests; keep fakes injectable (DNS `LookupTXT`, HTTP clients).

```bash
# Shared identity package
go test ./packages/identity/...

# Relay
go test ./apps/api/...

# CLI
go test ./apps/cli/...

# OAuth/OIDC bridge (EPIC-022)
go test ./apps/oauth/...
```

### Integration tests (CLI / Go)

Any change that touches **relay HTTP**, **CLI send/inbox/register**, **identity resolution**, or **cross-relay forwarding** needs coverage under `apps/integration/`.

That suite runs **real in-process relays** (`httptest`) + CLI + fake DNS zone — not mocks of the protocol. Add or extend tests there when behavior is end-to-end.

```bash
cd apps/integration && go test ./... -count=1
```

Hosted-identity path (web identity, no DNS token):

```bash
cd apps/integration && go test -run TestINT_HOSTED -count=1 -v
```

### TypeScript client tests (`packages/client-ts`)

`@poweur/client` is the published TS/JS implementation of the protocol. It has
Vitest unit tests **plus** suites that spawn a real Go relay (`go run`), same
spirit as the CLI integration tests, **plus** a Go↔TS interop suite that drives
the real `poweur` binary as a subprocess against a shared `~/.poweur` tree.

```bash
pnpm client:build
pnpm client:test        # unit + conformance + live-relay + Go↔TS interop
pnpm client:typecheck
```

**Protocol changes must regenerate the conformance vectors in the same change
set.** Go stays canonical; the TS client conforms to it via fixtures generated
by three Go tests, each living beside the code it pins:

| Generator | Pins |
|-----------|------|
| `packages/identity/vectors_test.go` | identity documents, grants, groups, PoW, names, `poweur-sys` docs |
| `apps/api/internal/crypto/vectors_test.go` | every canonical signing string |
| `apps/cli/internal/crypto/vectors_test.go` | message encryption (X25519 + HKDF + ChaCha20-Poly1305) |

```bash
pnpm vectors    # regenerate all of them into packages/identity/testdata/vectors/
```

A canonical string changed in Go without a matching TypeScript change turns CI
red. Do not "fix" that by editing the fixture — change `packages/client-ts` to
match Go, or reconsider the protocol change.

### Web client tests (Vitest + Playwright)

`apps/web` is the React + Tailwind client (Vite, TypeScript), served by the relay at `/app/`
and staged into the Capacitor shell. Its live-relay and e2e suites spawn a **real Go relay**
(`go run` with `POWEUR_DATA` + `HOSTED_DOMAINS` + `WEB_STATIC_DIR=apps/web/dist`), same spirit
as CLI integration tests.

```bash
pnpm install
pnpm --filter @poweur/web exec playwright install chromium   # once, for e2e
pnpm web:typecheck
pnpm web:test      # Vitest: screens, components, stores, lib + live-relay
pnpm web           # vite build → apps/web/dist (what the relay and the shell serve)
pnpm web:test:e2e  # builds, then Playwright against /app/ on a real relay
```

The app bundles `@poweur/client` from the workspace, so there is no vendored copy to refresh:
a change to `packages/client-ts` reaches the browser on the next web build. The protocol itself
lives in `@poweur/client` and is tested there; `apps/web` tests the screens and the browser
key-custody seam (`src/lib/`).

If a feature cannot be asserted in unit tests alone (Host routing, restart/`POWEUR_DATA`, E2E encrypt/send/inbox), write an integration test.

### After substantive changes

Run the slice you touched **and** `apps/integration` (and `apps/web` tests if the SPA changed) before declaring done. Fix failures; do not skip with `-short` to hide them.

## Architecture notes agents should respect

| Concern | Rule |
|---------|------|
| Identity discovery | Web-first: `/.well-known/poweur/id.json`, then DNS TXT; key mismatch → fail closed (`packages/identity`) |
| Hosted registration | No DNS token; signed `identity_document`; `HOSTED_DOMAINS` + `POWEUR_DATA` |
| Relay trust | Relay never holds identity private keys; clients sign documents and messages |
| Sessions | Short-lived; memory-only until EPIC-009 |
| Storage | Durable identity docs under `POWEUR_DATA/identities/.../poweur-sys/public/id.json` |
| SSRF | Well-known fetch: no redirects, size/time caps, no private IPs unless test flag |
| Two implementations | Go is canonical; `packages/client-ts` conforms via generated vectors |
| Shared CLI state | Both CLIs read/write `~/.poweur` (`config.toml`, `keys/`, `sessions/`, `pending/`) — never fork the format |

## Epics and deferrals

- Update checkboxes (`[x]` / `[ ]`) and the **Progress** table in the epic when shipping.
- If you defer work, say so in the epic (**open** / **Deferred**) and name the follow-up epic if any (e.g. inbox durability → EPIC-009; key rotation → EPIC-001 E01-T5 + EPIC-011).
- Do not silently drop acceptance criteria.

## Version bumps (required)

When a change **implements or fixes product behavior** in a shippable package,
bump that package's **patch** version in the **same change set**. Otherwise
Settings → About and `poweur --version` still print the previous number, and
two builds with different behavior share one version.

Do **not** bump for tests, fixtures, config, docs, or tooling that leave the
shipped code's behavior unchanged. Bump only the packages whose implementation
you touched; a protocol change that lands in Go + TS + web bumps all three.

Go modules do **not** store this module's semver in `go.mod` (that file is the
module path and its *dependencies*). Do not invent a version comment there —
bump the `Version` constant instead.

| Package | Where to bump | Also stamp |
|---------|---------------|------------|
| Relay (`apps/api`) | `apps/api/internal/buildinfo.Version` | `BUILD_TIME` / `VERSION_HASH` env or VCS info at build; `GET /` exposes them |
| Go CLI (`apps/cli`) | `apps/cli/internal/buildinfo.Version` | same; printed by `poweur version` / `--version` / `-v` |
| `@poweur/client` | `packages/client-ts/package.json` **and** `SDK_VERSION` / `SDK_BUILD_TIME` in `src/index.ts` | UTC `YYYY-MM-DD HH:MM` |
| Web app | `apps/web/package.json` **and** `apps/web/src/build-info.ts` | `APP_VERSION` / `APP_BUILD_TIME` |
| OAuth bridge (`apps/oauth`) | `apps/oauth/bridge.Version` (`bridge/doc.go`) | `VERSION_HASH` / `BUILD_TIME` baked into the image; `poweur-oauth version` and `GET /health` print them |
| Mobile shell | `apps/mobile/package.json` | native store versions (Xcode / Gradle) only when the shell itself changed |

Default bump is **patch**. Minor/major is for breaking protocol or public API
changes. `VERSION` on the relay is the semver override for tests — never pass a
git sha as `VERSION` (that belongs in `VERSION_HASH`).

Shared code counts for every package that ships it: a `packages/identity`
change that alters what the relay, CLI or bridge does bumps each of them.

**What deploys when.** A push to `master` runs **Deploy** (relay image with the
web app, plus infra config) every time, and **Deploy OAuth bridge**
(`.github/workflows/deploy-oauth.yml`) only when `apps/oauth/**`,
`packages/identity/**` or `go.work` change. Both check that `/health` reports
the pushed commit's `versionHash`. Runbook: [`deploy/OPS.md`](deploy/OPS.md).

## Style

- Match existing Go and JS style; minimal diffs; no drive-by refactors.
- Specs that change the protocol land in `apps/docs/docs/` in the same change set.
- PR-oriented commits when the user asks: focus on why; no secrets.

## Quick map

```
apps/api          Go relay
apps/cli          Go CLI
apps/web          React + Tailwind client (served at /app/, wrapped by apps/mobile)
apps/oauth        OAuth 2.0 / OIDC / IndieAuth bridge (EPIC-022), a separate service
apps/integration  In-process E2E tests
packages/identity Shared identity document + resolver (Go, canonical)
packages/client-ts @poweur/client — TS/JS SDK + `poweur` CLI (conforms to Go)
epics/            Roadmap / task tracking
```

## Local relay (manual)

```bash
export RELAY_ADDRESS=localhost:8080
export POWEUR_DATA=./data
export HOSTED_DOMAINS=poweur.net
export RESOLVER_ALLOW_PRIVATE=1   # only for local HTTP testing
cd apps/api && go run .
```

CLI against local HTTP:

```bash
export POWEUR_RESOLVER_SCHEME=http
export RESOLVER_ALLOW_PRIVATE=1
export POWEUR_RESOLVER_DIAL=127.0.0.1:8080
cd apps/cli && go run . identity create alice.poweur.net --hosted --relay http://127.0.0.1:8080
```
