---
id: sharing
sidebar_position: 5
title: Sharing & grants
---

# Sharing & grants

Any file or folder under `/shared` or `/apps` can be shared with other Poweur IDs and with
owner-local **groups**, with read or read-write permission (EPIC-005). The share subject is
a cryptographic identity that works across relays and domains — the thing incumbent drives
can't ACL. This page is the normative spec for grant documents, groups, the evaluation
rules, and revocation.

## Grants are signed files in the owner's tree

A grant lives at `poweur-sys/relay/shares/<share-id>.json`, signed by the **owner's
identity key**:

```json
{
  "share_id": "shr_1f2e3d4c5b6a7988",
  "owner": "alice.poweur.net",
  "path": "shared/project-x",
  "audience": [{"id": "bob.example.org"}, {"group": "team"}],
  "permissions": ["read", "write"],
  "created_at": "2026-07-17T10:00:00Z",
  "expires_at": null,
  "signature": "<base64url ed25519 over the canonical string>"
}
```

The relay **enforces** grants but cannot forge them: it verifies the owner's signature on
every load, so tampering with the stored files grants nothing. Because grants are ordinary
files in `poweur-sys/relay/`, they sync to the owner's devices like everything else — the
filesystem stays the source of truth, and the web/CLI "share" UX is just writing a file.

**Canonical signing string** (fields joined with `\n`; audience and permissions sorted, so
JSON ordering never matters):

```
poweur-share-grant
<share_id>
<owner, lowercase>
<normalized path>
id:bob.example.org,group:team      ← sorted, lowercase
read,write                         ← sorted
<created_at>
<expires_at or empty>
```

A direct grant created by upgrading a public capability carries the optional,
owner-signed `source_share_id`. Its canonical string appends:

```
poweur-share-source
<source_share_id>
```

This provenance is allowed only on a grant with exactly one direct identity recipient. It
does not grant authority; it lets the relay attribute a later, signed acceptance to the
correct public-link funnel without inspecting encrypted message content.

Groups use the same pattern (`poweur-share-group\n<name>\n<owner>\n<members sorted>\n<updated_at>`).

### What is shareable

- Paths under `/shared/...` and `/apps/...` — a folder or a single file, but not the roots
  themselves.
- `/private` and `/poweur-sys` are **never** shareable: the privacy invariant of the layout
  is not overridable by a grant document, even a validly signed one (the engine rejects
  such grants on load, loudly).
- `/public` needs no grant to read (any valid Poweur ID already can); write-sharing
  `/public` is not supported in v1.

### Permissions

v1 vocabulary: `read`, `write` (reserved for later: `share`, `admin`).

- **`write` implies `read`.** Delete, move and mkdir are write operations.
- **Inheritance: a grant covers its path and the entire subtree under it** — including
  children created after the grant. There are no per-file exceptions inside a granted
  subtree in v1: deny-listing single files inside a share complicates every cache and sync
  path for a case better served by restructuring folders. Share a narrower folder instead.
- **Traversal:** read access extends to the *ancestor directories* of a granted path so the
  recipient can browse to it (e.g. list `/shared` and see `project-x`). Listings filter
  per-entry, so ungranted siblings are invisible, and ancestor access is read-only.

### Evaluation order (deny wins)

For a visitor request on (path, access):

1. **Credential scope** caps everything — a `dav:read` token cannot write even into a
   write-granted share, and a path-scoped token stays inside its prefix.
2. **Layout defaults**: `poweur-sys/public` world-readable; `/public` readable by any
   valid ID; `/private`, `poweur-sys/relay`, `poweur-sys/private` closed to visitors.
3. **Grants** for `/shared` and `/apps`: union over all valid, unexpired grants whose
   audience matches the visitor (directly or through a group). No matching grant → deny.

Grant checks run identically on **every access surface**: WebDAV methods, the sync
changes feed, the manifest, and chunked uploads. A grant-holder's changes feed for
someone else's tree contains exactly the paths they can read.

### Audit

Writes through a share are journaled with `actor` = the visitor's identity (EPIC-004
journal), so the owner's changes feed shows who changed what. Cross-identity requests are
additionally logged to `poweur-sys/relay/logs/access.log`.

## Public links (capability URLs)

For sharing with people who have no Poweur ID, a grant may name a **link token**
instead of an identity or group:

```json
{
  "share_id": "shr_link0011223344",
  "owner": "alice.poweur.net",
  "path": "shared/project-x",
  "audience": [{"link": "k7m4qz2rt6vwx3ab5cdefghijn"}],
  "permissions": ["read"],
  "created_at": "2026-07-17T10:00:00Z",
  "expires_at": "2026-12-31T00:00:00Z",
  "link": {"password": "$argon2id$…", "max_downloads": 25},
  "signature": "…"
}
```

The token is 16 random bytes as lowercase unpadded base32 — 26 characters of
`[a-z2-7]`, 128 bits of entropy. Lowercase and base32 are deliberate: the URL
survives a mail client that lowercases it, and the alphabet has no `0`/`1`/`8`
to be misread when a link is copied by hand.

Visit it at **`https://<owner-identity>/s/<token>`** — browse/download, or an
upload-only file request, with no account or client software. (`https://<relay>/s/<owner>/<token>`
addresses the same share where the owner has no vanity host. The two forms
cannot be confused: a token never contains a dot, and an identity always does.)

Folder and file-request landing pages show the owner ID and its comparable signing-key
fingerprint, plus the signed expiry and password state when present. File requests also show
the remaining upload count and aggregate capacity set by the signed grant. This context lets a
visitor verify who asked and understand the boundary before acting; it never includes the
capability token, storage path, submitted filenames or visitor data.

### Rules the format enforces

- **One token per grant, and nothing else in the audience.** Identity/group
  audiences and link tokens are enforced on completely different code paths;
  mixing them in one document would mean one grant with two very different
  meanings.
- **Read-only or create-only.** Ordinary links carry exactly `read`. A file-request link
  carries exactly `create`; no link may ask for `write`.
- **Options are signed.** `link.password` and `link.max_downloads` are part of
  the canonical string, so the relay that *stores* the grant cannot strip the
  password off it or raise the cap. Passwords are argon2id PHC hashes (the same
  encoding app passwords use); a grant carrying a plaintext password is refused.
- A grant with no `link` object signs exactly the eight lines it always did, so
  introducing link shares invalidated no existing signature:

```
poweur-share-grant
…the eight lines above…
poweur-share-link                  ← only when a `link` object is present
<password hash, or empty>
<max_downloads, 0 = unlimited>
poweur-file-request                ← only when `link.file_request` is present
<max_uploads>
<max_bytes>
<max_object_bytes>
<sorted allowed_types, comma-separated>
<notify: true|false>
```

### Threat model

A capability URL is a **bearer credential in a string**. That is the whole point
— it is what lets someone with no Poweur ID read the file — and it is also the
whole risk. Treat one like a password you have mailed to someone.

| Leak path | What happens | What the system does |
|-----------|--------------|----------------------|
| **Referrer** | A shared HTML page links out; the browser sends the capability URL in `Referer` | Every `/s/` response sends `Referrer-Policy: no-referrer` and the page carries `<meta name="referrer" content="no-referrer">`. The viewer is one self-contained document with **no scripts and no external references** under `default-src 'none'`, so nothing it renders can phone the URL out |
| **Browser history / shared devices** | The URL persists on any machine that opened it | Not preventable. `Cache-Control: private, no-store` keeps the *content* out of shared caches; expiry and revocation are the real answer |
| **Forwarding** | The recipient forwards the mail; now a stranger has it | Not preventable by design — the token is the audience. Use a password, a download cap, and an expiry for anything that matters |
| **Crawlers and link previews** | A chat client or crawler fetches the URL | `X-Robots-Tag: noindex, nofollow, noarchive` and a matching meta tag. Note that a link-preview fetch still **spends a download** against a `max_downloads` cap |
| **Guessing** | Someone enumerates tokens | 128 bits of entropy, constant-time comparison, and per-IP rate limiting on the endpoint (password attempts cost far more budget than page views) |
| **Relay operator** | The relay stores the grant files | It cannot forge one (owner signature) and cannot edit the password or the cap off one (both are signed). It *can* read the shared bytes — the same trust the rest of the file layer assumes |
| **Stored XSS on an identity origin** | A shared `.html` file executing on `https://alice.poweur.net` | Active content (HTML/SVG/XML/JS) is served as `text/plain` with `Content-Disposition: attachment` and `nosniff`, exactly as `/pub` does it |
| **Bandwidth abuse** | A popular link becomes someone else's CDN | Per-share download and byte counters, persisted per identity, plus the optional `max_downloads` cap |

Error pages are part of the model: a **revoked** link and a token that never
existed render the *identical* page, so a link cannot be used to probe what an
identity once shared. Only **expiry** says what it is — whoever holds the token
already knows the link existed, so that admission leaks nothing and is the
difference between a usable page and a mystery 404. No error page ever echoes
the token back.

### Expiry, caps and revocation

- **Revocation is deleting the grant file** — the same verb as any other share
  (`poweur share revoke <share-id>`). Grants are re-read per request, so the
  next click is dead. **There is no cache window**, and no separate token store
  to fall out of sync with the grant.
- **`expires_at`** works exactly as it does for identity shares; an unparseable
  expiry fails closed. Re-issuing a link with a live grant beats a stale one
  carrying the same token, so replacing a link does the obvious thing.
- **`max_downloads`** counts successful *file* downloads, not page views;
  browsing a folder is free. The slot is claimed before any bytes move, so the
  cap holds when several visitors click at once — an aborted transfer costs the
  visitor a slot, which is the safe direction to be wrong in.
- **Passwords** gate the share behind an argon2id check. A correct password sets
  an HMAC session cookie bound to the owner, the token, the share id **and the
  password hash**, scoped to that share's URL prefix — so changing the password
  invalidates every session issued under the old one, and one share's cookie
  never opens another. Sessions last 12 hours, never outlive the grant's own
  expiry, and end on a relay restart.
- Counters live outside the visible tree (next to the metadata index) and
  survive restarts, so a cap that was reached stays reached.

### CLI

```
poweur share link add /shared/project-x [--password … | --password-stdin] \
    [--expires 2026-12-31T00:00:00Z] [--max-downloads 25]
poweur share link ls
poweur share revoke shr_…            # revoking a link is the ordinary verb
```

The URL is printed **once**, at creation. The relay stores only the token inside
the signed grant and the CLI keeps nothing, so a lost URL means issuing a new
link. `poweur share ls` shows a link share's audience as `link` rather than
printing the token.

## File requests (upload-only links)

A file request is a public-link grant with a signed `link.file_request` object and exactly
the `create` permission:

```json
{
  "share_id": "shr_request00112233",
  "owner": "alice.poweur.net",
  "path": "shared/inbox",
  "audience": [{"link": "k7m4qz2rt6vwx3ab5cdefghijn"}],
  "permissions": ["create"],
  "link": {"file_request": {
    "max_uploads": 10,
    "max_bytes": 104857600,
    "max_object_bytes": 10485760,
    "allowed_types": ["image/*", "text/plain"],
    "notify": true
  }},
  "created_at": "2026-09-24T00:00:00Z",
  "signature": "…"
}
```

The token authorizes only creation of a fresh object below the named folder. It does not
authorize a directory listing, download, overwrite, rename or delete. The relay prefixes
each submitted filename with fresh randomness and opens the destination with create-exclusive
semantics, so two guests can submit `photo.jpg` without observing or replacing one another.
Nested GETs always return the request's not-found page. The relay also enforces a 64 MiB hard
per-object safety ceiling until resumable anonymous uploads exist.

Count and aggregate-byte reservations are atomic and happen before bytes are committed. An
aborted attempt may consume quota; failing closed prevents concurrent requests from racing
the final slot. Persisted metrics contain only opens, upload counts, upload bytes and
timestamps per share — never IPs, paths, filenames, contents, tokens or user agents.
Revocation is the ordinary grant deletion and takes effect on the next request.

```text
poweur share request add /shared/inbox [--password … | --password-stdin] \
    [--expires 2026-12-31T00:00:00Z] [--max-uploads 10] \
    [--max-bytes 104857600] [--max-object-bytes 10485760] \
    [--allow-type 'image/*'] [--notify]
poweur share revoke shr_…
```

The terminal can complete the identity-upgrade lifecycle without switching to the web app.
`poweur requests --json` exposes the decrypted lifecycle body; save its `plaintext` field to a
mode-0600 file (or pipe it on standard input) for the explicit approval/accept steps:

```text
# Claimant: prove continuity with the public capability.
printf '%s' '<token>' | poweur share claim request alice.poweur.net \
  --share-id shr_… --token-file - --action uploaded

# Owner: verify the live source grant, issue a direct offer, then optionally consume the link.
poweur share claim approve --claim-file claim.json --perm rw --consume-link

# Claimant: verify the owner signature and create the credential-free local mount.
poweur share accept --offer-file offer.json
```

`poweur requests` drains the queue. For each valid offer or claim it writes the decrypted
body to `~/.poweur/requests/<message-id>.json` (mode `0600`) and prints the accept or approve
command with that path. An invalid body is not written.

Approval always delivers the new offer before deleting the public grant. A delivery failure
leaves both the new direct grant and the public link in place and exits non-zero, so the owner
can inspect or retry without silently cutting off either party. Acceptance resolves the owner,
verifies the signed grant, confirms the accepting identity is a direct audience member and only
then writes `shared/<owner>/<name>/.poweur-mount.json`. The owner's relay delivers that
`sys.share.accept` into the inbox when the sender is a direct audience member of the live
grant named in `metadata.share_id`, including while the inbox is `contacts_and_requests` or
`contacts_only`. Anyone else is still rejected.

`notify` is signed owner intent. After a successful upload the relay emits a payload-free
`file_request` event to the owner's authenticated live stream, carrying only the share ID as
the wake-up key. The files tree and persisted counters remain truth, so dropping a stream event
cannot lose an upload. Durable/offline submission notification UX remains E05-T6 work.

### Claiming an identity after a public action

Viewing, downloading or uploading remains complete without an account. Folder listings and
file-request pages link **Get a Poweur ID** to `/claim`, which redirects to the configured
launcher with a base64url-encoded claim context in the URL fragment: `share_id`, owner,
capability token and the completed action (`viewed`, `downloaded`, or `uploaded`). The fragment
is not sent in the HTTP request. The web app removes it from the address bar immediately and
preserves it through sign-in or identity creation. Download links do not send the visitor to
the bare launcher URL.

After the identity is unlocked, the claimant sends that context to the owner as an encrypted,
seven-day `sys.share.claim` message. Possession of the token proves continuity with the public
action but grants no new authority. The owner client checks that the source grant still exists,
is unexpired and contains the same token, then asks the owner to choose one of two explicit
actions:

- **Grant access** creates a fresh direct-ID grant and encrypted `sys.share.offer`, retaining
  the public link.
- **Grant + close link** creates and offers the direct grant first, then deletes the public
  grant. If offer creation fails, the public capability is left intact.

The claimant accepts the ordinary offer and gets the same credential-free mount as any other
direct share. The already-completed upload is not repeated or moved: the new grant points at
the existing folder. Claim messages carry the token only inside end-to-end encrypted content;
the plaintext envelope exposes only the registered type and `share_id`. The resulting direct
grant binds `source_share_id` into the owner's signature. Its acceptance envelope repeats that
opaque source ID so the relay can compare it to the grant without decrypting the body. The relay
records only aggregate `claim_started`, `id_claimed`, and `share_accepted` counters.
`id_claimed` advances only after a signed
identity claim is accepted for a verified, live file-request grant; no claimant ID is retained
with the capability metrics. `share_accepted` advances at most once per resulting direct grant,
and only after the named recipient's signed acceptance matches its signed source binding.

## Groups

`poweur-sys/relay/groups/<name>.json` — an owner-local, owner-signed member list:

```json
{"group": "team", "owner": "alice.poweur.net",
 "members": ["bob.example.org", "carol.poweur.net"],
 "updated_at": "2026-07-17T10:00:00Z", "signature": "…"}
```

A grant with `{"group": "team"}` in its audience follows the *current* member list: adding
a member grants access with one signed file update and no new grant; removing a member
revokes theirs. Limits: 1000 members per group, 100 audience entries per grant, 64 KB per
document. Group membership is visible to the relay (it must be, to enforce) but never to
other users.

Cross-owner **group identities** — a group with its own Poweur ID, usable in *any* owner's
grants — are the same document with an `admins` list and an `epoch`, kept in the group's own
tree and signed by the group's own key. They get their own page:
[Group identities](group-identities.md). An owner-local group name may not contain a dot,
which is what keeps `{"group": "team"}` and `{"group": "team.acme.poweur.net"}` from ever
meaning the same thing.

## Revocation & expiry

- **Revocation = deleting the grant file** (`poweur share revoke <id>` is exactly that).
  The relay re-reads grant files per request — the same immediate-revocation pattern app
  passwords use — so access dies on the next request, with no cache window.
- `expires_at` (RFC3339): expired grants deny; an unparseable expiry fails closed.
- DAV bearer tokens held by the recipient keep working for *other* things they're entitled
  to; the grant check happens per request, so token lifetime never extends a revoked share.

## Offer, accept and recipient mounts

Direct-identity grants are announced over the encrypted message channel. The plaintext
envelope exposes only the registered `sys.share.*` type and a signed `metadata.share_id`;
the lifecycle body and grant remain end-to-end encrypted.

An offer carries the complete owner-signed grant because the recipient cannot read the
owner's private grant directory:

```json
{"version":1,"grant":{"share_id":"shr_…","owner":"alice.poweur.net","path":"shared/project-x","audience":[{"id":"bob.example.org"}],"permissions":["read","write"],"created_at":"…","signature":"…"},"offered_at":"…"}
```

Before showing or accepting it, a client MUST resolve the owner, verify the grant signature,
and confirm that its own identity is a direct audience member. The offer is discovery
evidence, not authority: every read or write still obtains a fresh visitor token and the
owner's relay reloads the authoritative grant on that request.

Acceptance writes a credential-free pointer into the recipient's own tree at
`shared/<owner>/<name>/.poweur-mount.json`:

```json
{"version":1,"share_id":"shr_…","owner":"alice.poweur.net","source_path":"shared/project-x","permissions":["read","write"],"accepted_at":"…"}
```

A Poweur-aware client follows that pointer by resolving `owner`; an ordinary DAV client sees
the small JSON file and cannot mistake cached remote bytes for local data. The recipient then
sends `sys.share.accept` with `share_id`, `owner`, `recipient`, `mount_path`, and
`accepted_at`. A conversion acceptance also carries the non-secret `source_share_id` in signed
envelope metadata; the relay accepts it for accounting only when the live direct grant binds the
same source and names that sender. Deleting the grant revokes authority immediately; `sys.share.revoked` is a
best-effort hint that lets recipients remove or mark a dead pointer sooner.

Unaccepted offers expire: SDK-created offer envelopes live for at most seven days (or until
the grant expiry, whichever comes first). For a stranger, `contacts_and_requests` admits one
pending slot per `share_id`; `contacts_only` rejects the offer. Accepted contacts and an open
inbox receive it in the normal message stream. The relay caps encrypted offers at 64 KiB and
requires the expiry plus `metadata.share_id`, so offers cannot become an unbounded chat side
channel.

## CLI

```
poweur share add /shared/project-x --with bob.example.org --perm rw [--expires 2026-12-31T00:00:00Z]
poweur share ls
poweur share revoke shr_1f2e3d4c5b6a7988
poweur share group set team --members bob.example.org,carol.poweur.net
poweur share group ls
poweur share group remove team
```

Group identities have their own verbs (`poweur group create|show|add|remove`) and are
addressed with `--with-group <poweur-id>` — see [Group identities](group-identities.md).

For a manual or older-client flow, the recipient may still mint a DAV token for the owner's
tree directly (`poweur dav token --audience alice.poweur.net --scope dav:full --relay …`).
The lifecycle flow automates owner discovery and records the local mount pointer; both paths
use the same grant engine.

## Web app

**Files → 🔗** on any folder or file under `/shared` or `/apps` opens the share dialog:
audience (contacts, groups or a typed identity), read or read-write, optional expiry. The
grant is signed **in the browser** with the identity key and PUT into the owner's own tree,
so the same relay-cannot-forge-it property holds; **Files → 🔗** at the root lists every
grant with a Revoke button, which deletes the document.

**Files → Shared with me** is the recipient side. Accepted offers appear from the local mount
pointers; manually naming an owner remains available for grants made by older clients. A
visitor token is minted at `dav:full` — the scope is not the permission, the owner's signed
grant is, so a read-only token would refuse a write the owner actually allowed. Because read
covers ancestors of a granted path and listings filter siblings, an owner who shared one
folder shows exactly that folder and nothing else.

## Threat notes

- **Forged grants:** files in `poweur-sys/relay/shares/` signed by anyone but the owner
  are rejected on load (and logged). A relay-side write into the shares folder cannot
  mint access by itself.
- **Grant replay across owners:** the `owner` field is inside the signature and must match
  the tree being accessed; alice's grant copied into mallory's tree is rejected.
- **Audience enumeration:** grant documents live in `poweur-sys/relay` (owner + relay
  only); recipients learn only what their own feed/listing shows them, not who else has
  access.
- **Revoked-but-cached windows:** none server-side (per-request reads). Recipients may
  hold already-downloaded bytes; that is unavoidable in any sharing system.

## Deferred (tracked in EPIC-005)

- File-request links are implemented as the strict create-only v1 subset. Guest-to-ID claim
  continuity and owner-approved link-to-ID upgrade are implemented; opt-in live submission
  notifications, the full CLI lifecycle, and privacy-safe conversion counters ship, while
  durable/offline notification UX stays in E05-T6. Direct Poweur-ID offers,
  acceptance, mounts and revocation pruning are implemented; manual owner entry remains a
  compatibility path for grants created by older clients.
- Write access through a link, and per-link revocation without deleting the
  grant. Both are deliberate v1 omissions, not oversights.

## Expiring transfers (E05-T7, CLI foundation)

`poweur transfer create <file>` uploads a non-empty local file through the resumable chunked
endpoint into `shared/.transfers/<transfer-id>/`, then signs a read-only public-link grant for
that folder. Transfers expire after seven days by default and accept the same password and
download-cap controls as other public links. The command returns the URL once both the bytes and
grant are durable; a grant failure removes the just-uploaded transfer folder.

This is the CLI-first foundation, not the completed Send product. Multi-file web/mobile UX,
client-persisted resume state after a process restart, expiry cleanup that releases storage,
Poweur-ID/email delivery and first-download receipts remain tracked in E05-T7.
