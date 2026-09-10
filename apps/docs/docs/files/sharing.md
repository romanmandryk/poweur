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

Visit it at **`https://<owner-identity>/s/<token>`** — read-only browse and
download, no account, no client software. (`https://<relay>/s/<owner>/<token>`
addresses the same share where the owner has no vanity host. The two forms
cannot be confused: a token never contains a dot, and an identity always does.)

### Rules the format enforces

- **One token per grant, and nothing else in the audience.** Identity/group
  audiences and link tokens are enforced on completely different code paths;
  mixing them in one document would mean one grant with two very different
  meanings.
- **Read-only in v1.** A grant that names a link and asks for `write` is
  rejected at signing and on load, so a leaked URL can never mutate the tree.
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
other users. Cross-owner *group identities* (a group with its own Poweur ID, usable across
owners and as a message recipient) are designed in E05-T5 and deferred.

## Revocation & expiry

- **Revocation = deleting the grant file** (`poweur share revoke <id>` is exactly that).
  The relay re-reads grant files per request — the same immediate-revocation pattern app
  passwords use — so access dies on the next request, with no cache window.
- `expires_at` (RFC3339): expired grants deny; an unparseable expiry fails closed.
- DAV bearer tokens held by the recipient keep working for *other* things they're entitled
  to; the grant check happens per request, so token lifetime never extends a revoked share.

## CLI

```
poweur share add /shared/project-x --with bob.example.org --perm rw [--expires 2026-12-31T00:00:00Z]
poweur share ls
poweur share revoke shr_1f2e3d4c5b6a7988
poweur share group set team --members bob.example.org,carol.poweur.net
poweur share group ls
poweur share group remove team
```

The recipient needs no ceremony in v1: they mint a DAV token for the owner's tree at the
owner's relay (`poweur dav token --audience alice.poweur.net --scope dav:full --relay …`)
and the grant engine does the rest — including cross-relay recipients, whose keys the
owner's relay verifies through the resolver chain.

## Web app

**Files → 🔗** on any folder or file under `/shared` or `/apps` opens the share dialog:
audience (contacts, groups or a typed identity), read or read-write, optional expiry. The
grant is signed **in the browser** with the identity key and PUT into the owner's own tree,
so the same relay-cannot-forge-it property holds; **Files → 🔗** at the root lists every
grant with a Revoke button, which deletes the document.

**Files → Shared with me** is the recipient side: name an owner and browse their tree with
a visitor token minted at `dav:full` — the scope is not the permission, the owner's signed
grant is, so a read-only token would refuse a write the owner actually allowed. Because
read covers the ancestors of a granted path and listings filter siblings, an owner who
shared one folder shows exactly that folder and nothing else. There is no "shares granted
to me" listing: grants live in the owner's `poweur-sys`, which only they can read, and
changing that is the `sys.share.offer` work below.

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

- `sys.share.offer` / accept / revoked notification messages and recipient-side
  `/shared/<owner>/…` mount-references (needs EPIC-009 typed messages). Until then a
  recipient has to be told *who* shared with them out of band — the web app's
  "Shared with me" asks for the owner by name for exactly this reason.
- Link shares in the **web app's** share dialog — E05-T4 ships the format, the
  `/s/<token>` endpoint and the CLI; the browser-side dialog for issuing one is
  still to come (the grant is signed client-side, so it is UI work, not protocol).
- Write access through a link, and per-link revocation without deleting the
  grant. Both are deliberate v1 omissions, not oversights.
