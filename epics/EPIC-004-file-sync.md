# EPIC-004 — File sync protocol & sync clients

- **Status:** proposed
- **Priority:** P1
- **Depends on:** EPIC-003
- **Unlocks:** EPIC-010 (watch-folder automations), offline-capable apps

## Goal

Keep an identity's home filesystem continuously in sync between the relay and the user's
devices (laptop folder, phone, headless agent hosts) — bidirectional, resumable, conflict-aware.
This is the "Dropbox client" of the ecosystem and the mechanism by which a user's data is both
on their device *and* available to relays/apps/shares.

## Background & protocol research

WebDAV alone (EPIC-003) is a fine access protocol but a poor sync protocol: PROPFIND-walking a
big tree per poll is O(tree), ETag comparison gives no ordering, and there's no efficient
"what changed since X". Prior art to mine:

- **RFC 6578 (DAV sync-collection REPORT)** — standard delta sync over WebDAV; Nextcloud/ownCloud
  ship it. Pro: stays inside DAV, existing clients benefit. Con: per-collection tokens, awkward
  for tree-wide cursors, still XML.
- **Nextcloud desktop sync** — DAV + custom chunked-upload endpoints + polling/notify push;
  battle-tested model very close to what we need.
- **rsync/librsync delta encoding** — bandwidth-optimal for large mutated files; significant
  complexity, defer.
- **Syncthing** — peer-to-peer block exchange protocol; great ideas (block-level dedup, version
  vectors) but its trust/device model doesn't fit relay-mediated identity sharing. Not adopted,
  but its conflict handling (rename-with-suffix, no silent loss) is the UX to copy.

**Decision:** keep WebDAV for transfer of file *bodies*; add a small JSON **changes API** for
delta discovery (tree-wide cursor), a **chunked upload** endpoint for big files, and reuse
Poweur messaging/WebSocket (EPIC-009) as the change-notification push channel. Implement RFC 6578
later only if third-party DAV sync clients demand it.

## Design direction

- Every mutation on an identity's tree increments a per-identity monotonic `change_id`
  (introduced in E03-T1's metadata index) and appends to a **changes journal**:
  `{change_id, op: put|delete|move, path, etag, size, mtime, actor}`.
- Sync clients hold a cursor, call `GET /sync/{identity}/changes?since=<cursor>`, then
  fetch/push bodies via DAV. Push notifications (message of type `sys.sync.changed`, or
  WebSocket event once EPIC-009 lands) collapse polling latency to near-real-time.
- Conflicts: server never merges. Last-writer-wins on the server copy **plus** the losing
  version is preserved as `name (conflicted copy from <device> <date>).ext` — the
  Dropbox/Syncthing convention. Apps that need real merging use their own formats (CRDTs etc.,
  see EPIC-006 conventions).

## Tasks

### E04-T1 — Changes journal & sync API spec

- [ ] Spec `apps/docs/docs/files/sync-protocol.md`: journal record schema, cursor semantics
      (opaque, per-identity), compaction rules (journal pruned after N days — full-resync path
      must exist), move detection (or document moves-as-delete+put for v1)
- [ ] `GET /sync/{identity}/changes?since=` endpoint design w/ auth scopes from E03-T3,
      pagination, and `?paths=/apps/taskapp/` filtering (agents sync only their slice)
- [ ] Full-resync procedure: tree manifest endpoint (`GET /sync/{identity}/manifest`, streamed
      NDJSON of path+etag+size) for first sync and journal-gap recovery
- [ ] Define `sys.sync.changed` notification payload (sent via existing messaging to the
      owner's own devices)

**Acceptance:** spec merged with worked examples (fresh sync, incremental, gap recovery).

### E04-T2 — Relay implementation: journal, changes & manifest endpoints

- [ ] Journal writes hooked into every DAV mutation path from E03-T2 (PUT/DELETE/MOVE/MKCOL),
      transactional with the metadata index
- [ ] `changes` + `manifest` endpoints with auth, pagination, filtering; journal compaction job
      (extend the `runPruner` pattern in `apps/api/internal/relay/server.go`)
- [ ] Per-path-scope visibility: a visitor's changes feed for someone else's tree only contains
      paths they can read (depends on E03-T4 permission engine; shares from EPIC-005 slot in)
- [ ] Integration tests incl. journal compaction forcing a client full-resync

**Acceptance:** two clients observe each other's writes through the changes feed within one
poll interval; gap recovery test passes.

### E04-T3 — Chunked, resumable upload

512 KB messages were fine; multi-GB files over flaky links need resumability. Follow the
**tus.io** resumable-upload protocol if practical (open standard, many client libs), else
Nextcloud-style chunk-assembly:

- [ ] Evaluate tus vs custom chunk dirs — write up choice in the sync spec (1 page)
- [ ] Implement chosen protocol at `/sync/{identity}/upload`, final assembly atomically
      replaces target path + journal entry; partial uploads expire (pruner)
- [ ] Quota check at upload start, not just finish
- [ ] Wire into web app uploads (E03-T5) for files > 50 MB

**Acceptance:** kill-and-resume test: a 1 GB upload interrupted at 60% resumes and completes
without re-sending earlier chunks.

### E04-T4 — `poweur sync` daemon (Go, CLI)

The reference sync client, sharing code with `apps/cli`.

- [ ] One-shot `poweur sync push/pull/status <local-dir>` (rsync-like UX) first
- [ ] Daemon mode: fsnotify watcher + changes-feed poller (and push channel when available),
      bidirectional reconciliation against a local state DB (path → etag/cursor)
- [ ] Conflict handling per spec (conflicted-copy rename, never silent overwrite), with tests
      for the classic matrix: local-edit/remote-edit, local-delete/remote-edit, both-create
- [ ] Selective sync: include/exclude path patterns in config
      (`apps/cli/internal/config/config.go` style)
- [ ] `.poweurignore` support (gitignore syntax)

**Acceptance:** two laptops (two temp dirs in the integration suite) converge through a relay
under concurrent edits with all conflicts surfaced as conflicted copies; no data loss in the
test matrix.

### E04-T5 — Mobile & desktop integration passes

- [ ] Desktop: document mounting (DAV) vs syncing (daemon) trade-offs; ship
      launchd/systemd service templates for the daemon
- [ ] iOS/Android scaffolds (`apps/ios`, `apps/android`): File-Provider /
      Storage-Access-Framework design notes — what the native apps must implement so the home
      appears in Files/Documents apps (design + stub tasks; native code arrives with the apps)
- [ ] Web: changes-feed-driven auto-refresh in the file browser

**Acceptance:** docs merged; service templates run the daemon on boot on macOS/Linux.

### E04-T6 — Device registry

Sync introduces "the user's devices" as first-class actors (they already exist implicitly as
sessions, `apps/api/internal/relay/sessions.go` keeps a `DeviceFingerprint`).

- [ ] `poweur-sys/relay/devices.json` convention: device id, name, kind (laptop/phone/agent),
      sync scopes, added_at, last_seen — written by the relay, readable by owner
- [ ] CLI/web UI to list devices and revoke one (revokes its app passwords/tokens + sessions)
- [ ] Per-device sync cursors stored server-side so the owner can see staleness ("phone last
      synced 3 days ago")

**Acceptance:** revoking a device kills its sync within one poll cycle; device list visible in
web app.
