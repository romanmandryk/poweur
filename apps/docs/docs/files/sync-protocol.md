---
id: sync-protocol
sidebar_position: 4
title: File sync protocol
---

# File sync protocol

WebDAV (see [WebDAV access](webdav.md)) moves file *bodies*; this page specifies the Poweur
extensions that make continuous sync efficient (EPIC-004): the per-identity **changes
journal** with a cursor-based delta feed, the **manifest** for full resync, and **chunked
resumable uploads** for large files. A vanilla DAV client keeps working without any of
this — the sync API is additive.

The reference client is `poweur sync` (one-shot `pull` / `push` / `run` / `status` against a
local directory). Design lineage: RFC 6578 sync-collection (rejected: per-collection tokens,
XML), Nextcloud's chunking + polling model (followed loosely), Syncthing's conflict UX
(copied: conflicted-copy renames, never silent loss).

## Changes journal

Every mutation of an identity's tree — DAV `PUT`/`MKCOL`/`DELETE`/`MOVE`, chunked-upload
assembly, or relay-internal writes — increments the identity's monotonic `change_id` and
appends one record to the journal:

```json
{"change_id": 42, "op": "put", "path": "private/docs/a.txt",
 "etag": "<hex sha256 of content>", "size": 5, "mtime": "2026-07-17T10:00:00Z",
 "actor": "alice.poweur.net", "time": "2026-07-17T10:00:00Z"}
```

- `op` is `put` (file content), `mkdir` (directory created), or `delete` (**the path and
  everything under it** is gone).
- **Moves are journaled as delete + put** in v1: one `delete` of the old prefix, then a
  `put`/`mkdir` per moved entry at the new prefix, each with its own `change_id`. Clients
  need no move logic; rename detection may come later as an optimization.
- `actor` is the authenticated identity that performed the write (empty for relay-internal
  writes). Grant-holder writes (EPIC-005) will carry the visitor's identity here, which is
  the owner's audit trail.
- `etag` is the full content sha256 (the same value the metadata index stores; DAV serves
  it truncated as `"sha256-<first32>"`). Directories have no etag.

Storage: the journal is part of the metadata layer (`meta/sync-journal.ndjson` next to
`meta/files-index.json`, outside the DAV-visible tree). Like the index, it is keyed by the
identity, not by storage location — an S3-class `StorageProvider` (E03-T8) supplies file
bodies only and pairs with the same journal/index, so sync works identically on any
provider.

### Compaction & the resync contract

The journal is pruned (records older than 30 days, or beyond 10 000 records). The relay
remembers the highest discarded `change_id` (`compacted_through`). A cursor older than that
cannot be served incrementally: the feed answers `full_resync: true` and the client must
rebuild from the manifest. **A full-resync path must always exist; the journal is an
optimization, never the source of truth** (the metadata index is).

## `GET /sync/{identity}/changes`

Query parameters:

| param | meaning |
|---|---|
| `since` | opaque cursor from a previous response (absent/empty = from the beginning) |
| `limit` | max records (default 1000, cap 5000) |
| `paths` | prefix filter, repeatable or comma-separated (`?paths=/apps/net.poweur.tasks/`) — agents sync only their slice |

Response:

```json
{"identity": "alice.poweur.net", "since": "40", "next": "42", "latest": "42",
 "full_resync": false, "changes": [ { …journal records, ascending change_id… } ]}
```

- Poll again with `since=<next>`; `next < latest` means more pages are pending. When the
  feed is drained with a filter active, `next` fast-forwards to `latest`.
- Cursors are opaque strings — clients must not do arithmetic on them.
- `full_resync: true` ⇒ ignore `changes`, fetch the manifest, keep its cursor.

**Auth & visibility:** same credentials as WebDAV (Bearer DAV token or Basic app password,
see [WebDAV access](webdav.md)). The feed only ever contains records whose path the caller
may *read*: owners see everything their token scope covers, visitors see `/public` (and
granted paths once EPIC-005 lands), anonymous callers see only `poweur-sys/public`. The
same rule applies to the manifest.

## `GET /sync/{identity}/manifest`

The full-resync path (first sync, journal gap, or state-DB loss): a streamed NDJSON
snapshot of the visible tree, taken from the metadata index.

```
{"manifest":1,"identity":"alice.poweur.net","cursor":"42"}
{"path":"private/docs","mtime":"…","dir":true}
{"path":"private/docs/a.txt","etag":"<hex sha256>","size":5,"mtime":"…"}
```

The header's `cursor` is the changes-feed position the snapshot corresponds to — after
reconciling against it, resume incremental polling with `since=<cursor>`. Entries are
path-sorted (parents before children). `?paths=` filtering applies as above.

## Chunked resumable upload — `/sync/{identity}/upload`

DAV `PUT` is fine up to tens of MB; multi-GB files over flaky links need resumability.
**Decision (E04-T3):** a minimal custom protocol using tus.io's header vocabulary
(`Upload-Length`, `Upload-Offset`) rather than full tus conformance — tus's metadata
encoding and extension negotiation added surface without value here, and our auth (DAV
tokens) already diverges from stock tus servers. Revisit full tus if third-party clients
materialize.

```
POST   /sync/{id}/upload?path=/private/big.bin    Upload-Length: 1073741824
  → 201 {"id":"up…","offset":0,"length":…,"expires_at":"…"} + Location header

PATCH  /sync/{id}/upload/{upload-id}              Upload-Offset: 0, body = chunk
  → 204 (Upload-Offset: <new offset>)             while incomplete
  → 200 {"path":…,"etag":…,"size":…,"change_id":…} on the final chunk
  → 409 offset mismatch (stale retry) — HEAD to learn the true offset, resume
  → 413 chunk would exceed Upload-Length

HEAD   /sync/{id}/upload/{upload-id}              resume probe
  → 200 with Upload-Offset / Upload-Length

DELETE /sync/{id}/upload/{upload-id}              cancel, frees the spool
```

Semantics:

- **Quota is checked at upload start** (`Upload-Length` against the identity quota and max
  file size), not just at completion — a client can't spool gigabytes only to be rejected.
- Chunks append to a server-side spool outside the visible tree (`meta/uploads/`); on the
  final byte the file is **assembled atomically** into the target path through the
  `StorageProvider` and journaled as a single `put`. Readers never observe a partial file.
  An S3 provider may later replace the spool with native multipart upload behind the same
  endpoints.
- Partial uploads expire after 24 h of inactivity (swept opportunistically).
- Write permission on the target path is required (owner scope now; EPIC-005 grants later).

The reference client chunks files ≥ 64 MiB (8 MiB chunks).

## Client reconciliation (`poweur sync`)

State: `.poweur-sync.json` at the sync root — the changes cursor plus, per path, the
content sha256 at last sync (identical to the server etag, so one value describes both
sides), with size+mtime as a rehash-avoidance cache. `.poweurignore` (gitignore-flavored
subset: globs, `#` comments, `dir/` prefixes; no negation in v1) excludes paths; the state
and ignore files never sync. By default all roots except `poweur-sys` sync; `--path`
narrows the scope.

**Pull** applies the changes feed (or a manifest diff after a gap) three-way against the
state base:

| remote says | local vs base | action |
|---|---|---|
| `put` | unchanged | download |
| `put` | same content already | update state only (own-push echo) |
| `put` | edited | **conflict**: local → conflicted copy, download remote |
| `put` | deleted locally, remote == base | keep deleted (push will delete remote) |
| `put` | deleted locally, remote edited | download (edit wins over delete) |
| `delete` | unchanged | delete locally |
| `delete` | edited | keep local, untrack (edit wins; push re-uploads) |
| `mkdir` | dir deleted locally | keep deleted (push propagates) |
| `mkdir` | otherwise | mkdir locally |

**Push** uploads new/modified files (parents `MKCOL`ed first, top-level roots skipped —
they always exist), deletes remotely what disappeared locally (deepest ancestor only), and
never touches the cursor. **The server never merges: last writer wins on the server copy**,
and conflict *detection* is the puller's job — hence `run` = pull, then push.

Conflicted copies follow the Dropbox/Syncthing convention:
`name (conflicted copy from <device> <yyyy-mm-dd>).ext` — the losing version stays visible
next to the winner and syncs everywhere like a normal file. No state transition ever
silently discards content.

### Worked examples

- **Fresh sync:** empty state ⇒ manifest fetch; every entry is a `put`/`mkdir` against an
  empty base; local-only files are then pushed. Cursor ← manifest header.
- **Incremental:** poll `changes?since=<cursor>`; apply the table above; cursor ← `next`.
- **Gap recovery:** `full_resync: true` ⇒ manifest diff: entries newer than state are
  downloads, tracked paths absent from the manifest are remote deletes (same conflict
  rules), then resume from the manifest cursor.

## `sys.sync.changed` push notification

Polling sets the floor latency; the push channel collapses it. When a tree mutates, the
relay MAY notify the owner's other devices with a message of type `sys.sync.changed`:

```json
{"type": "sys.sync.changed", "identity": "alice.poweur.net", "latest": "42",
 "paths_hint": ["private/docs"]}
```

Recipients treat it purely as a "poll now" hint — the feed remains the source of truth,
and `paths_hint` is best-effort (may be truncated or absent). **Delivery is deferred to
EPIC-009** (typed messages / WebSocket events); v1 clients poll.

## Device registry (E04-T6)

Deferred to land with EPIC-007/009 groundwork: `poweur-sys/relay/devices.json` (device id,
name, kind, sync scopes, last_seen, per-device cursor), written by the relay, revocable via
CLI/web. Sync cursors are client-side until then.
