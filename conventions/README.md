# Poweur Conventions

Interoperability in Poweur emerges from **a shared filesystem + shared identity + published
conventions** — the way email emerged from RFC 822. Apps start with their own formats inside
their `/apps/<app-id>/` namespace; when several apps converge on a domain (tasks, calendars,
contacts…), the convention gets written down here and matures into a standard.

## What lives here

| Path | Contents |
|------|----------|
| `pcp-XXXX-*.md` | Poweur Convention Proposals — the RFC-style documents |
| `schemas/poweur-sys/*.schema.json` | JSON Schemas for the `/poweur-sys` system documents (normative shape; Go validators in `packages/identity` are the enforced implementation) |
| `schemas/<namespace>/*.schema.json` | JSON Schemas for any other claimed namespace (e.g. `net.poweur.tasks/`). For app-domain conventions the schema **is** the normative artifact — the relay does not validate `/apps` payloads. Every namespace listed in `registry.json` is CI-validated. |
| `registry.json` | Claimed app-ids, `sys.*` message types and schema namespaces (first-come + PR review) |

## Process (PCP)

See [pcp-0001-process.md](pcp-0001-process.md). Short version: propose in a PR
(`draft`), ship an implementation (`experimental`), reach two independent
implementations (`stable`). The normative human-readable spec for `/poweur-sys` is
[`apps/docs/docs/files/storage-v2.md`](../apps/docs/docs/files/storage-v2.md).
Schema URLs retain their historical namespace during the storage transition.

## Seed PCPs

| PCP | Subject | Status |
|-----|---------|--------|
| [pcp-0001](pcp-0001-process.md) | The convention process itself | experimental |
| [pcp-0002](pcp-0002-identity-document.md) | Identity document (`id.json`) | experimental |
| [pcp-0003](pcp-0003-share-grant.md) | Share grant & group documents | withdrawn |
| [pcp-0004](pcp-0004-contacts.md) | Contacts, inbox policy & `sys.contact.*` | experimental |
| [pcp-0005](pcp-0005-sync-journal.md) | Sync journal record & changes cursor | withdrawn |
| [pcp-0006](pcp-0006-anon-challenges.md) | Anonymous ingress & sender challenges (PoW) | experimental |
| [pcp-0007](pcp-0007-tasks.md) | Tasks & projects (`net.poweur.tasks`) | withdrawn |
| [pcp-0006](pcp-0006-anon-challenges.md) | Anonymous ingress & sender challenges (PoW) | experimental |
