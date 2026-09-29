# EPIC-007 — Contacts, trust & anti-spam

- **Status:** complete (T1–T5 shipped; the `stranger_challenge` PoW gate is EPIC-014 E14-T3's open item, recommended as the phase-2 pick)
- **Priority:** P1
- **Depends on:** EPIC-003 (`poweur-sys/relay`), EPIC-006 (schemas); interacts with EPIC-009
- **Unlocks:** spam-free collaboration — a core promise of the project

## Progress

| Task | Status | Notes |
|------|--------|-------|
| E07-T1 Spec + schemas | **done** | [`apps/docs/docs/trust/contacts.md`](../apps/docs/docs/trust/contacts.md); formats in `packages/identity` (`contacts.go`, `inboxpolicy.go`), schemas + PCP-0004; `sys.contact.*` are envelope-level types bound into the message signature (`CanonicalMessageTyped`) since EPIC-009 typed payloads haven't landed |
| E07-T2 Relay enforcement | **done** | Policy evaluated in `handleMessagesPost` for local + forwarded senders (`TestPolicyEnforcedOnForwardedCrossRelay`); requests queue (`GET /requests/{identity}`, memory-only until EPIC-009) with one-slot dedup + 7-day cooldown; uniform `policy_rejected` (blocks not observable); default without a policy file = `open` for compatibility — clients set `contacts_and_requests`. **Fixed since:** `contacts_only` bounced the *answer* to a request it had sent, so a closed inbox could ask to connect and never hear back — its own contacts stayed `requested` and its own policy then bounced every message the accepter sent, a stalemate neither side could see. A `sys.contact.accept` from someone we already list as `requested` is now admitted, into the requests queue only (`TestPolicyContactAcceptReachesContactsOnlyRequester`). **Also:** contact request/accept now consult their type hooks *before* the accepted-contact and `open` short-circuits, so a knock is always a knock (`TestPolicyOpenQueuesContactRequest`, `TestPolicyAcceptedContactRequestStillQueued`, `TestINT_JOURNEY_05`/`_06`) — inbox policy decides who may *chat*, not whether the handshake can complete |
| E07-T3 Contact UX | **done** | CLI shipped: `contacts ls/add/request/accept/block/rm`, `requests`, `policy show/set`; full request→accept→chat covered by `TestINT_CONTACTS_01`, both sides of the handshake by `TestINT_JOURNEY_02`/`_05`/`_06`. Web app shipped with [EPIC-015](EPIC-015-web-app-ux.md) E15-T2/T3: contacts with profile cards and states, requests tray, policy panel. **Closed since:** (a) the requester's side never promoted itself — nothing read `sys.contact.accept`, so `contacts.json` said `requested` forever; `poweur inbox` and `poweur requests` now finish the handshake, narrowly (only someone we asked, only while the key we pinned is still theirs). (b) A queued request's intro was never decrypted, so under the *recommended* policy you were asked to accept or block a stranger with nothing but their name — `fetchRequests` takes a decryptor and fills `plaintext`. (c) `contacts accept <id> --petname X` failed with a usage line: the contacts subcommands did not normalise flag order like the rest of the CLI. (d) The send auto-prompt: a `policy_rejected` refusal now offers to convert itself into a contact request and carries the typed message across as the intro (`--request-on-reject` for scripts; never for `sys.*` envelopes, so a rejected request cannot answer itself; non-TTY stdin prints the hint instead of hanging). The Go CLI's `poweur requests` also decrypts intros now — the TS client had done so since (b), so a CLI recipient was still being asked to accept or block a stranger on a bare name (`TestINT_CONTACTS_05/_06`) |
| E07-T4 Key pinning | **done** | Pin at add/request/accept; send-time compare with rotation-statement awareness (`previous_keys`) and `--accept-new-key` override; key-swap covered by `TestINT_CONTACTS_02`. The web blocking dialog landed with [EPIC-015](EPIC-015-web-app-ux.md) E15-T2. **Closed since:** the fingerprint format — **numeric safety numbers**, four groups of five digits, derived in `packages/identity/fingerprint.go` (SHA-256 over `poweur-fingerprint-v1` + the algorithm-prefixed key, four 40-bit chunks mod 10^5), mirrored in `packages/client-ts/src/fingerprint.ts` and pinned by the `fingerprints` vectors. Digits over emoji because the primary comparison surfaces are a *terminal* and a *phone call*: emoji render unreliably in terminals, have no shared pronunciation across languages, cannot be typed back in, and their alphabet is a versioned table that silently rewrites every fingerprint when it changes. Shown at pin time, in `contacts ls`, in `identity lookup`, in the send-time refusal (both numbers **and** both keys) and in both web surfaces (`TestINT_CONTACTS_03/_04`, `apps/web/test/fingerprint.test.js`) |
| E07-T5 Abuse pressure | **done** | Four things shipped. (a) **Per-sender-relay metering** of the requests queue (`ratelimit.PeerLimiter`, `REQUEST_RELAY_LIMIT_*`, default 10/60/300): a thousand cheap identities sending one request each never trip a per-identity cap, and the relay carrying them is the one envelope field an attacker cannot vary for free. Keyed on the host serving the sender (falling back to their parent domain), charged on the *attempt*, scoped to the requests queue only — conversation between accepted contacts is never metered — and this relay's own users are exempt, since it already meters and gates them directly. Rejection is `429` with `scope: sender_relay`, never `sender`. Blast radius is one relay: `TestRequestsQueueMeteredPerSenderRelay`, `TestRequestMeterSparesLocalSendersAndConversation`, `TestINT_ABUSE_01` (which also pins relay-not-domain in both directions: a fresh domain on the exhausted relay is still throttled, a name in the flood's own domain hosted elsewhere is not). (b) **`sys.abuse.report` activated** (reserved → experimental): `POST /abuse` takes a signed report about a *locally hosted* subject, verifies the reporter's key, dedups one report per reporter per subject per day, logs and counts. It carries message IDs and never content — the traffic is E2E encrypted and shipping plaintext would hand a third party the reporter's own conversation — and the response never returns the count, so the endpoint is not a probeable reputation oracle. `poweur report` routes it to the subject's relay, not the reporter's (`TestINT_ABUSE_02`). (c) **Blocklist export/import**: a signed `Blocklist` in `shared/blocks.json` — *not* `poweur-sys/relay/`, where the permission layer refuses every visitor and a shareable document could never actually be adopted — distributed by an EPIC-005 share, adopted as a copy into the importer's own contacts tagged `source: blocklist:<publisher>`, never silently blocking someone they have accepted (`TestINT_ABUSE_03`). (d) **Design doc** [`trust/relay-reputation.md`](../apps/docs/docs/trust/relay-reputation.md) weighing shared relay blocklists, PoW on stranger requests, postage deposits and vouching, with a recommended phase-2 pick. **Open (not this epic):** the `stranger_challenge` / `stranger_pow_bits` gate in front of the requests queue — the seam is specified and the primitive shipped with EPIC-014; it is E14-T3's remaining checkbox and the doc's phase-2 recommendation |

## Goal

Make "communicate and collaborate with identities you trust, free from anonymous spam" real:
a contacts model stored in the user's home, a consent-based contact-request flow, relay-enforced
inbox policies, and key pinning so a compromised relay or registrar can't silently impersonate a
contact. Identity verification is what Poweur already does well (signatures + resolver chain);
this epic turns verified identity into *usable trust*.

## Background (current code)

- Today **any** identity can message any other: `handleMessagesPost` in
  `apps/api/internal/relay/server.go` verifies signatures and encryption envelope but has no
  notion of sender acceptability. Rate limiting (`apps/api/internal/ratelimit/`) is the only
  spam defense.
- Hosted registration (EPIC-002) makes identities *cheap*, which makes per-identity reputation
  weak — policy must therefore be **recipient-consent based** (allow-list + request flow), not
  reputation based. Cheap IDs also argue for cost on the *sender's relay* (see E07-T5).
- The relay can enforce policy because it can read `poweur-sys/relay/` (the explicit trust
  split documented in EPIC-003; `poweur-sys/private/` is owner-only).

## Design direction

- **Contacts** = `poweur-sys/relay/contacts.json` (PCP schema, EPIC-006): per contact the
  identity, **pinned public key** (TOFU — trust on first use), state
  (`requested|accepted|blocked`), petname, tags/groups, added_at, and the source of the intro.
- **Inbox policy** = `poweur-sys/relay/inbox-policy.json`:
  `contacts_only` (default for humans) | `contacts_and_requests` | `open` (bots/support
  addresses). Non-contact senders get exactly **one** pending contact-request slot — no
  message stream until accepted. **Follow-on:** optional opt-in for unsigned / web-form /
  anonymous ingress with sender challenges (none / proof-of-work / verified / payment) is
  [EPIC-014](EPIC-014-anonymous-messaging-challenges.md), optionally reached from EPIC-012's
  public contact action — default remains deny; the generated page never exposes this private
  policy surface.
- **Contact requests ride on messaging** as typed system messages (`sys.contact.request` with
  a short E2E-encrypted intro, `sys.contact.accept`, `sys.contact.block`). Accept = both sides
  write the other into contacts with pinned keys (mutual, like Signal/XMPP presence
  subscription, unlike email).
- **Key pinning**: clients alert when a contact's resolved key differs from the pinned key
  (and the change isn't covered by a signed rotation statement, E01-T5) — the SSH
  known-hosts/Signal safety-number model.

## Tasks

### E07-T1 — Contacts & inbox-policy spec + schemas

- [x] PCP + schemas for `contacts.json` and `inbox-policy.json` (fields above; petnames vs
      display names; tags reusable as share-audience groups via EPIC-005 groups)
- [x] Spec the request lifecycle state machine (none → requested → accepted/blocked;
      re-request cooldowns; unblock) in `apps/docs/docs/trust/contacts.md`
- [x] Define `sys.contact.*` message payloads (encrypted intro; 4 KB envelope cap) and
      register them (EPIC-006 registry) — envelope-level `type` field, signature-bound
- [x] Privacy analysis: contact lists are sensitive — document exactly who reads them (owner
      devices + enforcing relay; never other users), and the EPIC-003 E2EE-design implications

**Acceptance:** spec + schemas merged; state machine has a test-vector table.

### E07-T2 — Relay enforcement of inbox policy

- [x] `handleMessagesPost`: after signature verification, evaluate recipient's
      inbox policy against sender's contact state; non-contacts under `contacts_only` are
      rejected with a distinct error (`policy_rejected`) — sender's client can explain why
- [x] `contacts_and_requests`: non-contact sender's first `sys.contact.request` is accepted into
      a separate **requests queue** (not the main inbox: extend
      `apps/api/internal/storage/inbox.go` or store under `poweur-sys/relay/requests/`);
      anything else from them is rejected until accepted
- [x] Per-sender pending-request dedup + cooldown (one open request, re-request after 7 days)
- [x] Forwarded (cross-relay) traffic: policy enforced by the **recipient's** relay — verify
      this holds in the forwarding path (`forwardMessage` peers POST to recipient relay, so
      enforcement point is already right; add tests)
- [x] Integration tests: contact can message; stranger is rejected; stranger's request lands in
      requests queue; accept converts to normal flow (`policy_test.go` matrix +
      `TestINT_CONTACTS_01`)

**Acceptance:** policy matrix integration tests pass for local and cross-relay senders.

### E07-T3 — Contact UX in CLI and web app

- [x] CLI: `poweur contacts ls/add/request/accept/block/rm`, `poweur requests`, `poweur
      policy show/set`, and the send-to-non-contact prompt: a `policy_rejected` send
      offers to become a contact request carrying the same message as its intro
      (`--request-on-reject` skips the question; `sys.*` envelopes never offer)
- [x] Web app: contacts list with profile cards, requests tray with accept/block, and the
      policy panel — shipped with [EPIC-015](EPIC-015-web-app-ux.md) E15-T2/T3.
      Tray badges count what is *waiting* — pending requests, anonymous messages — and the
      inbox deliberately gets none, since the app has no read state and a badge that never
      clears teaches people to ignore badges.
      **Still open:** a composer that reads a policy rejection back into the UI (it
      surfaces the relay's error text today)
- [x] CLI writes/reads `contacts.json` through the normal file API so contacts sync
      across devices for free (EPIC-004)

**Acceptance:** full request→accept→chat flow demo between two browsers on two relays.
Two browser contexts on one relay ship with EPIC-015 E15-T2
(`apps/web/test/e2e/contacts.spec.js`); the two-relay variant stays open.

### E07-T4 — Key pinning & change alerts

- [x] Clients pin the contact's key at accept time (already in `contacts.json` schema)
- [x] Resolver results compared against pins on every send; mismatch without a valid
      rotation statement (E01-T5) → hard warning UX (CLI: refuse without `--accept-new-key`;
      web: blocking dialog with both key fingerprints)
- [x] Fingerprint display format (short auth string / emoji or numeric) — **numeric
      safety numbers** (`56963 45073 70021 85367`), canonical in
      `packages/identity/fingerprint.go`, mirrored in `packages/client-ts` and pinned by
      the `fingerprints` conformance vectors; format + rationale specced in
      [`trust/contacts.md`](../apps/docs/docs/trust/contacts.md#safety-numbers-the-fingerprint-format)
- [x] Test: simulated key swap → CLI refuses (`TestINT_CONTACTS_02`); the web client
      raises the blocking dialog and sends only after "Trust new key"
      (`apps/web/test/e2e/contacts.spec.js`, EPIC-015 E15-T2)

**Acceptance:** key-swap test produces warnings in CLI and web; legitimate rotation does not.

### E07-T5 — Abuse pressure beyond the individual inbox (design + v1)

Recipient consent stops 1:1 spam but not request-flood and not bad *relays*.
The unit of accountability here is the **relay**, not the identity: hosted registration
makes identities cheap on purpose, so the sender name is free to vary and the relay
behind it is not.

- [x] Requests-queue rate limits per sender-relay (cheap IDs cluster on relays; meter the
      relay, not just the ID) — `ratelimit.PeerLimiter`, keyed on the host serving the
      sender (parent domain as fallback), charged at `policyQueueRequest` only; local
      senders exempt; `REQUEST_RELAY_LIMIT_MINUTE|HOUR|DAY` (10/60/300, `0` disables)
- [x] Design doc: relay reputation options — [`trust/relay-reputation.md`](../apps/docs/docs/trust/relay-reputation.md)
      weighs shared signed relay blocklists (with the DNSBL delisting/appeal history as a
      warning, not a template), PoW on requests from unknown relays, postage deposits
      (recorded, not built) and vouching. **Recommended phase-2 pick: PoW** — the
      primitive, protocol, difficulty table and client solvers already shipped with
      [EPIC-014](EPIC-014-anonymous-messaging-challenges.md), the `stranger_challenge`
      seam is already specified, and it creates a price rather than an authority
- [x] User-level block export/import: `poweur blocks export|import`, signed `Blocklist`
      documents at `shared/blocks.json` distributed by an EPIC-005 share. Adoption is a
      copy into the importer's own contacts, not a subscription; an accepted contact is
      never blocked without `--force`; an unsigned or altered list is refused
- [x] Abuse-report message type `sys.abuse.report` to sender's relay operator — registry
      entry promoted to `experimental`, `POST /abuse` on the subject's relay, signed
      reports only, one per reporter/subject/day, log + counter (no enforcement, no
      export, no count in the response)
- [ ] **Deferred to [EPIC-014](EPIC-014-anonymous-messaging-challenges.md) E14-T3:** the
      `stranger_challenge` / `stranger_pow_bits` gate in front of the requests queue.
      It is that task's remaining checkbox and this doc's phase-2 recommendation; it
      needs policy-schema fields plus a matching `@poweur/client` change and new
      inbox-policy vectors, which belongs with the rest of the challenge work rather
      than bolted onto the metering pass

**Acceptance:** per-relay request throttling tested (`TestRequestsQueueMeteredPerSenderRelay`,
`TestRequestMeterSparesLocalSendersAndConversation`, `TestINT_ABUSE_01`); design doc merged
with a recommended phase-2 pick.
