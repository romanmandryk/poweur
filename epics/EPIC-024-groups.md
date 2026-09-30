# EPIC-024 — Groups: a Poweur ID that people share

- **Status:** in progress — T3 (group key & sharing with a group) done; rewritten
  2026-09-30 (replaces "Spaces": a group *is* the collaborative place, there is no separate
  Space object or `space.json`)
- **Priority:** P1 (the product layer that turns identities, messages and files into a place a
  team, family or project lives)
- **Depends on:** EPIC-020 (drive, shares, key rotation), EPIC-009 E09-T5 (group messaging),
  EPIC-017 (`@poweur/client`)
- **Consolidates:** EPIC-005 E05-T5 (group identities), EPIC-009 E09-T5 (group chat) and
  EPIC-020 E20-T7 (groups as share members) — those tasks stay done; everything about *managing*
  groups is owned here from now on
- **Interacts with:** EPIC-025 (live documents in a group folder), COM-1 (the owner pays),
  EPIC-010 / COM-2 (agents as members), COM-4 (apps pinned to a group)
- **Unlocks:** project rooms, family folders, client workspaces, game parties, agent teams

## Progress

| Task | Status | Notes |
|------|--------|-------|
| E24-T1 Group model: owner, admins, members, group folder | open | the owner is a person and pays; `group.json` is the only manifest |
| E24-T2 Admin authority | open | admins sign membership changes with their own keys; owner adds/removes admins |
| E24-T3 Group key & sharing with a group | **done** | a group key per membership epoch, sealed to every member; shares to a group seal to it; CLI `--group`, TS `groups.keys/publicKey`; `TestINT_GROUP_SHARE_01`, Go↔TS interop |
| E24-T4 SDK & web: create and run a group | open | TS group management; web create, members, group screen = chat + folder |
| E24-T5 Invitations & joining | open | invite by ID or link, accept/decline/leave, guest → ID |
| E24-T6 Activity (optional) | open | decide after T4 whether chat + file changes are enough |
| E24-T7 Federation, export & threat model | open | members on several relays, offline, export/import |
| E24-T8 Ownership transfer | open | new owner accepts; keys and billing move; no data moves |

## What a group is

A **group is a Poweur ID that several people share** — `atlas.poweur.net` — and because it is a
full identity it already has everything a workspace needs:

| A group has | Built on | Status |
|---|---|---|
| its own address, keys and profile | an ordinary hosted identity | done (E05-T5) |
| members and admins | signed `.poweur/relay/group.json`, versioned by `epoch` | done; owner/admin model is T1–T2 |
| a group chat | message fan-out by the group's relay, one envelope per member | done (E09-T5) |
| a group folder | the group's own drive, shared with the group | relay done (E20-T7); keys are T3 |
| a group key | a key per epoch, sealed to every member | T3 |

There is no separate Space object. The group's `group.json` is the manifest; the group chat is
its conversation; the group's drive is its folder. Anything that understands identities,
messages and shares already understands a group — a client that knows nothing about groups
still sees a chat thread and a shared folder.

**Roles:**

| Role | How many | Can |
|---|---|---|
| **owner** | exactly one, a person's Poweur ID | everything an admin can; add and remove admins; delete the group; transfer ownership. The group's storage counts against the owner's quota and plan (COM-1). |
| **admin** | any number | add and remove members; manage the group folder's shares; rename |
| **member** | up to 100 for chat (E09-T5 fan-out limit), 1000 in `group.json` | read and write the group folder and chat |

Roles are presets over what already exists (group.json lists, drive share roles, message
fan-out); there is no second permission engine. Guests (a subset of the folder, time-boxed)
are ordinary drive shares to a person, not group members.

**Organizations** — one party managing many IDs, seats and invoices — are a separate concern
(COM-1). A group does not need an organization, and an organization may own many groups.

### What happens when someone leaves or is removed

- **Chat:** the epoch moves; the group's relay refuses a fan-out built against the old epoch,
  so the removed member receives nothing sent after the change. Messages already delivered stay
  on their devices and in their own encrypted archive — no end-to-end encrypted system can
  recall what a device has decrypted.
- **Files:** their next request to the group folder is refused; every node under a share to the
  group becomes `rotate_required`, and the next write rotates keys (E20-T7), so content written
  afterwards is under keys they never held. Files they downloaded stay with them.
- **Group key:** a new epoch key is sealed to the remaining members only (T3).

## Tasks

### E24-T1 — Group model: owner, admins, members, group folder

- [ ] Record the owner (a person's ID) in `group.json`, signed; the existing `owner` field means
      "the group itself", so the person gets a new field (T1 names it and adds vectors, keeping
      old signatures valid the way `admins`/`epoch` were added).
- [ ] Group folder convention: `group create` makes the group drive's root the group folder and
      shares it with the group (`write`); members reach it as `--drive <group>`.
- [ ] Storage attribution: a group's drive usage is reported against its owner (hook for
      COM-1; today the relay counts it on the group identity).
- [ ] Late join: members added later do not see earlier chat (E09-T5 decision). Decide whether
      a group-key-sealed chat archive in the group folder should change that.
- [ ] Migrate existing groups: the creator becomes owner.

**Acceptance:** `group create` yields a group whose members can chat and open the group folder
immediately; `group show` names owner, admins, members and epoch.

### E24-T2 — Admin authority

Today only the device holding the group identity's key can change `group.json`; `--admin`
records intent but grants no power.

- [ ] Admin-signed membership changes: the owner signs the admin list; an admin signs a member
      change with their own key; relay and clients verify the chain (owner → admin → change) and
      the epoch precondition. The group key never has to be copied to admins.
- [ ] Owner-only admin add/remove; the last owner/admin invariants.
- [ ] Admins receive the group key (T3) so they can issue the next epoch's key.
- [ ] Go/TS vectors; relay validation; CLI `group add|remove` as an admin on another device.

**Acceptance:** an admin on relay B removes a member of a group hosted on relay A without the
group's identity key; a removed admin can no longer change membership.

### E24-T3 — Group key & sharing a folder with a group

A share seals a node key to one public key. For a group that must be a key every member holds.

- [x] Design (see "Group key" below).
- [x] `packages/identity`: group keyring format (`groupkeys.go`), seal domain
      `poweur/group/key/v1`, `group-keys.json` vectors; `@poweur/client` twin (`groupkeys.ts`).
- [x] Relay: validates `.poweur/relay/group-keys.json` and `.poweur/public/group-key.json` on
      write (group-signed, epoch = `group.json` epoch, sealed to exactly group ∪ members ∪
      admins); `GET /groups/{group}/keys` for members, `GET /groups/{group}/public-key` for
      anyone; the group itself may read its roster (its drive verifies members' versions).
- [x] CLI: `group create|add|remove` issue the next epoch key; `drive share add <path> <group>`
      seals to the group key; members open with `--group <group>` on drive and sync commands
      (the roster is presented to a relay that does not host the group); rotation re-issues group
      shares to the current group key.
- [x] TS SDK: `groups.keys()` and `groups.publicKey()`, `FileKeys.groupKeys`,
      `DriveClient.groupRoster`; Go↔TS interop test (`group-share-interop.test.ts`).
- [x] Integration: `TestINT_GROUP_SHARE_01` — group folder and a folder shared by an outsider on a
      third relay; removal rotates and locks the removed member out; a later member reads old
      and new content; fs and S3, same and cross relay.
- [x] Clients start decrypting at the highest node their share opens: a group admin sees the
      whole group drive but holds keys only through the group key.

**Acceptance:** Bob and Carol, on another relay than the group, read and write a folder shared
with the group using only their own keys; after Carol's removal she cannot read new content and
Dave, added later, can read old and new.

### E24-T4 — SDK & web: create and run a group

- [ ] TS: `groups.create/show/add/remove`, group keyring, group folder handle.
- [ ] Web: create a group, see and change members, a group screen with the chat and the folder;
      degraded states explained (e.g. not a member any more).
- [ ] Group avatar and profile (it is an identity: reuse the profile editor).

**Acceptance:** Playwright: create a group, add a member on another relay, chat, share a file in
the group folder, remove the member — on a phone viewport too.

### E24-T5 — Invitations & joining

- [ ] `sys.group.invite`, `sys.group.accept`, `sys.group.decline`, `sys.group.removed` and
      `sys.group.updated` typed messages, reusing offer delivery and inbox-policy rules.
- [ ] Invite by Poweur ID, contact picker and expiring invite link; a guest preview shows only the
      inviter and the group's public profile — never members or file names.
- [ ] Claim/sign-in handoff keeps the invitation and returns the new user to the group.
- [ ] Expiry, resend, revoke, duplicate acceptance and blocked-inviter behaviour; abuse limits.

**Acceptance:** a signed-out guest opens an invite, claims an ID, joins and sees the group; a
revoked or expired invite cannot be replayed.

### E24-T6 — Activity (optional)

An **activity feed** is a durable, shared list of what happened in the group — "Carol joined",
"Bob updated plan.md", "Alice mentioned you in a comment" — that members scroll and that drives
notifications, separate from the chat so the chat is not flooded by bots and file events.

- [ ] After T4, decide whether the group chat (with system messages) plus the drive's change
      stream cover it. If not: an append log in the group folder sealed to the group key,
      mentions delivered as notifications, per-member notification preferences.

### E24-T7 — Federation, export & threat model

- [ ] Owner, admins and members on at least three relays; offline membership and file edits;
      stale-epoch recovery; relay restart.
- [ ] Export/import a group from its drive and signed documents; nothing poweur.net-only.
- [ ] Threat model: malicious admin, compromised member, invite forwarding, membership probing
      (no "is X in this group?" oracle), activity spam.

### E24-T8 — Ownership transfer

The owner hands the group to another person — for example, stepping back from a project.

- [ ] `sys.group.transfer` offer; the new owner must **accept** (ownership costs quota).
- [ ] On acceptance: the group identity's keys are sealed to the new owner, `group.json` names the
      new owner, and storage attribution moves (COM-1).
- [ ] **No data moves and nothing is re-encrypted.** Files, the group folder and group metadata
      live in the *group's* drive under the group's keys, not in the owner's drive; chat history
      lives with each member. Only key custody and billing change.
- [ ] Afterwards, rotate the group identity's signing key (identity rotation, EPIC-001) and issue
      a new group-key epoch, so the previous owner cannot act as the group; they stay a member
      or admin only if the new owner keeps them.

**Acceptance:** after a transfer the old owner cannot change membership or sign as the group,
members notice nothing but the new owner's name, and the group's storage counts against the new
owner.

## Group key (E24-T3 design)

Option A of the 2026-09-30 decision: one group key per membership epoch, instead of one share
per member.

- **Keyring** — `.poweur/relay/group-keys.json` in the group's drive, signed by the group:

  ```json
  {
    "version": 1,
    "group": "atlas.poweur.net",
    "epoch": 4,
    "public": "<x25519 public key of epoch 4>",
    "sealed": { "bob.example.org": { "ciphertext": "…", "ephemeral_public_key": "…", "nonce": "…" } },
    "previous": [ { "epoch": 3, "public": "…", "sealed": { … } } ],
    "updated_at": "…",
    "signature": "…"
  }
  ```

  `sealed` holds the epoch's private key sealed to each member's and admin's encryption key
  (domain `poweur/group/key/v1`, context `group \n epoch \n recipient`). `previous` holds each
  earlier epoch's private key sealed to the current epoch's public key, so a current member can
  open every older epoch and therefore every older share. The relay sees only ciphertext.
- **Public key** — `.poweur/public/group-key.json` (`group`, `epoch`, `public`, signature),
  served at `https://<group>/.well-known/poweur/group-key.json` so anyone can share with the
  group without being in it. It reveals that the ID is a group and its epoch, not its members.
- **Sharing** — a share to a group seals the node key to the current public key; the share
  format does not change. A member opens it by trying the epoch keys they hold, newest first.
- **Membership change** — whoever changes `group.json` generates the next epoch key and writes
  the keyring; on removal the relay marks group-shared nodes `rotate_required` and the sharer's
  next write rotates and re-issues the share to the new public key.
- **Groups on another relay** — members present the signed roster
  (`X-Poweur-Group-Roster`, E20-T7) when the drive they read is not on the group's relay.

## Non-goals

- A Space object, `space.json` or a workspace database — a group is the place.
- A second permission system beside `group.json`, drive shares and message fan-out.
- CRDT merging and live cursors — EPIC-025.
- Plans, seats and invoices — COM-1 (organizations live there).
- Voice/video and game matchmaking.
