---
id: webdav
sidebar_position: 2
title: WebDAV access
---

# WebDAV access

Every hosted identity exposes its home filesystem over **WebDAV**. Vanilla DAV clients
(Finder, Explorer, rclone, davfs2) work for plain read/write; Poweur-specific auth and
layout rules sit underneath.

See also: [Storage model](./storage-model.md) (layout, quotas, providers).

## Endpoints

| URL | Purpose |
|---|---|
| `https://<relay>/dav/<identity>/…` | Canonical tree URL |
| `https://<identity>/dav/…` | Vanity alias (Host-routing on wildcard relays) |

On a shared wildcard relay both can apply at once (`Host: <identity>` and a
canonical `/dav/<identity>/…` path). The path wins: the first `/dav/` segment is
the owner when it names a hosted identity; otherwise Host selects the owner
(the Finder-style vanity mount).
| `POST /auth/dav-token` | Mint a bearer token (session- or identity-signed) |
| `DELETE /auth/dav-token/{token}` | Revoke a token immediately |
| `GET /files/<identity>/quota` | Used bytes / quota / change_id (owner credentials) |
| `https://<identity>/pub/<path>` | Optional plain-web serving of marked `/public` folders |

## Authentication

WebDAV clients speak Basic/Bearer, not Ed25519. The bridge never weakens the key model:

### Bearer tokens (`Authorization: Bearer dav_…`)

`POST /auth/dav-token` with a signed body:

```json
{
  "identity": "alice.poweur.net",
  "audience": "alice.poweur.net",
  "scope": "dav:full",
  "issued_at": "2026-07-16T12:00:00Z",
  "nonce": "…",
  "session_id": "sess_…",
  "signature": "…"
}
```

Canonical string (newline-separated):

```
dav-token
<identity>
<audience>
<scope>
<issued_at>
<nonce>
```

- Sign with a registered **session** key when `session_id` is set (token TTL ≤ session expiry).
- Or sign with the long-lived **identity** key (no `session_id`) — used by visitors and the CLI.
- `audience` defaults to `identity` (own tree). Visitors set `audience` to the tree owner;
  visitor tokens are read-only in v1 (`dav:read`); write scopes are rejected until EPIC-005 grants.

Scopes: `dav:full`, `dav:read`, `dav:rw:<path>`, `dav:read:<path>`.

Tokens live in relay memory only and are revocable. A relay restart invalidates all of them;
clients re-mint from their session or identity key.

### App passwords (Basic auth)

Legacy clients that cannot send Bearer (notably macOS Finder) use **app passwords**:

- Username = the tree owner identity
- Password = a named secret whose argon2id hash lives at
  `poweur-sys/relay/app-passwords.json` (relay-readable config zone)
- CLI: `poweur dav password add --name=finder`, then `poweur dav mount`

Revocation = edit or remove the entry; the next Basic attempt fails.

## Cross-identity access

A visitor authenticates as *their own* Poweur ID and obtains a token with
`audience: <owner>`. The permission engine then applies the layout rules from the
[storage model](./storage-model.md):

| Path | Visitor |
|---|---|
| `/poweur-sys/public/` | read (also anonymous) |
| `/public/` | read (any valid ID) |
| `/shared/`, `/apps/…` | grant-based (EPIC-005; v1 denies) |
| everything else | denied |

Cross-identity reads are appended as JSON lines to
`poweur-sys/relay/logs/access.log` (owner-readable).

## Public web serving (`/pub`)

Sharing a file with the *web* (not just with Poweur IDs) is an explicit, opt-in act:

1. Place the file under `/public/…`.
2. Drop a `.poweur-web-public` marker in that folder (or an ancestor still under `/public`).
3. Browse `https://<identity>/pub/<relative-path>`.

Unmarked `/public` paths stay ID-auth only. Directory listings are off unless the marker
JSON sets `"listings": true`. Responses always send `X-Content-Type-Options: nosniff`;
HTML/SVG/XML are served as `text/plain` in v1.

## CLI

```bash
poweur dav token [--audience=…] [--scope=dav:full|dav:read|dav:rw:<path>]
poweur dav mount          # prints a ready-to-paste mount command for this OS
poweur dav password add --name=<name> [--scope=…]
poweur dav password list
poweur dav password remove --name=<name>
```

## Related

- [Storage model](./storage-model.md)
- [E2EE design study](./e2ee-design.md)
- [CLI reference](/clients/cli-reference)
