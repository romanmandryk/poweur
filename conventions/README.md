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
| `registry.json` | Claimed app-ids, `sys.*` message types and schema namespaces (first-come + PR review) |

## Process (PCP)

See [pcp-0001-process.md](pcp-0001-process.md). Short version: propose in a PR
(`draft`), ship an implementation (`experimental`), reach two independent
implementations (`stable`). The normative human-readable spec for `/poweur-sys` is
[`apps/docs/docs/conventions/poweur-sys.md`](../apps/docs/docs/conventions/poweur-sys.md);
app-namespace rules are in
[`apps/docs/docs/conventions/app-data.md`](../apps/docs/docs/conventions/app-data.md).

## Seed PCPs

| PCP | Subject | Status |
|-----|---------|--------|
| [pcp-0001](pcp-0001-process.md) | The convention process itself | experimental |
| [pcp-0002](pcp-0002-identity-document.md) | Identity document (`id.json`) | experimental |
| [pcp-0003](pcp-0003-share-grant.md) | Share grant & group documents | experimental |
| [pcp-0004](pcp-0004-contacts.md) | Contacts, inbox policy & `sys.contact.*` | experimental |
| [pcp-0005](pcp-0005-sync-journal.md) | Sync journal record & changes cursor | experimental |
