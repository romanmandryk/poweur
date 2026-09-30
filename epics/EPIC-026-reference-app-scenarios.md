# EPIC-026 — Headless reference apps: collaboration scenarios as the acceptance gate

- **Status:** in progress — T1 (harness), T2 (Markdown documents), Tasks and the Guestbook relying party done
- **Priority:** P0 — gates [EPIC-020](EPIC-020-storage-protocol-v2.md) waves 2–3 and
  [EPIC-009](EPIC-009-messaging-upgrades.md) T7–T10
- **Depends on:** EPIC-020 (drive, shares, append logs, sealed appends, CLI), EPIC-009 T7–T10
  (intent types, typed routing, shared inboxes, follow feeds), EPIC-014 (anonymous messages,
  proof-of-work), EPIC-024 (group identity as host of shared state)
- **Also exercises:** EPIC-027 (community boards, listings, following) in E26-T9
- **Unlocks:** app templates (each headless app becomes a UI later without protocol
  work), EPIC-025 (the Markdown and whiteboard scenarios are its starting point)

## Progress

| Task | Status | Notes |
|------|--------|-------|
| E26-T1 Scenario harness & topology matrix | **done** | `apps/integration/scenario_harness_test.go`: CLI actors (Go SDK underneath) on same/cross-relay × fs/S3 (MinIO via `POWEUR_TEST_S3_*`); privacy scan over the provider (`relay.WalkStore`); relay restart, dropped SSE; TS `startRelays()` + `scenario-cross-relay.test.ts`; `make refapps` / `pnpm refapps:test` |
| E26-T2 Collaborative Markdown documents | **done** | `refapps/docs` (layout, comment reducer, paragraph three-way merge) + `TestE26_T2_CollaborativeMarkdown` in every topology and provider |
| Tasks (v1 PCP-0007 successor) | **done** | `refapps/tasks`: a project is an event log (`events.jsonl`) folded by a last-writer-wins reducer; PCP-0007 status vocabulary and unknown-field preservation; `TestE26_Tasks` (offline edits by two members converge, read-only observer refused, typed assignment notice, late joiner, revocation) in every topology and provider. The Kanban scenario (T5) builds on it |
| Guestbook relying party | **done** | `refapps/guestbook`: the v1 "Sign in with Poweur ID" reference site restored without the retired WebDAV grant (entries stay with the site); its unit suite plus `TestINT_SIGNIN_01/02` (CLI approval cross-device; same-device approval finishes only in the starting browser) |
| E26-T3 Personal site, contact & newsletter | **open** | public folder, `contact.message`, `list.subscribe`, anonymous senders, follow feed |
| E26-T4 Form → CSV | **open** | sealed appends from link and ID respondents, caps, proof-of-work, one response per ID |
| E26-T5 Kanban board | **open** | event log + reducer, offline convergence, notifications, group hosting, transfer |
| E26-T6 CRM with shared inbox | **open** | group-hosted records, shared-inbox identity, email-only customers, member removal |
| E26-T7 Whiteboard (durable layer) | **open** | object ops with last-writer-wins by position, batching, latency budget; cursors deferred |
| E26-T8 TypeScript mirror & mixed-implementation actors | **open** | TS headless apps; a Go actor and a TS actor collaborate in every scenario that has one |
| E26-T9 Community board & classifieds | **open** | group-hosted public board, member and anonymous posts, listings, moderation, legal removal, followers |

## Goal

Prove the storage and messaging design with the apps we actually want, **before any UI
exists**. Each app is a small headless module (its data layout and reducer) plus a scenario in
which several users perform every action they would need to collaborate — through SDK calls or
`poweur` CLI commands only — on one relay and across relays. If an app needs something the
protocol does not offer, the scenario fails and the gap is fixed in EPIC-020 or EPIC-009, not
worked around in the app.

## Design direction

- **Headless apps are real code, not test fixtures.** Go versions live in
  `apps/integration/refapps/<app>/`; TypeScript versions in `packages/refapps/` (on
  `@poweur/client`), which can later seed app templates. An app module is its folder layout,
  record types, reducer and a thin API ("create board", "move card") over the SDK's scoped drive
  handle, event-log helper and typed messages.
- **Every user action has a CLI path.** Each scenario document carries a table
  *user action → `poweur` command → SDK call*. Generic drive and messaging commands
  (`poweur drive …`, `poweur send --type …`, `poweur follow`) are preferred; an app-specific CLI
  is added only where an action cannot be expressed with them.
- **Actors** are an identity + its relay + a client kind (`cli`, `go-sdk`, `ts-sdk`). Scenarios
  are written against actors, so the same scenario runs in every topology.
- **Topologies** (every scenario runs in the first two; E26-T8 adds the third):
  1. **Same relay** — all actors on relay A.
  2. **Cross relay** — the host drive on relay A, members on relay B (and C where a scenario
     has an outside party), resolved through the integration suite's fake DNS.
  3. **Mixed implementations** — at least one actor on the Go CLI and one on the TS SDK.
- **Providers:** the filesystem provider always; the S3 provider (MinIO) for T4 and T5.
- **Common assertions** for every scenario:
  - **Privacy scan:** after the run, the host store contains none of the scenario's plaintext
    (document text, file and folder names, CSV values, card titles, message bodies) outside
    relay-readable nodes (`.poweur/public`, `.poweur/relay`, `.poweur/state`, public folders).
  - **Statelessness:** the host relay is restarted mid-scenario with every cache deleted; the
    scenario continues with no observable difference.
  - **Revocation:** a removed member's next request fails and cached keys do not open content
    written after removal.
  - **Offline:** one actor goes offline, keeps working locally, reconnects and converges.
- **Out of scope here:** UI, email delivery (EPIC-023 — email-only contacts are records until
  then), live cursors and presence (EPIC-025 rooms).

## Tasks

### E26-T1 — Scenario harness & topology matrix

- [x] Actor abstraction and scenario runner in `apps/integration` (in-process relays, fake DNS,
      CLI actors in-process — the Go SDK underneath; a subprocess cannot see the in-memory DNS
      zone); topology and provider matrix as table-driven subtests. Test relays and the CLI
      resolver dial each identity's own relay through the zone, so hosted identities work
      across relays.
- [x] TS side: `startRelays(n)` in `packages/client-ts/test/helpers/relay.ts` shares a hosts
      file between relays (`RESOLVER_HOSTS_FILE`, honoured only with `RESOLVER_ALLOW_PRIVATE`)
      and gives clients a Host-routing `fetch`
- [x] Privacy scanner over a provider's stored objects (`relay.WalkStore`, fs and S3; an empty
      store fails the scan); fault injection: relay restart with caches wiped, dropped SSE
      connection. Offline work is a save from an old base: the conflict/merge path (T2).
- [x] `make refapps` / `pnpm refapps:test` entry points; the scenarios are ordinary
      integration tests, so CI runs them with the suite (S3 leg when `POWEUR_TEST_S3_ENDPOINT` is set)

**Acceptance:** a trivial two-actor scenario (share a file, edit, read back) passes in every
topology and provider, and the privacy scanner catches a deliberately planted plaintext file.

### E26-T2 — Collaborative Markdown documents

**Layout:** `Docs/<title>/` with `doc.md` (replace), `comments.jsonl` (append), `assets/`.
**Actors:** Alice (owner, relay A), Bob (editor, B), Carol (commenter, B), anonymous link reader.

Actions: create doc; write and edit `doc.md`; add an image to `assets/`; share the folder with
Bob (`write`); share the folder with Carol (`read`) plus `append` on `comments.jsonl`; accept
offers and mount; watch for changes; edit concurrently (one side gets `409`, three-way merges,
recommits); comment; resolve a comment (append a resolve record); list versions and restore
one; create a password-protected read link and open it without an account; revoke Bob.

**Acceptance:**
- concurrent edits by Alice and Bob to different paragraphs both survive; the same paragraph
  yields a merge (or a conflicted copy by driver setting) — never a silent loss
- Carol reads the doc and comments, appends comments, and gets `403` writing `doc.md`
- Bob on relay B sees Alice's edit through the change stream without polling
- the anonymous link reader decrypts the doc in a clean client; the relay never saw the key
- after revocation Bob cannot read, and his cached keys do not open newer versions

**Done.** `apps/integration/refapps/docs` + `TestE26_T2_CollaborativeMarkdown`. The CLI keeps no
node keys between commands, so "cached keys" is covered cryptographically by `INT_DRIVE_08`
(rotation on revoke); here Bob's reads fail after revocation and Carol keeps reading.
Adjacent-paragraph edits by two people merge paragraph by paragraph; the same paragraph becomes
a labelled conflict block holding both versions.

| User action | `poweur` command | SDK |
|---|---|---|
| Create the doc folder | `drive mkdir /Docs/<title>` | `Files.Create(folder)` |
| Write / edit `doc.md` | `drive put <local> /Docs/<title>/doc.md [--base <version>]` | `Files.Create` / `Files.Replace` |
| Add an image | `drive put <img> /Docs/<title>/assets/<name>` | `Files.Create` |
| Share with an editor / reader | `drive share add <folder> <id> --role write\|read` | `Files.ShareWith` |
| Comment-only on the log | `drive share add <folder>/comments.jsonl <id> --role append` | `Files.ShareWith` |
| Accept an offer (mount) | `inbox`, then `drive accept <offer.json>` | offer verify + mounts |
| Watch for changes | `drive watch --drive <owner> [--count N] [--timeout D]` | `Client.Subscribe` |
| Save loses a race | `drive put … --base <v>` exits 3 with `{"error":"conflict","head":…}` | relay `409` on a stale parent |
| Merge and save | `drive get … --version <base>` + `drive get …`, `docs.Merge`, `drive put … --base <head>` | `Files.ReadVersion` |
| Comment / resolve | `drive append <folder>/comments.jsonl <record.json>` | `Files.Append`, `docs.Reduce` |
| Read comments | `drive tail <folder>/comments.jsonl` | `Files.Tail` |
| List versions / restore | `drive history <doc>`; `drive get <doc> <out> --version <v>` + `drive put` | `Client.History`, `Files.ReadVersion` |
| Password read link | `drive link create <folder> --password <p>` | `Files.Link` |
| Open it with no account | `drive link get <url> <out> --path doc.md --password <p>` | link client (`LinkID`, verifier) |
| Revoke an editor | `drive share ls`, `drive share rm <id>` (rotates keys) | `Client.Unshare` + `Rotate` |

Members address a shared folder by node ID: `/<folder-node>/doc.md` with `--drive <owner>`.

### E26-T3 — Personal site, contact & newsletter

**Layout:** `Site/` (public folder: `index.html`, `posts/*.md`, `feed.json`);
`Newsletter/subscribers.jsonl` (private, append).
**Actors:** Alice (site owner, contacts-only inbox, relay A), Dave (Poweur ID stranger, B),
an anonymous web visitor, Eve (subscriber by ID, B), an email-only subscriber, Frank (follower, C).

Actions: publish the site and a post; fetch it anonymously over `/pub`; Dave sends
`sys.contact.message`; the anonymous visitor sends one with proof-of-work; Eve sends
`sys.list.subscribe`; the email-only subscriber subscribes anonymously with an email; Alice's
headless app drains both types and appends subscribers; Eve unsubscribes; Frank follows the
feed; Alice publishes a second post; Frank receives it.

**Acceptance:**
- contact messages and subscriptions land in Requests / the app, never open a chat, and a
  flood is stopped by proof-of-work and rate limits
- anonymous message bodies are E2E encrypted (privacy scan covers the spool)
- `subscribers.jsonl` is encrypted at rest and reflects the unsubscribe
- Frank receives the new post on relay C without Alice sending anything to him
- the public site is plaintext by design and is the only plaintext Alice has besides `.poweur`

### E26-T4 — Form → CSV

**Layout:** `Forms/<name>/form.json` (published or link-shared), `responses.csv` (append; header
written by the owner).
**Actors:** Alice (owner, A), anonymous respondents via link, Bob (ID respondent, B).

Actions: create the form and the CSV header; create a respond-only link (sealed append, caps:
max responses, max bytes each, proof-of-work) and an ID-only variant (one response per
identity); fetch the form; submit 50 responses concurrently; Bob submits once, then again;
exceed the cap; Alice reads and exports the CSV; Alice compacts sealed records; Alice closes the
form (revokes the link).

**Acceptance:**
- all 50 concurrent responses appear exactly once, in one order for every reader
- respondents can submit but cannot read `responses.csv` (not even their own row back)
- Bob's second response and the over-cap response are refused with reasons
- the exported CSV is byte-identical before and after compaction
- after the link is revoked, submissions fail; runs on the filesystem and S3 providers

### E26-T5 — Kanban board

**Layout:** `Boards/<name>/board.log` (append: `list.create`, `card.create`, `card.move`,
`card.update`, `card.assign`, `comment.add`), `snapshot.json`, `cards/<id>/` attachments.
**Actors:** Alice (creator, A), Bob (member, B), Carol (observer: read + comment, B), Dan (joins
later, C), the group `team` that ends up hosting the board.

Actions: create the board with lists and cards; share with Bob and Carol; move and edit cards
concurrently; assign a card to Bob (sends `sys.app.notify`); attach a file to a card; Bob goes
offline, makes three moves, reconnects; Carol tries a move (ignored by reducers) and adds a
comment (allowed); snapshot and trim; Dan joins and loads snapshot + tail; transfer the board
from Alice to the group; Alice leaves the group; a member hits the per-member write cap.

**Acceptance:**
- every actor's folded state is identical after each step, including concurrent moves of one
  card (last writer by position wins) and Bob's offline moves
- Bob receives the assignment notification through typed routing, not chat
- Carol's move has no effect on anyone's state; her comment appears
- Dan's first load reads the snapshot and the tail only
- after the transfer and Alice leaving, the board keeps working and bills to the group drive
- runs on the filesystem and S3 providers

### E26-T6 — CRM with shared inbox

**Layout (group `acme` drive):** `CRM/contacts/<id>.json` (a pinned Poweur ID or an email-only
record), `CRM/deals.log` (append), `CRM/activity.log` (append); shared-inbox identity
`sales.acme…` (EPIC-009 T9).
**Actors:** Alice and Bob (members, relays A and B), Carl (customer with a Poweur ID, C), an
email-only prospect.

Actions: create the group, the shared inbox and the CRM folder; add Carl as a contact (key
pinned) and the prospect as an email-only record; Carl messages `sales`; both members read the
thread; Bob replies on behalf of `sales`; Alice links the thread to Carl's record and logs an
activity; both move one deal through the pipeline concurrently; Carl's key changes and the CRM
flags it; Bob is removed from the group.

**Acceptance:**
- both members read the same thread from the shared inbox; Carl sees Bob's reply as from
  `sales`, signed by Bob
- deal state converges; the email-only record exists and "send" reports no channel until EPIC-023
- the key change is flagged on Carl's record, not silently accepted
- after removal Bob can read neither new messages to `sales` nor new CRM writes

### E26-T7 — Whiteboard (durable layer)

**Layout:** `Boards/<name>.canvas/ops.log` (append: `shape.create`, `shape.update` per property,
`shape.delete`), `snapshot.json`.
**Actors:** three users on relays A and B.

Actions: create a canvas and share it; each actor streams operations in 50 ms batches for 10 s
(moves, resizes, recolors, deletes, concurrent edits to one shape); one actor reloads mid-way
from snapshot + tail; measure append-to-remote-event latency.

**Acceptance:**
- all actors end with an identical canvas; per-property last-writer-wins by position
- latency meets E20-T15's budget on the filesystem provider
- recorded as expected-missing: live cursors and in-progress drags (EPIC-025 rooms), rich text
  inside shapes (CRDT, EPIC-025)

### E26-T8 — TypeScript mirror & mixed-implementation actors

- [ ] `packages/refapps/` TS versions of T2–T7 and T9's app modules on `@poweur/client`
- [ ] Every scenario runs once with mixed actors (e.g. Alice on the Go CLI, Bob on the TS SDK);
      reducers in both languages must fold the same logs to identical state
- [ ] Shared fixtures: recorded logs + expected folded state, run by both languages

**Acceptance:** all seven scenarios pass in the mixed topology; the shared fixtures fold
identically in Go and TS.

### E26-T9 — Community board & classifieds

**Layout:** group identity `ericeira-market` (EPIC-027 E27-T5) hosting `Board/board.json` and
`Board/board.log` (public append); sellers keep listings in their own `Classifieds/` folders
(E27-T6).
**Actors:** Alice (creator and moderator, relay A), Bob (member and seller, B), Carol (no
Poweur ID, posts through the anonymous link), Dan (follows the board, C), Eve (buyer, C).

Actions: create the group identity and board; add Bob as a member and Alice as moderator;
create an anonymous posting link with caps and proof-of-work; Bob publishes a listing (coarse
cell, photos, expiry) in his own `Classifieds/` and posts a reference to the board; Carol posts
through the link; Dan follows the board through his relay's subscription proxy; Eve contacts Bob
about the listing with `sys.contact.message` and Bob replies with the exact address; the listing
expires; Carol exceeds the link cap; Alice hides one post and legally removes another; Bob
deletes a listing (tombstone); the board is transferred to a second moderator.

**Acceptance:**
- every reader folds the same board state, including hides, expiry and tombstones
- Dan receives new entries on relay C without polling Bob's or Alice's relay directly
- Carol's posts are marked anonymous; over-cap and unsolved-proof-of-work posts are refused
- after legal removal the removed bytes are gone from the store, not just hidden
- the public board and listings are plaintext by design; the privacy scan finds Eve and Bob's
  conversation (including the exact address) only as ciphertext
- storage used by members' posts counts against the group identity's drive

## Non-goals

- UI of any kind (app templates and the web app build on these modules later)
- Email delivery (EPIC-023), live cursors/presence and text CRDTs (EPIC-025)
- App-specific servers — if a scenario seems to need one, that is a protocol gap to fix
