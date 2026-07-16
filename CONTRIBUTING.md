# Contributing

Poweur is an open identity + messaging protocol with a Go relay, Go CLI, and web client.
Roadmap and pick-up tasks live in [`epics/`](epics/README.md).

## For humans and agents

Coding agents should follow [`AGENTS.md`](AGENTS.md). Summary:

1. **Unit tests** for new logic (especially crypto, identity, storage, resolvers).
2. **Integration tests** in `apps/integration/` when changing relay/CLI messaging or registration — those tests run real local relays in-process.
3. **Update epic checklists** when you finish or defer a task.
4. Keep protocol changes documented under `apps/docs/docs/`.

## Dev setup

- Go ≥ 1.23 (see `go.work`), Node/pnpm for docs if needed.
- From repo root: `go test ./packages/identity/... ./apps/api/... ./apps/cli/... ./apps/integration/...`

## PRs

- Prefer small PRs mapped to an epic task ID (`E02-T2: …`).
- Do not commit `.env`, DNS tokens, or private keys.
