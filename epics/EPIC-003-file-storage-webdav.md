# EPIC-003 — Per-identity file storage & WebDAV access

- **Status:** proposed
- **Priority:** P0
- **Depends on:** EPIC-002 (durable per-identity storage)
- **Unlocks:** EPIC-004 (sync), EPIC-005 (sharing), EPIC-006 (conventions), EPIC-009 (attachments)

## Goal

Give every Poweur ID a **home filesystem** hosted by its relay: a private/shared/public file
tree comparable to a personal Dropbox, addressable by identity, accessible over **WebDAV** (so
every OS, library and existing tool can mount it today) and authenticated with Poweur keys. This
filesystem is the substrate for everything that follows: identity documents, contact lists, app
data, shared folders, message attachments and agent pipelines.

## Background (current code & constraints)

- EPIC-002 establishes `$POWEUR_DATA/identities/<id>/` as the durable per-identity directory;
  this epic makes it user-visible and writable.
- Messages cap at 512 KB (`maxMessageBytes` in `apps/api/internal/relay/server.go`) — files are
  explicitly *not* messages; attachments later become file references (EPIC-009).
- Auth primitives already exist and must be reused, not reinvented: challenge-response
  (`GET /auth/challenge`, `apps/api/internal/storage/challenges.go`) and short-lived session
  keys with identity-signed `SessionProof` (`apps/api/internal/relay/sessions.go`). WebDAV auth
  should be a thin bridge to these.
- Go has a maintained WebDAV server implementation (`golang.org/x/net/webdav`) with a pluggable
  `FileSystem` + `LockSystem` — a strong reason to choose WebDAV over a from-scratch protocol
  for the *access* layer. (Sync is a different question — see EPIC-004.)

## Design direction

**Protocol choice: WebDAV for access, custom extension for sync.** WebDAV gives us instant
interop: Finder/Explorer mounting, rclone, davfs2, hundreds of client libraries. What WebDAV is
bad at (efficient delta sync, change feeds, chunked resumable upload) we add as Poweur
extensions alongside, the same way Nextcloud extends SabreDAV (RFC 6578 sync-collection,
chunking endpoints). We do **not** fork WebDAV semantics — a vanilla DAV client must always work
for plain read/write.

**Namespace.** Each identity's tree is served at:

```
https://<relay>/dav/<identity>/            ← canonical
https://<identity>/dav/                    ← vanity alias via Host-routing (wildcard hosting)
```

**Top-level layout** (normative; full spec in EPIC-006):

```
/poweur-sys/          system data — managed, convention-governed
  public/             world-readable (id.json, profile, capabilities) — backs /.well-known/poweur/
  private/            owner + relay only (contacts, device registry, inbox policy, share grants)
/public/              readable by ANY authenticated Poweur ID (valid signature required, no grant)
/shared/              readable/writable per explicit grants (EPIC-005)
/private/             owner (and owner's devices/agents) only
/apps/<app-id>/       per-application data, ACL defaults private (EPIC-006)
```

Note the deliberate trust split the system depends on: `poweur-sys/private` is readable by the
**relay** (it must enforce inbox policy and shares from it) but never by other users. True
end-to-end-encrypted storage (relay-blind) is a later, opt-in layer — see E03-T7.

## Tasks

### E03-T1 — Storage-layer spec: layout, limits, metadata

- [ ] Spec page `apps/docs/docs/files/storage-model.md`: the five top-level roots, who can
      read/write each (owner / relay / contacts / any-valid-ID / grant-holder), reserved names,
      path rules (UTF-8 NFC, max depth/length, forbidden names), file size limits, quota model
- [ ] Metadata model: per-file `etag` (content hash — pick xxh3 or sha256 and justify), mtime,
      size; per-tree change counter (monotonic `change_id`, the backbone for EPIC-004 sync)
- [ ] Decide hard caps for v1 (e.g. 5 GB/identity default quota, 2 GB max file, configurable)
- [ ] Threat-model section: zip-slip/path traversal, symlinks (forbid), case-collision on
      case-insensitive backends, MIME sniffing on public serving

**Acceptance:** spec merged and reviewed; limits reflected in config defaults.

### E03-T2 — WebDAV server on the relay

- [ ] Mount `golang.org/x/net/webdav` handler at `/dav/{identity}/` backed by a custom
      `webdav.FileSystem` over `$POWEUR_DATA/identities/<id>/files/` with the layout-aware
      permission checks from E03-T1
- [ ] Class 2 DAV (locks) via in-memory `LockSystem` per identity; document lock semantics
- [ ] ETags from content hashes stored in the metadata index (E02-T1's SQLite), updated on PUT
- [ ] Host-header vanity routing (`https://alice.poweur.net/dav/`) reusing E01-T2 plumbing
- [ ] Quota enforcement on PUT/MKCOL (`507 Insufficient Storage`), per-identity from config
- [ ] Conformance check with litmus (WebDAV test suite) in CI for the owner-access case

**Acceptance:** macOS Finder / rclone can mount an identity's tree with full read/write on
`/private`; litmus basic+copymove+props suites pass.

### E03-T3 — WebDAV authentication bridge (Poweur sessions → HTTP auth)

WebDAV clients speak Basic/Bearer, not Ed25519. Bridge without weakening the key model:

- [ ] **Bearer tokens**: `POST /auth/dav-token` — request signed by a registered session key
      (same envelope as message signing, reuse `resolveSigningKey`); returns an opaque token
      bound to (identity, scope, expiry ≤ session expiry). Token store is in-memory + revocable
- [ ] **App passwords** for legacy Basic-auth clients (Finder can't do Bearer): owner generates
      named app passwords via CLI/web, stored hashed (argon2id) in `poweur-sys/private/`;
      Basic username = identity, password = app password. Revocation = file edit
- [ ] Scopes: `dav:full`, `dav:read`, path-scoped tokens (`dav:rw:/apps/taskapp/`) — the same
      scope grammar agents use later (EPIC-010)
- [ ] Rate-limit and audit-log auth failures (extend `apps/api/internal/ratelimit/`)
- [ ] CLI: `poweur dav token`, `poweur dav mount` (prints ready-to-paste mount command per OS)

**Acceptance:** integration test obtains a token via session-signed request and PROPFINDs;
Finder mounts with an app password; revoked credentials fail within 60 s.

### E03-T4 — Cross-identity access: `/public` for any valid Poweur ID

The defining feature versus a plain WebDAV host: **other identities** authenticate with *their
own* Poweur ID and get exactly the access the layout grants them (here: read `/public`; EPIC-005
extends to `/shared`).

- [ ] Visitor auth: visitor obtains a DAV token *for someone else's tree* —
      `POST /auth/dav-token` with `audience: bob.example.org`; relay verifies the visitor's
      signature via the resolver chain (EPIC-001) so visitors from any relay/domain work
- [ ] Permission engine maps (visitor-identity, path) → allow/deny per the layout rules;
      `/public` requires only "valid Poweur ID", later consults grants (design the interface
      now, EPIC-005 fills it in)
- [ ] Anonymous (no-auth) requests: only `poweur-sys/public/` (it backs `.well-known`) —
      everything else 401
- [ ] Audit log of cross-identity reads in `poweur-sys/private/logs/access.log` (owner-readable;
      this is also anti-abuse evidence for EPIC-007)
- [ ] Integration test: bob (hosted on relay B in the test harness) reads alice's `/public`
      over WebDAV using a token issued by alice's relay

**Acceptance:** cross-relay public-folder read works end to end; unauthorized paths are denied
with tests for each layout root.

### E03-T5 — Web app file browser

- [ ] File browsing UI in `apps/web/` (vanilla JS like the rest): list, upload, download,
      mkdir, rename, delete against the DAV API; show quota
- [ ] Drag-and-drop upload; progress for large files
- [ ] Visual distinction of the layout roots and their audience (private/shared/public badges)

**Acceptance:** a user can manage files entirely from the web app; works against a fresh relay.

### E03-T6 — Public web serving of `/public` (read-only HTTP)

Sharing a file with the web (not just with IDs) should be a first-class but *explicit* act.

- [ ] `https://<identity>/pub/<path>` serves files the owner marked web-public (per-folder
      marker file `.poweur-web-public` — convention documented in EPIC-006), correct
      Content-Type with sniffing protections, `Content-Disposition` for downloads
- [ ] Directory listings opt-in; default off
- [ ] Bandwidth accounting hooks (quota story for egress later)

**Acceptance:** marked folder is browsable in a plain browser; unmarked `/public` paths are not
web-exposed (ID-auth still required).

### E03-T7 — Design doc: relay-blind (E2EE) storage option

Not implementation — a serious design study so v1 decisions don't paint us into a corner.

- [ ] Survey: Cryptomator-style per-file envelope encryption, Nextcloud E2EE folders lessons,
      age/AES-GCM streaming formats; key distribution to share recipients via Poweur messaging
- [ ] Identify which layout roots can ever be relay-blind (relay *must* read
      `poweur-sys/private` for policy enforcement; `/private` and `/shared` could be blind)
- [ ] Define the v1 hooks to keep: content-hash ETags must not preclude encrypted blobs;
      metadata index must tolerate opaque names
- [ ] Output: `apps/docs/docs/files/e2ee-design.md` with a recommended phased path

**Acceptance:** design doc merged; v1 storage spec (E03-T1) updated with any hooks it requires.
