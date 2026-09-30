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

## Sharing a folder with a group

A group is a share member like a person (storage v2, EPIC-020 E20-T7): a share's `member` is the
group's Poweur ID. Two things make it work:

- **Access** — the relay holding the drive lets a caller in when they are in the group's
  `members` or `admins`. For a group hosted on the same relay it reads the roster itself; for a
  group hosted elsewhere the member presents the group's signed roster in
  `X-Poweur-Group-Roster`, which the relay verifies with the group's key and checks against the
  group relay's current epoch. When a newer roster drops someone, their access ends and every
  node under a key-bearing share to the group becomes `rotate_required`.
- **Keys** (EPIC-024 E24-T3) — a share seals the node key to one public key. For a group that
  is the **group key**: one X25519 key per membership epoch, published at
  `https://<group>/.well-known/poweur/group-key.json` (and `GET /groups/{group}/public-key` on the
  group's relay) and sealed to every member, admin and the group itself in
  `.poweur/relay/group-keys.json`. Each keyring also seals every earlier epoch's key to the
  current one, so a current member opens shares made at any epoch — including a member added
  later. A removed member gets no key for the new epoch; the sharer's next write rotates the
  node keys and re-issues the share to the new group key.

Members read the keyring through `GET /groups/{group}/keys` (members, admins and the group
itself; everyone else gets the same `404` as "no such group"). The relay checks on every write
that the keyring is signed by the group, issued at the roster's current epoch and sealed to
exactly the group, its members and its admins; it never sees a group key in the clear.

The group's own drive is its **group folder**: `group create` issues the first key, and the
group shares its folders with itself.

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

# Share a folder with the group (from any drive: the sharer need not be in it).
poweur drive share add /Plans crew.acme.poweur.net --role write --no-offer

# Members open it with the group key.
poweur drive list /<shared-node-id> --drive <sharer> --group crew.acme.poweur.net
poweur sync run ~/Crew --drive <sharer> --folder /<shared-node-id> --group crew.acme.poweur.net
```

`group create` registers the group as an ordinary hosted identity, stores its keys on the
device that created it, writes the signed `group.json` and the first group key, and then puts
the **active identity back** — creating a group must not quietly change which identity the next command speaks
as. With no `--admin`, the creating identity is the sole admin; with no `--member`, it is
also the sole member.

Every `add` or `remove` that changes the membership also issues the next epoch's group key.
`poweur group create` refuses a name without a dot.

## Limits and privacy

- 1000 members and 1000 admins per group; 100 audience entries per grant; 64 KB per
  document (shared with grants).
- Group membership is visible to the **relay hosting the group** — it must be, to enforce —
  and to anyone holding owner credentials for the group. It is not exposed to other
  users, and there is no endpoint that answers "is X in this group?"; that omission is
  deliberate. The group key (`group-key.json`) is public: it reveals that an ID is a group and
  its epoch, not its members.
- A grant naming a group leaks the group's *name* to whoever can read the grant, which in
  practice is the owner's relay and the owner's own devices.
