# EPIC-020 — Storage v2: an end-to-end encrypted drive with a stateless relay

- **Status:** proposed — rewritten 2026-09-25 (replaces the earlier chunked-DAV / split files
  service plan; old task IDs are mapped at the end)
- **Priority:** P0 (pre-launch: changing the storage model now costs nothing in migrations)
- **Depends on:** EPIC-011 (seed-derived identity keys; recovery is now also file recovery),
  EPIC-005 (grant/offer/link/file-request semantics carried over), EPIC-009 (SSE push, message
  envelopes), EPIC-006 (system document schemas)
- **Supersedes:** EPIC-003's storage model (WebDAV, fixed roots, path grants, `relay-fs`
  whole files, E03-T7 opt-in E2EE design), EPIC-004's v1 sync protocol, EPIC-009 E09-T1's
  history layout and E09-T4's plaintext attachment bytes
- **Unlocks:** honest "files are end-to-end encrypted" claims, S3-compatible hosting, desktop
  sync, message-history paging (E15-T13), append-based collaboration (EPIC-024/025), hosted
  agents with explicit key grants (EPIC-027), ciphertext-only managed hosting (EPIC-028)

## Progress

| Task | Status | Notes |
|------|--------|-------|
| **Wave 1 — spec & primitives** | | |
| E20-T1 Storage v2 spec & ADR | **open** | object model, drive layout, API, what is retired; one spec replaces four docs |
| E20-T2 Key tree & encryption format | **open** | Proton-style node keys, encrypted names + name hashes, chunk AEAD, signed manifests; Go + TS vectors |
| E20-T3 Storage providers: filesystem & S3 | **open** | minimal interface, conditional put, presigned URLs, one conformance suite for both |
| **Wave 2 — relay** | | |
| E20-T4 Drive engine | **open** | journal as the database, tree cache, replace/append commits, GC, quota, rebuild from scratch |
| E20-T5 Drive HTTP API & change stream | **open** | chunks, commits, reads, listings, changes feed, SSE with inline appends; replaces `/dav` and `/sync` |
| E20-T6 `.poweur` system files & stateless relay | **open** | settings as files the relay validates and applies; no relay state outside the drive |
| E20-T7 Shares, roles, links & file requests | **open** | shares on any node; read/write/append/create/admin; revocation + key rotation; key-in-fragment links |
| **Wave 3 — clients** | | |
| E20-T8 SDK drive clients (Go + TS) | **open** | encryption, uploads, commits, cursors, chunk caches |
| E20-T9 Sync daemon & merge drivers | **open** | `poweur sync --watch`; Obsidian-style per-type merges; conflicted copies |
| E20-T10 Web & mobile Files on v2 | **open** | Files, Shared with me, share dialog, in-browser link viewer, client-side thumbnails and search |
| E20-T11 Message history & attachments on v2 | **open** | one append file per conversation; encrypted attachments |
| **Wave 4 — cutover** | | |
| E20-T12 Migration & v1 removal | **open** | client-driven re-encryption; remove WebDAV, v1 sync, app passwords, `poweur-sys` |
| **Wave 5 — after launch, demand-led** | | |
| E20-T13 rclone backend | **open** | desktop mount and local `serve webdav/sftp` for third-party tools |
| E20-T14 Native OS file integration | **open** | macOS/iOS File Provider, Windows Cloud Files, Android DocumentsProvider |
| E20-T15 Append performance for live apps | **open** | latency budgets and batching so EPIC-025 can build CRDT/realtime on append files |
| E20-T16 Advanced shares & delegation | **open** | time-box presets, version-pinned snapshots, per-audience caps, resharing |
| E20-T17 Multi-instance relays over one store | **open** | per-identity leases via conditional writes; horizontal scale for EPIC-028 |

## Goal

Every identity gets **a drive**: an end-to-end encrypted tree of files and folders, synced to
its devices, shareable node by node with other identities and by link, and stored on a local
disk or any S3-compatible bucket. The relay authorizes, orders and notifies. It never reads
private or shared content, and it keeps **no durable state outside the drive** — its settings
are files the owner (or the owner's agent) can edit in a synced folder.

## Principles

1. **Everything durable is a file in a drive.** The relay has no database. Its memory holds
   caches (rebuildable from the drive) and ephemeral state (sessions, challenges, rate limits,
   SSE subscribers). Deleting a relay's local caches and restarting it changes nothing
   observable.
2. **End-to-end encrypted by default, modelled on Proton Drive.** Every node has its own key,
   wrapped by its parent's key; names and contents are encrypted; the relay reads only the
   plaintext system files and folders explicitly published as public. A server that needs to
   read something (a hosted agent, a search service) gets a share like any other identity —
   there is no plaintext mode.
3. **No WebDAV on the relay.** A native HTTP/JSON API plus the existing SSE stream. Third-party
   tools reach plaintext through clients on the user's device (sync daemon, rclone backend,
   native file providers).
4. **One primitive: a file version is a list of encrypted chunks.** Two commit kinds —
   **replace** (checked against a base version) and **append** (ordered by the relay, never
   conflicts). Logs, chat history and collaboration streams are append files, not a separate store.
5. **No fixed top-level folders.** The tree is the user's. Sharing and publishing are
   properties of nodes, not of where they sit. The only reserved name is `.poweur/`.
6. **Two storage providers, one small interface.** Filesystem and S3-compatible. Bytes move
   client ↔ bucket directly through presigned URLs where the provider allows it.
7. **Merging is the client's job.** The relay detects conflicts (`409`); clients merge by file
   type, as Obsidian Sync does. CRDTs are an application choice layered on append files.

## Background (current code — what is being replaced)

- **Storage** (`apps/api/internal/files/`, E03): `StorageProvider` with open/read/write/rename,
  `relay-fs` under `POWEUR_DATA/identities/<id>/`, plaintext for every root. ETags are SHA-256
  of content. Fixed roots `/public`, `/shared`, `/private`, `/apps`, `poweur-sys/{public,relay,private}`.
- **Access** is WebDAV (`/dav/{identity}/`, Class 2 locks) with DAV tokens and app passwords
  (E03-T3), plus the web client's DAV client (`packages/client-ts/src/files.ts`).
- **Sync** (E04) is a changes journal with a cursor, a manifest for full resync and a tus-style
  upload at `/sync/{id}/upload` for large files. The relay has no `If-Match`, so concurrent
  writers are detected client-side, if at all.
- **Grants** (E05) are owner-signed documents at `poweur-sys/relay/shares/`, path-scoped,
  checked on every request; links add password, expiry and download caps; file requests and
  Send ride on links.
- **Message history** (E09-T1) is one sealed file per message in month shards, because
  whole-file replacement without preconditions made any shared file a lost-update hazard.
  Loading history costs one GET + unseal per message (see E15-T13).
- **Attachments** (E09-T4) upload plaintext bytes; only the caption is encrypted and the
  filename/MIME travel in plaintext metadata.
- **Relay settings already live in the tree** — contacts, inbox policy, blocks, devices, app
  passwords, groups (`poweur-sys/relay/*`, read by `relay/policy.go` `readSysJSON`). v2 keeps
  this idea and makes it the rule for *all* relay state; the inbox spool and other
  `POWEUR_DATA` side files move into the drive too.

## Design

### The drive as the user sees it

```
~/Poweur/                         any folders the user likes — no reserved roots
  Photos/  Taxes/  Notes/ …        encrypted names and contents
  Website/                         a folder marked public: plaintext, web-servable
  .poweur/
    public/                        plaintext, world-readable: id.json, profile.json, capabilities.json
    relay/                         plaintext, owner-written, relay-read: contacts.json,
                                   inbox-policy.json, blocks.json, publish.json, shares/*.json
    state/                         plaintext, relay-written, owner-read: devices.json,
                                   link-stats.json, inbox (append file), quota.json
    private/                       encrypted, owner only: settings, read marks,
                                   messages/*.jsonl (append files), mounts/ (shared-with-me pointers)
```

- **One writer per file, always.** The owner writes `.poweur/relay/`; the relay writes
  `.poweur/state/`; nobody else writes either. That rules out owner/relay write races by
  construction.
- **Keys are never materialized as files** in a synced folder. Wrapped node keys live in
  version manifests and share documents, not in the tree.

### What the relay can read

| Node | Relay reads content | Why |
|------|--------------------|-----|
| `.poweur/public/*` | yes | identity discovery, `/.well-known/poweur/` |
| `.poweur/relay/*` | yes | it enforces inbox policy, contacts, blocks, shares, publishing |
| `.poweur/state/*` | yes (it writes them) | device registry, link counters, inbound spool of E2E-encrypted envelopes |
| folders marked public | yes | the audience is everyone; `/pub` web serving |
| everything else | **no** | encrypted names and contents |

The relay also necessarily sees **tree shape, node ids, sizes, timestamps, authors, share
documents (who shares what with whom) and access patterns**. The contact list is visible to the
relay because "contacts only" is enforced there. E20-T1 writes this inventory down; the UX and
privacy policy never claim more.

### Keys (after Proton Drive)

- **Node key:** every file and folder has an X25519 key pair. Its private half is sealed to the
  **parent's** node public key; a drive root's is sealed to the identity encryption key
  (EPIC-011 — one key, already on every device). Moving a node re-seals one private key and
  re-encrypts one name; no content is re-encrypted.
- **Names** are encrypted to the parent node key. A **name hash** (HMAC with a key held in the
  parent) lets the relay enforce unique names per folder and resolve lookups without learning
  names.
- **Contents:** each file has a symmetric content key sealed to its node key. Chunks are
  XChaCha20-Poly1305 with a random nonce. Chunk plaintext is at most 4 MiB.
- **Authenticity:** every version manifest (and every append) is signed by the author's
  identity key and names its parent version, so the relay cannot reorder, splice or forge
  history; it can only withhold it.
- **Asymmetric node keys make drop boxes free:** anyone holding a folder's *public* key can add
  a child they cannot read back (file requests, E20-T7).
- **Public folders** are the exception: plaintext names and contents, flagged in the manifest;
  moving a file into one is an explicit, confirmed plaintext re-upload by the client.

### Files, versions and commits

```
chunk      immutable ciphertext, id = SHA-256(ciphertext), ≤ 4 MiB + overhead
version    signed manifest: {node, version, parent_version, kind, chunks:[{id,size}], size,
           wrapped content key, author, sig}
node       {id, parent, enc_name, name_hash, type: file|folder, mode: replace|append, head}
journal    append-only record of every commit in the drive — the relay's only "database"
```

- **Replace commit:** `{node, base_version, chunks}` → `409` with the current head when stale.
  Upload only missing chunks; editing one block of a large file re-uploads one chunk.
- **Append commit:** `{node, chunks}` with no base — the relay orders concurrent appends and
  returns the assigned position. Appends never conflict. Small appends create small chunks; a
  client may later compact many small chunks into large ones with a replace commit.
- **No content-defined chunking.** Fixed-size chunks for uploads, variable for appends. CDC's
  benefit (mid-file inserts) is rare for this product, and its chunk sizes leak information
  about encrypted content.
- **Long files** keep their chunk list in pages so an append rewrites one page, not the whole
  manifest (E20-T1 decides the page format).
- **Readers keep a cursor** `(version, chunk index)` and fetch only what they have not seen.
- **No cross-identity dedup.** Chunks belong to a drive; reuse across versions of the same
  file is the only sharing.
- **Version retention** defaults to a window (E20-T1 decides, e.g. 30 days) — the basis for
  three-way merges, restore and snapshots.

### Sharing (replaces path grants)

- A **share** is an owner-signed document in `.poweur/relay/shares/` naming a **node id** (file
  or folder), members with roles, optional expiry, and the node key sealed to each member.
  The relay enforces membership on every request, as E05-T2 does today; recipients decrypt
  with the key they were sealed.
- **Roles:** `read`, `write`, `append` (append commits only — logs, chat, audit trails),
  `create` (add new children, no read/list — file requests), `admin` (manage members).
  Readers verify each version's author held a writing role.
- **Revocation** removes the member (immediate on the next request) and **rotates** the
  node's keys for future writes; already-downloaded content stays readable, and the UI says so.
- **Links:** `https://<identity>/s/<token>#<secret>`. The fragment never reaches the server.
  The relay gates on token, expiry and caps; an optional password is stretched with argon2id,
  one branch derives the key, another the verifier the relay checks and rate-limits — so the
  relay never learns the password or the key. The viewer is a static page that decrypts in the
  browser.
- **Offers** stay E05-T3's encrypted `sys.share.offer`; accepting writes a mount pointer into
  the recipient's `.poweur/private/mounts/`.
- **Groups** (E05-T5) are members like identities; membership changes rotate keys.

### The stateless relay

- **Everything the relay needs is in drives or recomputable from them:** identity documents,
  settings, shares, device registry, inbound spool, link counters, quotas. Names registered on
  a relay = the identities present in its store.
- **Settings as files:** a commit to `.poweur/relay/*` is validated before it is accepted
  (`422` with a reason otherwise) and applied to the in-memory cache before the commit returns —
  "add contact, then receive their message" is consistent. Signed documents (`id.json`, shares)
  must carry a valid owner signature; the sync daemon signs them on upload. An agent editing
  `~/Poweur/.poweur/relay/contacts.json` on the owner's machine is a supported workflow.
- **Cold start** replays the journal from the latest tree snapshot; snapshots are written
  periodically as files so start-up does not read the whole history.
- **Concurrency:** one relay process owns an identity at a time (in-memory lock). Several
  processes over one bucket (E20-T17) use leases built on conditional writes.

### Providers

The interface is `get(key, range)`, `put(key, bytes)`, `put_if(key, bytes, match)`,
`delete(key)`, `list(prefix)`, and optionally `presign_get/put`. Two implementations:
filesystem (development, small self-hosts; the relay proxies bytes) and S3-compatible (AWS,
R2, MinIO, …; clients upload and download chunks directly via presigned URLs with the SHA-256
checksum signed in, so the bucket rejects mismatched bytes). The bucket sees ciphertext and
metadata only, so a third-party or customer-owned bucket ("bring your own bucket") changes
no trust assumption.

### Sync and merging (Obsidian-style)

Clients merge; the relay only says `409`. Default merge drivers by type:

| Type | On conflict |
|------|-------------|
| `.md`, `.txt` | three-way merge (diff-match-patch) against the last synced version |
| `.json` | key-level merge, local keys over remote |
| append-mode files (`.jsonl`, `.csv`, `.log` created as append) | nothing to merge — appends are ordered by the relay |
| everything else | conflicted copy: `name (conflicted copy <device> <time>).ext` |

Changes arrive over the existing SSE stream (E09-T2); small appends ride inline in the event,
so live updates cost no extra round trip. No WebSocket is required. This gives near-real-time
collaboration on notes, lists, tables and chat. Simultaneous typing in the same paragraph is
the one case that needs a CRDT — that is EPIC-025, built as append files of CRDT updates plus
snapshot files, with no protocol change here.

## Tasks

### E20-T1 — Storage v2 spec & ADR

- [ ] `apps/docs/docs/files/storage-v2.md`: principles, drive layout, node/version/chunk/journal
      model, commit semantics, API, provider layout, relay-readable table and metadata
      inventory; replaces `storage-model.md`, `webdav.md`, `sync-protocol.md` and
      `e2ee-design.md` (kept as history)
- [ ] ADR: why WebDAV, fixed roots, path grants and a relay database are all dropped; why no CDC
- [ ] Decide: manifest encoding (JSON vs CBOR), chunk-list paging for long/append files,
      version retention default, journal segment and tree snapshot formats, name-hash scheme,
      padding buckets for sizes
- [ ] Threat model: malicious relay (withholding, rollback, forked views), stolen bucket or
      backup, compromised device, revoked member with cached keys, fragment leakage (history,
      referrers, chat link previews), abuse reports on links (the reporter supplies the fragment)
- [ ] Privacy policy and store privacy labels drafted against the metadata inventory

**Acceptance:** spec merged with worked examples — new file, edit one chunk of a large file,
concurrent replace + merge, concurrent appends, share + revoke, link with password, file
request, agent edits `contacts.json` locally, relay cold start.

### E20-T2 — Key tree & encryption format

- [ ] `packages/identity` (Go, canonical): node keys, sealing to parent / identity key, name
      encryption + name hash, content keys, chunk AEAD, manifest and append signatures
- [ ] Moves (re-seal + re-name), key rotation after revocation, identity encryption-key
      rotation (EPIC-011 E11-T5) re-sealing only drive roots
- [ ] `@poweur/client` twin; conformance vectors for every construction, including tamper,
      truncation, reorder and cross-node substitution failures

**Acceptance:** Go and TS produce and open byte-identical vectors; every tamper vector fails
with a distinct error.

### E20-T3 — Storage providers: filesystem & S3

- [ ] Provider interface above; filesystem implementation (atomic writes, `put_if` via
      rename + lock); S3 implementation (AWS SDK or minimal signer — record the dependency
      decision), presigned PUT/GET with checksum
- [ ] Conformance suite run against both, with MinIO in CI; document which S3-compatible
      stores support conditional writes and checksums, and the proxy fallback for those that don't
- [ ] Config: `STORAGE_PROVIDER=fs|s3`, bucket, prefix, credentials, presign on/off

**Acceptance:** the same suite passes on the filesystem and on MinIO; a presigned upload with
wrong bytes is rejected by the store.

### E20-T4 — Drive engine

- [ ] Journal (append-only segments) as the source of truth; in-memory tree and quota caches;
      periodic tree snapshots; rebuild from the journal alone
- [ ] Replace commits with `base_version` → `409`; append commits ordered per node; name-hash
      uniqueness per folder
- [ ] Uncommitted chunks expire (24 h); GC marks from live + retained versions and never
      deletes a chunk referenced by an in-flight commit
- [ ] Quota = unique chunk bytes per drive; `507` on overflow; plans hook for EPIC-026

**Acceptance:** fuzzed concurrent replace/append commits never lose an acknowledged commit;
deleting every cache and restarting yields identical listings and quotas; GC under a concurrent
commit fuzz never removes a live chunk.

### E20-T5 — Drive HTTP API & change stream

- [ ] Endpoints under `/drive/{identity}/`: `chunks/missing` (returns presigned or relay URLs),
      chunk upload (filesystem mode), `commit`, node read (head + manifest), chunk read through
      an authorized version, children listing, version history, changes feed with cursor
- [ ] Auth: existing session and signed-challenge auth for owners and visitors; no DAV tokens,
      no app passwords
- [ ] SSE `drive.changed` events per node; appends ≤ 16 KiB carried inline
- [ ] `/pub` and `/.well-known/poweur/` served from public nodes and `.poweur/public`

**Acceptance:** integration suite (`apps/integration`, real relays) covers upload, resume,
replace conflict, append ordering, listing, history and live events across two relays.

### E20-T6 — `.poweur` system files & stateless relay

- [ ] Layout and schemas for `.poweur/{public,relay,state,private}` (EPIC-006 schemas carried
      over from `poweur-sys`); one-writer rule enforced by the relay
- [ ] Commit-time validation and synchronous cache apply for every relay-read file; signed
      documents verified
- [ ] Move relay side state into drives: inbound spool (append file in `.poweur/state/`),
      device registry, link counters; drop app passwords with WebDAV
- [ ] Statelessness test: stop the relay, delete everything except the provider, start it —
      policy, contacts, shares, devices, pending inbox and quotas behave identically
- [ ] Rejection feedback contract for clients (reason codes the sync daemon surfaces)

**Acceptance:** an agent edits `~/Poweur/.poweur/relay/contacts.json` and `inbox-policy.json`
through the sync daemon; the next inbound message is accepted or refused accordingly; an
invalid edit is rejected with a readable reason and never half-applied.

### E20-T7 — Shares, roles, links & file requests

- [ ] Share documents on node ids; roles `read`/`write`/`append`/`create`/`admin`; enforcement
      on every read and commit; author-role verification on the reading client
- [ ] Offers and accepts (E05-T3 bodies) carry sealed node keys; mounts in `.poweur/private/mounts/`
- [ ] Revocation with immediate access removal and key rotation on the next owner write
- [ ] Links with key-in-fragment and the split password verifier; expiry, download caps,
      rate limits; static decrypting viewer with strict CSP and `no-referrer`
- [ ] File requests on `create` + folder public key; guest isolation, quotas and claim flow
      from E05-T6; Send (E05-T7) as links on sealed files
- [ ] Groups as members; membership change rotates keys

**Acceptance:** `TestINT_SHARE_*` equivalents pass on v2; a revoked member's cached keys do
not open post-revocation writes; a link opens in a clean browser and the relay's logs and
store never contain the key or password.

### E20-T8 — SDK drive clients (Go + TS)

- [ ] Encrypt/decrypt, chunking, missing-chunk upload, replace/append commits with retry,
      cursors, change subscription
- [ ] Chunk cache: a directory under the CLI home; IndexedDB in web/shell — immutable, never revalidated
- [ ] Streaming decrypt with range reads for large files and media

**Acceptance:** both SDKs pass the same scenario suite against a real relay on each provider.

### E20-T9 — Sync daemon & merge drivers

- [ ] `poweur sync --watch <dir>`: file watcher, debounce, upload on stable files, atomic
      downloads, trash for deletes, selective sync, ignore rules, resume after sleep
- [ ] launchd and systemd templates; `poweur status` shows progress, conflicts and rejections
- [ ] Merge drivers as in Design; append-mode detection (local growth → append commit, rewrite →
      conflicted copy); overridable per path in `.poweur/private/merge.json`
- [ ] `.poweur/relay` and `.poweur/public` synced as editable files, signed on upload where required

**Acceptance:** two machines editing the same Markdown note offline converge on reconnect with
both edits; a binary conflict yields one conflicted copy; `TestINT_SYNC_01` convergence on v2.

### E20-T10 — Web & mobile Files on v2

- [ ] Files destination, Shared with me, share dialog (people, links, file requests), public
      folder toggle with a plaintext warning
- [ ] Thumbnails and previews generated on the client at upload and stored as encrypted
      sidecars; name search as a client-side index
- [ ] Link viewer page decrypting in the browser, streaming large downloads

**Acceptance:** web e2e — upload, share, open as recipient on another relay, revoke, open a
password link in a clean browser; thumbnails render for encrypted images.

### E20-T11 — Message history & attachments on v2

Replaces E09-T1's layout and closes E09-T4's plaintext-bytes gap.

- [ ] History: `.poweur/private/messages/<peer-hash>.jsonl`, one append file per conversation
      (peer hash keyed so paths do not reveal the social graph); groups keyed by group identity;
      multi-device dedupe by message id
- [ ] SDK: `tail(peer, {limit})`, `before(peer, cursor, {limit})`, `append(records)`; tray loads
      read one tail chunk per conversation
- [ ] Attachments: sealed file + per-file share with the recipient; content key, filename and
      MIME inside the encrypted payload; plaintext metadata keeps only node id, ciphertext size
      and hash
- [ ] CLI `poweur history [peer] --limit --before --thread --json`

**Acceptance:** with 20 conversations and 10k messages the tray fetches ≤ 20 tail chunks;
a 20 MB attachment crosses two relays and the sender's store holds only ciphertext; E15-T13
paging works against it.

### E20-T12 — Migration & v1 removal

- [ ] Client-driven migration on unlock: read v1 via DAV, write v2 encrypted, delete v1 after
      the v2 commit is confirmed; resumable; progress in Settings. (The relay cannot migrate
      for the user: any keys it generated would be keys it knows.)
- [ ] `poweur-sys/*` → `.poweur/*`; fixed roots become ordinary folders; path grants become
      node shares; links keep working (new URL with fragment)
- [ ] Remove `/dav`, `/sync` v1, DAV tokens, app passwords, `relay-fs` whole-file code and the
      v1 docs; port the integration suites
- [ ] Backups: document when the last plaintext copy of a migrated identity expires

**Acceptance:** a seeded v1 identity with files, shares, links, history and attachments
migrates across a kill/restart, and no plaintext outside relay-readable nodes remains on disk.

### E20-T13 — rclone backend

- [ ] Poweur backend for rclone (Go, reusing `packages/identity`): list, read, write, mount;
      decrypts on the device
- [ ] Recipes for `rclone mount` and `rclone serve webdav|sftp` for Finder, Joplin, Obsidian
      and other WebDAV tools; update INT-000-T3 and INT-004 to point here

**Acceptance:** Finder mounts the drive through `rclone mount` and round-trips a file.

### E20-T14 — Native OS file integration

- [ ] macOS/iOS File Provider, Windows Cloud Files API, Android DocumentsProvider — files on
      demand; picks up E04-T5's deferred desktop/mobile items

**Acceptance:** per platform, a file opened from the OS file browser downloads on demand and
an edit syncs back.

### E20-T15 — Append performance for live apps

- [ ] Budgets and measurements: append-to-remote-render latency on both providers; batching of
      chunk-list page writes on S3; inline SSE payloads
- [ ] Guidance for EPIC-025: CRDT updates as append records, snapshots as replace-mode files,
      presence over rooms (E25-T7), compaction by clients

**Acceptance:** p50 append-to-remote-event under 150 ms on the filesystem provider and under
400 ms on S3 in the same region.

### E20-T16 — Advanced shares & delegation

- [ ] Expiry presets and `not_before`; version-pinned snapshot shares; per-audience download and
      byte caps (generalizing link caps)
- [ ] Resharing through `admin`, or attenuated delegation tokens (evaluate UCAN / Biscuit /
      macaroons) — share documents stay the source of truth
- [ ] Recorded non-goal: "view but don't download" is not a security control

**Acceptance:** a snapshot share never shows later edits; a delegated share dies when the root
share is revoked.

### E20-T17 — Multi-instance relays over one store

- [ ] Per-identity leases with conditional writes; request routing to the owning instance;
      lease takeover on failure
- [ ] Deployment profile for EPIC-028

**Acceptance:** killing the owning instance moves the identity to another within the lease
timeout without losing an acknowledged commit.

## Non-goals

- WebDAV on the relay; a relay database; fixed top-level roots
- Relay-side reading, search, previews or scanning of encrypted content
- Content-defined chunking and cross-identity dedup
- Server-side merging; the relay never interprets file formats
- Recovering files for someone who loses their seed and every device (EPIC-011 owns recovery)
- DRM-style "view but not copy"

## Superseded task IDs (the pre-2026-09-25 plan)

| Old | Now |
|-----|-----|
| E20-T1 Research & decision record | E20-T1 |
| E20-T2 Object model & vectors | E20-T2 |
| E20-T3 Chunk store & commit API | E20-T4, E20-T5 |
| E20-T4 WebDAV as a view | dropped — no relay WebDAV; E20-T13 for tools |
| E20-T5 Chunk encryption & key domains | E20-T2 |
| E20-T6 SDK sync engines | E20-T8, E20-T9 |
| E20-T7 Migration & negotiation | E20-T12 |
| E20-T8 Message history v2 | E20-T11 |
| E20-T9 Authorization for versions & chunks | E20-T7 |
| E20-T10 `append` / `create` permissions | E20-T7 |
| E20-T11 Advanced share types | E20-T16 (excerpt shares dropped) |
| E20-T12 Delegation & capability tokens | E20-T16 |
| E20-T13–T16 Separable files service | dropped — the bucket is the data plane; E20-T17 for scale |
