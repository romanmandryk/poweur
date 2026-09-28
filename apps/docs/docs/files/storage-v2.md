---
id: storage-v2
title: Storage v2
---

# Storage v2: encrypted drives

**Implementation status:** under construction on master (EPIC-020). Do not deploy
this branch until the baseline and production migration are verified. WebDAV,
path shares, app passwords, v1 sync, and the Tasks and Guestbook reference apps
have been removed. Their replacements are not available yet.

The current system-file API is a transitional adapter, not the drive engine:
`GET|PUT|DELETE /identities/{identity}/system/{path}`. It stores public and
relay-readable documents under `.poweur`, with owner authentication and schema
validation. Private history and attachments remain unavailable. Phase 6 replaces
its backing with the journalled drive; no client should depend on its disk layout.

## Decisions and boundaries

An identity owns one drive. A drive is a tree of random node IDs, immutable file
versions and content-addressed chunks. The relay authorizes operations, sequences
commits and accounts for bytes. It never receives a private node key or a private
content key. The only reserved root name is `.poweur`.

One relay process owns a drive at a time. Distributed writers require the leases
in E20-T17 and are not supported by the first implementation. A successful commit
means its journal record is durable; an uploaded chunk alone is not a commit.

JSON is the wire format. Binary fields use unpadded base64url, hashes use lowercase
SHA-256 hex, timestamps use UTC RFC3339. Protocol counters must fit an unsigned
JavaScript safe integer (0 through 9007199254740991); implementations reject
fractions and overflow. Signatures use explicit canonical field encodings, never
arbitrary incoming JSON bytes. Unknown versions and duplicate JSON keys are
rejected in signed protocol documents.

## System files and writers

| Path | Writer | Readers |
|---|---|---|
| `.poweur/public/id.json` | registration / identity rotation, owner-signed | everyone |
| `.poweur/public/profile.json`, `capabilities.json`, `avatar.<ext>` | owner | everyone |
| `.poweur/relay/contacts.json`, `inbox-policy.json`, `analytics.json` | owner | owner and relay |
| `.poweur/relay/group.json` | group identity, self-signed | relay; authenticated group lookup |
| `.poweur/relay/shares/<id>.json` | owner / authorized share administration | relay and authorized members |
| `.poweur/state/devices.json`, `connected-apps.json` | relay | owner and relay |
| `.poweur/private/messages/<peer-hash>.jsonl` | owner's clients | owner, encrypted |
| `.poweur/private/read-state.json`, `logs/auth.log` | owner's clients | owner, encrypted |

Contact blocking is represented in `contacts.json`; portable blocklist import /
export uses the existing signed blocklist schema. Private log appends use the
same encrypted record primitive as other append files. No private file is
interpreted by the relay. The transition currently stores connected apps in the
owner-written relay zone; moving this to relay-written state is part of Phase 6,
not a completed property of the temporary API.

**Implemented system zone.** The relay cannot find a file in the encrypted tree (names are
sealed and name hashes are keyed by the parent's private key), so `.poweur/public`,
`.poweur/relay` and `.poweur/state` are not encrypted tree nodes. They are the drive's
**system zone**: plaintext documents stored once per content at `system/<sha256>` and
changed by journalled `system` operations (`system.put` / `system.delete` in the changes feed
and `drive.changed` events, with `path` instead of `node`). They share the drive's journal,
snapshots, quota and restart path; clients present them at the same `.poweur/…` paths in a
synced drive. `.poweur/private` is an ordinary encrypted folder the relay never interprets.
The relay records the writer (`owner` or `relay` by zone); owner writes count against quota
and are refused over it (`507`), the relay's own records are counted but never refused.
Documents are verified against their journalled hash on read. `id.json` is written by
registration and rotation, served from the relay's identity index and mirrored into the
zone. Remaining Phase 6 gaps: `connected-apps.json` is still owner-written in
`.poweur/relay/` until sign-in moves to the relay (Phase 9); the spool is one object per
entry under `relay/spool/` rather than an append file; pending contact requests and sessions
stay in memory.

Known settings are validated before durable commit. A successful commit updates
the policy cache before its response: adding a contact and then receiving a
message must observe the new policy. Unknown relay-enforced documents cannot
silently acquire semantics. Owners cannot write the state zone. Schema errors
return `422` and do not mutate the head, quota, journal or cache.

`/.well-known/poweur/<file>` serves the public zone. `profile.avatar` is a flat
filename such as `avatar.png`, not a `/pub` path. Images must pass format and size
validation. JSON and images receive `nosniff`; mutable heads have ETags and short
cache lifetimes. Private responses use `Cache-Control: no-store`.

## Provider objects and durability

Both filesystem and S3 providers expose Get (optional byte range), Put,
conditional PutIf, Delete, List, and optional presigned PUT/GET. Object keys are
relative slash-separated paths; traversal, empty segments, backslashes, and
symlink escapes are invalid. Conditional create and replacement are atomic.
Missing objects and failed preconditions are distinct errors. ETags are opaque
provider tokens, not content hashes.

Under `POWEUR_DATA` or an S3 bucket prefix, using `SanitizeIdentityDirName`:

```text
drives/<identity>/journal/<20-digit-sequence>.json
drives/<identity>/snapshots/<20-digit-sequence>.json
drives/<identity>/versions/<node>/<version>.json
drives/<identity>/chunks/<sha256>
drives/<identity>/system/<sha256>
drives/<identity>/pages/<sha256>.json
relay/identities/<identity>.json
relay/spool/{messages,acks}/<identity>/<20-digit-sequence>.json
relay/keystore/<identity>.json
```

Journal segments are immutable JSON envelopes containing a format version, first
and last sequence, previous segment hash and ordered accepted operations. The
record includes the resulting node head, append position and accounting delta.
Write required chunks and manifests first, then publish the journal segment by
conditional create. Only then acknowledge and notify subscribers. An interrupted
write before journal publication creates collectible orphans, not a visible node.
After uncertain publication, retry by operation ID and compare the stored hash;
an operation ID cannot be reused with different content.

Snapshots contain the applied sequence and journal hash, tree, retained versions,
append cursors and author sequences, and quota/accounting state. They are immutable
accelerators, not a second source of truth. Cold start validates a snapshot against
the journal and replays its suffix. Missing or invalid snapshots trigger a full
replay; a corrupt or missing committed journal segment fails closed. Never start
an empty writable drive after an I/O or integrity error.

Filesystem writes sync the temporary file before atomic rename and sync the
containing directory before acknowledgement. Presigned PUTs bind the expected
SHA-256 checksum. A missing-chunk probe does not prove durability: commit
verifies the referenced bytes and sizes.

### S3-compatible stores

Conditional publication uses `PutObject` with `If-None-Match: *` (create) and
`If-Match` (replace). Startup writes a probe object, requires the second create
to fail and an ETag replace to succeed. A bucket that overwrites instead is
refused. The bucket must already exist.

A conditional write whose precondition fails but finds exactly its own bytes
stored reports success, on every provider. SDKs retry writes that may have
landed (and Ceph answers some racing writes with HTTP 500), so a 412 can answer
the retry of a write that won; identical bytes are the same outcome for the
journal.

Direct upload (`S3_PRESIGN=1`, the default) signs `x-amz-checksum-sha256` into
the PUT. The store must reject a body that does not match that full-object
digest; the startup probe checks this and refuses to run with presign on
otherwise.

| Store | Conditional `PutObject` | Full-object SHA-256 on a presigned PUT | Configuration |
|---|---|---|---|
| AWS S3 | Yes | Yes | `STORAGE_PROVIDER=s3`, presign on |
| MinIO (current) | Yes | Yes. The conformance suite, including a mismatched checksum, passes against local MinIO when `POWEUR_TEST_S3_ENDPOINT` is set | `STORAGE_PROVIDER=s3`, presign on |
| Cloudflare R2 | Yes (`If-Match` and `If-None-Match` on `PutObject`) | SHA-256 is documented as a composite checksum only, not a full-object checksum | `STORAGE_PROVIDER=s3` and `S3_PRESIGN=0` until a full-object checksum is available |
| Hetzner Object Storage (hel1, Ceph) | `If-None-Match: *` works. A specific `If-Match` succeeds only when the ETag is sent without quotes; the provider detects that and signs the bare value. A burst of racing replaces can return HTTP 500, which is retried once | A presigned `x-amz-checksum-sha256` is ignored. A body that does not match is stored | `STORAGE_PROVIDER=s3` and `S3_PRESIGN=0`, so chunk bytes go through the relay. The startup probe uploads mismatched bytes to a presigned URL and refuses to start with presign on when the store accepts them |

Any other S3-compatible service stays unsupported until that same suite passes
against it. Set `POWEUR_TEST_S3_ENDPOINT`, `POWEUR_TEST_S3_ACCESS_KEY` and
`POWEUR_TEST_S3_SECRET_KEY`, then run `go test ./internal/drive/...` from
`apps/api`.

`S3_PRESIGN=0`, and the filesystem provider, do not give clients an upload URL.
Chunk bytes then go through the relay, which still writes with a conditional
put. That is the fallback when a store can sequence writes and cannot enforce
the checksum. Conditional writes stay mandatory on every store.

## Key tree and encryption

Every node has an X25519 key pair. The root private key is sealed to the identity
encryption public key. Other private keys are sealed to the parent's node public
key. A file has a random 32-byte content key sealed to its node public key.

Sealing reuses message encryption: ephemeral X25519, HKDF-SHA256 with salt
`ephemeral-public || recipient-public`, and ChaCha20-Poly1305 with a random
12-byte nonce. Messages retain `poweur/msg/v1` byte for byte. Drive key seals use
HKDF info `poweur/drive/seal/v1`; names and sealed records use distinct domains
`poweur/drive/name/v1` and `poweur/drive/record/v1`. AAD starts with the domain plus
newline and both public keys, followed by an unambiguous context binding the drive,
node, purpose and key generation. Reject malformed nonce/key lengths before AEAD.
The context is four fields in order: owner identity, node ID, purpose, generation.
Each is prefixed with a uint32 big-endian UTF-8 byte length; generation is unsigned
decimal ASCII. Fields are 1–1024 bytes, valid UTF-8, without NUL. Generation must
be a safe integer. Message seals have no extra context. Key-wrap purposes are
`node-key` and `content-key`; chunks use `content`. No age or HPKE dependency is
introduced.

Names are UTF-8 NFC, case-sensitive, nonempty, contain neither slash nor NUL, and
are not `.` or `..`. Clients seal names to the parent public key. Name lookup uses
HMAC-SHA256 over the normalized name. Derive its 32-byte key with HKDF-SHA256,
IKM = the 32-byte parent private key, salt = parent public key, info =
`poweur/drive/name-index/v1`. Names are at most 255 UTF-8 bytes after normalization. The root's `.poweur` name
is reserved. A create-only outsider cannot compute the private name index; its
sealed create is assigned a random opaque name token until the owner accepts and
indexes it. Do not reveal the parent's private index key to drop-box writers.

Chunks use XChaCha20-Poly1305 with a random 24-byte nonce. Maximum user plaintext
is 4 MiB. The encrypted payload contains its true byte length followed by content
and zero padding; round to 4 KiB buckets (including the length header). The public
chunk is `nonce || ciphertext-with-tag`; its ID is SHA-256 of those bytes. AAD
binds the drive, node and content-key generation, but not a version or ordinal:
unchanged chunks can be reused in later versions. Signed manifests bind their
ordering. Equal content is not deterministically encrypted or deduplicated across
identities. Public system files are explicitly plaintext and never claim padding
or ciphertext confidentiality.

## Versions, appends and changes

A version contains format version, drive, node, version ID, parent version,
operation, author, key generation, encrypted name and name hash (when changing),
wrapped keys, chunk references, and signature. Each reference includes hash and
stored byte length. Large lists use immutable pages of at most 1024 references;
the signed manifest binds ordered page hashes and total count. An append adds
pages and may replace only the last partial page, not the preceding history.
Manifest canonicalization and its vectors are specified below and pinned in
`drive-manifests.json`.

Replace commits compare `base_version` to the current head and return `409` with
the head on mismatch. Creating a node requires a nonexistent ID and an unused
sibling name hash. A move verifies the destination is not a descendant and changes
only the wrapped node key and encrypted name; bytes remain encrypted as before.

Each append record includes node, author, per-author sequence, previous author
record hash, encrypted payload or sealed record, and signature. The relay assigns
a total per-node position. Duplicate operation IDs return the prior result;
gaps, reordered author sequences and conflicting duplicates are rejected. Clients
verify signatures and per-author continuity: signatures do not prove that the
relay showed every reader the same inter-author order. Checkpoint gossip is future
work; a malicious relay can withhold or fork views until clients compare them.

The group-commit window is at most 100 ms. All records in a batch become visible
only after one durable segment publication. Trim requires owner/admin authority
and a committed snapshot reference covering the discarded prefix. Readers behind
the retained prefix receive a resync response naming the snapshot; they never
silently skip missing records.

Retain versions for 30 days by default. Uncommitted uploads expire after 24 hours.
Quota counts unique stored chunk bytes reachable from live and retained versions,
plus explicit system/relay state accounting; per-member/link accounting controls
write caps. GC marks retained versions and pins in-flight commits under the same
per-drive serialization boundary before sweeping. It must not race a new commit
into referencing a chunk already selected for deletion.

### Implemented append-record encoding

`AppendRecord` has `format: 1`, `drive`, `node`, `author`, `generation`, `sequence`,
`previous`, `chunks`, optional `sealed`, and `signature`. Drive/author are canonical
lowercase identities without surrounding whitespace. Node IDs are 16 random bytes
encoded as 32 lowercase hex characters. Generation and sequence start at 1 and are
safe integers. Sequence 1 has an empty previous hash; subsequent records name the
SHA-256 hash of their author's preceding record on that node.

Exactly one payload form is allowed: 1–1024 encrypted chunk references `{id,size}`,
or a sealed envelope with an empty `chunks: []`. A chunk ID is 32 bytes of lowercase
hex; size includes nonce/padding/tag and must have the chunk format's size/alignment.
The sealed envelope uses `ephemeral_public_key`, `nonce`, `ciphertext`, all strict
unpadded base64url. Its encryption context uses purpose
`record:<author>:<sequence>:<previous>` so author-chain substitution also fails AEAD.

The Ed25519 signing bytes are the UTF-8 prefix `poweur/drive/record-sign/v1\n`
followed by uint32 big-endian length-prefixed UTF-8 fields, in this exact order:
format (`1`), drive, node, author, generation, sequence, previous, chunk count;
then each chunk's ID and stored size in order; then sealed ephemeral key, nonce,
ciphertext (three empty fields for a chunk record). Counters are unsigned decimal
ASCII. The signature is not included in its own input.

Record hash = SHA-256(`poweur/drive/record-hash/v1\n` || canonical signing bytes ||
raw 64-byte signature), encoded as lowercase hex. Verify the signature with the
resolved author's key and check its role before applying content; knowing a record
hash alone is not verification. Keep separate `(sequence,hash)` cursors for each
`(drive,node,author)`. A duplicate/reordered sequence, gap, or mismatched previous
hash is an error and must not advance the cursor. Retry idempotency is the engine's
separate operation-ID contract.

### Implemented manifest and page encoding

A `ChunkPage` is `{format: 1, drive, node, chunks}` with 1–1024 chunk references. Its
hash (its content address, lowercase hex) is SHA-256 of `poweur/drive/page/v1\n`
followed by length-prefixed fields: format, drive, node, reference count, then each
chunk's ID and stored size. A page names its drive and node, so it cannot be spliced
into another node's content.

A `Manifest` is one signed version of a node: `format: 1`, `drive`, `node`, `version`
(16 random bytes, hex), `parent` (the base version; empty only on create), `operation`,
`author`, `generation`, `kind` (`file` or `folder`), `mode` (`replace` or `append` for
files, empty for folders), `folder` (the containing folder's node ID), `name` and
`name_hash`, `node_key`, `content_key`, `count` and ordered `pages`, and `signature`.
Every version carries the node's complete content: every page but the last is full,
and `count` must fit the page list exactly. Operations constrain the fields:

| Operation | Carries |
|---|---|
| `create` | no parent; node key; folder and name (both absent only for the drive root); a file's content key |
| `replace` | parent; a file's new content only |
| `move` | parent; new folder, new name and hash, node key re-sealed to the new folder; content unchanged |
| `rotate` | parent; new node key (and a file's new content key) at a higher generation; content re-encrypted |
| `remove` | parent; nothing else, count 0 |

Envelopes are bound with `Context(drive, node, purpose, generation)` using purposes
`name` (sealed to the containing folder's public key), `node-key` (to the containing
folder, or the identity key for the root) and `content-key` (to the node's own public
key). A create by a writer who holds only the folder's public key (a drop box) seals
its name and node key to the folder and uses a random 32-byte `name_hash` token; the
owner, who can open both, re-indexes it with a move.

Signing bytes are `poweur/drive/manifest-sign/v1\n` followed by length-prefixed fields:
format, drive, node, version, parent, operation, author, generation, kind, mode,
folder, name (ephemeral key, nonce, ciphertext), name hash, node key (three fields),
content key (three fields), count, page count, then each page hash. An absent envelope
is three empty fields. Manifest hash = SHA-256(`poweur/drive/manifest-hash/v1\n` ||
signing bytes || raw signature). Readers verify the signature with the author's
resolved key, the author's role, and that the fetched pages hash to the signed list in
order and total `count`.

These formats are pinned by `drive-names.json`, `drive-seals.json`, `drive-chunks.json`,
`drive-records.json` and `drive-manifests.json`. Do not infer a network API
implementation from the primitive package.

## Implemented drive engine

`apps/api/internal/drive/engine` implements the journal described above with these
current choices:

- **One operation per segment.** Each accepted commit (manifest, append batch of up to
  1024 records, trim or GC) is its own segment `journal/<seq>.json`, published with a
  create-only write after its pages and version manifests are stored. Group commit is
  not implemented yet.
- **Snapshots** are written every 64 segments. Cold start takes the newest snapshot whose
  sequence and segment hash match the journal, then replays; a gap fails closed.
- **Several processes, one store.** Before validating a commit or collecting, the engine
  replays any segments another process published (one missing-object read when nothing
  changed). A lost publish race returns `ErrStale` and the caller retries. Reads are
  served from the cache and may lag another process until its next write or event.
- **Idempotency.** A request ID (16 bytes hex) returns the prior result for the same
  content and is refused for different content, across restarts.
- **GC** drops superseded versions after the retention period through a journalled `gc`
  operation, deletes chunks no retained version or record references, and deletes
  uncommitted uploads older than the upload TTL. Clients that pause longer than the TTL
  between upload and commit must re-run `chunks/missing`. Run GC in one process per
  drive until E20-T17 leases exist.
- **Trim** releases the chunks of trimmed records; the segments that hold them stay until
  journal compaction.

## HTTP surface

All drive endpoints are under `/drive/{identity}` (`apps/api/internal/relay/drive_api.go`).
Every request is authenticated like an inbox pickup: `GET /auth/challenge?identity=<caller>`,
then `X-Poweur-Identity`, `X-Poweur-Challenge` and `X-Poweur-Signature` (the challenge signed by
the identity key or a session key with `X-Poweur-Session-Id`). The caller may be a visitor homed
on another relay; its key is resolved like any peer's. Until node shares (E20-T7) only the drive's
owner is permitted (`403` otherwise) — superseded by shares below. Knowing a hash is never permission to read a chunk.

| Operation | Request | Response |
|---|---|---|
| Drive root, usage, quota | `GET /drive/{identity}` | `{drive, root, used, quota}` |
| Missing chunks / transfer URLs | `POST /chunks/missing` `{chunks:[{id,size}]}` (≤ 1024) | `{missing:[{id,size,upload:{method,url,headers}}]}` |
| Upload through the relay | `PUT /chunks/{hash}` (body = encrypted chunk) | `{id,size}`; `422` if bytes do not hash to `{hash}` |
| Commit | `POST /commit` `{id, manifest, pages}` or `{id, records}` or `{id, trim:{node,before,snapshot:{node,version}}}` | `{seq, head, positions}`; `409 {head}` on a stale base |
| Changes | `GET /changes?cursor=N&limit=` | `{changes, cursor}` |
| Node at head | `GET /nodes/{node}` | node info (`ETag` = head) |
| Children | `GET /nodes/{node}/children?cursor=&limit=` | `{children, cursor}` |
| Retained history | `GET /nodes/{node}/history` | `{versions}` |
| Signed manifest / page | `GET /nodes/{node}/versions/{version}`, `…/pages/{page}` | immutable JSON |
| Chunk through a version | `GET /nodes/{node}/versions/{version}/chunks/{hash}` | immutable bytes |
| Chunk of an append record | `GET /nodes/{node}/chunks/{hash}` | immutable bytes |
| Append tail | `GET /nodes/{node}/records?from=N&limit=` | `{records:[{position,record}], next}`; `410 {trimmed_before, snapshot}` |
| Shares | `GET /shares` | `{shares}`: all for the owner; own and administered for members |
| Event stream | `GET /events` | SSE `drive.changed`, filtered per caller; `drive.revoked` then close |

`upload.url` is either the relay's own `PUT /chunks/{hash}` or, on S3 with `S3_PRESIGN=1`, a
15-minute presigned PUT whose `x-amz-checksum-sha256` header binds the chunk hash. Commit
requires the manifest or record author to be the authenticated caller, so a signed object
cannot be replayed through someone else's session. Commit request IDs are 16 random bytes
(hex); retrying one returns the first result. Limits: 16 MiB commit bodies, 4 MiB chunks,
1000 entries per listing page. Content-addressed responses carry
`Cache-Control: private, max-age=31536000, immutable`; everything else is `no-store`.

After a commit is durable the owner's event stream (`GET /events/{identity}`) receives

```json
{"type":"drive.changed","identity":"alice.example","timestamp":"…",
 "drive":{"drive":"alice.example","seq":7,"node":"…","operation":"append","position":3,
          "records":[{"position":3,"record":{…}}]}}
```

Appends whose records serialize to at most 16 KiB carry them inline. Events are advisory:
a reconnecting client reads `changes` from its cursor. Cross-relay member subscriptions,
presigned downloads and serving `/.well-known/poweur/` from `.poweur/public` come with
shares (E20-T7) and system files (E20-T6).

## Shares, links and worked flows

Shares identify nodes, not paths. Roles are capabilities: `read`, `write`,
`append`, `create`, `admin`. Effective access is the union of applicable grants;
append/create are not automatically read permissions. Each member gets the
appropriate node key sealed to their identity key. Revocation immediately removes
API access, then the owner rotates keys before further private writes. Previously
downloaded bytes remain readable. Ownership transfer copies ciphertext to the new
drive, re-seals the root key and re-issues shares under the new owner.

**Implemented share format and enforcement.** A share (`drive/share.go`,
`src/drive/share.ts`, pinned by `drive-shares.json`) names the drive, a random share ID,
the node, exactly one member identity or link ID, the role, the node key generation, the
node private key sealed for the member (only for `read`, `write` and `admin`; `append` and
`create` get only `node_public`), an optional RFC3339 expiry, caps (`bytes`, `files`,
`records`, `downloads`, `per_hour`), link-password parameters (`kdf`
`argon2id-m65536-t3-p1`, 16-byte `salt`, `verifier_hash` = SHA-256 of the verifier half),
a proof-of-work difficulty for anonymous link writes, the issuer and issue time, and the
issuer's Ed25519 signature over `poweur/drive/share/v1` length-prefixed fields.
Shares are committed through `POST /commit` (`{"share": …}` / `{"unshare": {"id": …}}`)
and journalled; the relay verifies the issuer's signature and that the issuer owns the drive
or holds `admin` on the node, that the generation matches the node, and that the node is
not waiting for a rotation. Commits need: create → `create` on the folder; replace/remove →
`write`; move → `write` on the node and `create` on the destination; rotate, trim, share and
revoke → `admin` (a member may also revoke their own share). Reads need `read` on the node.
Expired shares stop working at their expiry without a commit. Revoking a `read`/`write`/
`admin` share marks every live node below the shared node `rotate_required`: new content,
new children and new key-bearing shares there return `409` until that node's key is rotated.
Members discover their grants and sealed keys at `GET /shares`, read `GET /changes` filtered
to what they can read, and stream `GET /events`; a revocation that leaves them no share
closes the stream with `drive.revoked`.

A new private file: generate node/content keys, seal the name and keys, encrypt and
upload chunks, then sign and commit its manifest. Editing one chunk reuses the
other chunk IDs in a new manifest. Two replacements against one base produce one
success and one `409`; the losing client loads the base and head, merges locally,
and submits a newly signed version.

Concurrent appenders submit individually signed author chains. The relay assigns
positions 1 and 2 and durably publishes them together; both readers fold the same
ordered tail. A create-only file request seals its new child key to the folder's
public key, and cannot list or download any prior submission. Caps and existing
proof-of-work protect anonymous writes.

A link uses `/s/<token>#<secret>`; only the viewer reads the fragment. A password
uses argon2id with a random salt and separately domain-derived encryption and
verification outputs. Exact KDF parameters and link documents remain E20-T7 work;
no server password, fragment or decryption key may appear in logs or requests.
The viewer uses strict CSP and `no-referrer` and clears the fragment after capture.

An agent editing contacts validates and uploads the plaintext system document.
The relay publishes its commit and applies the policy before acknowledging. Cold
start rebuilds that same policy from the provider, including when every local
cache has been deleted.

## Threat model and privacy inventory

The relay and bucket see identity IDs, tree shape, opaque node IDs, padded stored
sizes, timestamps, versions, authors, share members, public/relay-readable files,
IP addresses and access patterns. Contacts and policies are intentionally visible
to the relay. Private names, content and content keys are not. A stolen bucket
reveals this same stored metadata; encryption does not hide the social graph of
explicit shares. A compromised unlocked device can disclose keys and plaintext.

Signatures and AEAD detect mutation, substitution and invalid author chains, but
not all rollback or fork attacks by a malicious relay. Clients retain last-seen
heads and must report regressions. A fresh device needs an independently trusted
checkpoint to rule out rollback. Revocation cannot erase a recipient's cached
keys. Fragments can leak through browser history, copying or a compromised viewer;
an abuse report deliberately supplies the fragment to its recipient.

Privacy copy and store labels must say: encrypted private content; provider-visible
metadata and public/system data; operational network metadata; optional analytics
subject to consent. They must not claim zero metadata or that the relay cannot
read contacts. Final legal/store copy remains an explicit E20-T1 release gate.

## Cutover

Only plaintext system data is migrated: identity document, profile and its avatar,
capabilities, contacts, policy, analytics, devices, connected apps and group roster.
Old files, shares, links and history are deliberately dropped. The operator command
must support dry run and idempotent restart, validate every imported document and
move the old tree to `identities.v1-backup/` only after successful migration.
Rehearse against a copy of production before deployment. Private history,
attachments, read state and consent logs must work on v2 before that cutover.
