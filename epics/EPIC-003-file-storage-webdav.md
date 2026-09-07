# EPIC-003 — Per-identity file storage & WebDAV access

- **Status:** complete (`relay-fs` shipped; S3 provider deferred)
- **Priority:** P0
- **Depends on:** EPIC-002 (durable per-identity storage)
- **Unlocks:** EPIC-004 (sync), EPIC-005 (sharing), EPIC-006 (conventions), EPIC-009 (attachments)

## Progress

| Task | Status | Notes |
|------|--------|-------|
| E03-T1 Storage-layer spec | **done** | [`apps/docs/docs/files/storage-model.md`](../apps/docs/docs/files/storage-model.md) |
| E03-T2 WebDAV server | **done** | `/dav/{identity}/` + Host vanity; Class 2 in-mem locks; etags; quota 507 |
| E03-T3 Auth bridge | **done** | `POST /auth/dav-token`, app passwords in `poweur-sys/relay/`, CLI `poweur dav` |
| E03-T4 Cross-identity `/public` | **done** | Visitor tokens + access.log; integration `TestINT_DAV_02` |
| E03-T5 Web file browser | **done** | shipped as `apps/web/js/files.js` + a SPA panel; since E15-T6/T1 the DAV client is `packages/client-ts/src/files.ts` and the browser is a first-class Files destination. Vitest/Playwright coverage |
| E03-T6 `/pub` web serving | **done** | `.poweur-web-public` marker; Host-routed `/pub/` |
| E03-T7 E2EE design doc | **done** | [`apps/docs/docs/files/e2ee-design.md`](../apps/docs/docs/files/e2ee-design.md) |
| E03-T8 Storage providers | **done** (relay-fs) | `StorageProvider` interface; `STORAGE_PROVIDER=relay-fs`; S3 deferred |

**Litmus CI deferred** (manual/optional): owner-access covered by Go unit + `apps/integration` DAV suite.

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
  relay/              owner + RELAY (config the relay must read to function: contacts,
                      inbox policy, device registry, share grants, app passwords, logs)
  private/            OWNER ONLY — relay stores but must not read; contents that live on the
                      relay are encrypted to the owner's X25519 key (e.g. storage-credentials)
/public/              readable by ANY authenticated Poweur ID (valid signature required, no grant)
/shared/              readable/writable per explicit grants (EPIC-005)
/private/             owner (and owner's devices/agents) only
/apps/<app-id>/       per-application data, ACL defaults private (EPIC-006)
```

The trust split is explicit in the tree: `poweur-sys/relay` is the *relay-readable* config zone
(the relay must enforce inbox policy and shares from it; never visible to other users), while
`poweur-sys/private` is *owner-only* — the relay hosts and syncs those bytes but has no business
reading them, and anything sensitive placed there is encrypted client-side to the owner's key.
True end-to-end-encrypted storage for the general roots (relay-blind) is a later, opt-in
layer — see E03-T7.

**Why not git.** Decision record (2026-07): git-over-HTTPS could reuse the same token bridge
(E03-T3), but git's ACL boundary is the *repo* — per-path **read** ACLs inside one repo are
structurally impossible (a clone ships every object + full history). Our model needs per-path
audiences (`public`/`private`/`shared`/`poweur-sys`), cheap revocation (git history retains
shared data forever), large binaries (would force LFS), and non-developer conflict UX
(conflicted-copy rename, not merge markers). WebDAV + changes journal (EPIC-004) keeps the
useful 20% of git — content-hash etags and "what changed since X" — without the history
liability. Git remains welcome as an *app layer* (relay-hosted forge, INT-004; git working
trees inside `/apps/...`).

**Storage providers.** The relay accesses file bodies through a `StorageProvider` adapter, not
the OS filesystem directly:

- **`relay-fs` (v1, shipped):** all roots under a configurable directory (reuses `POWEUR_DATA`
  from EPIC-002, `identities/<id>/`). The relay sees all bytes; this is the mode where
  relay-mediated everything (DAV, shares, public serving) just works.
- **`s3` (deferred):** S3-compatible backends (AWS, MinIO, R2). The adapter interface is
  reviewed against S3 semantics (no atomic rename, virtual dirs, eventual list consistency)
  so a provider can plug in without reworking DAV/permissions. The S3 gateway may be hosted
  by the same relay operator (one root key managing prefixes) or as a **separate service** —
  undecided; the adapter must not care. Sketch when it lands: relay keeps
  `poweur-sys/public` + `poweur-sys/relay` (and mediates `/public` + `/shared`); owner
  `/private` goes client→S3 with credentials encrypted at
  `poweur-sys/private/storage-credentials.json`. Target state: OIDC/STS once EPIC-008 exists.

## Tasks

### E03-T1 — Storage-layer spec: layout, limits, metadata

- [x] Spec page `apps/docs/docs/files/storage-model.md`: the five top-level roots, who can
      read/write each (owner / relay / contacts / any-valid-ID / grant-holder), reserved names,
      path rules (UTF-8 NFC, max depth/length, forbidden names), file size limits, quota model
- [x] Metadata model: per-file `etag` (content hash — SHA-256), mtime, size; per-tree change
      counter (monotonic `change_id`, the backbone for EPIC-004 sync)
- [x] Decide hard caps for v1 (5 GB/identity default quota, 2 GB max file, configurable)
- [x] Threat-model section: zip-slip/path traversal, symlinks (forbid), case-collision on
      case-insensitive backends, MIME sniffing on public serving

**Acceptance:** spec merged and reviewed; limits reflected in config defaults.

### E03-T2 — WebDAV server on the relay

- [x] Mount `golang.org/x/net/webdav` handler at `/dav/{identity}/` backed by a custom
      `webdav.FileSystem` over the `StorageProvider` interface (E03-T8) — `relay-fs` v1
      implementation reads `$POWEUR_DATA/identities/<id>/` — with the layout-aware
      permission checks from E03-T1
- [x] Class 2 DAV (locks) via in-memory `LockSystem` per identity; document lock semantics
- [x] ETags from content hashes stored in the metadata index, updated on PUT
- [x] Host-header vanity routing (`https://alice.poweur.net/dav/`) reusing E01-T2 plumbing
- [x] Quota enforcement on PUT/MKCOL (`507 Insufficient Storage`), per-identity from config
- [ ] ~~Conformance check with litmus in CI~~ — **deferred** (covered by unit + integration
      DAV suites; litmus remains a manual/optional check)

**Acceptance:** macOS Finder / rclone can mount an identity's tree with full read/write on
`/private`; Go unit + `apps/integration` DAV suites pass for owner access.

### E03-T3 — WebDAV authentication bridge (Poweur sessions → HTTP auth)

WebDAV clients speak Basic/Bearer, not Ed25519. Bridge without weakening the key model:

- [x] **Bearer tokens**: `POST /auth/dav-token` — request signed by a registered session key
      (same envelope as message signing, reuse `resolveSigningKey`); returns an opaque token
      bound to (identity, scope, expiry ≤ session expiry). Token store is in-memory + revocable
- [x] **App passwords** for legacy Basic-auth clients (Finder can't do Bearer): owner generates
      named app passwords via CLI/web, stored hashed (argon2id) in `poweur-sys/relay/`;
      Basic username = identity, password = app password. Revocation = file edit
- [x] Scopes: `dav:full`, `dav:read`, path-scoped tokens (`dav:rw:/apps/taskapp/`) — the same
      scope grammar agents use later (EPIC-010)
- [x] Rate-limit and audit-log auth failures (extend `apps/api/internal/ratelimit/`)
- [x] CLI: `poweur dav token`, `poweur dav mount` (prints ready-to-paste mount command per OS)

**Acceptance:** integration test obtains a token via session-signed request and PROPFINDs;
Finder mounts with an app password; revoked credentials fail within 60 s.

### E03-T4 — Cross-identity access: `/public` for any valid Poweur ID

The defining feature versus a plain WebDAV host: **other identities** authenticate with *their
own* Poweur ID and get exactly the access the layout grants them (here: read `/public`; EPIC-005
extends to `/shared`).

- [x] Visitor auth: visitor obtains a DAV token *for someone else's tree* —
      `POST /auth/dav-token` with `audience: bob.example.org`; relay verifies the visitor's
      signature via the resolver chain (EPIC-001) so visitors from any relay/domain work
- [x] Permission engine maps (visitor-identity, path) → allow/deny per the layout rules;
      `/public` requires only "valid Poweur ID", later consults grants (design the interface
      now, EPIC-005 fills it in)
- [x] Anonymous (no-auth) requests: only `poweur-sys/public/` (it backs `.well-known`) —
      everything else 401
- [x] Audit log of cross-identity reads in `poweur-sys/relay/logs/access.log` (owner-readable;
      this is also anti-abuse evidence for EPIC-007)
- [x] Integration test: bob (hosted on relay B in the test harness) reads alice's `/public`
      over WebDAV using a token issued by alice's relay

**Acceptance:** cross-relay public-folder read works end to end; unauthorized paths are denied
with tests for each layout root.

### E03-T5 — Web app file browser

- [x] File browsing UI in `apps/web/` (vanilla JS like the rest): list, upload, download,
      mkdir, rename, delete against the DAV API; show quota
- [x] Drag-and-drop upload; progress for large files
- [x] Visual distinction of the layout roots and their audience (private/shared/public badges)

**Acceptance:** a user can manage files entirely from the web app; works against a fresh relay.

### E03-T6 — Public web serving of `/public` (read-only HTTP)

Sharing a file with the web (not just with IDs) should be a first-class but *explicit* act.

- [x] `https://<identity>/pub/<path>` serves files the owner marked web-public (per-folder
      marker file `.poweur-web-public` — convention documented in EPIC-006), correct
      Content-Type with sniffing protections, `Content-Disposition` for downloads
- [x] Directory listings opt-in; default off
- [x] Bandwidth accounting hooks (quota story for egress later)

**Acceptance:** marked folder is browsable in a plain browser; unmarked `/public` paths are not
web-exposed (ID-auth still required).

> Active HTML as a real site at the identity (or a sister host), plus contact forms → messages,
> is **not** this task — see [EPIC-012](EPIC-012-identity-websites.md) (design notes).

### E03-T7 — Design doc: relay-blind (E2EE) storage option

Not implementation — a serious design study so v1 decisions don't paint us into a corner.

- [x] Survey: Cryptomator-style per-file envelope encryption, Nextcloud E2EE folders lessons,
      age/AES-GCM streaming formats; key distribution to share recipients via Poweur messaging
- [x] Identify which layout roots can ever be relay-blind (relay *must* read
      `poweur-sys/relay` for policy enforcement; `poweur-sys/private`, `/private` and
      `/shared` could be blind)
- [x] Define the v1 hooks to keep: content-hash ETags must not preclude encrypted blobs;
      metadata index must tolerate opaque names
- [x] Output: `apps/docs/docs/files/e2ee-design.md` with a recommended phased path

**Acceptance:** design doc merged; v1 storage spec (E03-T1) updated with any hooks it requires.

### E03-T8 — Storage provider abstraction (`relay-fs` first, `s3`-ready)

Lands with/before E03-T2 so the DAV layer never assumes local disk. v1 implements only
`relay-fs`; `s3` is deliberately deferred — the deliverable here is an interface robust enough
that it plugs in later without touching the DAV/permission layers.

- [x] `StorageProvider` interface in the relay (open/read/write/delete/stat/list/rename +
      content-hash etag hooks), consumed by the DAV `FileSystem` and later by sync endpoints
      (EPIC-004); no `*os.File` or path-on-disk leaks through the interface
- [x] `relay-fs` provider: configurable root (reuse `POWEUR_DATA`,
      `identities/<id>/`); v1 default; the relay mediates all roots in this mode
- [x] Provider selection via config (`STORAGE_PROVIDER=relay-fs`, default) with validation
- [x] `s3` provider: **not implemented in v1** — design notes above; interface reviewed
      against S3 semantics (no atomic rename, no real directories, eventual list consistency)
      so the contract doesn't assume POSIX
- [x] Credential note: future client-direct S3 credentials live encrypted at
      `poweur-sys/private/storage-credentials.json` (owner-only zone; relay cannot read)

**Acceptance:** DAV suite runs entirely through the provider interface; grepping the DAV/
permission packages shows no direct `os.*` file access; a doc comment records the S3
semantics review.
