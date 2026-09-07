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
- Public-link (capability URL) shares — E05-T4.
- Group identities — E05-T5 (design only).
