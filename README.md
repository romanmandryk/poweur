# eurything

A pnpm monorepo for the Eurything DNS-identity protocol, relay, clients, docs, and infrastructure.

## Structure

```
eurything/
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
node src/index.js
```

### Docs (`apps/docs`)

The repository contains a Docusaurus documentation site under `apps/docs`, alongside the higher-level product requirements in `requirements.md`.

## Current implementation status

- `apps/api` currently exposes only minimal root and health endpoints.
- `apps/cli` is still a placeholder.
- `apps/ios` and `apps/android` are package-level scaffolding; native app sources are not yet present in the repo.
- The most complete source of truth today is the documentation set in `apps/docs` together with `requirements.md`.
