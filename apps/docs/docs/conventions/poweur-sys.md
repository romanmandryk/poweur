---
id: poweur-sys
sidebar_position: 1
title: /poweur-sys specification
---

# `/poweur-sys` — normative specification

The system folder makes an identity's home self-describing. This page is the single
normative home for every file in it: schema, audience, writer, and limits. JSON Schemas
live in [`conventions/schemas/poweur-sys/`](https://github.com/romanmandryk/poweur/tree/master/conventions/schemas/poweur-sys);
the enforced validators are Go (`packages/identity`), run by the relay **on write** —
a malformed system document is rejected with `422 invalid_document`.

## The tree

| File | Audience | Writer | Schema-validated on write | Purpose |
|------|----------|--------|---------------------------|---------|
| `public/id.json` | world | **relay only** (registration/rotation endpoints; owner DAV writes are 403) | n/a | identity document (PCP-0002) |
| `public/profile.json` | world | owner | ✔ | display name, avatar (path into `/public`), bio, links |
| `public/capabilities.json` | world | owner | ✔ | supported features + endpoint hints |
| `relay/contacts.json` | owner + relay | owner | ✔ | contact list with pinned keys (PCP-0004) |
| `relay/inbox-policy.json` | owner + relay | owner | ✔ | message acceptance mode |
| `relay/app-passwords.json` | owner + relay | owner | ✔ | hashed Basic-auth credentials |
| `relay/shares/<id>.json` | owner + relay | owner | ✔ (+ signature verified on load) | share grants (PCP-0003) |
| `relay/groups/<name>.json` | owner + relay | owner | ✔ (+ signature verified on load) | owner-local groups |
| `relay/devices.json` | owner + relay | relay (reserved, EPIC-004 T6) | — | device registry |
| `relay/logs/…` | owner + relay | relay | — | access/audit logs (owner-readable) |
| `private/…` | **owner only** | owner | — | relay stores but must not read; sensitive content is client-encrypted |

`/.well-known/poweur/` is backed by `poweur-sys/public/` — any file written there is
world-served (with `nosniff`), which is how `profile.json` and `capabilities.json` are
discovered without new endpoints.

## Rules

- **Forward compatibility:** unknown files inside `poweur-sys` MUST be preserved by
  relays and sync clients — never rejected, never deleted. Only the paths listed above
  are schema-governed; everything else is schema-free by design.
- **Size cap:** schema-governed documents are capped at 64 KB.
- **Relay-written files** (`devices.json`, `logs/`) are pull-only for sync clients:
  clients read them but must not push local edits over them.
- **Bootstrap:** registration creates the directory skeleton (five roots, the three
  `poweur-sys` zones, `relay/shares`, `relay/groups`) and writes `public/id.json`.
  Everything else appears when the owner (or relay) first writes it — absence of any
  optional file must be handled by consumers (documented per-file defaults, e.g. no
  `inbox-policy.json` = `open`).

## `profile.json` and `capabilities.json`

Human-facing and machine-facing self-description (E06-T2):

```json
{"version": 1, "display_name": "Alice", "avatar": "public/avatar.png",
 "bio": "…", "links": [{"label": "web", "url": "https://…"}], "locale": "en"}
```

`avatar` is a tree path under `/public` — never an external URL (no tracking pixels on
profile views). Capabilities supersede the `_poweur-caps` TXT sketch for web-resolved
identities:

```json
{"version": 1,
 "features": {"messaging": "v1", "files": "webdav", "sync": "v1", "sharing": "v1"},
 "endpoints": {}}
```

Unknown feature names are allowed (forward compatibility); consumers ignore what they
don't speak. The DNS TXT equivalent remains for DNS-only identities.
