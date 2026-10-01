# EPIC-009 — Messaging upgrades: persistence, push, typed messages, attachments, groups

- **Status:** T1–T6 done; **T7–T10 open** (messaging needs of apps, found by the EPIC-026 reference apps)
- **Priority:** P1
- **Depends on:** EPIC-002 (durable storage), EPIC-003 (files, for attachments)
- **Unlocks:** EPIC-005/007 system messages, EPIC-010 (event-driven automations)

> **Inbound from EPIC-002:** identity documents are durable under `POWEUR_DATA`; **inbox / acks /
> sessions remain memory-only** until this epic (E09-T1). Do not re-implement inbox durability
> under EPIC-002.

> **Outbound to EPIC-020:** E09-T1's history layout (one sealed file per message, month shards)
> is v1. Reading it costs one request per message, so history v2 — one append-mode file per
> conversation — is [EPIC-020](EPIC-020-storage-protocol-v2.md) E20-T11. Do not add paging or
> compaction to the v1 layout. E20-T11 also closes E09-T4's gap: attachment bytes are stored in
> plaintext today (only the caption is encrypted).
- **Plans note:** plan enforcement is external to the protocol. Large 1 → many *broadcast channels* are a separate shape from 1:1 and small-group messaging.

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
  `metadata` (`apps/docs/docs/protocol/message-format.md`) — typed
  messages are an *activation* of reserved design, not a breaking change.
- Acks are two-tick (`Ack` in `apps/api/internal/relay/types.go`); 512 KB message cap
  (`maxMessageBytes`) is the right boundary to keep — files go through EPIC-003 storage.
- Several earlier epics queue up dependencies on this one: `sys.share.*` (EPIC-005),
  `sys.contact.*` (EPIC-007), `sys.sync.changed` (EPIC-004).

## Tasks

### E09-T1 — Persistent inbox & message history — **done**

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
- [x] Presence side-effect on `devices.json`: an authenticated stream with a device id updates
      only that device's `last_seen` in the owner's relay-managed document; no public presence
      endpoint exists
- [x] Go CLI `poweur listen` subscribes with the same catch-up semantics as the TS CLI
- [x] Go CLI `listen --json --decrypt` / `inbox --json --decrypt`: the wire shape plus each message's
      plaintext as `body` (and acks, read receipts, journal as in a human pickup), so bots and agents
      never need the identity's message key (hello bot, `TestINT_LISTEN_03`). The TS CLI has the same flag
      and output (`cli-interop.test.ts` runs both against each other), plus `listen --once`; pickups
      are serialized, and `RelayClient.raw` no longer drops the caller's abort signal once a stream
      has connected (Ctrl-C used to leave `listen` hanging).
- [x] **Deliberately deferred to EPIC-004 E04-T4:** the event-driven sync daemon does not yet
      exist; shipped sync remains one-shot/cron-driven, so there is no daemon subscription to wire

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

### E09-T3 — Typed messages & threads (activate reserved fields) — **done**

- [x] Spec update: `type` (default `chat.text`; `sys.*` reserved for the platform, registry in
      `conventions/registry.json`), `thread_id`, `expires_at`, `metadata` — all signed, all
      appended to the canonical string only when present. Rules live in
      `packages/identity/msgtypes.go` (canonical) with the string itself in
      `crypto.CanonicalMessageEnvelope`; documented in
      `apps/docs/docs/protocol/message-format.md` under **Typed Messages** and **Ordering
      rules**. No wire version number: the *presence of a field*, not a version, selects the
      new behaviour
- [x] Relay: opaque handling of every type outside `sys.*`; an unregistered `sys.*` is
      refused with `400 unsupported_type` rather than forwarded. Per-type inbox-policy hooks
      in `apps/api/internal/relay/typed.go` — the EPIC-007 contact special-case became the
      first two entries in a hook table (`inboxTypeHooks`) rather than a parallel path, and a
      type with no hook falls to the closed-inbox default
- [x] Clients: `chat.text` renders as before; anything a client does not implement gets
      "app message from &lt;sender&gt; (&lt;type&gt;)". Go CLI (`--type/--thread/--expires/--meta`,
      thread marker in `inbox`), `@poweur/client` (`msgtypes.ts`, `describeMessage`,
      `groupByThread`), and the web app, which renders one tray row per (contact, thread) and
      keeps replies inside the thread they came from
- [x] `sys.contact.*` migrated onto the hook foundation. `sys.share.*` and `sys.sync.changed`
      are registered in the code's closed set, accepted and routed opaquely by the relay, and
      have the hook seam waiting — but nothing **emits** them yet; their senders are
      EPIC-005 and EPIC-004 work. `TestConventionsRegistryValid` now asserts the registry and
      the code agree in *both* directions, so adding one without the other fails a test

**Acceptance:** met. `apps/integration/typed_test.go` proves both directions of compatibility
against the pre-E09-T3 canonical string written out longhand: an old-shaped envelope is
accepted by a new relay and delivered readable (`TYPED_02`), a new client's plain message
verifies against what an old relay would rebuild (`TYPED_03`), and a threaded envelope does
**not** — an old relay fails closed rather than accepting and silently dropping the thread
(`TYPED_04`). Conformance vectors `message-threaded` and `message-full-envelope` pin the
TypeScript client to Go.

**Canonical line order (downstream tasks build against this).** Optional lines appear only
when the field is set, and the list is append-only — a future field goes at the end, never
between:

```
<sender>
<recipient>
<timestamp>
<payload>
id:<message_id>
session:<session_id>
enc:<alg>:<ephemeral_public_key>:<nonce>
type:<type>
thread:<thread_id>
expires:<expires_at>
meta:<key>:<value>          # one line per entry, keys ascending
```

**Deferred, on purpose:**

- `expires_at` enforcement shipped in E09-T6: the relay now rejects expired envelopes and
  clients do not retry them. It remains listed here because T3 defined and signed the field.
- Per-thread unread counts. The read mark in `poweur-sys/private/messages/` is a
  conversation-level cursor (E09-T1); a per-thread one would be a second, disagreeing answer.
  The badge stays per contact and shows on that contact's newest row.
- Thread creation UI in the web app (naming a new thread). Replying inside an existing thread
  works; starting one is a compose-screen affordance nobody has asked for yet.

### E09-T4 — File attachments via the home filesystem

Keep the 512 KB envelope; attachments are **references** to files + share grants.

- [x] Convention `chat.attachment` metadata: file path/share pointer, size, MIME, content hash
      (the existing E03 etag), optional thumbnail inline (≤ 64 KB, encrypted in payload)
- [x] Sender flow: upload to `/shared/.attachments/<id>` (or reuse existing path), auto-grant
      read to recipient (EPIC-005 grant), send message referencing it — one CLI/web action
- [x] Recipient flow: fetch via DAV with their own token; offline copies via sync
- [x] Garbage collection: attachment grants/files for deleted conversations (policy: sender
      owns the file; document retention defaults)

**Acceptance:** send-a-photo demo: 20 MB file alice→bob across relays, bob views it in web
app; spool stays small (envelope only).

**Shipped.** Go CLI and web upload, issue a recipient read grant, and send a signed seven-field
reference. Downloads mint the recipient's own DAV token and verify size plus SHA-256 before
opening. `TestINT_ATTACHMENT_01` moves a 20 MB file across two relays and asserts the JSON
envelope remains below 512 KB; the browser E2E then uploads, opens and byte-compares a real
attachment through the UI. Retention is sender-owned: attachments remain until explicit
conversation/file deletion; CLI `attachment rm` and SDK `removeAttachment` revoke the grant
before deleting bytes. Inline thumbnails remain an optional rendering optimization, not a
second storage format.

### E09-T5 — Group messaging

The hard one — scope tightly, lean on EPIC-005 group identities for membership.

- [x] v1 design decision (write it up first): **server-fanout with per-member encryption** —
      sender's client encrypts the payload separately per member (N× X25519+AEAD, fine to
      ~100 members) and posts one envelope per member; the group identity's relay fans out.
      No new cryptography. Sender-keys/MLS deferred with an explicit revisit threshold
      (member count / message volume)
- [x] Group addressing: send to `team.acme.poweur.net` (EPIC-005 group identity); relay
      expands membership at delivery time; membership changes mid-thread documented
      (new member sees nothing before join — forward secrecy by construction here)
- [x] `thread_id` semantics in groups; ack semantics (per-member ticks vs aggregate — spec it)
- [x] CLI + web minimal group chat UI
- [x] Design doc for v2: MLS (RFC 9420) adoption study — what we'd inherit (PCS, scale) and
      what it demands (group state coordination); explicit criteria for switching

**Acceptance:** 5-member cross-relay group chat integration test (delivery + late-join
behavior); v2 design doc merged.

**Shipped.** The relay exposes an authenticated signed-roster read and an epoch-checked batch
fan-out. The Go CLI and `@poweur/client` encrypt once per recipient; the web compose screen
uses the SDK path and only files inbound group hints after verifying the current signed roster.
`TestINT_GROUP_01` covers five members on two relays plus a late join. The wire and operational
decisions are in [`group-messaging.md`](../apps/docs/docs/protocol/group-messaging.md), with the
switch criteria in [`mls-adoption.md`](../apps/docs/docs/future/mls-adoption.md).

### E09-T6 — Delivery semantics & offline UX polish

- [x] Read receipts as a third tick (`AckState` extension — the `State` field was reserved for
      exactly this), per-contact opt-out in inbox policy
- [x] Outbox with retry/backoff in CLI and web (currently sends fail hard; see error paths in
      `packages/client-ts/src/http.ts`) — queued-while-offline UX
- [x] Message expiry honored (`expires_at`): relay refuses delivery after expiry, clients
      render countdown for ephemeral messages

**Acceptance:** offline-send test (relay down → up) delivers queued mail; read receipts
respect opt-out.

**Shipped.** `read` is a signed third ack state, controlled globally or per contact in inbox
policy and emitted only at the UI's read boundary. CLI and browser outboxes retain encrypted
sends, refresh relay routing, and retry with capped exponential backoff; permanent 4xx and
expired entries are not retried. `expires_at` is enforced after signature verification with
`410 message_expired`, and clients render countdowns. `TestINT_OUTBOX_01` covers a relay going
down, returning at a new address, and receiving the queued message.

## Messaging for apps (T7–T10)

Working through the [EPIC-026](EPIC-026-reference-app-scenarios.md) reference apps (site
contact + newsletter, forms, board, CRM, whiteboard) showed four gaps. Storage-side needs are
in [EPIC-020](EPIC-020-storage-protocol-v2.md); app permissions are scoped
drive handles, also in EPIC-020.

### E09-T7 — Well-known intent types

Many apps need the same few gestures from people who are not (yet) contacts. Without a shared
vocabulary, every app invents its own and inbox policy cannot reason about any of them.

- [ ] PCP in `conventions/` + `conventions/registry.json` entries, with payload schemas and
      Go/TS validators and vectors. Starting set (names final in the PCP):
      - `sys.contact.message` — "contact me" from a site or app: a message with a subject,
        from a signed ID or anonymous (E14), landing in Requests
      - `sys.list.subscribe` / `sys.list.unsubscribe` — join or leave a named list
        (newsletter, updates) with an optional email for bridge delivery (EPIC-023)
      - `sys.app.invite` — "join me in this board/doc/game": app id, node reference, optional
        share offer; builds on the `opened_with` convention
      - `sys.app.notify` — a short notification from a share member ("assigned to you",
        "mentioned you"), carrying a node reference
      - social types (`sys.social.mention`, `sys.social.reply`, optional `sys.social.follow`)
        are added by EPIC-027 E27-T3
- [ ] Inbox-policy hooks per type: `contact.message` and `list.subscribe` accepted from
      strangers under rate limits / proof-of-work (never opening a chat); `app.notify` accepted
      only from members of a share the recipient has accepted; `app.invite` follows the
      stranger rules of `sys.share.offer`
- [ ] Anonymous senders (E14) may send `contact.message` and `list.subscribe`: the page seals
      to the owner's encryption key in the browser, so anonymous bodies are still E2E encrypted
- [ ] CLI `poweur send --type <type> --payload <json>` and SDK helpers for each intent

**Acceptance:** a stranger (signed and anonymous) sends `contact.message` and
`list.subscribe` to an owner with a contacts-only inbox; both land in Requests, neither opens a
chat, and a flood is stopped by proof-of-work and rate limits.

### E09-T8 — Typed routing to apps

- [ ] Inbox query by `type` / type prefix (the envelope `type` is already plaintext and signed),
      so an app reads only its own messages and the chat tray never shows them
- [ ] Clients route non-chat types to the installed app that declares them (the app manifest);
      unknown types keep the existing generic line
- [ ] Per-type retention: app messages can be acked and dropped once the app has materialized
      them into its files (e.g. subscribers into a list file)

**Acceptance:** a board app drains `sys.app.notify` and its own `net.example.board.*` messages
without them appearing in chat; chat drains exclude them.

### E09-T9 — Shared-inbox identities

A team needs one address (`sales@acme`, `support@acme`) whose conversations every member can
read and answer — a CRM and a support desk cannot work from members' private histories.

- [ ] A group identity (EPIC-005 T5) whose **encryption key** is sealed to each member and
      rotated on membership change; senders encrypt once to the group key (unlike E09-T5
      fan-out, which stays for group chat)
- [ ] Conversation history lives as append files in the group identity's drive (E20-T11), so
      members share it and it survives members leaving
- [ ] Replies are signed by the member and marked "on behalf of" the group identity; recipients
      see both
- [ ] Email arriving through the bridge (EPIC-023) for the group lands in the same inbox

**Acceptance:** a customer on relay A messages `sales@acme` on relay B; two members on
different relays read the thread and one replies; a removed member cannot read later messages.

### E09-T10 — Follow feeds

Newsletters and updates for Poweur IDs should not need push fan-out from the owner. The post
and feed formats, followers-only feeds, the relay subscription proxy and indexers are specified
in [EPIC-027](EPIC-027-public-web-feeds-boards-indexers.md); this task delivers the follow
mechanics they build on.

- [ ] Convention for a feed folder (posts + `feed.json` index) published publicly or shared
      with subscribers (subscriptions are renewing shares)
- [ ] Following = subscribing to that folder's change events (E20-T5) plus a local follow
      list; the owner need not know public followers
- [ ] `list.subscribe` (T7) remains for owners who want a subscriber list and for email
      subscribers, who get pushed copies via EPIC-023; 1 → many push is out of scope here
- [ ] CLI `poweur follow|unfollow|feed`

**Acceptance:** an owner publishes a post; followers on two relays receive it from the feed
without the owner sending anything per follower.
