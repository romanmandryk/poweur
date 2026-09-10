# EPIC-004 — File sync protocol & sync clients

- **Status:** core complete (T1–T4 shipped; T5/T6 + daemon mode deferred, see Progress)
- **Priority:** P1
- **Depends on:** EPIC-003
- **Unlocks:** EPIC-010 (watch-folder automations), offline-capable apps

## Progress

| Task | Status | Notes |
|------|--------|-------|
| E04-T1 Sync protocol spec | **done** | [`apps/docs/docs/files/sync-protocol.md`](../apps/docs/docs/files/sync-protocol.md); moves-as-delete+put documented for v1; `sys.sync.changed` payload defined, delivery deferred to EPIC-009 |
| E04-T2 Journal + changes/manifest | **done** | Journal hooked into the metadata `Index` (single choke point: DAV, uploads, relay writes all journal); compaction is lazy (on load/append: 30 d / 10 000 records) instead of a `runPruner` job; per-principal visibility via the E03-T4 permission engine (EPIC-005 grants slot in) |
| E04-T3 Chunked resumable upload | **done** | tus-header-style protocol at `/sync/{id}/upload` (choice written up in the spec); quota at start; 24 h spool expiry; kill-and-resume covered in `TestSyncChunkedUploadResume`; web app routes files ≥ 64 MiB through `SyncClient.uploadChunked` (`doUploadFiles` in `apps/web/js/app.js`); **open:** a web-side test for that threshold branch |
| E04-T4 `poweur sync` client | **done** (one-shot) | `pull`/`push`/`run`/`status` + conflicted-copy matrix + `--path` selective sync + `.poweurignore` (subset, no negation); engine in `apps/cli/internal/sync` with fake-remote conflict-matrix tests + `TestINT_SYNC_01` two-device convergence; **deferred:** fsnotify daemon mode (poll with `run` or cron until then) |
| E04-T5 Mobile & desktop passes | **deferred** (web half done) | mount-vs-sync doc, launchd/systemd templates, iOS/Android File-Provider notes still open; web changes-feed auto-refresh shipped with [EPIC-015](EPIC-015-web-app-ux.md) E15-T4 |
| E04-T6 Device registry | **deferred** | design sketch in sync-protocol.md §Device registry; lands with EPIC-007/009 groundwork (`devices.json`, per-device cursors, revocation) |

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

- [x] Spec `apps/docs/docs/files/sync-protocol.md`: journal record schema, cursor semantics
      (opaque, per-identity), compaction rules (journal pruned after N days — full-resync path
      must exist), move detection (or document moves-as-delete+put for v1)
- [x] `GET /sync/{identity}/changes?since=` endpoint design w/ auth scopes from E03-T3,
      pagination, and `?paths=/apps/taskapp/` filtering (agents sync only their slice)
- [x] Full-resync procedure: tree manifest endpoint (`GET /sync/{identity}/manifest`, streamed
      NDJSON of path+etag+size) for first sync and journal-gap recovery
- [x] Define `sys.sync.changed` notification payload (spec only; delivery via messaging to
      the owner's own devices lands with EPIC-009)

**Acceptance:** spec merged with worked examples (fresh sync, incremental, gap recovery).

### E04-T2 — Relay implementation: journal, changes & manifest endpoints

- [x] Journal writes hooked into every DAV mutation path from E03-T2 (PUT/DELETE/MOVE/MKCOL),
      transactional with the metadata index
- [x] `changes` + `manifest` endpoints with auth, pagination, filtering; journal compaction
      is lazy (on load/append: 30 d / 10 000 records) rather than a `runPruner` job
- [x] Per-path-scope visibility: a visitor's changes feed for someone else's tree only contains
      paths they can read (depends on E03-T4 permission engine; shares from EPIC-005 slot in)
- [x] Integration tests incl. journal compaction forcing a client full-resync
      (`TestJournalCompactionForcesResync`, `TestJournalGapFallsBackToManifest`)

**Acceptance:** two clients observe each other's writes through the changes feed within one
poll interval; gap recovery test passes.

### E04-T3 — Chunked, resumable upload

512 KB messages were fine; multi-GB files over flaky links need resumability. Follow the
**tus.io** resumable-upload protocol if practical (open standard, many client libs), else
Nextcloud-style chunk-assembly:

- [x] Evaluate tus vs custom chunk dirs — choice written up in sync-protocol.md (tus header
      vocabulary, minimal custom protocol)
- [x] Implement chosen protocol at `/sync/{identity}/upload`, final assembly atomically
      replaces target path + journal entry; partial uploads expire (pruner)
- [x] Quota check at upload start, not just finish
- [x] Wire into web app uploads (E03-T5) for large files — shipped: `doUploadFiles` in
      `apps/web/js/app.js` routes anything ≥ `DEFAULT_CHUNK_THRESHOLD` (64 MiB, from
      `packages/client-ts/src/sync.ts`) through `SyncClient.uploadChunked`, single PUT below
      it. **Open:** no web-side test pins the threshold branch (`SyncClient` itself is covered
      in `packages/client-ts`)

**Acceptance:** kill-and-resume test: a 1 GB upload interrupted at 60% resumes and completes
without re-sending earlier chunks.

### E04-T4 — `poweur sync` daemon (Go, CLI)

The reference sync client, sharing code with `apps/cli`.

- [x] One-shot `poweur sync push/pull/status <local-dir>` (rsync-like UX) first (+ `run` =
      pull-then-push bidirectional one-shot)
- [ ] Daemon mode: fsnotify watcher + changes-feed poller — **deferred** (the state-DB
      reconciliation core is shipped and daemon mode is a loop around it; run `poweur sync
      run` from cron/launchd until then)
- [x] Conflict handling per spec (conflicted-copy rename, never silent overwrite), with tests
      for the classic matrix: local-edit/remote-edit, local-delete/remote-edit, both-create
- [x] Selective sync: `--path <prefix>` (repeatable) includes; `.poweurignore` excludes
- [x] `.poweurignore` support (gitignore-flavored subset; no negation in v1)

**Acceptance:** two laptops (two temp dirs in the integration suite) converge through a relay
under concurrent edits with all conflicts surfaced as conflicted copies; no data loss in the
test matrix.

### E04-T5 — Mobile & desktop integration passes

- [ ] Desktop: document mounting (DAV) vs syncing (daemon) trade-offs; ship
      launchd/systemd service templates for the daemon
- [ ] iOS/Android scaffolds (`apps/ios`, `apps/android`): File-Provider /
      Storage-Access-Framework design notes — what the native apps must implement so the home
      appears in Files/Documents apps (design + stub tasks; native code arrives with the apps)
- [x] Web: changes-feed-driven auto-refresh in the file browser — shipped with
      [EPIC-015](EPIC-015-web-app-ux.md) E15-T4 (5 s poll while the destination is open,
      reloading only when a change touches the folder in view)

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
