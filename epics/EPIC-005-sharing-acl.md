# EPIC-005 — Sharing, ACLs, groups & public-to-any-valid-ID

- **Status:** complete except the offer/accept UX (grant engine, CLI, web dialog, link shares and group identities shipped; `sys.share.offer/accept` messages and recipient mounts deferred to EPIC-009)
- **Priority:** P1
- **Depends on:** EPIC-003 (storage + cross-identity auth); interacts with EPIC-004 (sync), EPIC-007 (contacts)
- **Unlocks:** EPIC-010 (cross-identity pipelines), collaborative apps

## Progress

| Task | Status | Notes |
|------|--------|-------|
| E05-T1 Sharing & permissions spec | **done** | [`apps/docs/docs/files/sharing.md`](../apps/docs/docs/files/sharing.md); grant/group canonical signing in `packages/identity/grants.go`; inheritance = whole-subtree, no per-file exceptions (documented why); mount model deferred with T3 |
| E05-T2 Relay grant engine | **done** | `apps/api/internal/files/grants.go`: per-request verified snapshot (revocation immediate, no ≤60 s window — app-password pattern); enforced on DAV + changes + manifest + chunked upload; visitor writes journal `actor`; forged/replayed/malformed grants rejected loudly; extensive scenario matrix in `apps/api/internal/relay/shares_test.go` + cross-relay `TestINT_SHARE_01` |
| E05-T3 Share lifecycle UX | **partial** | CLI shipped: `poweur share add/ls/revoke`, `share group set/ls/remove` (grants written over DAV, listed via the sync manifest); web share dialog, revoke view and received-shares browser shipped with [EPIC-015](EPIC-015-web-app-ux.md) E15-T4. **Deferred:** `sys.share.offer/accept/revoked` messages and recipient-side `/shared/<owner>/…` mount-references (needs EPIC-009 typed messages) — until then a recipient must be told who shared with them; unaccepted-offer policy (EPIC-007) |
| E05-T4 Public-link shares | **done** | `audience: [{"link": …}]` grant variant (26-char base32 token, argon2id password, expiry, download cap) in `packages/identity/grants.go`; `/s/<token>` endpoint in `apps/api/internal/relay/`; `poweur share link add/ls` (revoke via `share revoke`); spec + threat model in [`sharing.md`](../apps/docs/docs/files/sharing.md); cross-relay `TestINT_SHARE_03` + web e2e |
| E05-T5 Group identities | **done** | design doc [`group-identities.md`](../apps/docs/docs/files/group-identities.md); `admins` + `epoch` on `ShareGroup`, group's own tree at `poweur-sys/relay/groups/self.json` signed by the group's key; engine resolves named group identities out of their own trees (`apps/api/internal/files/grants.go`); `poweur group create/show/add/remove`; `TestINT_SHARE_04`. **Deferred:** cross-relay group resolution (needs a membership-check endpoint — enumeration oracle); per-admin signed updates; group messaging fan-out + key agreement = EPIC-009 E09-T5 |

## Goal

Let an identity share any file or folder with other Poweur IDs and **groups**, with granular
permissions (read / write), verifiable grant records, easy revocation, and a receiving UX where
shares appear in the recipient's own namespace. This is the "share with alice@…" of Google
Drive — except the share subject is a cryptographic identity that works across providers, which
no incumbent offers (per the analysis in `apps/docs/docs/future/capabilities.md`: Drive/Dropbox
can't ACL a DNS identity; Poweur makes the identity itself the ACL subject).

## Design direction

- **Grants are signed documents, stored in the owner's tree.** A grant lives at
  `poweur-sys/relay/shares/<share-id>.json` (relay-readable zone — the relay must enforce
  grants), signed by the owner's identity key:

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
- **Groups are files too**: `poweur-sys/relay/groups/<name>.json` = signed member list owned
  by whoever administers the group. v1 groups are owner-local (alice's groups, used in alice's
  grants). Cross-owner "group identities" (a group with its own Poweur ID) shipped in E05-T5 as
  a layer on the same format: `poweur-sys/relay/groups/self.json` in the **group's** tree, signed
  by the group's own key, carrying an `admins` list and a monotonic `epoch`.
- **Share notification & acceptance ride on messaging**: the owner's relay sends a
  `sys.share.offer` message to each audience member; the recipient accepts, and the share gets
  mounted at `/shared/<owner-id>/<name>/` in *their* namespace (a mount-reference, not a copy).
- **Permission evaluation order** (deny wins): explicit grant > layout defaults
  (`/public` read for any valid ID, `/private` owner-only) > deny.

## Tasks

### E05-T1 — Sharing & permissions spec

- [x] Spec `apps/docs/docs/files/sharing.md`: grant document format + canonical signing,
      permission vocabulary (v1: `read`, `write`; reserve `share`, `admin`), evaluation order,
      inheritance (grants apply to subtree; no per-file exceptions in v1 — document why),
      expiry, revocation semantics (tombstone + token invalidation timing)
- [x] Group document format, membership update rules, max sizes
- [ ] Mount model for recipients — **deferred with E05-T3** (v1 recipients access the
      owner's relay directly with a visitor token): how `/shared/<owner>/<name>/` paths resolve to the owner's
      relay (recipient's relay proxies vs client connects to owner's relay directly — decide:
      v1 = client-direct, proxy later for privacy parity with messaging's privacy-proxy mode)
- [x] Threat analysis: audience enumeration, grant replay across relays, group-membership
      privacy (who can see who's in a group), revoked-but-cached access windows

**Acceptance:** spec merged with worked examples for direct, group, expiring and revoked shares.

### E05-T2 — Relay grant engine

- [x] Grant store: load `poweur-sys/relay/shares/` + `groups/` into the permission engine
      stubbed in E03-T4 (per-request snapshot, not a watcher); verify signatures on load;
      reject malformed grants loudly (relay log; owner `sys.*` notification deferred to
      EPIC-009)
- [x] Enforce on every DAV/sync/changes request: visitor identity × path → effective permission
      (plus manifest and chunked-upload endpoints; visitor token scope caps grants)
- [x] `PUT`-through-share: writes by grant-holders journal with `actor` = visitor id (E04-T1
      journal already carries `actor`) so owners can audit who changed what
- [x] Revocation: deleting the grant file invalidates on the next request (grants are
      re-read per request, app-password style — no cache window), tested
- [x] Integration tests: read-only audience can't write; expiry honored; group member added →
      gains access without new grant (relay scenario matrix in `shares_test.go` + engine
      matrix in `files/grants_test.go` + cross-relay `apps/integration/sharing_test.go`)

**Acceptance:** cross-relay share (bob on relay B granted by alice on relay A) read+write works;
revocation and expiry tests pass.

### E05-T3 — Share lifecycle UX: offer, accept, mount, list

- [ ] **Deferred (EPIC-009):** `sys.share.offer` / `sys.share.accept` / `sys.share.revoked` message types (uses the
      typed-message groundwork from EPIC-009; if that hasn't landed, define `type` in payload
      JSON — coordinate)
- [ ] **Deferred (with offer flow):** recipient's relay materializes accepted shares as mount-references under
      `/shared/<owner>/…` (a small JSON pointer file; sync clients and DAV resolve through it)
- [x] CLI: `poweur share add <path> --with bob.example.org --perm rw`, `poweur share ls`,
      `poweur share revoke`, plus `poweur share group set/ls/remove` (`poweur shares`
      received-view deferred with the offer flow)
- [x] Web app share dialog on any file/folder (audience picker fed by contacts), a
      received-shares view (name the owner, browse their tree with a visitor token) and
      "Shared" badges — shipped with [EPIC-015](EPIC-015-web-app-ux.md) E15-T4. The
      received view has to *ask* who shared with them until the offer flow above exists
- [ ] **Deferred (EPIC-007):** unaccepted-offer policy: offers expire after N days; offers from non-contacts follow
      EPIC-007 inbox policy (shares are spam vectors too)

**Acceptance:** end-to-end demo test: alice shares a folder with bob, bob accepts in web UI,
edits a file, alice sees the edit + audit trail; alice revokes, bob loses access.

### E05-T4 — Public-link shares (capability URLs)

For sharing with people *outside* the network (no Poweur ID yet) — also the on-ramp funnel.

- [x] Link-share grant variant: `audience: [{"link": "<token>"}]`, optional password
      (argon2id-hashed in grant), optional expiry + download-count limit
- [x] `https://<identity>/s/<token>` web endpoint: read-only browse/download with a minimal
      viewer page; "claim a Poweur ID to get edit access" upsell hook
- [x] Rate limiting + bandwidth accounting on link endpoints

**Acceptance:** link share with password + expiry works in a browser with no auth; revocation
kills the link.

### E05-T5 — Group identities (design + v1)

Groups that are *addressable* (`team.acme.poweur.net` as a share audience AND message
recipient) unify EPIC-005 and EPIC-009 group messaging.

- [x] Design doc: [`apps/docs/docs/files/group-identities.md`](../apps/docs/docs/files/group-identities.md) —
      a group as a hosted identity whose `poweur-sys/relay/groups/self.json` holds members;
      admin operations are member-list updates signed by the group's own key; relays resolve
      group→members server-side out of the group's own tree for shares and (E09-T5) message
      fan-out
- [x] v1: create/admin group identities via CLI (`poweur group create/show/add/remove` in
      `apps/cli/internal/cli/groups.go`); usable as a share audience with
      `poweur share add --with-group <poweur-id>` — no grant-format change
- [x] Defer/coordinate: group E2EE messaging key agreement is EPIC-009's problem (E09-T5).
      `epoch` is the membership version it binds keys to; `members` is the fan-out list and
      `admins` is authority only, implying no membership either way

**Acceptance:** met — `TestINT_SHARE_04_GroupIdentityShare` grants one folder to
`gidcrew.poweur.net`, lets its members in and everyone else out, and moves membership with a
single signed update that never touches the grant; the design doc states the membership and
addressing model E09-T5 consumes.

**Deferred within T5:**

- **Cross-relay group resolution** — v1 resolves only group identities hosted on the same
  relay as the grant's owner; anything else fails closed and logs why. A relay would have to
  fetch and verify another relay's membership document on the permission path, which needs a
  membership-check endpoint with its own caching, rate limiting and privacy story ("is X in
  your group?" is an enumeration oracle).
- **Per-admin signed updates** — v1 authority is possession of the group's identity key; the
  `admins` list is recorded authority the CLI enforces client-side. Making it cryptographic
  changes who signs, not what the document says.
- **Web UI for group identities** — v1 is CLI-only. The web share dialog's audience picker
  expands owner-local groups to their members and turns a free-typed name into
  `{"id": …}`, so typing a group identity there would produce a grant addressed to the
  group *as a visitor* rather than to its members. Creating, administering and addressing
  group identities in the SPA is a follow-up in EPIC-015.
