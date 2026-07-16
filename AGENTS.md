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

### Integration tests

Any change that touches **relay HTTP**, **CLI send/inbox/register**, **identity resolution**, or **cross-relay forwarding** needs coverage under `apps/integration/`.

That suite runs **real in-process relays** (`httptest`) + CLI + fake DNS zone — not mocks of the protocol. Add or extend tests there when behavior is end-to-end.

```bash
cd apps/integration && go test ./... -count=1
```

Hosted-identity path (web identity, no DNS token):

```bash
cd apps/integration && go test -run TestINT_HOSTED -count=1 -v
```

If a feature cannot be asserted in unit tests alone (Host routing, restart/`POWEUR_DATA`, E2E encrypt/send/inbox), write an integration test.

### After substantive changes

Run the slice you touched **and** `apps/integration` before declaring done. Fix failures; do not skip with `-short` to hide them.

## Architecture notes agents should respect

| Concern | Rule |
|---------|------|
| Identity discovery | Web-first: `/.well-known/poweur/id.json`, then DNS TXT; key mismatch → fail closed (`packages/identity`) |
| Hosted registration | No DNS token; signed `identity_document`; `HOSTED_DOMAINS` + `POWEUR_DATA` |
| Relay trust | Relay never holds identity private keys; clients sign documents and messages |
| Sessions | Short-lived; memory-only until EPIC-009 |
| Storage | Durable identity docs under `POWEUR_DATA/identities/.../poweur-sys/public/id.json` |
| SSRF | Well-known fetch: no redirects, size/time caps, no private IPs unless test flag |

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
packages/identity Shared identity document + resolver
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
