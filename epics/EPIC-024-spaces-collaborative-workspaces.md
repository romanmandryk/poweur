# EPIC-024 — Spaces: collaborative workspaces

- **Status:** proposed
- **Priority:** P1 (the product layer that turns protocol primitives into a repeatable habit)
- **Depends on:** EPIC-005 E05-T3 (offers/acceptance/mounts), EPIC-009 (threads, groups, push),
  EPIC-017 (`@poweur/client`)
- **Interacts with:** EPIC-007 (trust), EPIC-011 (membership key rotation), EPIC-020
  (append logs and advanced permissions), EPIC-025 (live documents), EPIC-010 (agents)
- **Unlocks:** a single user-facing object for projects, families, client rooms, game parties,
  agent teams and real-time collaboration

## Progress

| Task | Status | Notes |
|------|--------|-------|
| E24-T1 Space model & manifest | open | compose group identity, shared root and default thread |
| E24-T2 Membership, roles & key epochs | open | owner/admin/member/guest/agent; no parallel ACL engine |
| E24-T3 Invitations & guest conversion | open | ID invites, public invite links, accept/leave/remove |
| E24-T4 Activity, comments & mentions | open | durable activity vs ephemeral notification |
| E24-T5 Web/mobile product surface | open | spaces list; overview, discussion, files, people, activity |
| E24-T6 Federation, migration & E2E | open | cross-relay membership, offline changes, revoke, export |

## Goal

Give users one understandable collaborative object — a **Space** — that composes Poweur's
existing identities, groups, messages, files and grants. A Space is not a new server or a second
authorization system. It is a signed manifest binding:

- one stable Space ID and human profile,
- one group identity and membership epoch,
- one shared file root,
- one default conversation,
- optional app documents, agents and live documents.

The first journeys are a project room, a family folder and a client workspace. Each must work
across relays and remain exportable as ordinary Poweur data.

## Design direction

### Composition, not a new silo

A Space manifest lives in the group's public/control data and points at protocol objects rather
than copying them. The group document remains the membership authority; signed grants remain the
file authority; message envelopes remain the conversation transport. Clients that do not
understand Spaces can still browse the mounted files and receive the messages.

Suggested shape (T1 makes this normative):

```json
{
  "version": 1,
  "space_id": "spc_...",
  "identity": "project.example.org",
  "name": "Project Atlas",
  "group_epoch": 7,
  "root": "/shared/space-spc_.../",
  "default_thread": "thr_...",
  "apps": [],
  "created_at": "...",
  "signature": "..."
}
```

`space_id` is stable across display-name, host and path changes. Every referenced resource is
resolved and authorized independently; possession of a manifest grants no access.

### Roles map to existing capabilities

Roles are UX presets over group administration, signed grants and message permissions:

| Role | Membership | Files | Conversation | Administration |
|------|------------|-------|--------------|----------------|
| owner | member | read/write/share/admin | send/moderate | transfer/delete space |
| admin | member | read/write/share | send/moderate | invite/remove/change roles |
| member | member | read/write | send | — |
| guest | optional/time-boxed | explicitly granted subset | explicitly granted | — |
| agent | member or delegated identity | narrow path/scope grant | typed-message allowlist | — |

The table is a product default, not a new wire-level permission vocabulary. T2 must specify the
exact mapping and fail closed if one component cannot be updated atomically.

### Durable and ephemeral state stay separate

Membership, file commits, comments, mentions and consequential actions are durable. Online
presence, typing, cursors and transient call/game state are not Space activity records. EPIC-025
owns the generic live-session transport; this epic owns the durable workspace view.

## Tasks

### E24-T1 — Space model, manifest & lifecycle

- [ ] Specify `space.json`, canonical signing string, stable ID generation, schema and Go/TS
      conformance vectors.
- [ ] Define create, rename, move, archive, leave, ownership transfer and delete semantics.
- [ ] Bind a group identity, shared root and default message thread without making the manifest
      itself an authority token.
- [ ] Define discovery: spaces owned by an identity, accepted remote spaces and a portable local
      index that can be rebuilt from accepted offers/mounts.
- [ ] Define partial availability: files service offline, messaging-only member, stale group
      epoch and deleted owner identity.
- [ ] CLI/SDK primitives to create/show/list/archive/export a Space.

**Acceptance:** two independent clients construct the same verified Space view from the manifest
and referenced objects; deleting or forging the manifest cannot grant access to files or messages.

### E24-T2 — Membership, roles & membership-key epochs

- [ ] Specify role-to-grant/message/admin mappings and the allowed transitions.
- [ ] Make group identity administration usable across relays; close E05-T5's membership-check
      privacy and enumeration design before using remote groups for authorization.
- [ ] Atomic or recoverable membership updates: group epoch, grants, message membership and
      encrypted key-domain rotation cannot silently disagree.
- [ ] Removal semantics: immediate future-access revocation, honest treatment of already
      downloaded content and rotation for subsequent encrypted writes.
- [ ] Ownership transfer and last-owner invariants.
- [ ] Attribute every membership and role change to a verified actor in the activity log.

**Acceptance:** an admin on relay A adds and later removes a member on relay B; the member gains
the documented surfaces, then loses future file/message access within one event cycle, with no
claim that downloaded data was recalled.

### E24-T3 — Invitations, acceptance & guest conversion

- [ ] `sys.space.invite`, `sys.space.accept`, `sys.space.decline`, `sys.space.removed` and
      `sys.space.updated` typed messages, reusing E05-T3 delivery and inbox-policy rules.
- [ ] Invite by Poweur ID, contact/group picker and expiring public invite link.
- [ ] Guest preview states reveal only the inviter, Space profile and explicitly public summary;
      membership and private filenames never leak before acceptance.
- [ ] Claim/sign-in handoff preserves the invitation and returns the user to the Space.
- [ ] Pending-invite expiry, resend, revoke, duplicate acceptance and blocked-inviter behavior.
- [ ] Abuse limits and owner-visible invite status without read-tracking individual members.

**Acceptance:** a signed-out guest opens an invite, claims an ID, joins, and sees the same Space;
a revoked or expired invite cannot be replayed, and a blocked inviter cannot bypass inbox policy.

### E24-T4 — Activity, comments & mentions

- [ ] Space activity PCP: membership changes, file/version events, comments, mentions, shares,
      agent actions and application-defined events.
- [ ] Append-only, independently sealed records using E20 log frames when available; define a
      v1 representation that migrates without changing event identity.
- [ ] Comments target stable resource/version IDs rather than mutable display paths; replies and
      resolution state are ordinary events.
- [ ] Mentions resolve Poweur IDs and deliver a notification without duplicating the comment body
      into relay-readable metadata.
- [ ] Pagination, compaction, retention and redaction/tombstone semantics.
- [ ] Notification preferences are per member and do not mutate shared history.

**Acceptance:** a comment on a renamed file remains attached to its intended version; offline
members reconstruct ordered activity after reconnect, and muted notifications do not remove
durable activity.

### E24-T5 — Web and mobile Space experience

- [ ] Spaces destination with owned/joined/archived sections and unread/activity indicators.
- [ ] Space shell: Overview, Discussion, Files, People, Activity and Apps/Agents slots.
- [ ] Create, invite, accept, role change, leave, archive and delete journeys with accessible
      mobile, tablet and desktop layouts.
- [ ] Plain explanations for degraded capabilities (for example, a messaging-only member cannot
      upload files) rather than generic errors.
- [ ] First-run templates: project room, family share and client workspace. Templates create the
      same open manifest and grants; they are not proprietary server modes.
- [ ] Product analytics limited to lifecycle counts and latency/error outcomes; no content,
      filenames or social graph export.

**Acceptance:** the three templates pass Playwright journeys spanning invitation, discussion,
file editing and member removal, including a narrow mobile viewport.

### E24-T6 — Federation, compatibility, migration & E2E

- [ ] Integration topology with owner and members distributed across at least three relays and a
      separately hosted files service.
- [ ] Offline membership and file edits, duplicate/out-of-order events and stale-epoch recovery.
- [ ] Export/import a Space using documented files and signed records; no poweur.net-only state is
      required to reconstruct it.
- [ ] Move an owning identity or files endpoint without changing `space_id`.
- [ ] Compatibility behavior for clients that understand shares/groups/messages but not Spaces.
- [ ] Threat model: malicious admin, compromised member, invite forwarding, membership probing,
      confused-deputy grants and activity spam.

**Acceptance:** the federation suite survives relay restart and endpoint migration; a legacy
client retains access to the constituent share and thread; export/import reconstructs the Space.

## Non-goals

- A new centralized workspace database or Space-only relay.
- Replacing signed grants, group identities or message authorization.
- CRDT/document merging and cursor transport — EPIC-025.
- Voice/video media infrastructure or game matchmaking.
- Subscription plans and seat billing — EPIC-026.

