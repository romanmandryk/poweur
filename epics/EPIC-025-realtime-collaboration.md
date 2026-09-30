# EPIC-025 — Real-time collaboration protocol & reference editor

- **Status:** proposed
- **Priority:** P2 (does not wait for EPIC-024: documents work in any shared folder; a group folder is one)
- **Depends on:** EPIC-020 (append-mode files, version-checked commits, shares, E20-T15 latency),
  EPIC-017 (`@poweur/client`); interacts with EPIC-024 (a group folder is where most team documents live)
- **Interacts with:** EPIC-009 (typed messages/SSE), EPIC-011 (key epochs), EPIC-019 (mobile)
- **Unlocks:** interoperable co-editing applications, whiteboards and live tools on Poweur;
  the generic room transport that apps and games build on
- **Operator limits:** TURN relay bytes, room size and retained update history are
  entitlements, never plan names in the protocol

## Progress

| Task | Status | Notes |
|------|--------|-------|
| E25-T1 CRDT/session decision record | open | adopt an existing CRDT; define generic vs app-specific layers |
| E25-T2 Durable collaborative-document format | open | updates, snapshots, compaction, export and recovery |
| E25-T3 Ephemeral live-session transport | open | presence/cursors; relay fallback, optional WebRTC |
| E25-T4 Authorization, encryption & membership changes | open | roles, guests, epochs, removal and offline devices |
| E25-T5 Collaborative Markdown reference app | open | offline-first editor, comments, attachments and history |
| E25-T6 Federation, conformance & performance | open | multi-relay convergence, reconnect, load and abuse limits |
| E25-T7 General-purpose rooms API for non-document apps | open | the T3 transport exposed for games/tools; `@poweur/live` SDK |
| E25-T8 TURN fallback & metering | open | ephemeral per-identity TURN credentials; bytes metered per identity |

## Goal

Define the smallest open layer applications need to collaborate live while retaining Poweur's
identity, federation, sharing and user-owned storage properties. Prove it with one excellent
collaborative Markdown editor inside a group.

The storage service does **not** merge arbitrary files. Collaborative applications store an
explicit CRDT update log and snapshots as normal versioned Poweur data. The generic layer covers
session discovery, authenticated membership, durable update transport and ephemeral awareness;
the document type owns its semantic schema and export.

## Design principles

1. **Adopt, do not invent, the CRDT.** Evaluate Yjs and Automerge against offline behavior,
   implementation availability, binary size, compaction and deterministic fixtures.
2. **Durable edits and ephemeral awareness are different traffic.** Text/object operations must
   survive reconnect; cursors, typing and presence expire and need not enter message history.
3. **Relay fallback is mandatory; peer acceleration is optional.** Correctness cannot depend on
   NAT traversal. WebRTC may reduce latency/cost when available.
4. **One writer cannot overwrite another.** E20 compare-and-swap protects log/snapshot commits;
   CRDT convergence resolves semantic concurrency.
5. **The open file is still useful.** Every reference document can export ordinary Markdown and
   attachments without the Poweur editor.

## Tasks

### E25-T1 — CRDT and session architecture decision

- [ ] Benchmark Yjs and Automerge for TypeScript/browser, Go interoperability needs, offline
      update volume, snapshot cost, bundle size and malicious-input handling.
- [ ] Choose one normative update encoding for the reference document; version it and preserve
      opaque forwarding so other implementations need not share the UI stack.
- [ ] Draw the boundary between generic session messages and app-specific document operations.
- [ ] Decide relay-mediated update fan-out versus durable-log notification; specify deduplication,
      ordering assumptions, backpressure and reconnect.
- [ ] Evaluate WebRTC data channels only as an acceleration path, including signaling, TURN cost,
      metadata exposure and fallback.
- [ ] Publish a threat model covering update bombs, decompression bombs, replay, malicious peers,
      presence scraping and oversized offline histories.

**Acceptance:** the decision record includes a two-client offline/concurrent prototype and rejects
alternatives with measured reasons; no custom CRDT algorithm is introduced.

### E25-T2 — Durable collaborative-document format

- [ ] PCP for document manifest, CRDT update frames, stable actor/update IDs, snapshots,
      attachments and exported representation.
- [ ] Store update logs using E20 frame-aligned append semantics and snapshots as immutable
      versions; define a migration-compatible v1 fallback only if E20 is not yet deployed.
- [ ] Compaction protocol: any authorized editor may propose a snapshot, but stale or malicious
      compaction cannot discard updates accepted by the current head.
- [ ] Crash recovery for an incomplete tail frame and verification limits before allocating.
- [ ] Version history and point-in-time export without promising semantic undo across every app.
- [ ] Go/TS fixtures for framing, snapshot hashes, malformed data and deterministic convergence.

**Acceptance:** two implementations replay the same fixtures to the same document state; an
interrupted append loses at most the incomplete frame and never corrupts an earlier snapshot.

### E25-T3 — Ephemeral live-session transport

- [ ] Session descriptor: document/group, participant ID, device/session proof, supported
      protocol versions and expiry.
- [ ] Awareness messages for join/leave, presence, cursor/selection, typing and app-defined
      transient state; hard size/rate/TTL limits.
- [ ] Authenticated relay fallback with fan-out only to current authorized participants and no
      insertion into durable inbox/history.
- [ ] Optional peer transport negotiation through signed signaling; end-to-end encrypt peer and
      relay awareness traffic under the document/group key domain.
- [ ] Reconnect switches transports without duplicating durable updates or presenting two copies
      of one device.
- [ ] Privacy behavior: presence is opt-in per session, not a public identity status.

**Acceptance:** three clients, including one behind relay fallback, see bounded-latency awareness;
disconnecting a client expires its state, and no cursor record appears in durable history.

### E25-T4 — Authorization, encryption & membership changes

- [ ] Map view/comment/edit/manage roles to group membership and existing grants; document how a
      standalone shared document works outside a group.
- [ ] Encrypt durable updates and snapshots in the shared key domain; bind every update to an
      authenticated actor/device without exposing plaintext to relays.
- [ ] Membership epoch transition: distribute the next key, close the old live session and reject
      new writes at a stale epoch.
- [ ] Guest editor/commenter grants with explicit expiry and capability attenuation.
- [ ] Offline-device rules after removal: already-held content is not recalled; stale updates may
      be shown to the removed author for manual export but never committed to the new epoch.
- [ ] Audit relevant actions without logging high-volume cursor/presence data.

**Acceptance:** removing an editor prevents subsequent relay and peer writes at the old epoch;
remaining members continue after rotation and can attribute every durable update.

### E25-T5 — Collaborative Markdown reference application

- [ ] Editor embedded in EPIC-024 groups and usable as a standalone shared file.
- [ ] Offline-first create/edit/reconnect with visible sync state and deterministic convergence.
- [ ] Markdown source plus safe preview, attachments stored in the document's shared root, and
      ordinary `.md` export/import.
- [ ] Comments, replies, resolution and mentions using E24 activity semantics.
- [ ] Version history, restore-as-new-head and presence/cursor UI.
- [ ] Accessible keyboard, screen-reader, mobile and reduced-motion behavior.
- [ ] SDK example showing a second minimal client consuming the open format.

**Acceptance:** two browsers on different relays edit offline and concurrently, reconnect and
converge; export produces a usable Markdown bundle; removal during editing fails closed.

### E25-T6 — Federation, conformance, scale & abuse

- [ ] Conformance vectors and a protocol harness independent of the reference editor UI.
- [ ] Multi-relay and split-files-service e2e covering packet loss, duplication, reordering,
      restart, long offline intervals and membership changes.
- [ ] Load targets for participants, operations/second, tail size, compaction and awareness fanout;
      publish measured limits instead of an unbounded "real time" promise.
- [ ] Per-document and per-identity rate limits that distinguish durable updates from awareness.
- [ ] Resource accounting hooks for hosted plans without putting plan names into the protocol.
- [ ] Compatibility/migration policy for CRDT library and encoding upgrades.

**Acceptance:** the documented participant and operation targets pass under relay fallback; a
malicious participant is throttled without corrupting the durable document or starving unrelated
groups.

### E25-T7 — General-purpose rooms API for non-document apps

T3's session transport is useful beyond documents: game moves, shared pointers, live polls, a
"who is looking at this file" indicator. Expose it once, generically, instead of every
app inventing a side channel.

- [ ] Room reference `room:<host-id>/<room-id>` with three binding kinds: `document` (T3/T4
      authorization), `group` (EPIC-024 membership) and `invite` (signed, expiring invite token
      for ad-hoc sessions such as a game with a friend-of-a-friend, gated by inbox policy).
- [ ] Topology: clients connect to their own relay, which holds one upstream per remote room to
      the host relay (star, not mesh); the host relay fans out and assigns per-sender sequence
      numbers, nothing else. Payloads stay opaque and may be sealed with a room key sent over
      ordinary E2E messages.
- [ ] Limits advertised in `capabilities.json` (max peers, message size, messages/second, TURN
      available) so apps and self-hosters negotiate rather than guess.
- [ ] `@poweur/live` in `packages/`: `joinRoom(ref) → { broadcast, on, presence, peers,
      channel(name) }`, resume-from-sequence reconnect, automatic upgrade to a WebRTC data
      channel when T3's peer path succeeds; framework-free core plus React hooks.
- [ ] Presence/awareness encoding compatible with the Yjs awareness protocol so existing editor
      bindings (Tiptap, CodeMirror, tldraw/Excalidraw) plug in directly.

**Acceptance:** a < 50-line "shared counter" example runs across two relays; an invite-bound room
rejects a replayed or expired invite; the same SDK drives the T5 editor's awareness.

### E25-T8 — TURN fallback & metering

Peer-to-peer is free to operate; relayed media/data is the real cost line of live features.

- [ ] coturn profile in `deploy/`; relay mints short-lived TURN credentials (REST-style shared
      secret) bound to an authenticated identity and room.
- [ ] Per-identity TURN byte meter feeding an external usage ledger; entitlement resource
      `turn_bytes_monthly`; behaviour at the limit is "relay fallback only", never a dropped
      document update.
- [ ] Self-host documentation: run your own TURN, point at a third-party one, or disable it.

**Acceptance:** a symmetric-NAT test peer connects via TURN; its bytes appear in usage; with the
allowance exhausted the session degrades to relay fallback and durable edits still converge.

## Non-goals

- A generic server-side merger for arbitrary files.
- A Figma-compatible design model, office-file engine or game-state protocol.
- Voice/video calls and SFU media relay (a later epic once rooms and TURN exist; 1:1 calls are
  the obvious first consumer).
- Matchmaking and game rules — a game kit can build on the T7 rooms API.
- Hiding membership from the collaboration peers who must exchange keys and updates.

