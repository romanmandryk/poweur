# EPIC-020 — Storage v2: an end-to-end encrypted drive with a stateless relay

- **Status:** in progress — v1 removed on master; deployment blocked until baseline + migration; rewritten 2026-09-25 (replaces the earlier chunked-DAV / split files
  service plan; old task IDs are mapped at the end)
- **Priority:** P0 (pre-launch: changing the storage model now costs nothing in migrations)
- **Depends on:** EPIC-011 (seed-derived identity keys; recovery is now also file recovery),
  EPIC-005 (grant/offer/link/file-request semantics carried over), EPIC-009 (SSE push, message
  envelopes), EPIC-006 (system document schemas)
- **Supersedes:** EPIC-003's storage model (WebDAV, fixed roots, path grants, `relay-fs`
  whole files, E03-T7 opt-in E2EE design), EPIC-004's v1 sync protocol, EPIC-009 E09-T1's
  history layout and E09-T4's plaintext attachment bytes
- **Gated by:** [EPIC-031](EPIC-031-reference-app-scenarios.md) — waves 2–3 are done only when
  the headless reference apps (Markdown docs, site + contact + newsletter, forms, board, CRM,
  whiteboard) pass on one relay and across relays
- **Unlocks:** honest "files are end-to-end encrypted" claims, S3-compatible hosting, desktop
  sync, message-history paging (E15-T13), append-based collaboration (EPIC-024/025), hosted
  agents with explicit key grants (EPIC-027), ciphertext-only managed hosting (EPIC-028)

## Progress

| Task | Status | Notes |
|------|--------|-------|
| **Wave 1 — spec & primitives** | | |
| E20-T1 Storage v2 spec & ADR | **in progress** | Target spec and ADR written; exact signed formats/vectors and final privacy/store copy remain open |
| E20-T2 Key tree & encryption format | **done** | Shared domain-separated seals, key wrapping, padded context-bound XChaCha20 chunks, NFC names/name hashes, signed/sealed author-chain records, and signed version manifests with 1024-ref chunk-list pages (create/replace/move/rotate/remove, drop-box name tokens); Go↔TS vectors for all |
| E20-T3 Storage providers: filesystem & S3 | **done** | Interface, filesystem and minio-go S3. Conformance and a mismatched presigned checksum verified on local Docker MinIO. Relay config is `STORAGE_PROVIDER=fs\|s3` |
| **Wave 2 — relay** | | |
| E20-T4 Drive engine | **in progress** | journal as the database, tree cache, replace/append commits, append positions, prefix trim, group commit, GC, quota, rebuild from scratch |
| E20-T5 Drive HTTP API & change stream | **in progress** | chunks, commits, reads from a position, listings, changes feed, SSE with inline appends for owners and share members across relays; replaces `/dav` and `/sync` |
| E20-T6 `.poweur` system files & stateless relay | **in progress** | settings as files the relay validates and applies; no relay state outside the drive |
| E20-T7 Shares, roles, links & file requests | **in progress** | shares on any node; read/write/append/create/admin; inheritance; caps + PoW; revocation + key rotation; key-in-fragment links; ownership transfer |
| **Wave 3 — clients** | | |
| E20-T8 SDK drive clients & CLI (Go + TS) | **done** | Encrypted file and append workflows, missing-chunk and presigned uploads, change subscriptions, directory and IndexedDB chunk caches, range reads, scoped handles, event-log helper, and `poweur drive` collaboration commands with `--json` |
| E20-T9 Sync daemon & merge drivers | **open** | `poweur sync --watch`; Obsidian-style per-type merges; conflicted copies |
| E20-T10 Web & mobile Files on v2 | **open** | Files, Shared with me, share dialog, in-browser link viewer, client-side thumbnails and search |
| E20-T11 Message history & attachments on v2 | **in progress** | Append-log history and CLI attachments (share + sealed payload) are in; web download, the 20 MB cross-relay case and the rest of the Phase 9 restore list remain |
| **Wave 4 — cutover** | | |
| E20-T12 Migration & v1 removal | **in progress** | v1 implementation removed; system-only operator migration and production rehearsal remain open; no deployment |
| **Wave 5 — after launch, demand-led** | | |
| E20-T13 rclone backend | **open** | desktop mount and local `serve webdav/sftp` for third-party tools |
| E20-T14 Native OS file integration | **open** | macOS/iOS File Provider, Windows Cloud Files, Android DocumentsProvider |
| E20-T15 Append performance for live apps | **open** | latency budgets and batching so EPIC-025 can build CRDT/realtime on append files |
| E20-T16 Advanced shares & delegation | **open** | time-box presets, version-pinned snapshots, resharing (caps moved to E20-T7) |
| E20-T17 Multi-instance relays over one store | **open** | per-identity leases via conditional writes; horizontal scale for EPIC-028 |

## Continuation checkpoint — 2026-09-28

The supplied ten-phase plan is authoritative. Do not push/deploy master until Phase 9
and the Phase 10 migration rehearsal are complete. Work started in `99901a9` during
Phase 0; no drive engine/provider existed at that checkpoint.

- [x] Remove v1 relay, SDK, CLI and web file/share/sync surfaces and reference apps.
- [x] Withdraw PCP-0003/0005/0007 registry entries; retain schema identifiers as historical
      compatibility identifiers. Remove v1 storage docs and upload dashboard metric.
- [x] Correct OAuth avatar tests to the well-known avatar path; stamp package versions.
- [x] Serialize temporary system-file conditional writes/deletes; concurrency regression test.
- [ ] Finish remaining historical wording and deploy references; identity-store disk paths
      intentionally remain legacy until Phase 6 and the migrator.
- [x] Write replacement storage target spec and ADR.
- [x] Extract canonical message sealing into `packages/identity/seal.go`; preserve
      frozen v1 message compatibility, reject malformed nonce lengths, add drive domains.
- [x] Add Go/TS key wraps, canonical contexts, padded XChaCha20 chunks and hash IDs;
      deterministic drive-seals/chunks vectors cover decryption and byte-identical re-encryption.
- [x] Add NFC encrypted names/name index and signed/sealed append records, with
      per-author duplicate/reorder/gap/fork checks and Go↔TS conformance vectors.
- [x] Complete Phase 2: signed manifests/pages, sealed creates, moves/key rotation
      and their failure vectors (`drive/manifest.go`, `src/drive/manifest.ts`,
      `drive-manifests.json`).
- [x] S3 on Hetzner (Ceph): bare `If-Match`, idempotent conditional writes on every
      provider, and a startup probe that refuses `S3_PRESIGN=1` where presigned
      checksums are not enforced.
- [x] Add provider contract and filesystem implementation with range reads, atomic
      conditional writes, durable rename, safe paths, restart and race conformance.
- [x] Verify the S3 provider against local Docker MinIO and select `fs` or `s3`
      from relay configuration. GitHub Actions does not run MinIO. The drive
      engine that publishes through this store is Phase 4.
- [x] Phase 4 core engine (`apps/api/internal/drive/engine`): journal segments with
      create-only publish, snapshots validated against the journal, catch-up replay of
      segments published by another process, replace/append/trim commits, idempotent
      request IDs, quota, version retention and orphan GC. Tests run on fs and, with
      `POWEUR_TEST_S3_*`, on an existing S3 bucket (verified on MinIO). Still open in
      E20-T4: group commit, per-member accounting, journal compaction.
- [x] Phase 5 owner drive API (`internal/relay/drive_api.go`): uploads (relay or presigned
      S3), commits, reads, history, changes, append tails and `drive.changed` events;
      integration `INT_DRIVE_01` covers resume, conflict, appends, live events, a
      cross-relay visitor, restart over the same data and a plaintext scan. Member
      streams, public serving and presigned downloads wait on E20-T6/T7.
- [x] Phase 6: system files are the drive's journalled system zone; identity index, spool,
      acks and keystore live on the provider under `relay/`; the relay writes nothing else
      (an S3 relay needs no `POWEUR_DATA`). Replaces the Phase 0 file-backed adapter.
      Open: connected apps as relay state (Phase 9), sync-daemon rejection codes.
- [x] Phase 7 slice 1: signed shares (Go/TS/vectors), roles with inheritance enforced on
      every read and commit, cross-relay members, filtered changes/events with revocation
      closing streams, revocation → `rotate_required` (`INT_DRIVE_02`).
- [x] Phase 7 slice 2: links (link-ID auth, password verifier with throttling, expiry,
      durable download caps, hourly caps) and share caps charged durably per share.
- [x] Phase 7 slice 3: anonymous writes through links with self-certifying guest authors
      and proof-of-work; file requests (`INT_DRIVE_03`).
- [x] Phase 7 slice 4: groups as members and Spaces; roster removals journal group
      revocations that force rotation.
- [x] Phase 7 source retirement and link forwarding (commit `e17fddb`), with a
      follow-up authorization fix: moved-node destinations require read access,
      including after transfer revokes a former member. `INT_DRIVE_04` covers
      owner access and denial to retired/unrelated identities.
- [x] Phase 7 remote groups: presented rosters verified against the group relay's
      epoch, cached and rechecked every minute, diffed to revoke departed members.
- [ ] Phase 7 remaining: client-orchestrated ownership transfer, offers/accepts
      and mounts, link viewer.
- [x] Phase 8: Go/TS drive clients and both CLIs. Challenge authentication,
      missing-chunk and presigned uploads, verified downloads, idempotent commit
      retries, cursors, and `drive.changed` subscriptions. Encrypted
      mkdir/put/get/mv/rm/list, append/tail/history/trim, share and link
      commands, and transfer. Chunk caches (CLI directory and IndexedDB),
      range reads, scoped handles, and the event-log helper.
- [x] Phase 8 rework (review 2026-09-29): members and link holders open shared
      nodes with their own share key (verified share signature and issuer
      authority, node public key match); version and record authors are checked
      against the node's shares via `GET /nodes/{node}/shares`, including guest
      (link) authors and group members; new versions are authored by the
      caller. Both CLIs take `--drive <identity>` (the drive's own relay),
      `shared` lists entry points, member paths start at `/<shared-node-id>`,
      and transfer copies into a destination as its owner or as a member with a
      share there — no test or command uses another identity's private keys
      (`INT_DRIVE_06`, TS live-relay member test).
- [ ] Phases 9–10: sync daemon, web and mobile Files, complete baseline, migration and production rehearsal.

**Inherited implementation deviation (resolved in Phase 6):** Phase 0 kept an
operational owner-authenticated system-file API instead of the plan's unavailable
placeholder. Phase 6 moved its public/relay/state documents into the journalled
drive system zone. The private API still refuses history/log paths; those need
client-side encryption and append files. Connected apps remain owner-written
until the Phase 9 state migration.

### Phase 9 restore list (must not be silently dropped)

Restore these deleted v1 scenarios against the drive, using pre-removal commit `cc06649`
as the test reference; retain already ported temporary system-file tests as regression coverage:

- [x] Existing v2-adapter integration CONTACTS_02/04, PROFILE_01, GROUP_01 pass.
- [x] Restore PROFILE_02 public-avatar durability across a relay restart and
      DEVICES_03 relay-managed registry read/write/delete enforcement against v2.
- [x] TS system-file/device/drive APIs support explicit session-key authentication;
      live-relay tests cover reads/writes and rejection after session revocation.
- [x] Integration HISTORY_01: an inbox drain and a sent copy land in per-peer append
      logs, a second pickup does not duplicate them, and the provider holds no plaintext.
      HISTORY_02–05, TYPED_07, DEVICES_01–02, SIGNIN_01 and ABUSE_03 remain open.
      The full profile/web/cross-relay avatar acceptance remains below.
- [ ] SIGNIN_02 browser-bound completion returns with the EPIC-031 replacement RP;
      the removed test depended directly on the retired Guestbook server.
- [ ] SDK history journeys, Go/TS history cursors and private sign-in consent log.
- [ ] Web durability suite: received and sent messages survive reload, unread state
      persists, anonymous history persists, relay archive is ciphertext. Restore the
      conversation paging-after-reload and journey per-identity archive-isolation
      assertions and the stranger/contact tray persistence journey removed from the
      still-active messaging browser suites.
- [ ] Profile editor/avatar persistence, public lookup and OAuth picture claim, peer avatar
      visibility on a different relay.
- [ ] Encrypted attachment upload/open: CLI send/save keeps name, MIME and bytes out of
      the provider (`INT_HISTORY_02`). Still open: 20 MB across two relays, web download,
      and malformed/missing/revoked attachment failures.
- [ ] Contacts, policy, blocks export/import, devices/session revocation, connected apps,
      analytics consent, group create/add/remove and quota display on v2.

Files/sharing/attachment/durability browser suites that still invoked removed DAV APIs
are removed during Phase 0. Files, direct-share and link UI suites return in E20-T10;
Tasks/Guestbook return via EPIC-031; sync via E20-T9. The onboarding upload/share
flow and keyboard folder navigation return in E20-T10. Messaging-only browser coverage stays.

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
8. **Apps have no servers.** An app's state lives in exactly one host drive; collaborators get a
   share; outsiders write through links or messages; the app's code never holds a key.

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

### Append files as ordered event logs

The relay assigns every append a **position** in a total order. That makes an append file a
shared event log with a central sequencer: every client folds the same records in the same
order through a deterministic reducer and converges, with last-writer-wins per field by
position. This covers boards, CRMs, forms, comments, activity feeds and object-level
whiteboards (Figma's multiplayer uses the same server-ordered, last-writer-wins-per-property
model). Only fine-grained concurrent text editing needs a CRDT, and its updates are records in
an append file too.

- **Records** are the unit of an append: each is signed by its author and carries a per-author
  sequence number, so the relay can order across authors but cannot drop, duplicate or reorder
  one author's records undetected.
- **Snapshots** are replace-mode files that say "folded up to position N". New readers load the
  snapshot and read the log from N.
- **Trim:** the owner (or an `admin`) may drop the prefix before a snapshot's position, which
  bounds growth.
- **Group commit:** the relay buffers appends for a short window (≈ 50–100 ms) and writes them
  as one log segment, so tiny appends do not cost one provider write each (essential on S3).
- **Reducers enforce app roles:** because records are signed and share roles are readable by
  members, a reducer ignores operations from authors whose role does not allow them.

### Sealed appends and creates: writing without reading

A file's content key is symmetric, so anyone who could encrypt a normal chunk could also read
the file. Writers who must not read — form respondents, commenters with comment-only access,
guestbook visitors, file-request uploaders — seal each record (or each new file) to the
node's **public** key instead. Only holders of the node's private key (the owner and readers)
can open them; a client may later compact sealed records into normal chunks. The relay
enforces the `append` or `create` role, caps and proof-of-work, and never reads the records.

### Who hosts and who pays

- **Personal apps:** the user's own drive and quota.
- **Shared app state:** exactly one host drive — by default the creator's. Every member's
  writes count against the host's quota (as files in Google Drive count against their owner),
  so shares carry **per-member and per-link caps** (bytes, records, rate).
- **Team or long-lived state:** hosted by a **group identity** (an EPIC-024 Space) whose drive
  is billed to an organization or a sponsoring member (EPIC-026 pooled storage), so no single
  member leaving takes it down.
- **Ownership transfer** moves a subtree between drives (a person → a Space, or to another
  person): ciphertext chunks are copied, the subtree root key is re-sealed, shares are
  re-issued by the new owner. Nothing is decrypted by the relay.
- **Public-facing inputs** (forms, contact, signups) are paid by the owner and bounded by
  link caps, rate limits and proof-of-work.

### Apps and storage access

Apps run as static code on a sandboxed origin (EPIC-029); the host frame holds keys and hands
the app a **scoped drive handle**: the folder created for the app at install plus the nodes the
user explicitly opens with it (a picker grant, like Google Drive's `drive.file` scope). Headless
agents and tests use the same scoped handle from the SDK. There is no reserved `/apps` root.

### Build vs adopt

Almost every piece exists as a library; what is new is the combination. Adopt these rather than
writing our own, and keep the list current in the E20-T1 ADR:

| Piece | Adopt | Notes |
|-------|-------|-------|
| Encryption primitives | `golang.org/x/crypto`, `@noble/ciphers`/`curves`/`hashes` | already the only crypto dependencies |
| Sealing to a public key (node keys, sealed appends/creates) | Existing ephemeral X25519 + HKDF-SHA256 + ChaCha20-Poly1305 message construction, moved into shared identity code | Domain-separated drive seals; messages keep `poweur/msg/v1`; no age/HPKE dependency |
| Chunk AEAD | XChaCha20-Poly1305 from the libraries above | no format library needed |
| Link password stretching | argon2id (`x/crypto/argon2`, `@noble/hashes`) | — |
| S3 provider | `minio-go` | browsers use presigned URLs with plain `fetch`; no S3 SDK in TS |
| Sync daemon file watching | `fsnotify` (+ `fsnotify/fsevents` on macOS) | study Syncthing's scanner/ignores (MPL: ideas, not code) |
| Ignore rules | a gitignore matcher (e.g. `go-git`'s) | gitignore syntax, not our own |
| Text merge driver | diff-match-patch (`sergi/go-diff`, Google's JS) for prose; `node-diff3` for line files | the Obsidian approach |
| Desktop mount and tool access | **rclone** backend (E20-T13) | gives mount, sync and `serve webdav/sftp` |
| Real-time text (EPIC-025) | **Yjs** (or Automerge / Loro) | the relay never parses it |
| Encrypted CRDT sync patterns | secsync; Ink & Switch Keyhive/Beelay research | borrow design, possibly code |
| Large-group key agreement (later) | MLS (OpenMLS, ts-mls) | only if per-member sealing stops scaling |
| Delegation tokens (E20-T16) | Biscuit or UCAN | — |
| Client-side local index | bbolt or pure-Go SQLite (`modernc.org/sqlite`); IndexedDB via `idb` | "no database" applies to the relay only |

**Written here:** the key-tree layout, manifests, the journal, share documents, `.poweur`
validators and the event-log helper — each small and protocol-specific.

**Not adopted:** OpenPGP (Proton's choice; heavy; existing message sealing supplies the needed construction). **Studied as prior
art, not built on:** Peergos (cryptree key tree + sandboxed apps — read before E20-T2), Solid and
remoteStorage (apps writing to user storage, not E2E encrypted), AT Protocol (signed user
repositories, public-first).

## Tasks

### E20-T1 — Storage v2 spec & ADR

- [ ] `apps/docs/docs/files/storage-v2.md`: principles, drive layout, node/version/chunk/journal
      model, commit semantics, API, provider layout, relay-readable table and metadata
      inventory; replaces `storage-model.md`, `webdav.md`, `sync-protocol.md` and
      `e2ee-design.md` (kept as history)
- [ ] ADR: why WebDAV, fixed roots, path grants and a relay database are all dropped; why no CDC;
      the build-vs-adopt table above with the final library choices and licences
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
- [ ] **Sealed appends and sealed creates:** records and new files sealed to the node public
      key; readers open them with the node private key; compaction re-encrypts them as normal chunks
- [ ] **Signed records:** append records carry author, per-author sequence number and a
      signature; the vectors cover gaps, duplicates and reorders within one author
- [ ] `@poweur/client` twin; conformance vectors for every construction, including tamper,
      truncation, reorder and cross-node substitution failures

**Acceptance:** Go and TS produce and open byte-identical vectors; every tamper vector fails
with a distinct error.

### E20-T3 — Storage providers: filesystem & S3

- [x] Provider interface above; filesystem implementation (atomic writes, `put_if` via
      rename + lock); S3 implementation via minio-go (Apache-2.0), presigned PUT/GET
      with a SHA-256 checksum
- [x] Conformance suite passes on the filesystem and on local Docker MinIO
      (`POWEUR_TEST_S3_ENDPOINT`; not a GitHub Actions service). Compatible stores
      and the presign-off proxy fallback are in `storage-v2.md`
- [x] Config: `STORAGE_PROVIDER=fs|s3`, bucket, prefix, credentials, presign on/off

**Acceptance:** the same suite passes on the filesystem and on MinIO; a presigned upload with
wrong bytes is rejected by the store.

### E20-T4 — Drive engine

- [x] Journal (append-only segments) as the source of truth; in-memory tree and quota caches;
      periodic tree snapshots; rebuild from the journal alone
- [x] Replace commits with `base_version` → `409`; append commits ordered per node; name-hash
      uniqueness per folder
- [x] Uncommitted chunks expire (24 h); GC marks from live + retained versions and never
      deletes a chunk referenced by an in-flight commit (one GC process per drive; see
      E20-T17 for multi-instance)
- [x] Quota = unique chunk bytes per drive; `507` on overflow; plans hook for EPIC-026
- [x] Append positions (total order per node) returned on commit and exposed to readers
- [x] Prefix trim before a snapshot position (owner/`admin` only); trimmed record chunks are
      released
- [ ] GC reclaims journal segments that only hold trimmed records (journal compaction)
- [ ] Group commit of appends into log segments with a bounded buffering window; an append is
      acknowledged only after its segment is durable in the provider
- [ ] Per-member and per-link write accounting feeding E20-T7's caps

**Acceptance:** fuzzed concurrent replace/append commits never lose an acknowledged commit;
every reader sees appends in the same positions; a trimmed log reads correctly from its snapshot;
deleting every cache and restarting yields identical listings and quotas; GC under a concurrent
commit fuzz never removes a live chunk.

### E20-T5 — Drive HTTP API & change stream

- [x] Endpoints under `/drive/{identity}/`: `chunks/missing` (returns presigned or relay URLs),
      chunk upload (filesystem mode), `commit`, node read (head + manifest), chunk read through
      an authorized version, children listing, version history, changes feed with cursor
- [x] Auth: existing session and signed-challenge auth for owners and visitors; no DAV tokens,
      no app passwords
- [x] Read an append file from a position (`?from=N`), with the next position returned
- [x] SSE `drive.changed` events per node; appends ≤ 16 KiB carried inline
- [x] **Share members subscribe too:** a member on another relay opens an event stream on the
      host relay for the nodes shared with them (visitor auth), filtered to what they may read;
      revocation closes the stream
- [ ] `/pub` and `/.well-known/poweur/` served from public nodes and `.poweur/public`
- [ ] Public nodes CDN-cacheable (immutable chunk URLs, short-lived feed heads with `ETag`) and
      the relay subscription proxy + batch feed heads (memory only) specified in EPIC-032 E32-T4
- [ ] Presigned chunk downloads (reads go through the relay for now)

**Acceptance:** integration suite (`apps/integration`, real relays) covers upload, resume,
replace conflict, append ordering, listing, history and live events across two relays.

### E20-T6 — `.poweur` system files & stateless relay

- [x] Layout and schemas for `.poweur/{public,relay,state,private}` (EPIC-006 schemas carried
      over from `poweur-sys`); one-writer rule enforced by the relay — public/relay/state are
      the drive's journalled plaintext **system zone**, `private` an encrypted folder
- [x] Commit-time validation and synchronous cache apply for every relay-read file; signed
      documents verified (validators run before the journalled write; reads are served from
      the drive state the write just updated)
- [x] Move relay side state into the provider: identity index, inbound spool and ack queue,
      keystore (`relay/…`), device registry (`.poweur/state/devices.json`); app passwords
      dropped with WebDAV. The spool is one object per entry, not an append file
- [ ] Connected apps relay-written in `.poweur/state/` (moves with sign-in, Phase 9)
- [x] Statelessness test: stop the relay, delete everything except the provider, start it —
      policy, contacts, devices, pending inbox, identities and quotas behave identically
      (`INT_STATELESS_01` on disk; `TestRelayRestartsFromBucketOnly` on S3). Shares arrive
      with E20-T7; pending contact requests remain memory-only
- [ ] Rejection feedback contract for clients (reason codes the sync daemon surfaces)

**Acceptance:** an agent edits `~/Poweur/.poweur/relay/contacts.json` and `inbox-policy.json`
through the sync daemon; the next inbound message is accepted or refused accordingly; an
invalid edit is rejected with a readable reason and never half-applied.

### E20-T7 — Shares, roles, links & file requests

- [x] Share documents on node ids; roles `read`/`write`/`append`/`create`/`admin`; enforcement
      on every read and commit (relay); author-role verification on the reading client (Phase 8)
- [x] **Inheritance and combination:** a node's effective role for a member is the highest role
      granted by any share on the node or its ancestors, so a folder can be shared `read` while
      one file in it is shared `append` (e.g. comments)
- [x] **Caps:** per-member and per-link limits on bytes, records/files and rate, plus
      one-per-identity limits (one form response per ID); `429`/`507` with reasons
- [x] **Proof-of-work** (E14 primitive) required on anonymous link writes, difficulty set by the owner
- [ ] **Ownership transfer** of a subtree between drives (person ↔ group identity), re-issuing
      shares and keeping links working
- [ ] Offers and accepts (E05-T3 bodies) carry sealed node keys; mounts in `.poweur/private/mounts/`
- [x] Revocation with immediate access removal and key rotation on the next owner write
      (revoked key-bearing shares mark the subtree `rotate_required`; writes there `409` until rotated)
- [x] Links with key-in-fragment and the split password verifier; expiry, download caps,
      rate limits (relay side)
- [ ] Static decrypting viewer at `/s/<token>` with strict CSP and `no-referrer` (web)
- [x] File requests on `create` + folder public key; guest isolation and quotas (guest
      authors, `INT_DRIVE_03`)
- [ ] Claim flow from E05-T6 and Send (E05-T7) as links on sealed files (clients)
- [x] Groups as members; membership change rotates keys (groups hosted on the same relay;
      Space admins administer the group's drive)
- [x] Remote groups as members: members present the signed roster, checked against the
      group relay's public epoch; newer rosters revoke departed members (`INT_DRIVE_05`)

**Acceptance:** `TestINT_SHARE_*` equivalents pass on v2; a member with `append` on a file and
no read cannot read it; an anonymous link writer is stopped by caps and proof-of-work; a
board transferred from a person to a Space keeps its members and links; a revoked member's cached keys do
not open post-revocation writes; a link opens in a clean browser and the relay's logs and
store never contain the key or password.

### E20-T8 — SDK drive clients & CLI (Go + TS)

- [x] Encrypt/decrypt, chunking, missing-chunk upload, replace/append commits with retry,
      cursors, change subscription
- [x] Chunk cache: a directory under the CLI home; IndexedDB in web/shell — immutable, never revalidated
- [x] Streaming decrypt with range reads for large files and media
- [x] **Scoped drive handles:** a client restricted to a folder plus picked nodes, used by the
      EPIC-029 bridge, agents and the EPIC-031 headless apps
- [x] **Event-log helper:** `open(log, reducer)` → fold snapshot + tail, subscribe, append with
      per-author sequence, write snapshots, trim; Go and TS
- [x] **CLI:** `poweur drive ls|put|get|mv|rm|history|append|tail --from|trim|watch`,
      `poweur drive share add|rm|ls`, `poweur drive link create|rm`, `poweur drive transfer`,
      all with `--json`, so every collaboration action is scriptable

**Acceptance:** both SDKs pass the same scenario suite against a real relay on each provider,
and every action in the EPIC-031 scenarios is reachable from the CLI.

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

- [x] History: `.poweur/private/messages/<peer-hash>.jsonl`, one append file per conversation
      (peer hash keyed so paths do not reveal the social graph); groups keyed by group identity;
      multi-device dedupe by message id
- [x] SDK: `tail(peer, {limit})`, `before(peer, cursor, {limit})`, `append(records)`; tray loads
      read one tail chunk per conversation
- [x] Attachments: sealed file + per-file share with the recipient; content key, filename and
      MIME inside the encrypted payload; plaintext metadata keeps only node id, ciphertext size
      and hash. CLI `send --attach` and `attach save`. The web Open button still shows the
      name and does not download yet.
- [x] CLI `poweur history [peer] --limit --before --thread --json`

**Acceptance:** with 20 conversations and 10k messages the tray fetches ≤ 20 tail chunks;
a 20 MB attachment crosses two relays and the sender's store holds only ciphertext; E15-T13
paging works against it.

### E20-T12 — Migration & v1 removal

- [ ] Operator `poweur-relay migrate-v1`: dry-run, idempotent restart, validate and
      copy only plaintext system data (identity, profile/avatar, capabilities,
      contacts, policy, analytics, devices, connected apps, group roster).
- [ ] Also move the relay registries Phase 6 relocated: `identities/<id>/poweur-sys/public/id.json`
      → `relay/identities/<id>.json` (+ drive mirror), `spool/{messages,acks}/<id>/*.json` →
      `relay/spool/{messages,acks}/<id>/`, `keystore/<id>.json` → `relay/keystore/<id>.json`
      (WebAuthn key backups — losing them locks users out of recovery). Undelivered mail and
      key backups must survive the cutover.
- [ ] Drop old files, shares, links and history; never generate private content keys
      on the relay. Move successfully migrated old trees to `identities.v1-backup/`.
- [x] Remove `/dav`, v1 sync, DAV tokens, app passwords and whole-file implementation.
- [ ] Finish residual v1 documentation/reference cleanup and restore baseline tests on v2.
- [ ] Update OPS/BACKUP runbooks, rehearse on a copy of production, verify all baseline
      behavior and health before deployment; document backup expiry.

**Acceptance:** dry run does not mutate the source; interrupted migration resumes without
losing system documents; identity discovery, avatars, contacts/policy and group delivery
work from v2. Production data access and deployment are still required, not implied by a
local implementation. This replaces the earlier client-driven full-file migration decision.

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

- [ ] Expiry presets and `not_before`; version-pinned snapshot shares
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
- App backends: apps that need a neutral authority use EPIC-027, not the relay

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
