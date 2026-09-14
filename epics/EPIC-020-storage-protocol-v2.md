# EPIC-020 — Storage protocol v2: chunked, content-addressed sync & capability sharing

- **Status:** proposed
- **Priority:** P1
- **Depends on:** EPIC-003 (storage, `StorageProvider`), EPIC-004 (changes journal, uploads),
  EPIC-005 (grant engine), EPIC-006 (`capabilities.json`), EPIC-011 (device keys, for the
  encrypted domains), EPIC-013 (deployment topology)
- **Unlocks:** files as a separately deployed and scaled service (id/messaging relays without
  files), message history v2 (supersedes EPIC-009 E09-T1's layout), E15-T13 paged
  conversations, relay-blind storage (E03-T7 phases 1–2), recipient share mounts (E05-T3),
  append-only shared logs for EPIC-010 agents, S3 provider (E03-T8)

## Progress

| Task | Status | Notes |
|------|--------|-------|
| **Wave 0 — the files service boundary** (numbered after Wave 3 to keep IDs stable; build first) | | |
| E20-T13 Service topology spec & capability declaration | **open** | control plane vs files service ownership; signed `files` capability + endpoint binding |
| E20-T14 Control-plane ↔ files-service contract | **open** | control-zone documents, tokens + revocation feed, groups, notifications |
| E20-T15 Extract the files service | **open** | `internal/filesvc`, `RELAY_SERVICES`, combined and split deployments, topology test matrix |
| E20-T16 Clients follow the declared files endpoint | **open** | Go CLI, TS SDK, web, shell; messaging-only identities degrade explicitly |
| **Wave 1 — the protocol** | | |
| E20-T1 Research & decision record | **open** | survey + chunker/hash/encoding/commit decisions, `storage-v2.md` |
| E20-T2 Object model & conformance vectors | **open** | chunk ids, manifests, log framing — Go + TS vectors |
| E20-T3 Files-service chunk store & commit API | **open** | missing-chunks, upload, version-checked commit, GC, quota |
| E20-T4 WebDAV, links & public serving as views | **open** | DAV assembles chunks; existing DAV suite stays green |
| E20-T5 Chunk encryption & key domains | **open** | per-frame/per-chunk AEAD, keyed chunk ids, no cross-domain dedup |
| E20-T6 SDK sync engines (Go + TS) | **open** | chunk cache, commit/rebase loop; `poweur sync` and web uploads move over |
| E20-T7 Migration & protocol negotiation | **open** | v1 trees readable forever; lazy ingest; `capabilities.json` advertises v2 |
| **Wave 2 — first consumer** | | |
| E20-T8 Message history v2 | **open** | one append-only log per conversation; replaces month shards |
| **Wave 3 — permissions & sharing on content-addressed storage** | | |
| E20-T9 Authorization for versions & chunks | **open** | grants stay path-based; chunk reads authorized through a version, never by hash |
| E20-T10 New permissions: `append`, `create` | **open** | verifiable even on ciphertext because log frames are independent |
| E20-T11 Advanced share types | **open** | time-boxed, snapshot, excerpt (partial file), per-audience caps |
| E20-T12 Delegation & capability tokens | **open** | UCAN / Biscuit / macaroons evaluation; resharing (`share` permission) |

## Goal

Replace "a file is one blob, replaced whole" with the model modern sync products are built
on — **a file version is a list of immutable, content-addressed chunks** — so that appends
and small edits cost one chunk, concurrent writers are detected instead of silently
overwritten, everything already downloaded is cached forever, and permissions can target
*versions and parts* of files, not just whole paths.

WebDAV stays: as a **compatibility view** for Finder, rclone and davfs2 mounts, not as the
storage model the product's own clients are designed around.

## Background (current code)

- **Storage** is whole-file: `StorageProvider` (`apps/api/internal/files/`, E03-T8)
  exposes open/read/write/rename; `relay-fs` stores plain files under
  `POWEUR_DATA/identities/<id>/`. ETags are SHA-256 of content (E03-T1).
- **Sync** (E04) bolts delta discovery onto that: a changes journal with a tree-wide
  cursor, a manifest for full resync, a tus-style resumable upload at `/sync/{id}/upload`
  for files ≥ 64 MiB. Conflicts are last-writer-wins plus a conflicted copy, detected
  client-side. **The relay has no `If-Match` / ETag precondition handling**, so two writers
  cannot be told apart at the server.
- **Message history** (E09-T1) had to be one sealed file per message
  (`poweur-sys/private/messages/<YYYY-MM>/<sortkey>-<id>.json`) *because* whole-file
  replacement without preconditions makes any shared file a lost-update hazard. Reading it
  is one GET + unseal per message, the whole archive, on every unlock (see E15-T13).
- **Grants** (E05) are signed documents at `poweur-sys/relay/shares/`, path-scoped, whole
  subtree, permissions `read`/`write` (`share`/`admin` reserved), with `expires_at` already
  honoured on every request and no cache window. Link shares add password, expiry and
  `max_downloads`.
- **Relay-blind storage** is designed (E03-T7, [`e2ee-design.md`](../apps/docs/docs/files/e2ee-design.md))
  as age-style STREAM chunks with per-folder keys — never built. A per-file envelope is
  incompatible with cheap appends; this epic's chunk format is where that design lands.

## Design direction

### The object model

```
chunk     immutable bytes, id = keyed hash (multihash-encoded)       ~64 KiB avg (CDC)
manifest  one file version: {path, version, parent, size, mtime, chunks:[id…], actor}
tree      the existing changes journal, now carrying version ids instead of bare etags
```

- **Content-defined chunking** (FastCDC, keyed per key domain as restic does) so an insert
  or append perturbs one or two chunks, not every fixed-offset block after it.
- **Logs are a file kind, not a special store.** A log is a sequence of length-prefixed,
  independently sealed *frames*; its chunker cuts only at frame boundaries once a chunk
  reaches the target size, so every closed chunk decodes alone and only the open tail chunk
  is ever rewritten. Appending to ciphertext is still a byte-level prefix extension — which
  is what makes `append` enforceable on data the relay cannot read (E20-T10).
- **Commits are compare-and-swap.** `commit {path, base_version, chunks}` → `409` with the
  current version when `base_version` is stale. Clients rebase: for logs that is "re-append
  my frames on the new tail" and never produces a conflict; for ordinary files the E04
  conflicted-copy rule stays.
- **Upload only what is missing.** `POST chunks/missing` with a chunk-id list returns the
  subset the relay lacks (Dropbox's block-server flow); large chunks use the existing
  tus-style resumable path.
- **No cross-identity dedup, ever.** Chunk ids are keyed per *key domain* (the owner's
  private zone, a shared folder, a group) so the relay cannot learn that two parties hold
  the same content, and quota is simply the unique chunks an identity references.

### What it borrows (open protocols, not inventions)

| Source | Taken | Not taken |
|--------|-------|-----------|
| **Syncthing BEP** | file = block list with hashes, index updates, version vectors as a studied option | P2P device-certificate trust model, its transport |
| **restic / casync / desync** | keyed CDC chunker, encrypted chunks, index format for very large manifests | repository-level locking model |
| **multiformats (multihash, CIDv1)** | self-describing chunk ids, so SHA-256 → BLAKE3 is not a protocol break | IPFS networking, global dedup |
| **tus** | resumable upload of large chunks (already half-followed in E04-T3) | — |
| **Dropbox block server** (documented, not open) | commit with block list → missing blocks | — |
| **age STREAM** (via E03-T7) | chunk AEAD construction, recipient stanzas for key wrapping | per-file whole envelope |
| **UCAN / Biscuit / macaroons** | attenuated, expiring, delegable capabilities (E20-T12) | replacing signed grant documents as source of truth |
| RFC 6578, Nextcloud chunking v2 | — | still DAV/XML; resumable-only, no dedup or delta |

Transport stays HTTP with the existing signed/bearer auth, the E04 changes feed and the
E09-T2 SSE stream. Manifests are JSON in v2.0; CBOR is an E20-T1 decision.

### Permissions: what changes and what deliberately does not

- **Grants stay path-based signed documents.** The unit of authority is still "this audience
  may do X under this path until T". Content addressing changes *how a read is proven*, not
  who decides.
- **A chunk id is never a capability.** Knowing a hash grants nothing; chunks are fetched
  *through* a version the requester is authorized to read (E20-T9). This keeps E05-T2's
  guarantee — revocation and expiry take effect on the next request — intact.
- **Immutability changes revocation's meaning, honestly.** Already-downloaded chunks cannot be
  recalled (true today for whole files); for encrypted domains revocation means rotating the
  domain key for future writes, as E03-T7 phase 2 already says. The UX must never promise
  otherwise.
- **Versions and chunks make new share shapes cheap** — a snapshot is a version id, an excerpt
  is a derived manifest referencing a subset of existing chunks (zero copy), an append-only
  audience is a commit rule. None of these need a second enforcement engine; they compose
  onto the grant engine as new fields and permissions (Wave 3).

### Files is a service an identity *declares*, not a part of the relay

**Today it is optional in name only.** `davEnabled()` is just `filesProvider != nil`, but the
messaging half of the relay reads its own configuration *out of the file tree*:

| Messaging / identity feature | Reads from the file tree | Code |
|------------------------------|--------------------------|------|
| Inbox policy & stranger gate | `poweur-sys/relay/inbox-policy.json`, `contacts.json` | `relay/policy.go` `readSysJSON` |
| Group message fan-out | group identity `poweur-sys/relay/groups/self.json` | `relay/groups.go` → `files.GrantStore.GroupIdentity` |
| Device registry, presence, revocation | `poweur-sys/relay/devices.json`, `app-passwords.json` | `relay/devices.go`, `relay/events.go` |
| Sign-in connected apps | `ConnectedAppsPath` | `relay/signin_grants.go` |
| `/.well-known/poweur/profile.json`, `capabilities.json` | `poweur-sys/public/*` | `relay/wellknown.go` `serveSysPublicFile` |

**Without a provider, `readSysJSON` returns nil and the recipient silently falls back to
the default `open` policy.** So a messaging-only relay today would not just lack files — it
would drop contact-only and block enforcement without saying so. Splitting the services is
therefore a data-ownership decision first and a deployment option second.

**Ownership after the split** (id + messaging stay one process — they are too tightly coupled
to be worth separating now):

| Control plane (id + messaging relay) | Files service |
|--------------------------------------|---------------|
| identity documents, keystore, enrollment, sessions, challenges | content roots `/public`, `/shared`, `/private`, `/apps` |
| spool, acks, requests, anon queue, SSE hub | `poweur-sys/private` (message history, credentials) |
| **control zone:** `poweur-sys/public/*`, `poweur-sys/relay/{contacts,inbox-policy,blocks,devices,app-passwords,groups,analytics}`, connected apps | `poweur-sys/relay/shares/*` (only the files service enforces grants), link-share stats |
| issues DAV tokens and session tokens; owns revocation | changes journal, uploads, chunk store, quota, `/dav`, `/sync`, `/v2`, `/s/`, `/pub` |

**Clients still see one tree.** Contacts, policy and groups are written through the file API
today (E15-T2) and synced like any file. The files service keeps serving the control zone at
the same paths, and in split mode proxies those paths to a small documents API on the control
plane. The validators, journal entries and audit trail stay the same. Combined mode does this
in-process.

**Declaring and discovering it.**

- The signed identity document already carries a `capabilities` string list (default
  `["messaging"]`). Adding the *value* `"files"` changes no canonical signing field, so every
  existing verifier accepts it. Identities can then be `["messaging", "files"]`,
  `["messaging"]`, or — later — `[]` for id-only.
- Where the service lives goes in `capabilities.json`, whose `endpoints` map exists but no
  client reads yet (every client builds `relayUrl + "/dav"` / `"/sync"`). It holds a
  `services.files` entry: endpoint, supported protocols (`dav`, `sync-v1`, `storage-v2`), and
  an **owner-signed binding** over `(identity, service, endpoint, service_key, issued_at)`.
  The owner signs it, not the relay: `capabilities.json` itself is unsigned and served by the
  control plane, so without a signature an operator could point clients at any files server.
- Absent binding + `"files"` capability + a legacy relay = the compatibility rule
  `relayUrl + "/dav"`, so every identity that exists today keeps working unchanged.
- The binding does not assume one operator. A hosted identity on `poweur.net` can bind a
  self-hosted files service — v1 ships single-operator, but nothing in the contract may
  preclude this.

**Deployment modes, one binary:** `RELAY_SERVICES=identity,messaging,files` (default, as
today), `identity,messaging` + `FILES_SERVICE_URL`, and `files` + `CONTROL_PLANE_URL` +
service credentials. Files is the part that needs different scaling: it has large bodies,
is mostly stateless over the `StorageProvider`, and suits S3. Wave 1's chunk store is built
inside this boundary from the start rather than extracted later.

## Tasks

### E20-T13 — Service topology spec & capability declaration

- [ ] `apps/docs/docs/relay/service-topology.md`: the ownership table above made normative,
      with every current cross-read (policy, groups, devices, app passwords, connected apps,
      well-known) assigned an owner and an access path
- [ ] **Fail closed without files.** A relay whose control zone is unreachable refuses
      policy-dependent deliveries (`503`) rather than defaulting to `open`; a
      messaging-only relay stores the control zone itself. Test that removing the files
      provider can never widen an inbox
- [ ] Identity document: `"files"` as a `capabilities` value; conformance vector proving an
      old verifier accepts a document carrying it
- [ ] `capabilities.json` schema (EPIC-006): `services.files {endpoint, protocols[],
      binding}`; binding canonical signing string + vectors in `packages/identity`, same
      pattern as grants
- [ ] Discovery rule for clients, including the legacy fallback and what a missing or
      invalid binding means (refuse, never silently fall back to the relay)
- [ ] Threat notes: operator-substituted endpoint, stale binding after moving services,
      files service impersonating the control plane, cross-operator bindings

**Acceptance:** spec merged with worked resolution examples for combined, split,
messaging-only and legacy identities; vectors pass in Go and TS.

### E20-T14 — Control-plane ↔ files-service contract

- [ ] **Control-zone documents API** on the control plane (`GET/PUT /docs/{identity}/{path}`)
      with the existing `sysfiles.go` validators and journal semantics; the files service
      proxies control-zone paths to it in split mode
- [ ] **Tokens:** control plane issues short-lived signed access tokens (audience = files
      endpoint, identity, device id, scope, ≤ 15 min) that the files service verifies
      offline. Coordinate the format with E20-T12 rather than inventing a second one
- [ ] **Revocation feed:** device revoke, app-password delete, session end and grant-relevant
      group changes are pushed to the files service over an authenticated internal stream,
      so E04-T6's "revoking a device kills its DAV tokens" stays immediate, not TTL-bounded.
      On stream loss the files service fails closed for token auth until caught up
- [ ] App passwords (Basic auth for legacy DAV mounts) verified by the files service against
      the control plane's document, cached and invalidated by the feed
- [ ] Group membership for grant evaluation read from the control plane (cross-relay group
      resolution stays deferred, as in E05-T5)
- [ ] Signed-challenge (visitor and owner) auth works at the files service with its own
      challenge store and public identity resolution — no control-plane round trip
- [ ] **Notifications:** commits publish `sync.changed` to the control plane's SSE hub, so
      clients keep one event stream per identity
- [ ] Service-to-service authentication (mTLS or a signed service key from the binding) and
      the internal endpoints kept off the public mux

**Acceptance:** contract doc + tests for every row of the ownership table in both
directions; a revoked device's token is refused by a split files service on its next
request.

### E20-T15 — Extract the files service

- [ ] Move DAV, sync, uploads, link shares, `/pub`, quota and grants out of `internal/relay`
      into `internal/filesvc`, depending on a `ControlPlane` interface (documents, token
      verification, revocations, groups, notify) with an in-process implementation and an
      HTTP one
- [ ] Import-boundary test: `filesvc` never imports `relay`, and `relay` never touches a
      `files.StorageProvider` directly
- [ ] `RELAY_SERVICES` / `FILES_SERVICE_URL` / `CONTROL_PLANE_URL` in `internal/config` with
      validation (for example, `files` alone without a control plane URL is a startup error)
- [ ] `/health` and telemetry report which services a process runs; OTLP resource attributes
      distinguish them (EPIC-013)
- [ ] Deployment: compose profile and ansible role for a split files host; `deploy/OPS.md`
      section on moving an existing combined relay to split without downtime
- [ ] **Topology matrix in `apps/integration`:** files, sharing, history, attachments, device
      revoke and group suites run against both combined and split (two processes); plus a
      messaging-only relay suite

**Acceptance:** the whole integration suite is green in combined and split mode; a
messaging-only relay delivers, enforces contact-only policy, and answers `/dav` with `404`.

### E20-T16 — Clients follow the declared files endpoint

- [ ] Go CLI (`internal/sync/remote.go`, `history.go`, `share.go`) and `@poweur/client`
      (`files.ts` `davUrl`, `sync.ts`) resolve the files endpoint through the T13 discovery
      rule instead of concatenating the relay URL
- [ ] Visitors (shares, links, recipient mounts) resolve the *owner's* files endpoint
- [ ] Messaging-only identities, shown plainly rather than as errors:
      - the web/shell Files destination explains that the identity has no file storage;
      - attachments are disabled with a reason;
      - message history is local to the device and the UI says so
- [ ] `poweur identity lookup` prints the files service and binding verdict

**Acceptance:** e2e — the web app against a split deployment uploads, shares, and reads
history; against a messaging-only relay it messages normally and the Files, attachment and
history surfaces explain the missing capability.

### E20-T1 — Research & decision record

- [ ] Survey write-up of the sources in the table above, with one paragraph each on what
      breaks for Poweur's model (relay-mediated, identity-keyed, per-path ACL, encrypted zones)
- [ ] Decide and justify: chunker (FastCDC params: min/avg/max), hash (SHA-256 stdlib vs
      BLAKE3 — the relay's only direct dependencies today are `golang.org/x/crypto` and
      `golang.org/x/net`), manifest encoding (JSON vs
      dag-CBOR), manifest paging for huge files (casync-style index vs chunked manifest)
- [ ] Decide commit semantics: CAS on `base_version` vs version vectors; what the changes
      journal records per version; version retention default (e.g. last N or 30 days)
- [ ] Decide the index store (bbolt vs SQLite) — coordinate with EPIC-002's open E02-T1 index
- [ ] Spec at `apps/docs/docs/files/storage-v2.md`; ADR in the same PR explaining why v1
      WebDAV-as-model is retired and what stays

**Acceptance:** spec merged with worked examples for: new file, append to a log, concurrent
commit + rebase, large-file resume, a DAV client editing a v2 file, and a revoked visitor.

### E20-T2 — Object model & conformance vectors

- [ ] `packages/identity`: chunk id encoding, manifest canonical form + validation, log frame
      format (length prefix, frame AEAD header, record-aligned cut rule)
- [ ] Conformance vectors: chunker boundaries for fixed inputs + keys, manifest canonical
      bytes, log append producing a byte-prefix, frame decode across a chunk boundary rejected
- [ ] TS twin in `@poweur/client` passing the same vectors

**Acceptance:** Go and TS produce byte-identical chunk ids and manifests for every vector.

### E20-T3 — Files-service chunk store & commit API

Built inside `internal/filesvc` (E20-T15), never in `internal/relay`.

- [ ] Endpoints under `/v2/{identity}/`: `chunks/missing`, `chunks/{id}` (PUT), version
      read (`files/{path}` → manifest), chunk read *through a version* (see E20-T9),
      `commit` with `409` on stale base, version history list
- [ ] Chunk storage behind `StorageProvider` (immutable objects map cleanly onto S3 — record
      the review in E03-T8's terms: no rename needed, no list consistency needed)
- [ ] Quota over unique referenced chunks; uncommitted uploads expire (reuse the 24 h spool)
- [ ] GC: mark-sweep over live + retained versions, safe against in-flight commits
- [ ] Changes journal entries carry `version`; SSE `sync.changed` notification on commit
- [ ] Integration test: two relays' worth of clients commit concurrently; exactly one wins,
      the other gets `409` with the winner's version

**Acceptance:** append of 1 frame to a 10 MB log uploads ≤ one chunk; GC reclaims an
unreferenced version's chunks and never a live one under a concurrent-commit fuzz test.

### E20-T4 — WebDAV, link shares & public serving as views

- [ ] DAV GET assembles chunks with HTTP `Range` support; PUT chunks server-side and commits
      against the version the client last saw (ETag = version id; honour `If-Match`)
- [ ] `/s/<token>` and `/pub` serving read through versions; Range for media streaming
- [ ] Encrypted domains read through DAV as ciphertext — documented limitation, as E03-T7 says
- [ ] The existing DAV suite and `TestINT_SHARE_*` run unchanged against a v2 store

**Acceptance:** Finder/rclone/davfs2 smoke tests pass against a v2-only relay.

### E20-T5 — Chunk encryption & key domains

- [ ] Key domains: owner private zone (identity X25519-derived), per-folder keys for
      relay-blind `/private` and `/shared` (E03-T7 phases 1–2), group-epoch keys (E05-T5)
- [ ] Per-domain derived keys for: chunk-id MAC, chunker seed, chunk/frame AEAD
- [ ] Per-chunk content keys derived from the file key (so an excerpt can disclose only its
      chunks — E20-T11); document what chunk sizes still leak
- [ ] Update `e2ee-design.md`: the per-file envelope is replaced by this format

**Acceptance:** vectors for each domain; a relay-side test proves the same plaintext in two
domains yields unrelated ids.

### E20-T6 — SDK sync engines (Go + TS)

- [ ] Chunker, local chunk cache (IndexedDB in web/shell, a directory under the CLI home) —
      immutable, never revalidated
- [ ] Commit/rebase loop; log append helper (`appendFrames(path, frames)`) with automatic
      retry on `409`
- [ ] `poweur sync` (E04-T4) moves onto versions: uploads send missing chunks only; conflict
      detection moves from client guesswork to the server's `409`
- [ ] Web uploads use chunks for every size (drop the 64 MiB threshold branch)

**Acceptance:** `TestINT_SYNC_01` convergence passes on v2; editing 1 byte in a 1 GB file
transfers ≤ two chunks.

### E20-T7 — Migration & protocol negotiation

- [ ] `services.files.protocols` (E20-T13) advertises `storage-v2`; clients fall back to
      `dav` + `sync-v1` when it is absent
- [ ] v1 files ingest lazily (first v2 read or a background pass) into single-version
      manifests; plain DAV writes keep working throughout
- [ ] Existing grants, links and journal cursors survive the migration untouched

**Acceptance:** a relay upgraded in place serves an old CLI (DAV + v1 sync) and a new one
(v2) against the same tree without either noticing.

### E20-T8 — Message history v2

Supersedes the v1 layout from EPIC-009 E09-T1; E15-T13's paging consumes it.

- [ ] Layout: `poweur-sys/private/messages/<peer-hash>.log`, one log per conversation
      (peer hash keyed with the owner domain key so paths do not reveal the social graph);
      groups keyed by group identity; read-state unchanged
- [ ] API in both SDKs: `tail(peer, { limit })`, `before(peer, cursor, { limit })`,
      `append(records)` — tray loads = one manifest + one tail chunk per conversation
- [ ] Multi-device pickup: two devices appending the same message dedupe by id on read;
      rebase on `409` re-appends only frames not already present
- [ ] Migration from month shards (one pass, resumable, v1 files deleted only after the log
      commit is confirmed); `poweur history` reads both until migration completes
- [ ] CLI `poweur history [peer] --limit --before --thread`, `--json` with `next_cursor`

**Acceptance:** with 20 active conversations and 10k messages, drawing the tray fetches
≤ 20 manifests + ≤ 20 tail chunks (≈ 1–2 MB cold, ~0 warm); `TestINT_HISTORY_01/03` pass
on v2; migration of a 10k-record v1 archive is resumable after a kill.

### E20-T9 — Authorization for versions & chunks

- [ ] Grant engine (E05-T2) authorizes **version reads** exactly as it authorizes paths today
- [ ] Chunk reads are addressed through a version (`versions/{vid}/chunks/{index}` or
      `chunks/{id}?via={vid}`) and authorized per request — revocation and expiry stay
      immediate. Evaluate short-lived read tickets (≤ 10 min, capped at grant expiry) only
      for link/CDN paths where per-request checks are too costly; write down the window
- [ ] Visitor writes: chunks land in the owner's store under the share's key domain;
      commit authorized by grant; version `actor` = visitor (extends the E04 audit trail)
- [ ] Snapshot vs live semantics decided for existing grants (default: live — a path grant
      follows new versions)
- [ ] Recipient mounts (E05-T3's deferred half) cache chunks across the owner's versions
- [ ] Threat notes added to `sharing.md`: hash-as-capability, dedup oracle, cached-chunk
      revocation, chunk-size leakage in encrypted shares

**Acceptance:** scenario matrix — revoked visitor with a valid old version id and chunk id
gets `404` on the next chunk; expired grant likewise; a chunk shared in one domain is not
readable via a version in another.

### E20-T10 — New permissions: `append` and `create`

- [ ] `append`: a commit is accepted only if the new byte stream has the base version as a
      prefix — checked by the relay on chunk lists (all closed chunks identical, new tail
      begins with the old tail's bytes), so it works on ciphertext logs
- [ ] `create` (drop box): create new paths under the grant, no read, no list, no overwrite
- [ ] Grant format + canonical signing extended (new permission strings only — existing
      signatures stay valid, as link shares did)
- [ ] Use cases wired as tests: a group-owned append-only chat log; an agent audit log
      (EPIC-010); an upload-only inbox folder

**Acceptance:** an `append` holder cannot truncate, rewrite, or reorder a log, including by
committing a crafted tail chunk; a `create` holder cannot read back what they uploaded.

### E20-T11 — Advanced share types

Composes on E20-T9/T10; every item is a grant field or a derived object, not a new engine.

- [ ] **Time-boxed shares:** `expires_at` already exists; add `not_before`, per-audience
      expiry, and UX presets ("24 hours", "1 week", "until …"). Expiry of an encrypted share
      schedules domain-key rotation on the next owner write; the UI says plainly that expiry
      stops *access*, not copies already downloaded
- [ ] **Snapshot shares:** grant pinned to a `version` — the audience sees that version
      forever (or until expiry), never later edits
- [ ] **Excerpt shares (part of a file):** a derived manifest referencing a subset of the
      source's chunks — zero copy — with boundary chunks re-chunked (server-side for plaintext,
      client-side for encrypted domains, disclosing only the excerpt's chunk keys). Shapes:
      byte range, time range of a log (e.g. "messages from Tuesday"), page range for
      formats that can declare one later. Excerpts live at a real path and are shared with
      ordinary grants, so the engine never evaluates ranges
- [ ] **Per-audience caps:** generalize link `max_downloads` / byte counters to identity
      audiences
- [ ] Web share dialog + CLI: expiry presets, "share this version", "share a part"
- [ ] Explicit non-goal recorded: "view-only / no download" is not enforceable against a
      client that can render content; do not ship it as a security control

**Acceptance:** e2e — share a log excerpt for 24 hours; the recipient reads exactly those
records, nothing before or after (verified by fetching neighbouring chunk ids through the
excerpt version → denied); after expiry every read fails; the owner's later appends never
appear to the recipient.

### E20-T12 — Delegation & capability tokens

- [ ] Evaluate UCAN, Biscuit and macaroons against: attenuation (path prefix, permissions,
      version, expiry), offline verification across relays, proof-of-possession vs bearer,
      revocation story, library footprint in Go/TS
- [ ] Resharing: activate the reserved `share` permission — a holder can mint a *narrower*
      grant (subset path, subset permissions, earlier expiry), with the delegation chain
      verified by the relay and visible to the owner
- [ ] Tokens are derived artifacts for transport; signed grant documents in the owner's tree
      stay the source of truth, and deleting a root grant kills every delegated token

**Acceptance:** decision record merged; a two-hop delegation (alice → bob → carol, each
narrower) works across two relays and dies when alice revokes bob.

## Non-goals

- Cross-identity or global dedup (privacy oracle; see Design direction)
- Peer-to-peer block exchange between devices (Syncthing-style) — relay-mediated only in v2
- Server-side merging of ordinary files or CRDT support in the store; apps bring their own
  formats (EPIC-006), logs are the one merge the store understands
- Removing WebDAV
- DRM-style "view but not copy" guarantees
