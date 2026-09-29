# EPIC-005 — Sharing, ACLs, groups & public-to-any-valid-ID

- **Status:** complete except the adoption funnel in E05-T6 and the E05-T7 Send product. Grant enforcement, encrypted offer/accept/revoke lifecycle, credential-free recipient mounts, CLI/web UX, link shares and group identities have shipped.
- **Priority:** P1
- **Depends on:** EPIC-003 (storage + cross-identity auth); interacts with EPIC-004 (sync), EPIC-007 (contacts)
- **Unlocks:** EPIC-010 (cross-identity pipelines), collaborative apps

> **Re-based on [EPIC-020](EPIC-020-storage-protocol-v2.md) (E20-T7).** Shares move from path
> grants to owner-signed documents on **node ids** (any file or folder, no `/shared` root), with
> node keys sealed to each member — content is end-to-end encrypted. Roles become
> `read`/`write`/`append`/`create`/`admin`; revocation also rotates keys; links carry the key in
> the URL fragment; file requests seal to the folder's public key. Offer/accept/mount (T3), the
> claim loop (T6) and Send (T7) keep their product shape. Time-boxed, snapshot and delegated
> shares are E20-T16.

## Progress

| Task | Status | Notes |
|------|--------|-------|
| E05-T1 Sharing & permissions spec | **done** | [`apps/docs/docs/files/sharing.md`](../apps/docs/docs/files/sharing.md); grant/group canonical signing in `packages/identity/grants.go`; inheritance = whole-subtree, no per-file exceptions; T3 selected and shipped client-direct recipient mounts |
| E05-T2 Relay grant engine | **done** | `apps/api/internal/files/grants.go`: per-request verified snapshot (revocation immediate, no ≤60 s window — app-password pattern); enforced on DAV + changes + manifest + chunked upload; visitor writes journal `actor`; forged/replayed/malformed grants rejected loudly; extensive scenario matrix in `apps/api/internal/relay/shares_test.go` + cross-relay `TestINT_SHARE_01` |
| E05-T3 Share lifecycle UX | **done** | Versioned `sys.share.offer/accept/revoked` bodies in `packages/identity` + `@poweur/client`; CLI/web share creation emits encrypted offers; Requests accepts after resolving the owner and verifying the signed grant; credential-free `shared/<owner>/<name>/.poweur-mount.json` pointers drive “Shared with me”; revoke removes authority immediately and best-effort notices prune mounts. Stranger offers follow inbox policy, have one slot per grant, a 64 KiB cap and ≤7-day expiry. Real-relay SDK coverage plus Playwright proves accept → edit/audit → revoke. |
| E05-T4 Public-link shares | **done** | `audience: [{"link": …}]` grant variant (26-char base32 token, argon2id password, expiry, download cap) in `packages/identity/grants.go`; `/s/<token>` endpoint; CLI and web share-dialog creation; spec + threat model; cross-relay `TestINT_SHARE_03` + web coverage |
| E05-T5 Group identities | **done** | design doc [`group-identities.md`](../apps/docs/docs/files/group-identities.md); `admins` + `epoch` on `ShareGroup`, group's own tree at `poweur-sys/relay/groups/self.json` signed by the group's key; engine resolves named group identities out of their own trees (`apps/api/internal/files/grants.go`); `poweur group create/show/add/remove`; `TestINT_SHARE_04`. **Deferred:** cross-relay group resolution (needs a membership-check endpoint — enumeration oracle); per-admin signed updates; group messaging fan-out + key agreement = EPIC-009 E09-T5 |
| E05-T6 File requests & guest conversion | **partial** | Create-only requests, isolated uploads, owner context/quotas, opt-in live notifications, claim continuity, explicit owner-approved ID upgrades, and verified/idempotent accepted-share accounting ship across CLI, SDK and web. Single-recipient challenge and durable/offline notification UX remain. |
| E05-T7 Send: transfer front door, receipts & expiring transfers | **partial** | The v2 SDK and web Send panel create bounded-memory, multi-file encrypted transfers under `/shared/.transfers/<id>/`, checkpoint completed files, issue expiring passwordable/download-capped links, deliver expiring offers to Poweur IDs, revoke bytes, and show durable owner-only open counters. Transfers live under the hidden `.poweur/transfers/`; the web app releases expired transfers (files deleted, usage freed) when Files opens, with a browser journey covering Send → clean-browser link. Chunk-level resume of one large file (a dropped 5 GB upload restarts that file), relay-side expiry for owners who never return, mobile share-sheet entry, first-open typed notifications, zip download, email and entitlement limits remain. |

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
- [x] Mount model for recipients — `/shared/<owner>/<name>/` is a credential-free pointer;
      v1 clients resolve it by connecting directly to the owner's relay with a fresh visitor
      token. Relay proxying remains a later privacy-parity option.
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

- [x] `sys.share.offer` / `sys.share.accept` / `sys.share.revoked` versioned encrypted
      lifecycle messages; the complete owner-signed grant travels inside the offer and is
      re-verified after owner resolution before acceptance
- [x] Recipient clients materialize accepted shares as credential-free mount references at
      `/shared/<owner>/<name>/.poweur-mount.json`; Poweur-aware clients resolve the owner relay
      and mint fresh visitor authority rather than storing a bearer token
- [x] CLI: `poweur share add <path> --with bob.example.org --perm rw` sends offers by default
      (`--no-notify` opts out), `poweur share ls`, `poweur share revoke` sends best-effort
      notices, `poweur requests` recognizes offers, plus `share group set/ls/remove`
- [x] Web app share dialog on any file/folder, Requests tray acceptance, accepted-mount list,
      manual-owner compatibility path, revoke pruning and "Shared" badges — built on
      [EPIC-015](EPIC-015-web-app-ux.md) E15-T4
- [x] Unaccepted-offer policy: at most seven days, 64 KiB encrypted payload cap, required
      signed `share_id` metadata, one pending slot per grant for strangers under
      `contacts_and_requests`, and rejection under `contacts_only`

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

### E05-T6 — File requests & guest-to-ID conversion

Turn a public link from a terminal download surface into an adoption loop without weakening
the grant model. A recipient must be able to complete the immediate job before being asked to
register; claiming an ID adds durable identity, edit access and a mounted relationship rather
than unlocking bytes that were artificially withheld.

- [x] Extend the sharing spec with a **file-request** shape: the guest may create new objects
      under one folder but cannot list, read, overwrite or delete another submitter's objects.
      Use E20-T7's `create` role once available; if a v1 upload token ships earlier,
      specify it as a strict compatibility subset that upgrades to the same permission.
- [x] Public landing page for browse/download and file-request links: owner identity and pinned
      key, expiry, password/recipient challenge, quota/error states and a clear statement of what
      the visitor can do before creating an account.
- [x] Claim-ID handoff: after viewing, downloading or uploading, a guest can claim or sign in to
      a Poweur ID and accept the share without losing the link, destination or completed action.
- [x] Upgrade path from link audience to an explicit ID grant; consume or retain the public link
      according to the owner's choice, never silently broaden its audience.
- [x] Optional owner controls: upload count/bytes, allowed media types, per-object size, expiry,
      password and opt-in live notification on submission.
- [ ] Single-recipient challenge for guests — deferred to EPIC-023's verified-email delivery;
      passwords already cover shared-secret links, and inventing a second password field would
      not verify a recipient.
- [ ] Durable/offline submission-notification UX — defer the user-visible notification queue to
      EPIC-009; uploaded objects, the changes journal and aggregate counters are already durable.
- [x] Privacy-preserving funnel events for the hosted service: link opened, action completed,
      claim started, ID claimed and share accepted. Never record paths, filenames, message
      contents, document contents or visitor IP beyond the service's short-lived abuse logs.
- [x] Web/SDK/CLI support and end-to-end coverage for anonymous upload, isolation between two
      guests, quota exhaustion, expiry, claim handoff and revocation.

**Implemented v1 slice:** Go and TypeScript share the signed `link.file_request` canonical
shape and conformance vector. `/s/<token>` accepts create-exclusive, randomly prefixed uploads
without exposing a listing or read path; count/byte/type/object-size limits and revocation are
covered at the real relay. SDK, CLI (`share request add`) and web owner UI create requests.
Landing pages show the owner's identity-key fingerprint, expiry, password state and applicable
remaining upload quota without exposing the token or any submitted filename. Browse and
file-request pages transfer claim context in a URL fragment, so it is not sent to the launcher
server; after unlock the claimant sends an encrypted `sys.share.claim`. Claims have their own
request slot per grant. Anonymous uploads honor the relay file-size and identity-storage caps.
The owner must approve before a fresh direct-ID grant and encrypted offer are created, and
explicitly chooses whether the public capability is retained or consumed. `sys.share.accept`
is delivered to that owner's inbox when the sender is on the live grant, including under a
closed inbox policy. `poweur requests` writes the decrypted offer or claim to a `0600` file
and prints the next command. SDK and CLI integration tests cover anonymous upload, isolation,
the upload cap, claim, approval, mount, access to the guest's upload and subsequent editing.
Persisted privacy-safe metrics record opens, upload counts/bytes, claim starts, signed-ID
continuations and accepted shares. Conversion grants bind their source capability into the
owner's signature; the relay increments `share_accepted` only for a matching signed recipient
acceptance and deduplicates retries by the resulting direct grant ID. The unchecked owner-control
item remains open for single-recipient challenge and durable/offline submission notification UX.

**Acceptance:** Alice creates an upload-only request; two guests submit files without seeing each
other; one guest claims an ID and accepts the resulting share without repeating the upload; Alice
can revoke the link immediately and the metrics reveal conversion counts without content metadata.

### E05-T7 — Send: transfer front door, receipts & expiring transfers

T4 links and T6's claim loop are the mechanics; this task is the *product* people already pay
WeTransfer/Smash for, with a trust story those lack: the recipient sees a verified sender ID, not
an unverifiable email address. Every transfer is an introduction to Poweur.

- [x] CLI-first foundation: `poweur transfer create <file>` always uses the chunked-upload
      endpoint for non-empty files, defaults to seven-day expiry, and creates a passwordable,
      download-capped public link. Real-relay `TestINT_SHARE_07` proves anonymous download and
      cap exhaustion.

- [ ] Single-screen **Send** (web + mobile share sheet): drop files, optional message, expiry,
      password → link. Always uses resumable chunked upload (E04-T3 today, E20 later) so multi-GB
      transfers survive flaky networks.
- [ ] Transfers live in the dedicated `/shared/.transfers/<transfer-id>/` namespace with a TTL;
      a cleanup job deletes
      expired transfers and releases quota. Transfers are never permanent public hosting.
- [ ] Recipients: a link; a Poweur ID (grant + typed message, E2E); or an email address via the
      EPIC-023 bridge (link + footer).
- [ ] Recipient page reuses T6's landing: sender ProfileCard and verification state, zip-on-the-fly
      for many files, "claim your ID and reply" with the sender pre-added as a contact request.
- [ ] Receipts: first-download notification to the sender as a typed message; per-transfer view
      built on the existing link bandwidth counters (`internal/files/linkstats.go`).
- [ ] Guest senders: a visitor without an ID can send files *to* an ID through its inbox policy
      (EPIC-014 anon ingress + PoW or E16 payment); files land on the recipient's quota only if the
      policy allows attachments.
- [ ] Limits are entitlements (EPIC-026): max transfer size, retention days, egress; branding and
      longer retention are the natural paid line.

**Implemented v2 slice:** `@poweur/client/drive` uploads browser `File` sources one encrypted
4 MiB chunk at a time, so transfer memory is bounded rather than proportional to file size.
Transfer checkpoints are encrypted with the web app's identity snapshot key; completed files
survive a retry, while an interrupted uncommitted file restarts and normal drive GC reclaims its
orphan chunks. The Files header opens a multi-file Send panel with message, expiry, password,
download cap, direct Poweur-ID recipients, progress, encrypted local history and immediate
revocation. Owner-authenticated link statistics expose only the durable open count, node and
expiry. The link ID remains public capability metadata; fragment keys and passwords never reach
the relay. This deliberately composes E20-T7/T10 rather than reviving the v1 WebDAV transfer
implementation. Automatic server-side expiry deletion and first-open typed notifications remain
open; email delivery remains owned by EPIC-023 and plan enforcement by EPIC-026.

**Acceptance:** a 5 GB transfer resumes after a dropped connection; an email recipient downloads
without an account and claims an ID with the sender already requested as a contact; an expired
transfer disappears and its bytes leave the owner's usage.
