# Poweur

A pnpm monorepo for the Poweur ID DNS-identity protocol, relay, clients, docs, and infrastructure.

**Agents / contributors:** see [AGENTS.md](AGENTS.md) and [CONTRIBUTING.md](CONTRIBUTING.md). Roadmap tasks: [epics/](epics/README.md).

## Structure

```
poweur/
├── apps/
│   ├── api/      # Go relay / HTTP API
│   ├── cli/      # Node CLI
│   ├── docs/     # Docusaurus documentation site
│   ├── ios/      # Native iOS package scaffolding
│   ├── android/  # Native Android package scaffolding
│   └── infra/    # Terraform for Hetzner deployment
├── requirements.md
├── test-scenarios.md
├── package.json
└── pnpm-workspace.yaml
```

## Prerequisites

- [Node.js](https://nodejs.org/) >= 18
- [pnpm](https://pnpm.io/) >= 9
- [Go](https://go.dev/) >= 1.21

## Getting started

Install JS dependencies from the repo root:

```bash
pnpm install
```

### API (`apps/api`)

```bash
cd apps/api
go run main.go       # starts the HTTP server on :8080
```

### CLI (`apps/cli`)

```bash
cd apps/cli
go run .
```

The CLI signs outbound messages with a short-lived session key by default. Headless agents or operators who want to bypass session registration can opt into signing with the long-lived identity key on a per-send basis:

```bash
poweur send --sign-with=identity bob.poweur.net "hi bob"
```

Relays accept both paths; the recipient decrypts the same way regardless of which signing key the sender chose.

### Docs (`apps/docs`)

The repository contains a Docusaurus documentation site under `apps/docs`, alongside the higher-level product requirements in `requirements.md`.

## Current implementation status

- `apps/api` currently exposes only minimal root and health endpoints.
- `apps/cli` is still a placeholder.
- `apps/ios` and `apps/android` are package-level scaffolding; native app sources are not yet present in the repo.
- The most complete source of truth today is the documentation set in `apps/docs` together with `requirements.md`.
