---
id: storage-model
sidebar_position: 1
title: File storage model
---

# File storage model

Every Poweur ID hosted on a relay gets a **home filesystem**: a per-identity file tree stored
under `$POWEUR_DATA/identities/<id>/`, served over **WebDAV** at `/dav/<identity>/` and
authenticated with Poweur keys (see [WebDAV access](webdav.md)). This page is the normative
storage-layer spec: layout, audiences, path rules, limits, and metadata.

## Top-level layout

The tree has exactly five top-level roots. Anything else at the top level is reserved and
invisible over WebDAV (the relay may keep operational data such as message spools next to
them on disk).

```
/poweur-sys/          system data — managed, convention-governed (EPIC-006)
  public/             world-readable; backs /.well-known/poweur/ (id.json, profile, capabilities)
  relay/              owner + RELAY — config the relay must read to function
                      (contacts, inbox policy, devices, share grants, app passwords, logs)
  private/            OWNER ONLY — relay stores the bytes but must not read them; anything
                      sensitive placed here is encrypted client-side to the owner's key
/public/              readable by ANY authenticated Poweur ID (valid signature, no grant needed)
/shared/              readable/writable per explicit grants (EPIC-005)
/private/             owner (and the owner's devices/agents) only
/apps/<app-id>/       per-application data; default ACL private (EPIC-006)
```

## Audience matrix

"Owner" means a principal authenticated as the identity whose tree it is (via DAV token or
app password). "Any ID" means a principal authenticated as *some* valid Poweur identity,
local or remote. Deny wins; there are no per-file exceptions in v1.

| Path | Anonymous | Any ID | Grant holder | Owner | Relay process |
|---|---|---|---|---|---|
| `/poweur-sys/public/` | read | read | read | read + write | read |
| `/poweur-sys/relay/` | — | — | — | read + write | read (+ write for relay-managed files) |
| `/poweur-sys/private/` | — | — | — | read + write | stores only, MUST NOT read |
| `/public/` | — | read | read | read + write | read |
| `/shared/` | — | — | per grant (EPIC-005) | read + write | read |
| `/private/` | — | — | — | read + write | stores only (v1: readable in `relay-fs` mode; relay-blind is opt-in later, see [E2EE design](e2ee-design.md)) |
| `/apps/…` | — | — | per grant | read + write | stores only |

Notes:

- `poweur-sys/relay` is the **explicit trust split**: the relay must read it to enforce inbox
  policy, shares, and app-password auth. It is never visible to other users.
- `poweur-sys/private` is owner-only by contract. In `relay-fs` mode the bytes sit on the
  relay's disk, so the guarantee is procedural (the relay code never reads them) until the
  E2EE layer makes it cryptographic; clients SHOULD encrypt sensitive content placed here.
- Web (no-identity) exposure of `/public` paths is a separate, explicit act — see
  [Public web serving](webdav.md#public-web-serving-pub).

## Path rules

- Paths are `/`-separated UTF-8. Invalid UTF-8 or ASCII control characters (0x00–0x1F, 0x7F)
  in any segment → `400`.
- Segments must not be empty, `.`, or `..`. Max segment length **255 bytes**, max path length
  **4096 bytes**, max depth **32** segments.
- The prefix `.poweur-` is reserved for documented marker files (currently:
  `.poweur-web-public`); other `.poweur-*` names are rejected.
- **Symlinks are forbidden.** The provider never creates or follows them; a symlink found on
  disk is treated as nonexistent.
- **Case collisions are rejected**: creating a name that differs from an existing sibling only
  by Unicode simple case folding → `409`. This keeps trees portable to case-insensitive
  backends (macOS clients, future S3 gateways).
- New top-level names cannot be created: `MKCOL /dav/<id>/foo/` → `403`. The five roots are
  created on first use.

## Limits and quotas (v1 defaults)

| Limit | Default | Config |
|---|---|---|
| Per-identity quota (whole tree) | 5 GiB | `MAX_IDENTITY_BYTES` |
| Max single file | 2 GiB | `MAX_FILE_BYTES` |
| Max path depth / segment / path | 32 / 255 B / 4096 B | fixed |

Quota is enforced on `PUT` and `MKCOL`: if the tree's used bytes plus the incoming
`Content-Length` would exceed the quota, the relay answers `507 Insufficient Storage`.
Setting `MAX_IDENTITY_BYTES=0` disables the quota (not recommended on public relays).

## Metadata model

The relay maintains a per-identity **metadata index** (stored outside the visible tree, at
`identities/<id>/meta/files-index.json`):

- **`etag`** — hex SHA-256 of the file content, recomputed on every write and rename.
  SHA-256 over xxh3: it is in the Go standard library, collision resistance matters once
  etags are used for cross-device sync dedupe (EPIC-004), and hashing is not the bottleneck
  at our file sizes. Served as the HTTP `ETag` (`"sha256-<first 32 hex>"`).
- **`size`**, **`mtime`** — mirrored from the store for cheap `PROPFIND`.
- **`change_id`** — a per-tree monotonic counter, incremented by every mutation (PUT, MKCOL,
  MOVE, DELETE). This is the backbone of the EPIC-004 changes journal ("what changed since
  X"); v1 only persists the counter and per-file last-change ids.

If the index is missing or stale (e.g. the operator edited files on disk), it is rebuilt
lazily: a file with no index entry gets hashed on first access.

## Storage providers

The relay accesses file bodies through a `StorageProvider` interface, never the OS
filesystem directly. v1 ships one provider:

- **`relay-fs`** (default, `STORAGE_PROVIDER=relay-fs`) — files under
  `$POWEUR_DATA/identities/<id>/`. The relay sees all bytes.

The interface deliberately avoids POSIX assumptions so an S3-compatible provider can plug in
later (no atomic-rename requirement in the contract, directories may be virtual, listings may
be eventually consistent). S3 is deferred; when it lands, the gateway may share the relay
host or run as a separate service — the adapter does not care. Sketch: relay-mediated roots
(`poweur-sys/public`, `poweur-sys/relay`, `/public`, `/shared`) via the provider; owner
`/private` client-direct to the bucket with credentials encrypted at
`poweur-sys/private/storage-credentials.json`.

## Threat model

- **Path traversal / zip-slip** — every path is validated (rules above) and resolved
  relative to the identity home; the sanitized identity directory name (`SanitizeIdentityDirName`)
  cannot contain separators. Rejected before touching the provider.
- **Symlinks** — never followed (`Lstat` checks on every component below the home); a
  symlink is reported as not found, so a mounted client cannot read `/etc/passwd` through one.
- **Case collisions** — rejected at create time (see path rules), preventing shadowing
  attacks and backend-dependent behavior.
- **MIME sniffing on public serving** — `/pub/` responses always send
  `X-Content-Type-Options: nosniff`; content types come from the extension map only, and
  HTML/SVG/XML are served as `text/plain` unless the folder marker explicitly opts into raw
  serving (v1: always plain — active content via `/pub/` is out of scope).
- **Quota exhaustion** — per-identity quota plus the relay-wide rate limiter on write
  methods; `Content-Length` is required for `PUT` (chunked uploads without a length are
  rejected `411`) so quota can be checked before the body is consumed.
