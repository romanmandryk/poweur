# EPIC-009 — Messaging upgrades: persistence, push, typed messages, attachments, groups

- **Status:** in progress — T1 and T2 done; T3–T6 open
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

### E09-T1 — Persistent inbox & message history — **done** (history still open)

- [x] Inbox and acks moved onto a durable spool: one file per entry, ordered by a
      per-identity sequence number that doubles as the pickup cursor. **Not** under
      `identities/<id>/spool/` as sketched — it lives at `<POWEUR_DATA>/spool/…`, outside
      the DAV tree. The tree's roots are a documented, permission-checked layout and
      undelivered mail is not one of them; keeping it out means no new root, no new
      permission rule, and no way to reach the spool over DAV
- [x] Pickup protocol: `GET /messages/{id}?since=<cursor>` reads without forgetting and
      returns a cursor; `POST /messages/{id}/consume` is the explicit acknowledgement.
      Backward compatibility is the **absence of the parameter** rather than a version
      header — a request with no `since` gets the original drain, which is exactly what an
      old client already sends
- [x] Retention: `SPOOL_TTL` (default 30 days), expired messages reported to the sender as
      a `sys.delivery.failed` ack
- [x] `TestINT_SPOOL_01`: a message posted, never picked up, and still delivered after the
      relay restarts on the same data dir — and not delivered twice
- [x] Client-side *history*, at `poweur-sys/private/messages/` — the format is canonical in
      `packages/identity/history.go` with conformance vectors (`history-paths`,
      `history-read-state`), implemented in the Go CLI (`poweur history`), in
      `@poweur/client` (`MessageHistory`, `inboxAndArchive`, `sendAndArchive`,
      `anonAndArchive`) and in the web app, which now redraws from it after a reload

**How it is stored, and why that way.** One file per message under a month shard, named by a
sort key so a plain listing is chronological, sealed to the owner's own X25519 key — the same
envelope messages already use, with the owner as their own recipient. That needs no new key
custody (every identity has that key), makes `poweur-sys/private`'s "the relay stores but must
not read" a fact rather than a contract, and lets any enrolled device open the archive. Records
are write-once and their path is a pure function of `(timestamp, id)`, so two devices picking up
the same message write identical bytes to one path and there is no merge to get wrong. The
sender's own copy is archived too: the relay never hands a sender their own message back, so
without it a conversation shows only one side.

**Read marks are positions, not timestamps.** `read-state.json` records `(timestamp, id)` per
peer. Message timestamps are RFC3339 to the second, so two messages a moment apart routinely
share one, and a timestamp-only mark silently swallows the second. Comparing on the same total
order `SortHistory` imposes is what lets an unread count actually reach zero — and the web
app's conversation badge, which used to be `messages.length`, now counts from it.

**Found on the way: an accepted anonymous message published no event.** `handleAnonMessage`
never called `notify`, so a client sitting on the Messages screen learned about anonymous mail
only when something unrelated caused a re-render. It now publishes its own event kind (`anon`)
rather than `message`: told `message`, a client fetches the inbox — which by design never holds
an anonymous message — and leaves the tray that has something silent.

**The expiry notice is unsigned, and says so.** Only the relay can honestly report that it
gave up holding something, so the `sys.delivery.failed` ack it generates carries no
signature. Clients must read it as the relay's own admission rather than proof about the
recipient — written into the API reference next to the endpoint.

**Found on the way: one outstanding challenge per identity.** The challenge store kept a
single nonce per identity, so any two authenticated reads in flight at once invalidated
each other — E15-T6 worked around it with a single-flight guard in the web app, and a push
stream reconnecting behind a poll would have hit it constantly. Challenges are now spent
**by value** (the caller echoes the one it was issued), with the newest-wins behaviour kept
for WebAuthn assertions, which carry the challenge inside signed client data instead.

**Acceptance:** restart-loss test passes. Multi-device pickup is demonstrated:
`TestINT_HISTORY_01` reads a message once, throws the local home away, and a second device
holding only the identity reads the same archive; `TestINT_HISTORY_03` shows read marks
surviving to it. `apps/web/test/e2e/durability.spec.js` asserts the same properties through the
browser, after a reload each time — which is the failure people actually hit.

### E09-T2 — Push delivery — **done** (as SSE, not WebSocket)

- [x] `GET /events/{identity}`: authenticated with the same challenge-signed headers as the
      inbox pickup, pushing `message` and `ack` notifications, with polling untouched as the
      fallback
- [x] Reconnect/backoff and catch-up through the T1 cursor — `streamForever` in
      `@poweur/client`, with the backoff reset on every successful connection
- [x] Web client subscribes while unlocked; `poweur listen` on the TS CLI
- [x] Connection limits (`MAX_STREAMS_PER_IDENTITY`) and idle timeout (`STREAM_IDLE_TIMEOUT`)
- [ ] **Deferred:** presence side-effect on `devices.json` — that document is EPIC-004 T6
      and does not exist yet. The privacy decision it records (not user-visible in v1)
      stands unchanged
- [ ] **Open:** the Go CLI has no `listen`; the sync daemon does not subscribe

**Server-Sent Events rather than a WebSocket, deliberately.** The channel only ever pushes
one way — this epic's own rule is "the socket is notification, the cursor is truth" — and
SSE delivers that over plain `net/http`. A WebSocket would have made gorilla the relay's
**first third-party dependency**, for a stream that never reads. Nothing in the contract
depends on the transport: catching up is a cursor read either way, so a bidirectional
socket can replace it later without clients changing.

Events carry **no payload**, only `{type, message_id}`. Delivery semantics stay in one
place, so a dropped frame costs a round trip rather than a message — which is also why the
relay drops notifications for a slow reader instead of blocking the POST that produced them.

**Acceptance:** met. `test/events-relay.test.ts` covers notification, reconnect, and that a
message sent while nobody is listening is still there afterwards;
`apps/web/test/e2e/policy.spec.js` asserts the browser case that matters — a reader sitting
on Messages, touching nothing, sees the message arrive. Confirmed on an iOS simulator too
(EPIC-019), which is where two gaps in this task showed up: a **queued contact request**
produced no notification at all (the relay notified only on the inbox path), and the client
fetched it without repainting, because the loader only re-rendered when its own tray was
open while the badge lives on the tray bar.

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
      `packages/client-ts/src/http.ts`) — queued-while-offline UX
- [ ] Message expiry honored (`expires_at`): relay refuses delivery after expiry, clients
      render countdown for ephemeral messages

**Acceptance:** offline-send test (relay down → up) delivers queued mail; read receipts
respect opt-out.
