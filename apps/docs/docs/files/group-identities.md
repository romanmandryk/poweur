---
id: group-identities
sidebar_position: 6
title: Group identities
---

# Group identities

A **group identity** is a group that has its own Poweur ID. `crew.acme.poweur.net` is an
ordinary hosted identity with its own key pair, registered like any other — and one
document in its own tree makes it a group:

```
.poweur/relay/group.json
```

That is the whole idea. Because the group *is* an identity, it can be named anywhere an
identity can: as a share audience today (EPIC-005 E05-T5), and as a message recipient once
EPIC-009 lands group messaging on top of the membership and addressing model on this page.

This complements the **owner-local** groups described in
[Sharing & grants](storage-v2.md#shares-links-and-worked-flows), which are a naming convenience inside one owner's
grants. The two are the same document format; they differ in where the document lives, who
signs it, and who can name it.

|  | Owner-local group | Group identity |
|---|---|---|
| Name | a bare word, `team` | a Poweur ID, `crew.acme.poweur.net` |
| Document | `.poweur/relay/groups/<name>.json` in the **owner's** tree | `.poweur/relay/group.json` in the **group's** tree |
| Signed by | the owner's identity key | the **group's own** identity key |
| Meaningful to | that owner's grants only | anyone — it is an address |
| Extra fields | — | `admins`, `epoch` |
| Addressable | no | yes (shares now, messaging in E09-T5) |

## The membership document

```json
{
  "group": "crew.acme.poweur.net",
  "owner": "crew.acme.poweur.net",
  "members": ["bob.example.org", "carol.poweur.net"],
  "admins": ["alice.poweur.net"],
  "epoch": 3,
  "updated_at": "2026-07-17T10:00:00Z",
  "signature": "<base64url ed25519, the group's own key>"
}
```

Field by field:

- **`group`** — the group's Poweur ID, lowercase.
- **`owner`** — the same Poweur ID. A group identity is its **own owner**: the document
  lives in the group's tree and is signed by the group's key, so there is nobody else it
  could belong to. A document whose `group` and `owner` disagree is rejected.
- **`members`** — the identities the group confers access to. Lowercased, trimmed,
  de-duplicated and sorted; compared case-insensitively. Max 1000.
- **`admins`** — the identities entitled to change this document. **Its presence is what
  marks the document as a group identity**; an owner-local group has no `admins` key.
  Max 1000.
- **`epoch`** — a monotonic membership version, starting at 1, incremented on every change
  that actually changes the membership.
- **`updated_at`** — RFC3339.
- **`signature`** — over the canonical string below, by the group's identity key.

**`admins` is an authority list, not a membership list.** Access follows `members` alone.
An identity may be an admin without being a member (an operations account that administers
a team it is not in), a member without being an admin (the common case), both, or neither.
Nothing is implied in either direction — a consumer that wants "everyone associated with
this group" has to union the two itself and say so.

### Canonical signing string

```
poweur-share-group
<group, lowercase>
<owner, lowercase>
<members, lowercase, sorted, comma-joined>
<updated_at>
poweur-group-identity
<admins, lowercase, sorted, comma-joined>
<epoch, decimal>
```

The last three lines are appended **only when `admins` is non-empty**. A group with no
admins signs exactly the five lines owner-local groups always signed, so introducing group
identities invalidated no existing signature — the committed conformance vectors for
owner-local groups are byte-identical across the change.

Binding `admins` and `epoch` into the signature is what stops whoever *stores* the file —
the relay — from editing an admin off the list or replaying an older membership under a
newer version number.

### The two namespaces cannot collide

A grant's audience entry is `{"group": "<name>"}` for both kinds. They are told apart by a
single rule: **a group identity's name contains a dot; an owner-local group's name may
not.** A Poweur ID is a domain name and always has a dot, and owner-local names are now
validated to have none. So `{"group": "team"}` means alice's own `team` forever, and
`{"group": "team.acme.poweur.net"}` means the group identity — no lookup order, no
shadowing, no ambiguity to resolve.

## Addressing a group as a share audience

Nothing about the grant format changes. The owner names the group in the audience like any
other subject:

```json
{
  "share_id": "shr_1f2e3d4c5b6a7988",
  "owner": "alice.poweur.net",
  "path": "shared/crew-docs",
  "audience": [{"group": "crew.acme.poweur.net"}],
  "permissions": ["read"],
  "created_at": "2026-07-17T10:00:00Z",
  "signature": "<alice's key>"
}
```

The relay evaluates it per request, in one snapshot:

1. Load and verify alice's grants out of her tree with **alice's** key, as always.
2. For every audience entry naming a group identity, resolve that group **out of its own
   tree** and verify `self.json` with the **group's** key.
3. A visitor matches the grant if they are in that group's `members`.

Step 2 is the load-bearing one. The membership document is fetched from the group's tree,
never from the owner's, so:

- **Naming a group in a grant confers no power over that group.** A `self.json` alice signs
  for a group she does not own grants nobody anything — it is not in that group's tree and
  is not signed by that group's key.
- **One group's membership cannot be served for another.** The resolved document must name
  itself: `group` and `owner` must both equal the group being resolved.
- **An owner-local document cannot masquerade as a group identity.** A `self.json` with no
  `admins` list is refused at this path.
- **Membership changes need no new grant.** Adding a member is one signed update to the
  group; the next request by that member is allowed. Removing one revokes on the next
  request, with no cache window — grants and groups are re-read per request, the same
  immediate-revocation pattern app passwords use.

Every refusal is logged by the relay with the reason and **denies**. A group that cannot
be resolved never opens access.

### Deferred: cross-relay group resolution

v1 resolves only group identities hosted on the **same relay** as the grant's owner. A
group hosted elsewhere fails closed, with a log line saying so, because the relay would
have to fetch and verify another relay's membership document over the network on the
permission path. That needs a membership-check endpoint with its own caching, rate limiting
and privacy story (asking "is X in your group?" is an enumeration oracle), so it is a
follow-up, not a v1 shortcut.

## Authority: who may change a group

In v1 the enforceable authority is **possession of the group's identity key**. The document
lives in the group's own tree, so writing it needs owner authentication with that key, and
it is signed with that key so the relay verifies it exactly as it verifies any other
identity's document. No new relay-side authorization rule was introduced — a group is
administered the way an identity administers itself.

The `admins` list is the *recorded* authority: it is what audit and E09-T5 need in order to
say who legitimately holds the key, and the CLI refuses to sign an update on behalf of an
identity that is not on it. That check is client-side by construction, so treat `admins` as
a statement of intent that the signature commits to, not as a second gate.

Two rules the client enforces:

- **The last admin cannot be removed.** A group with no admins is a group nobody can ever
  change again, including to put an admin back.
- **The epoch advances only on a real change.** Re-adding an existing member is a no-op:
  it prints `no change`, signs nothing, and leaves the epoch alone.

Making authority cryptographic per admin — each admin signing their own update, the group
key never leaving a quorum — is the named follow-up. It changes who signs, not what the
document says, so the format above survives it.

## What `epoch` is for

`epoch` is a membership version, and it exists so that EPIC-009's group key agreement
(E09-T5) has a number to bind group keys to without inventing a second counter. The
contract it offers:

- It starts at **1** on creation.
- It increments by exactly 1 on each update that changes `members` or `admins`.
- It does **not** move for a no-op update, so a group key bound to epoch *N* is not
  invalidated by someone re-running a command.
- Because it is inside the signature, an older membership cannot be replayed under a newer
  epoch, and a newer one cannot be relabelled as older.

A consumer that caches a resolved membership can therefore use `(group, epoch)` as its
cache key, and a rekey trigger is simply "the epoch moved".

## CLI

```bash
# Register the group identity and sign its first membership document.
poweur group create crew.acme.poweur.net --member bob.example.org --member carol.poweur.net

# Read it back (verified, not just fetched).
poweur group show crew.acme.poweur.net [--json]

# One signed update per command; the epoch moves only if something changed.
poweur group add    crew.acme.poweur.net --member dave.example.org
poweur group add    crew.acme.poweur.net --admin  carol.poweur.net
poweur group remove crew.acme.poweur.net --member bob.example.org

# Address the group in a share.
poweur share add /shared/crew-docs --with-group crew.acme.poweur.net --perm read
```

`group create` registers the group as an ordinary hosted identity, stores its key on the
device that created it, writes the signed `self.json`, and then puts the **active identity
back** — creating a group must not quietly change which identity the next command speaks
as. With no `--admin`, the creating identity is the sole admin; with no `--member`, it is
also the sole member.

Owner-local groups keep their own verbs — `poweur share group set|ls|remove` — and
`poweur group create` refuses a name without a dot, pointing at them.

## Limits and privacy

- 1000 members and 1000 admins per group; 100 audience entries per grant; 64 KB per
  document (shared with grants).
- Group membership is visible to the **relay hosting the group** — it must be, to enforce —
  and to anyone holding owner credentials for the group. It is not exposed to other
  users, and there is no endpoint that answers "is X in this group?"; that omission is
  deliberate and is the same reason cross-relay resolution is deferred.
- A grant naming a group leaks the group's *name* to whoever can read the grant, which in
  practice is the owner's relay and the owner's own devices.
