# EPIC-007 — Contacts, trust & anti-spam

- **Status:** proposed
- **Priority:** P1
- **Depends on:** EPIC-003 (`poweur-sys/relay`), EPIC-006 (schemas); interacts with EPIC-009
- **Unlocks:** spam-free collaboration — a core promise of the project

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
  [EPIC-014](EPIC-014-anonymous-messaging-challenges.md), consumed by EPIC-012 contact
  forms — default remains deny; the policy vocabulary lands in this epic's schema so web
  forms and general messaging share one policy surface.
- **Contact requests ride on messaging** as typed system messages (`sys.contact.request` with
  a short E2E-encrypted intro, `sys.contact.accept`, `sys.contact.block`). Accept = both sides
  write the other into contacts with pinned keys (mutual, like Signal/XMPP presence
  subscription, unlike email).
- **Key pinning**: clients alert when a contact's resolved key differs from the pinned key
  (and the change isn't covered by a signed rotation statement, E01-T5) — the SSH
  known-hosts/Signal safety-number model.

## Tasks

### E07-T1 — Contacts & inbox-policy spec + schemas

- [ ] PCP + schemas for `contacts.json` and `inbox-policy.json` (fields above; petnames vs
      display names; tags reusable as share-audience groups via EPIC-005 groups)
- [ ] Spec the request lifecycle state machine (none → requested → accepted/blocked;
      re-request cooldowns; unblock) in `apps/docs/docs/trust/contacts.md`
- [ ] Define `sys.contact.*` message payloads (encrypted intro ≤ 1 KB, sender profile hint)
      and register them (EPIC-006 registry)
- [ ] Privacy analysis: contact lists are sensitive — document exactly who reads them (owner
      devices + enforcing relay; never other users), and the EPIC-003 E2EE-design implications

**Acceptance:** spec + schemas merged; state machine has a test-vector table.

### E07-T2 — Relay enforcement of inbox policy

- [ ] `handleMessagesPost`/`handleAcksPost`: after signature verification, evaluate recipient's
      inbox policy against sender's contact state; non-contacts under `contacts_only` are
      rejected with a distinct error (`policy_rejected`) — sender's client can explain why
- [ ] `contacts_and_requests`: non-contact sender's first `sys.contact.request` is accepted into
      a separate **requests queue** (not the main inbox: extend
      `apps/api/internal/storage/inbox.go` or store under `poweur-sys/relay/requests/`);
      anything else from them is rejected until accepted
- [ ] Per-sender pending-request dedup + cooldown (one open request, re-request after N days)
- [ ] Forwarded (cross-relay) traffic: policy enforced by the **recipient's** relay — verify
      this holds in the forwarding path (`forwardMessage` peers POST to recipient relay, so
      enforcement point is already right; add tests)
- [ ] Integration tests: contact can message; stranger is rejected; stranger's request lands in
      requests queue; accept converts to normal flow

**Acceptance:** policy matrix integration tests pass for local and cross-relay senders.

### E07-T3 — Contact UX in CLI and web app

- [ ] CLI: `poweur contacts ls/add/accept/block/rm`, `poweur requests` (pending in/out);
      `poweur send` to a non-contact prompts to send a contact request first
- [ ] Web app (`apps/web/js/app.js`): contacts list with profile cards (E06-T2), requests
      inbox with accept/block, badge counts; message composer constrained by policy errors
- [ ] Both clients write/read `contacts.json` through the normal file API so contacts sync
      across devices for free (EPIC-004)

**Acceptance:** full request→accept→chat flow demo between two browsers on two relays.

### E07-T4 — Key pinning & change alerts

- [ ] Clients pin the contact's key at accept time (already in `contacts.json` schema)
- [ ] Resolver results compared against pins on every send/receive; mismatch without a valid
      rotation statement (E01-T5) → hard warning UX (CLI: refuse without `--accept-new-key`;
      web: blocking dialog with both key fingerprints)
- [ ] Fingerprint display format (short auth string / emoji or numeric, pick one and spec it)
      for out-of-band verification
- [ ] Test: simulate a malicious relay swapping a hosted identity's key → both clients flag

**Acceptance:** key-swap test produces warnings in CLI and web; legitimate rotation does not.

### E07-T5 — Abuse pressure beyond the individual inbox (design + v1)

Recipient consent stops 1:1 spam but not request-flood and not bad *relays*.

- [ ] Requests-queue rate limits per sender-relay (cheap IDs cluster on relays; meter the
      relay, not just the ID) — extend `ratelimit` with per-peer-relay buckets keyed on the
      forwarding source
- [ ] Design doc: relay reputation options — shared blocklists (file-based, subscribable, like
      DNSBL but signed), proof-of-work on contact requests from unknown relays (primitive +
      challenge protocol come from [EPIC-014](EPIC-014-anonymous-messaging-challenges.md)),
      postage-style deposits (note only; don't build)
- [ ] User-level block export/import: blocklists as shareable signed files (EPIC-005 share of
      a `blocks.json`) so communities can pool defense
- [ ] Abuse-report message type `sys.abuse.report` to sender's relay operator (registry entry +
      minimal handling: log + counter)

**Acceptance:** per-relay request throttling tested; design doc merged with a recommended
phase-2 pick.
