# EPIC-005 — Sharing, ACLs, groups & public-to-any-valid-ID

- **Status:** proposed
- **Priority:** P1
- **Depends on:** EPIC-003 (storage + cross-identity auth); interacts with EPIC-004 (sync), EPIC-007 (contacts)
- **Unlocks:** EPIC-010 (cross-identity pipelines), collaborative apps

## Goal

Let an identity share any file or folder with other Poweur IDs and **groups**, with granular
permissions (read / write), verifiable grant records, easy revocation, and a receiving UX where
shares appear in the recipient's own namespace. This is the "share with alice@…" of Google
Drive — except the share subject is a cryptographic identity that works across providers, which
no incumbent offers (per the analysis in `apps/docs/docs/future/capabilities.md`: Drive/Dropbox
can't ACL a DNS identity; Poweur makes the identity itself the ACL subject).

## Design direction

- **Grants are signed documents, stored in the owner's tree.** A grant lives at
  `poweur-sys/private/shares/<share-id>.json`, signed by the owner's identity key:

```json
{
  "share_id": "shr_…",
  "owner": "alice.poweur.net",
  "path": "/shared/project-x/",
  "audience": [{"id": "bob.example.org"}, {"group": "grp_team@alice.poweur.net"}],
  "permissions": ["read", "write"],
  "created_at": "…", "expires_at": null,
  "signature": "<owner identity key>"
}
```

  The relay *enforces* grants but cannot forge them (signature check), and the grant set syncs
  to the owner's devices like any other file — the filesystem stays the source of truth.
- **Groups are files too**: `poweur-sys/private/groups/<name>.json` = signed member list owned
  by whoever administers the group. v1 groups are owner-local (alice's groups, used in alice's
  grants). Cross-owner "group identities" (a group with its own Poweur ID) are a later layer on
  the same format.
- **Share notification & acceptance ride on messaging**: the owner's relay sends a
  `sys.share.offer` message to each audience member; the recipient accepts, and the share gets
  mounted at `/shared/<owner-id>/<name>/` in *their* namespace (a mount-reference, not a copy).
- **Permission evaluation order** (deny wins): explicit grant > layout defaults
  (`/public` read for any valid ID, `/private` owner-only) > deny.

## Tasks

### E05-T1 — Sharing & permissions spec

- [ ] Spec `apps/docs/docs/files/sharing.md`: grant document format + canonical signing,
      permission vocabulary (v1: `read`, `write`; reserve `share`, `admin`), evaluation order,
      inheritance (grants apply to subtree; no per-file exceptions in v1 — document why),
      expiry, revocation semantics (tombstone + token invalidation timing)
- [ ] Group document format, membership update rules, max sizes
- [ ] Mount model for recipients: how `/shared/<owner>/<name>/` paths resolve to the owner's
      relay (recipient's relay proxies vs client connects to owner's relay directly — decide:
      v1 = client-direct, proxy later for privacy parity with messaging's privacy-proxy mode)
- [ ] Threat analysis: audience enumeration, grant replay across relays, group-membership
      privacy (who can see who's in a group), revoked-but-cached access windows

**Acceptance:** spec merged with worked examples for direct, group, expiring and revoked shares.

### E05-T2 — Relay grant engine

- [ ] Grant store: watch/load `poweur-sys/private/shares/` + `groups/` into the permission
      engine stubbed in E03-T4; verify signatures on load; reject malformed grants loudly
      (owner notification via `sys.*` message)
- [ ] Enforce on every DAV/sync/changes request: visitor identity × path → effective permission
- [ ] `PUT`-through-share: writes by grant-holders journal with `actor` = visitor id (E04-T1
      journal already carries `actor`) so owners can audit who changed what
- [ ] Revocation: deleting the grant file (or writing tombstone) invalidates within ≤ 60 s
      (token store re-check), tested
- [ ] Integration tests: read-only audience can't write; expiry honored; group member added →
      gains access without new grant

**Acceptance:** cross-relay share (bob on relay B granted by alice on relay A) read+write works;
revocation and expiry tests pass.

### E05-T3 — Share lifecycle UX: offer, accept, mount, list

- [ ] `sys.share.offer` / `sys.share.accept` / `sys.share.revoked` message types (uses the
      typed-message groundwork from EPIC-009; if that hasn't landed, define `type` in payload
      JSON — coordinate)
- [ ] Recipient's relay materializes accepted shares as mount-references under
      `/shared/<owner>/…` (a small JSON pointer file; sync clients and DAV resolve through it)
- [ ] CLI: `poweur share add <path> --with bob.example.org --perm rw`, `poweur share ls`,
      `poweur share revoke`, `poweur shares` (received)
- [ ] Web app: share dialog on any file/folder (audience picker fed by contacts, EPIC-007),
      received-shares view, "shared with" badges
- [ ] Unaccepted-offer policy: offers expire after N days; offers from non-contacts follow
      EPIC-007 inbox policy (shares are spam vectors too)

**Acceptance:** end-to-end demo test: alice shares a folder with bob, bob accepts in web UI,
edits a file, alice sees the edit + audit trail; alice revokes, bob loses access.

### E05-T4 — Public-link shares (capability URLs)

For sharing with people *outside* the network (no Poweur ID yet) — also the on-ramp funnel.

- [ ] Link-share grant variant: `audience: [{"link": "<token>"}]`, optional password
      (argon2id-hashed in grant), optional expiry + download-count limit
- [ ] `https://<identity>/s/<token>` web endpoint: read-only browse/download with a minimal
      viewer page; "claim a Poweur ID to get edit access" upsell hook
- [ ] Rate limiting + bandwidth accounting on link endpoints

**Acceptance:** link share with password + expiry works in a browser with no auth; revocation
kills the link.

### E05-T5 — Group identities (design + v1)

Groups that are *addressable* (`team.acme.poweur.net` as a share audience AND message
recipient) unify EPIC-005 and EPIC-009 group messaging.

- [ ] Design doc: a group as a hosted identity whose `poweur-sys/private/groups/self.json`
      holds members; admin operations are signed member-list updates; relays resolve
      group→members server-side for shares and message fan-out
- [ ] v1: create/admin group identities via CLI; usable as share audience
- [ ] Defer/coordinate: group E2EE messaging key agreement is EPIC-009's problem — keep
      formats compatible

**Acceptance:** a grant to a group identity gives all members access; adding a member is one
signed update; design doc covers messaging fan-out for EPIC-009 to consume.
