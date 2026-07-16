# EPIC-009 — Messaging upgrades: persistence, push, typed messages, attachments, groups

- **Status:** proposed
- **Priority:** P1
- **Depends on:** EPIC-002 (durable storage), EPIC-003 (files, for attachments)
- **Unlocks:** EPIC-005/007 system messages, EPIC-010 (event-driven automations)

> **Inbound from EPIC-002:** identity documents are durable under `POWEUR_DATA`; **inbox / acks /
> sessions remain memory-only** until this epic (E09-T1). Do not re-implement inbox durability
> under EPIC-002.

## Goal

Grow messaging from "polled, in-memory, 1:1 text" to the real-time collaboration substrate the
vision needs: messages survive relay restarts, arrive via push, carry types (so system flows
and apps can build on them), reference files as attachments, and work in groups. The E2E
encryption and signature model stays exactly as is — these are delivery/semantics upgrades,
not crypto changes (except groups, which get their own carefully-scoped task).

## Background (current code)

- Inbox is in-memory (`apps/api/internal/storage/inbox.go`); restart loses undelivered mail.
  Clients poll `GET /messages/{identity}` (challenge-signed, see `handleMessagesGet`).
- The protocol already reserves forward-compatible fields: `type`, `thread_id`, `expires_at`,
  `metadata` (`apps/docs/docs/protocol/message-format.md` / `requirements.md`) — typed
  messages are an *activation* of reserved design, not a breaking change.
- Acks are two-tick (`Ack` in `apps/api/internal/relay/types.go`); 512 KB message cap
  (`maxMessageBytes`) is the right boundary to keep — files go through EPIC-003 storage.
- Several earlier epics queue up dependencies on this one: `sys.share.*` (EPIC-005),
  `sys.contact.*` (EPIC-007), `sys.sync.changed` (EPIC-004).

## Tasks

### E09-T1 — Persistent inbox & message history

- [ ] Move inbox + acks onto the E02-T1 storage layer: spool undelivered messages under
      `identities/<id>/spool/`, delete on acked pickup; decide and spec *history* separately —
      delivered messages are the **client's** to keep (store under
      `/poweur-sys/private/messages/<peer>/<year-month>.ndjson` so history syncs via EPIC-004
      like any file — relay holds ciphertext only, exactly as today)
- [ ] Pickup protocol: `GET /messages/{identity}?since=<cursor>` with stable ordering +
      explicit consume-ack (replace the current implicit drain semantics; keep backward compat
      behind a version header for old clients)
- [ ] Retention config: spool TTL for never-picked-up messages (default 30 days), expired →
      `sys.delivery.failed` ack to sender
- [ ] Integration test: relay restart with undelivered mail → delivered after restart

**Acceptance:** restart-loss test passes; multi-device pickup (two sessions, one identity)
delivers to both.

### E09-T2 — WebSocket push delivery

- [ ] `GET /ws` endpoint: client authenticates with session-signed challenge (reuse
      `resolveSigningKey` flow), subscribes to its identity; relay pushes new messages, acks,
      and `sys.*` events over the socket; polling remains as fallback
- [ ] Presence side-effect: socket-connected sessions update `devices.json` last_seen
      (EPIC-004) — explicitly NOT user-visible presence in v1 (privacy decision, document it)
- [ ] Reconnect/backoff + missed-event catch-up via the T1 cursor (socket is notification,
      cursor is truth — no exactly-once requirement on the socket)
- [ ] Web client + CLI (`poweur listen`) consume the socket; sync daemon (E04-T4) subscribes
      for `sys.sync.changed`
- [ ] Load consideration: connection limits per identity, idle timeouts (config)

**Acceptance:** message latency drops from poll-interval to sub-second in the integration
demo; kill-the-socket catch-up test passes.

### E09-T3 — Typed messages & threads (activate reserved fields)

- [ ] Spec update: `type` (default `chat.text`; `sys.*` reserved for platform, registry in
      EPIC-006), `thread_id`, `expires_at`, `metadata` (signed — extend the canonical string,
      version the signing scheme carefully: new fields only signed when present, exactly like
      the existing optional-line pattern)
- [ ] Relay: opaque handling of all types except `sys.*` it owns; per-type inbox-policy hooks
      (EPIC-007 needs `sys.contact.request` special-cased)
- [ ] Clients: render `chat.text`, generic fallback for unknown types ("app message from …"),
      thread grouping in web UI
- [ ] Migrate the in-flight definitions from other epics (`sys.share.*`, `sys.contact.*`,
      `sys.sync.changed`) onto this foundation and register them

**Acceptance:** old clients interop with new relays (compat tests); typed system flows from
EPIC-005/007 ride on `type` end to end.

### E09-T4 — File attachments via the home filesystem

Keep the 512 KB envelope; attachments are **references** to files + share grants.

- [ ] Convention `chat.attachment` metadata: file path/share pointer, size, MIME, content hash
      (the existing E03 etag), optional thumbnail inline (≤ 64 KB, encrypted in payload)
- [ ] Sender flow: upload to `/shared/.attachments/<id>` (or reuse existing path), auto-grant
      read to recipient (EPIC-005 grant), send message referencing it — one CLI/web action
- [ ] Recipient flow: fetch via DAV with their own token; offline copies via sync
- [ ] Garbage collection: attachment grants/files for deleted conversations (policy: sender
      owns the file; document retention defaults)

**Acceptance:** send-a-photo demo: 20 MB file alice→bob across relays, bob views it in web
app; spool stays small (envelope only).

### E09-T5 — Group messaging

The hard one — scope tightly, lean on EPIC-005 group identities for membership.

- [ ] v1 design decision (write it up first): **server-fanout with per-member encryption** —
      sender's client encrypts the payload separately per member (N× X25519+AEAD, fine to
      ~100 members) and posts one envelope per member; the group identity's relay fans out.
      No new cryptography. Sender-keys/MLS deferred with an explicit revisit threshold
      (member count / message volume)
- [ ] Group addressing: send to `team.acme.poweur.net` (EPIC-005 group identity); relay
      expands membership at delivery time; membership changes mid-thread documented
      (new member sees nothing before join — forward secrecy by construction here)
- [ ] `thread_id` semantics in groups; ack semantics (per-member ticks vs aggregate — spec it)
- [ ] CLI + web minimal group chat UI
- [ ] Design doc for v2: MLS (RFC 9420) adoption study — what we'd inherit (PCS, scale) and
      what it demands (group state coordination); explicit criteria for switching

**Acceptance:** 5-member cross-relay group chat integration test (delivery + late-join
behavior); v2 design doc merged.

### E09-T6 — Delivery semantics & offline UX polish

- [ ] Read receipts as a third tick (`AckState` extension — the `State` field was reserved for
      exactly this), per-contact opt-out in inbox policy
- [ ] Outbox with retry/backoff in CLI and web (currently sends fail hard; see error paths in
      `apps/web/js/api.js`) — queued-while-offline UX
- [ ] Message expiry honored (`expires_at`): relay refuses delivery after expiry, clients
      render countdown for ephemeral messages

**Acceptance:** offline-send test (relay down → up) delivers queued mail; read receipts
respect opt-out.
