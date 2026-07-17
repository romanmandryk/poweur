---
id: app-data
sidebar_position: 2
title: /apps namespace rules
---

# `/apps/<app-id>` — application data conventions

Every application stores its data under `/apps/<app-id>/`, where `<app-id>` is the
**reverse-DNS name of the vendor** (`net.poweur.tasks`, `com.example.calendar`). Inside
its namespace an app has layout freedom, with one obligation:

## `manifest.json` (mandatory, validated)

`/apps/<app-id>/manifest.json` self-describes the namespace
([schema](https://github.com/romanmandryk/poweur/blob/master/conventions/schemas/poweur-sys/app-manifest.schema.json)):

```json
{"app_id": "net.poweur.tasks", "name": "Poweur Tasks", "vendor": "poweur core",
 "schema_version": "1", "docs_url": "https://…"}
```

The relay validates the shape on write and rejects a manifest whose `app_id` doesn't
match its directory. Write the manifest first; a namespace without one is considered
abandoned by tooling (cleanup UIs may flag it).

## Access

- **Default ACL: private** — only the owner (and their scoped credentials). Apps request
  exactly their slice with path-scoped tokens: `dav:rw:/apps/net.poweur.tasks/`
  (E03-T3 scope grammar; the same scope caps the changes feed an agent can see).
- **Sharing app data between users is just an EPIC-005 share** of an `/apps/...`
  subtree — no separate mechanism. Canonical worked example: alice runs
  `poweur share add /apps/net.poweur.tasks/project-x --with bob.example.org --perm rw`;
  bob's app instance reads and writes the same files through his grant, and the sync
  journal attributes every change (`actor`) for the app's activity view.

## Data portability guidance

- Prefer open formats: JSON / NDJSON / markdown / CSV. The user's data outlives the app.
- **Many small files beat one big mutable file.** Sync conflicts resolve per file with
  conflicted copies (EPIC-004); a single `db.json` makes every concurrent edit a
  conflict, one-file-per-task makes almost none. Apps needing real merging bring their
  own CRDT formats — the platform will not merge for you.
- Don't hide state in filenames; keep ids inside the documents so renames stay cheap.
