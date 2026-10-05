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
- What CI runs, locally: `pnpm client:test`, `pnpm web:test` and `pnpm --filter @poweur/web test:e2e:smoke` (the fast tier), or `pnpm --filter @poweur/web test:e2e` for the whole browser suite (the nightly tier, about 5 minutes).

## PRs

- Prefer small PRs mapped to an epic task ID (`E02-T2: …`).
- Three kinds of PR, each with its own template in `.github/PULL_REQUEST_TEMPLATE/`:
  - **Spec** (`spec.md`): markdown in `epics/` (and `apps/docs/docs/` for protocol changes). It merges before any implementation of that task starts.
  - **Implementation** (`implementation.md`): code for a task that a merged spec defines.
  - **Bugfix** (`bugfix.md`): fixes an existing issue and links it with `Fixes #n`.
- Choose a template with `gh pr create --template spec.md`, or add `?template=spec.md` to the compare URL.
- A pull request must pass **CI result** before it can merge: Go unit and integration tests, the SDK, the web app's unit tests and a browser smoke set, the docs build and the deployment checks, each only when the files it covers changed. The whole browser suite runs nightly.
- Changing shipped code means bumping that package's version in the same PR (`pnpm release:bump <package>`); CI's **Version bumps** check enforces it. How releases are cut: [`RELEASING.md`](RELEASING.md).
- Do not commit `.env`, DNS tokens, or private keys.

## Licensing of contributions

Contributions are accepted under the license of the directory they change (see
[README → License](README.md#license)): AGPL-3.0-only for the services, Apache-2.0 for the
SDK, identity package, CLI, conventions and docs. Sign off every commit (`git commit -s`) to
certify the [Developer Certificate of Origin](https://developercertificate.org/): that you
wrote the change, or otherwise have the right to submit it under that license.

