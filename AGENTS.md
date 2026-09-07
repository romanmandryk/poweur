# AGENTS.md — guidance for coding agents

This repo is a **pnpm + Go workspace** for Poweur: DNS/web identity, relay messaging, CLI, and web client. Roadmap work lives in [`epics/`](epics/README.md). Treat epic markdown as the issue tracker until GitHub issues exist.

## Before you change code

1. Read the relevant epic (`epics/EPIC-0NN-*.md`) and mark/check progress there when you finish or defer work.
2. Prefer extending existing packages (`packages/identity`, `apps/api`, `apps/cli`, `apps/web`) over new top-level apps.
3. Do not invent parallel crypto or message formats — match canonical strings and wire types already used by the relay and CLI.
4. Do not commit secrets (`.env`, DNS tokens, private keys). `.env` examples stay local.

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

`apps/web` tests spawn a **real Go relay** (`go run` with `POWEUR_DATA` + `HOSTED_DOMAINS` + `WEB_STATIC_DIR`), same spirit as CLI integration tests.

```bash
cd apps/web && pnpm install   # postinstall downloads Chromium for Playwright
pnpm test          # Vitest: vault/storage/components unit + live-relay
pnpm test:e2e      # Playwright: destinations at 375px, hosted create UI, protocol smoke
pnpm test:all
# If e2e says browser executable missing: pnpm exec playwright install chromium
```

**The web app has no bundler**: it imports `@poweur/client` through an import map that
points at `apps/web/vendor/`, a committed copy of the package's ESM (prod mounts `apps/web`
straight from the checkout, so it has to be in git). **Any change to `packages/client-ts`
must be re-vendored in the same change set:**

```bash
pnpm client:build && pnpm web:vendor    # refresh apps/web/vendor/
pnpm web:vendor:check                   # fails when it is stale (also asserted in Vitest)
```

Web Vitest mirrors CLI unit tests (`encrypt/decrypt`, identity persistence, session send,
identity-signed send, invalid `--sign-with`) plus CLI↔relay messaging (register → session
→ encrypt/send → inbox decrypt) driven through the app's own modules (`js/client.js`,
`js/vault.js`) against a real `go run` relay. The protocol itself lives in
`@poweur/client` and is tested there; `apps/web` tests the browser key-custody seam.

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

## Style

- Match existing Go and JS style; minimal diffs; no drive-by refactors.
- Specs that change the protocol land in `apps/docs/docs/` in the same change set.
- PR-oriented commits when the user asks: focus on why; no secrets.

## Quick map

```
apps/api          Go relay
apps/cli          Go CLI
apps/web          Vanilla JS client (served at /app/)
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
