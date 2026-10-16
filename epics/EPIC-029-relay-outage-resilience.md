# EPIC-029 — Relay & DNS outage resilience: cached identities, backup mailboxes

- **Status:** proposed 2026-10-16
- **Priority:** P1 (a single relay or DNS outage today stops all messaging for every ID it hosts)
- **Depends on:** EPIC-001 (web-first resolution), EPIC-002 (relay registration and storage),
  EPIC-007 (contacts, pinned keys, inbox policy), EPIC-009 (outbox, delivery acks, Web Push),
  EPIC-011 (device registry, rotation), EPIC-017 (TS SDK), EPIC-028 (web app at `/id/`)
- **Touches:** `packages/identity`, `packages/client-ts`, `apps/api` (relay), `apps/cli`,
  `apps/web`, `apps/integration`, `apps/docs`, `conventions` (PCP-0002), `deploy`
- **Follow-up:** [EPIC-030](EPIC-030-offgrid-p2p-resilience.md) (name attestations, relay
  takeover, DHT/Nostr discovery, P2P and LAN islands) — a draft

## Progress

| Task | Status | Notes |
|------|--------|-------|
| **Tier 0 — survive with what clients already know** | | |
| E29-T1 Last-known-good identity store (Go + TS) | open | `packages/identity` + `packages/client-ts`; stale-if-error, rollback guard |
| E29-T2 Relay: cache sender key resolution | open | `resolveIdentityPublicKey` resolves on every message today |
| E29-T3 CLI: send from cache; queue instead of failing on resolve errors | open | |
| E29-T4 SDK + web: persistent identity cache and contacts snapshot | open | |
| E29-T5 Web: installed app opens offline | open | `sw.js` caches nothing today |
| E29-T6 Connectivity state in every client | open | |
| **Tier 1 — more than one relay per identity** | | |
| E29-T7 Spec: relay set, mailbox role, delivery and polling rules, threat model | open | gate for T8–T14 |
| E29-T8 `RelaySet` statement, vectors, embedding in `id.json` | open | outside the document's canonical bytes |
| E29-T9 Relay: publish and update the relay set | open | |
| E29-T10 Relay: mailbox hosting (registration, storage, quotas, expiry) | open | |
| E29-T11 Relay: verifying senders and enforcing policy as a mailbox | open | owner-supplied pins; `503 sender_unresolvable` |
| E29-T12 Senders: fail over across the recipient's relay set | open | CLI, SDK, web; acks too |
| E29-T13 Recipients: poll every relay, merge, dedup | open | CLI, SDK, web |
| E29-T14 Managing backup mailboxes (CLI, SDK, web settings) | open | |
| E29-T15 Hosted default: a backup mailbox for every hosted ID | open | ops + onboarding |
| E29-T16 Integration and Playwright journeys | open | throughout |
| E29-T17 Docs | open | throughout |

**Order:** T1 → T2–T4 (parallel) → T5, T6 → T7 → T8 → T9–T11 → T12–T14 → T15. T16 and T17
grow with each task. Tier 0 ships on its own and is useful without tier 1.

## Goal

When a relay or its DNS goes away, people who already know each other keep messaging.
Concretely:

1. **Home relay down, internet up.** Alice's contacts can still deliver to her, and she can
   still read what they send, through a *backup mailbox* on another relay.
2. **poweur.net's DNS or web front down.** Clients use the signed identity documents they already
   hold, so they can still verify and reach their contacts.
3. **Nothing changes for users when everything is up.** An ID is still just a name such as
   `alice.poweur.net`; there is no new config to type. Hosted IDs get a backup mailbox
   automatically.

Out of scope here, drafted in EPIC-030: reaching IDs you have never resolved while DNS is down,
proving a stranger's name offline, ad-hoc relay takeover, DHT or Nostr discovery, peer-to-peer
and LAN-only operation, drive and sign-in resilience.

## Background (2026-10-16)

### What is already in our favour

- **The envelope does not name a relay.** The canonical signing string
  (`message-format.md` → *Canonical Signing String*) covers sender, recipient, timestamp,
  payload, id, session, encryption and the typed extensions, never the relay it is posted to.
  The same signed envelope is valid at any relay.
- **Messages have client-assigned ids,** and relays store them keyed by id, so delivering one
  message to two relays is harmless if the recipient dedups.
- **Outboxes exist.** The CLI keeps identity-signed envelopes in
  `~/.poweur/pending/<id>.outbox.json` (`apps/cli/internal/cli/outbox.go`), and the web app has
  `apps/web/src/lib/outbox.js` (localStorage). Both retry with backoff and drop on permanent 4xx.
- **Contacts pin keys.** `contacts.json` entries carry `pinned_key` (TOFU, PCP-0004), and
  `previous_keys` in `id.json` covers rotation.
- **Relays store `id.json` as raw bytes** (`identities.DocumentJSON`, served verbatim by
  `wellknown.go`), and both verifiers (`ParseDocument` in Go, `parseDocument` in TS) ignore
  unknown top-level fields. A new self-signed field can therefore ride inside `id.json` without
  breaking old clients, **as long as it stays outside `CanonicalBytes`**: the canonical form
  lists known fields explicitly, so a new field inside it would fail every old verifier.
- **The web vault keeps wrapped keys in `localStorage`** (`apps/web/src/lib/vault.js`), so
  unlocking does not need the relay.

### What breaks today

| Dependency | Where | Effect of an outage |
|---|---|---|
| Recipient relay = identity host | `resolveRecipientRelayURL` (CLI `cli.go`), `resolveRecipientRelayUrl` (SDK `messages.ts`): `document.relay` or the identity hostname | One relay per ID. If it's down, messages wait in outboxes until it's back |
| Sender key at the receiving relay | `resolveSigningKey` → `resolveIdentityPublicKey` (`apps/api/internal/relay/server.go`): local store, then a fresh `idpkg.Resolve` **with no cache** | Every cross-relay message resolves the sender live; if the sender's DNS or web is down, delivery fails with 401 |
| Session proof checks | `acceptSessionProof` resolves the sender the same way | same |
| Client resolution | `idpkg.Cache` / `ResolveCache`: in-memory, 5 min TTL | After a restart or reload, a contact behind a dead DNS can't be resolved at all |
| Web app shell | `apps/web/public/sw.js` handles push only; no fetch caching | If the identity's origin is down, the installed PWA doesn't open |
| Contact list, pins, policy | `.poweur/relay/contacts.json` in the drive on the home relay | Clients that don't keep a local copy lose them when the relay is down |

## Design direction

### D1 — Tier 0: a last-known-good store, not a longer TTL

A resolver keeps the **last verified document per identity** on disk (CLI, relay) or in
IndexedDB (browser). Resolution becomes:

1. Fresh cache hit (≤ 5 min, as today) → use it.
2. Otherwise fetch (web, then DNS, as today). A valid fresh answer replaces the stored one and
   is returned.
3. **Only if the fetch fails** (network error, timeout, 5xx; *not* a valid "not found", a key
   mismatch or a bad signature) → return the stored document marked `stale: true` with its
   `fetched_at`, if it is younger than the caller's `max_stale` (client default 30 days, relay
   default 7 days).

Rules:

- **Fail-closed stays.** Web/DNS key mismatch and invalid signatures are still errors; the store
  never hides them.
- **Rollback guard.** A fresh document with `updated_at` older than the stored one is rejected
  (`identity_document_rollback`) and the stored one is kept. Documents are client-signed, so
  `updated_at` only moves forward for an honest owner.
- **Only signed documents are stored.** DNS-only results (`resolveDNS` builds an unsigned
  document) are never persisted.
- Callers that must not use stale data (registration, rotation, sign-in verification by a
  relying party) pass `max_stale: 0`.

### D2 — Tier 1: a signed relay set inside `id.json`

The owner signs a separate statement listing the relays that accept mail for the ID:

```json
{
  "version": 1,
  "identity": "…",
  "public_key": "…",
  "relay": "alice.poweur.net",
  "updated_at": "…",
  "signature": "…",
  "relay_set": {
    "identity": "alice.poweur.net",
    "seq": 3,
    "issued_at": "2026-10-20T09:00:00Z",
    "relays": [
      { "url": "https://alice.poweur.net", "role": "home" },
      { "url": "https://mbox.poweur-backup.example", "role": "mailbox" }
    ],
    "signature": "…"
  }
}
```

- `relay_set` is **not** part of the document's canonical bytes, so old verifiers keep
  verifying the document and simply ignore it. It has its own canonical form, signed with the
  identity key under a domain-separation prefix (`poweur-relay-set-v1\n` + canonical JSON
  without `signature`).
- Valid only if: `identity` equals the document's; the signature verifies against the document's
  `public_key`, or a `previous_keys` entry still in its grace window; there are 1–4 entries; URLs
  are `https` (`http` only when the resolver allows private addresses, as in tests); exactly one
  `home` entry, whose host matches `relay`; and `seq` is higher than any set the verifier
  already holds for that ID (rollback guard, as in D1).
- A missing `relay_set` means `[{ url: <relay>, role: home }]`, which is today's behaviour.
- **Why inside `id.json` and not a separate file:** one fetch, one cache entry, and the set
  travels with the document wherever it is carried (the store in D1 now, gossip in EPIC-030).
- **Why separately signed:** the home relay re-serves the set but cannot change it, and the owner
  can update the set (a new `seq`) without re-signing the whole document.

### D3 — The mailbox role

A **mailbox** is a relay that holds mail for an identity it does not host. It accepts exactly:

| Endpoint | For a mailbox identity |
|---|---|
| `POST /messages` | ✅ inbound only (recipient = mailbox identity) |
| `POST /acks` | ✅ acks addressed to the mailbox identity |
| `POST /sessions`, `DELETE /sessions/:id` | ✅ verified against the stored document |
| `GET /messages/:identity`, consume | ✅ |
| `PUT` / `DELETE /mailboxes/:identity` | ✅ registration (D5) |
| `GET /mailboxes/:identity/document` | ✅ the stored `id.json` with its relay set, public, read-only |
| Everything else: drive, identity page, `/.well-known` for that host, push, groups, anonymous ingress, OAuth, keystore | ❌ `404 not_hosted` |

The mailbox never sends on the owner's behalf. The at-least-one-local rule treats a mailbox
identity as local **only when it is the recipient**; a message *from* a mailbox identity to a
non-local recipient is still refused, so a mailbox can't become an open forwarder.

A relay operator opts in with `MAILBOX_HOSTING=off|open|allowlist` (default `off`) and limits
(registrations, bytes per mailbox, retention, default 14 days). A deployment can also run
**mailbox-only** (`RELAY_MODE=mailbox`): no hosted identities, no registration, no web app. This
is the cheap shape for a backup on separate infrastructure.

### D4 — Delivery and polling

**Senders** (CLI, SDK, web) resolve the recipient (D1), read the relay set (D2) and try the
entries in order: `home` first, then mailboxes in listed order.

- Fail over on network error, timeout, `5xx` or `429` with a reset later than 1 minute.
- Never fail over on other `4xx`. A policy rejection from the home relay is final; trying the
  mailbox would be a policy bypass.
- Stop at the first `2xx`. **Failover, not fan-out:** one copy, so less metadata and storage.
- The outbox entry stores the whole relay set and the index reached, and retries walk the list
  again.
- Acks follow the same rule, addressed to the original sender's relay set.

**Recipients** poll every relay in their own set:

- Home on every poll, as today. Each mailbox on app open, whenever a home poll fails, and
  otherwise every 15 minutes (configurable).
- Each relay has its own session (the session flow is already per relay).
- Results are merged by message id and consumed per relay. History and threads see one message.
- The existing Web Push wake-up only comes from the home relay; see *Open questions*.

### D5 — Mailbox registration carries what the mailbox needs to enforce policy

A mailbox can't resolve senders when DNS is down, and in the likeliest outage (poweur.net down)
most of the owner's contacts are hosted on the same failed infrastructure. So registration
includes the owner's own enforcement data:

```
PUT /mailboxes/alice.poweur.net          (signed request, identity key: signedreq.go)
{
  "document":  { …signed id.json, with relay_set… },
  "policy":    "contacts_and_requests",            // from inbox-policy.json
  "pins":      [ { "identity": "bob.poweur.net", "key": "ed25519:…" }, … ],  // accepted contacts only
  "blocked":   [ "eve.example.org" ],
  "issued_at": "…"
}
```

- The owner's clients re-`PUT` whenever a contact is accepted or blocked, a pin changes, the
  policy changes or keys rotate, and **at least weekly**.
- A registration not refreshed for 30 days expires and its stored mail is deleted, so abandoned
  mailboxes don't pile up.
- **Document updates at the mailbox:** a new document is accepted only if the request is signed
  by the currently stored key, or a fresh web resolution returns that same document. A thief of
  a retired key can't repoint a mailbox.
- **Privacy:** this hands the mailbox operator Alice's contact list, which the home relay already
  sees. The settings screen says so plainly, and the hosted default (T15) uses a mailbox run by
  the same operator.

**Verifying a sender at a mailbox** (T11), in order:
1. local store (hosted or mailbox identity);
2. fresh resolve;
3. **the owner's pin for that sender**, if any;
4. the relay's last-known-good store (D1, ≤ 7 days);
5. otherwise reply **`503 sender_unresolvable`** with `Retry-After`. Senders treat that as
   retryable (it already is, as `≥ 500`), so the message waits in the outbox instead of being
   dropped.

Inbox policy then applies as on the home relay, using the registered `policy`, `pins` and
`blocked` lists. A stranger whose name resolves can still land in requests when the policy
allows. Anonymous ingress and PoW challenges are home-relay only for now.

### D6 — Hosted IDs get a backup by default

The poweur.net operator runs a mailbox-only relay on **a different provider, a different
registrar and DNS host, and a domain outside poweur.net** (and not behind the same Cloudflare
account). Otherwise the backup shares the failure it is meant to cover.

- The home relay advertises it in `GET /health` (a new `mailbox` block) as `recommended_mailbox`.
- Onboarding signs a relay set with it and registers the mailbox, with no extra step.
- Settings can turn it off or swap it for another mailbox.
- Own-domain IDs get the same option, but nothing is turned on for them by default.

### Decisions to confirm in T7

| # | Proposal | Alternative considered |
|---|---|---|
| A | Failover, not fan-out | Fan-out to all relays: survives a home relay that silently accepts and then loses mail, but doubles metadata and storage |
| B | Max 4 relays in a set | Unlimited: more polling and attack surface for little gain |
| C | Mailbox retention 14 days, registration expiry 30 days | Longer: more storage abuse |
| D | Pins uploaded in clear to the mailbox | Hashed pins (`H(identity‖key)`): hides the list from casual inspection, but the operator can still match senders, so it buys little |
| E | Rollback guard on `updated_at` (document) and `seq` (relay set) | none |

## Tasks

### E29-T1 — Last-known-good identity store (Go + TS)

`packages/identity` and `packages/client-ts`; behaviour per D1.

- [ ] Go: `Store` interface (`Get(identity) (StoredDocument, bool)`, `Put(identity, raw []byte, fetchedAt)`),
      a file implementation (one JSON file per identity under a directory, atomic rename, 0600)
      and an in-memory one for tests. `StoredDocument` keeps the **raw verified bytes**, so
      unknown fields (the future `relay_set`) survive.
- [ ] `ResolveOptions.Store`, `ResolveOptions.MaxStale`; `Result.Stale`, `Result.FetchedAt`.
- [ ] Classify fetch errors: only transport errors, timeouts and `5xx` allow the stale fallback.
      `404`, a key mismatch, a bad signature or an identity mismatch never do.
- [ ] Rollback guard: a fresh document with an older `updated_at` than the stored one →
      `ErrDocumentRollback`; the stored one is kept.
- [ ] Never store DNS-only (unsigned) results.
- [ ] `moved_to`: store the target's document under the target name; the source keeps its
      tombstone.
- [ ] TS twin in `resolve.ts`: `DocumentStore` interface, in-memory implementation, and
      `IndexedDbDocumentStore` under `src/browser` (falls back to memory when IndexedDB is
      unavailable or throws). Node file store under `src/node`.
- [ ] Shared test vectors in `packages/identity/testdata` covering each branch: fresh, stale
      allowed, stale too old, 404 not stale-able, key mismatch not stale-able, rollback.

**Acceptance:** Go and TS pass the same vectors; with the web endpoint and DNS both failing, a
previously resolved ID resolves with `stale: true`; with a valid fresh answer the stale one is
never returned.

### E29-T2 — Relay: cache sender key resolution

`apps/api/internal/relay/server.go`.

- [ ] `resolveIdentityPublicKey` uses an `idpkg.Cache` (5 min) and a file `Store` under
      `POWEUR_DATA/cache/identities/`, with `MaxStale = RELAY_STALE_IDENTITY_MAX` (default
      `168h`; `0` disables).
- [ ] `acceptSessionProof` and every other resolve on the message path go through the same
      function.
- [ ] Bounded size: LRU or a cap on entries (`RELAY_IDENTITY_STORE_MAX`, default 100k) with
      eviction of the oldest `fetched_at`, so spoofed sender names can't fill the disk. Store only
      after the message signature verified against the resolved key.
- [ ] Metric and event: `identity.resolve` with `source=fresh|cache|stale|failed`.
- [ ] Config documented in `apps/docs/docs/relay/configuration.md`.
- [ ] Unit tests: stale used on resolver failure, not used past max age, not stored before
      signature verification.

**Acceptance:** an integration test where the sender's relay goes down after one successful
message: a second identity-signed message (from the outbox) is still accepted by the recipient
relay within the stale window. Relay patch bump.

### E29-T3 — CLI: send from cache; queue instead of failing on resolve errors

`apps/cli`.

- [ ] `identity.ResolveIdentity` uses a file store at `~/.poweur/cache/identities/`
      (`MaxStale` 30 days, `--max-stale` and config `max_stale` to change it).
- [ ] Send path: if the recipient can't be resolved fresh but a stale document exists, encrypt
      to its `encryption_public_key`, print `using cached identity for <id> (fetched <ago>)` to
      stderr, and continue.
- [ ] If neither fresh nor stored resolution works **for an accepted contact**, the send
      can't encrypt, so it fails with a clear message (`no cached identity for …; try again
      when it resolves`). It is not queued as plaintext.
- [ ] Outbox: a transport failure on the resolved relay URL queues (as today); retry
      re-resolves with the store, so a stale document still produces a target.
- [ ] Inbox: verifying a received message's sender uses the store too (pinned contacts first,
      as today).
- [ ] `poweur resolve <id>` prints `source: web|dns|both|stale (fetched …)`.
- [ ] Unit tests for each path; integration covered in T16.

**Acceptance:** with fake DNS failing for `bob`'s name, `poweur send bob …` to a contact resolved
last week encrypts and queues; after DNS returns, `poweur outbox retry` delivers it. CLI patch bump.

### E29-T4 — SDK + web: persistent identity cache and contacts snapshot

`packages/client-ts`, `apps/web`.

- [ ] `PoweurClient` accepts a `documentStore`. The browser entry point defaults to
      `IndexedDbDocumentStore`, Node to the file store.
- [ ] `Messaging` send path matches T3: stale documents allowed, with a `stale` flag on the
      result.
- [ ] The web app keeps a local copy of `contacts.json` and `inbox-policy.json` per identity
      (check what `lib/snapshot.ts` already holds and extend it rather than adding a second
      cache), refreshed whenever the drive copy is read or written. Offline, the contacts screen
      shows the local copy read-only, with a "last synced" line.
- [ ] `lib/outbox.js`: retry re-resolves through the store. Entries survive reloads (already
      `localStorage`); add `relay_set` and the index reached once T12 lands.
- [ ] Unit tests (Vitest) for the store fallbacks and the snapshot.

**Acceptance:** in a Playwright run with every network request to the contact's host blocked, a
user who has messaged that contact before can compose and queue a message, which delivers when
the block is lifted. SDK and web patch bumps.

### E29-T5 — Web: installed app opens offline

`apps/web/public/sw.js`, build config.

- [ ] Precache the app shell at install: `index.html` and the hashed assets from the Vite
      manifest. Inject the asset list at build time; the service worker stays plain JS.
- [ ] Navigation requests under `/id/`: network first with a 3 s timeout, then the cached shell.
      Hashed assets: cache first. **Never cache API responses**, `/.well-known/*`, or anything
      with credentials.
- [ ] Clean up old caches on `activate`, keyed by build version. Keep the existing `/app/`
      retirement and push handlers unchanged.
- [ ] Booting without the relay: the vault unlocks from `localStorage` (verify the passkey PRF
      path works offline on Chrome and Safari), and screens render from local state (T4)
      instead of spinners.
- [ ] Unit test the routing function (which request goes to which strategy); Playwright with
      `context.setOffline(true)` after install.

**Acceptance:** an installed identity app opens, unlocks and shows cached contacts and history
with the relay unreachable. Web patch bump.

### E29-T6 — Connectivity state in every client

One shared vocabulary so users understand what's happening:

| State | Meaning | Web | CLI |
|---|---|---|---|
| `online` | home relay reachable | nothing shown | — |
| `home_unreachable` | home down, a mailbox reachable | thin banner: *Your relay is unreachable. Messages still arrive through your backup.* | `poweur status` line |
| `degraded` | no relay reachable, cached data only | banner: *Offline. Showing saved messages; new ones are queued.* | `poweur status` line; `send` prints *queued* |
| `stale_contact` | a contact is shown from cache | small *saved* marker on the contact | `using cached identity` |

- [ ] SDK: `client.connectivity()` with events, derived from poll outcomes (no extra pings).
- [ ] Web: banner component and per-contact marker. Copy reviewed for plain language.
- [ ] CLI: `poweur status` shows each relay in the identity's set with its last poll result.
- [ ] Tests: SDK state transitions; web component test.

**Acceptance:** the states appear in the T16 journeys at the right moments. SDK, web and CLI
patch bumps.

### E29-T7 — Spec: relay set, mailbox role, delivery and polling rules, threat model

Docs only; gate for T8–T14.

- [ ] New page `apps/docs/docs/protocol/relay-sets.md`: D2–D5 as normative text (MUST/SHOULD),
      with the canonical relay-set string and the verification order.
- [ ] `routing.md`: "Default: Direct Send" now walks the relay set, with a failover diagram.
- [ ] `identity-model.md` and `web-identity.md`: `relay_set` and why it's outside the canonical
      bytes.
- [ ] `conventions/pcp-0002-identity-document.md`: list `relay_set` (optional, separately signed).
- [ ] Threat model in `apps/docs/docs/security/model.md`:
  - a malicious mailbox (sees metadata and the contact list; can drop mail but not forge it);
  - a malicious home relay rewriting the set (can't: signature);
  - replay of an old set (`seq`);
  - a mailbox used as an open forwarder (refused by the locality rule);
  - storage abuse (quotas, expiry, the registration gate);
  - policy bypass by failover (no failover on `4xx`).
- [ ] Settle decisions A–E above; record the outcomes in this epic.

**Acceptance:** spec reviewed; the vector format for T8 agreed.

### E29-T8 — `RelaySet` statement, vectors, embedding in `id.json`

`packages/identity`, `packages/client-ts`.

- [ ] Go: `RelaySet`, `RelayEntry{URL, Role}`, `CanonicalBytes()` (prefix plus sorted-key JSON
      via `marshalCanonical`), `Sign`, `Verify(doc IdentityDocument, at time.Time)` with every
      D2 rule, and `EffectiveRelaySet(doc)` (falls back to the implicit home-only set).
- [ ] `IdentityDocument.RelaySet *RelaySet` with `json:"relay_set,omitempty"`, **excluded from
      `CanonicalBytes`**. A test asserts that adding or changing `relay_set` leaves the
      document's canonical bytes unchanged.
- [ ] `ParseDocument(…, verify=true)` also verifies `relay_set` if present. **An invalid set is
      dropped (logged), not fatal:** the document is still good, and failing it would let anyone
      who can write the field break resolution. Callers see `EffectiveRelaySet`.
- [ ] The T1 store keeps the highest-`seq` valid set per identity (rollback guard).
- [ ] TS twins in `document.ts` and a new `relayset.ts`; exported from `index.ts`.
- [ ] Vectors in `packages/identity/testdata/relay-set.json`: valid, bad signature, wrong
      identity, signed by a previous key in grace, signed by a previous key past grace, no home,
      two homes, home host ≠ `relay`, too many entries, `http` URL, lower `seq`. Both languages
      run them (`vectors_test.go`, SDK test).
- [ ] Compatibility test: a document with `relay_set` verifies under the current release's Go
      and TS verifiers. Use a fixture signed by the old code path.

**Acceptance:** vectors pass in both languages; old verifiers accept documents carrying the
field. No package bump needed for the identity package alone (not shipped separately); the SDK
bump is counted in T12/T13.

### E29-T9 — Relay: publish and update the relay set

`apps/api`.

- [ ] `PUT /identities/{id}/relay-set`, a signed request (identity key). Body is a `RelaySet`.
      The relay verifies it (T8), checks that `seq` is higher than the stored one, splices it
      into the stored raw `DocumentJSON` **without touching any other byte of the document's
      signed fields**, and serves it from `/.well-known/poweur/id.json`.
- [ ] Registration and rotation (`POST /identities`, rotation endpoints) accept an optional
      `relay_set` beside the document and keep the stored set across rotations. A rotated key
      must re-sign the set: the relay rejects a stored set the new key can't verify and tells the
      client to re-sign.
- [ ] `GET /health` gains a `mailbox` block: `hosting` (`off|open|allowlist`) and
      `recommended_mailbox` (URL or absent).
- [ ] Export (`identity_ops.go`) includes the set (it's inside `id.json`, so already there; add
      a test).
- [ ] Unit tests: splice keeps the document signature valid; lower `seq` rejected; wrong key
      rejected.

**Acceptance:** after `PUT`, a fresh resolve returns a document whose `relay_set` verifies, and
whose document signature still verifies. Relay patch bump.

### E29-T10 — Relay: mailbox hosting (registration, storage, quotas, expiry)

`apps/api`, per D3 and D5.

- [ ] Config: `MAILBOX_HOSTING`, `MAILBOX_ALLOWLIST` (identity globs or relay hosts),
      `MAILBOX_MAX` (registrations), `MAILBOX_MAX_BYTES` (per mailbox), `MAILBOX_RETENTION`
      (default `336h`), `MAILBOX_EXPIRY` (default `720h`), `RELAY_MODE=full|mailbox`.
- [ ] Storage: a mailbox registration record (document bytes, policy, pins, blocked list,
      `issued_at`, `refreshed_at`) beside the identities store, reusing the inbox storage for
      messages so `GET /messages` works unchanged.
- [ ] `PUT /mailboxes/{id}`: a signed request (identity key, against the document in the body
      for a first registration, against the stored key afterwards, per D5). Rate limited per
      identity and per IP; uses the existing registration gate (PoW) for first registrations on
      `open` relays.
- [ ] The document must carry a relay set listing **this** relay as `mailbox`. Otherwise
      `409 not_in_relay_set`, so a relay never holds mail senders won't send it.
- [ ] `DELETE /mailboxes/{id}`: a signed request; deletes the registration and its stored mail.
- [ ] `GET /mailboxes/{id}/document`: public, the stored document verbatim.
- [ ] Locality: `isLocalIdentity` gains a recipient-only variant. `handleMessagesPost` treats a
      mailbox identity as local when it is the recipient; sender-local stays hosted-only.
- [ ] Every other identity-scoped route returns `404 not_hosted` for mailbox identities
      (table-driven test over the route list in `server.go`).
- [ ] Retention sweep: delete stored messages past `MAILBOX_RETENTION`. Expiry sweep: delete
      registrations not refreshed within `MAILBOX_EXPIRY`.
- [ ] `RELAY_MODE=mailbox`: identity registration, the web app, the identity page and the drive
      are disabled; `/health` says so (`mode: mailbox`).
- [ ] CORS: mailbox endpoints and `/messages`/`/sessions`/`/acks` allow the web app origins the
      home relay already allows (browser clients poll mailboxes cross-origin).
- [ ] Metrics: registrations, bytes, sweeps, rejects by reason.

**Acceptance:** unit tests for every rule above; a mailbox-only relay in an integration test
accepts a registration, stores inbound mail, serves it to the owner, and refuses everything else.
Relay patch bump.

### E29-T11 — Relay: verifying senders and enforcing policy as a mailbox

`apps/api`, per D5.

- [ ] `resolveSigningKey` for a recipient-mailbox message: local store → fresh resolve →
      **owner's pin for the sender** → stale store (T2) → `503 sender_unresolvable` with
      `Retry-After: 300`.
- [ ] Session proofs from unresolvable senders follow the same order.
- [ ] `evaluateInboxPolicy` reads policy, pins and blocked lists from the mailbox registration
      instead of the owner's drive. `contacts_only` and `contacts_and_requests` behave as on the
      home relay. A request from a stranger needs a fresh or stale resolution: pins don't cover
      strangers.
- [ ] Anonymous ingress (`handleAnonMessage`) to a mailbox identity → `404 not_hosted`.
- [ ] A pinned contact whose fresh resolution returns a **different** key than the pin: reject
      with `409 key_changed` (same as clients do); never deliver against the pin when a fresh
      answer disagrees.
- [ ] Unit tests: each branch of the order; policy parity with the home relay (table-driven over
      the existing policy tests).

**Acceptance:** with all of `*.poweur.net` unresolvable in fake DNS, a mailbox accepts mail from
the owner's pinned contacts, returns `503 sender_unresolvable` for unknown senders, and accepts
strangers again once DNS answers. Relay patch bump.

### E29-T12 — Senders: fail over across the recipient's relay set

`apps/cli`, `packages/client-ts`, `apps/web`, per D4.

- [ ] CLI: `resolveRecipientRelayURL` becomes `resolveRecipientRelays` (ordered list from
      `EffectiveRelaySet`). Send tries each per the D4 rules and records which one accepted
      (`tick 1 ✓ via mailbox mbox.…`).
- [ ] CLI outbox: `outboxEntry` gains `relay_set` and `relay_index`. Retry re-resolves (stale
      allowed) and walks the list from the top. Old entries with only `target_relay` still work.
- [ ] `--via-home-relay`: unchanged; the home relay itself forwards using the same failover
      (relay forward path `forwardMessage` / `resolveRelayHost` updated to read the recipient's
      relay set).
- [ ] Acks: `emitMessageAck` targets the original sender's relay set with the same failover.
- [ ] SDK: `Messaging.send` and ack sending do the same; `SendResult.relay` reports the one that
      accepted.
- [ ] Web: outbox entries carry the set; the message bubble's tick tooltip says *delivered to
      backup* when it went to a mailbox.
- [ ] Unit tests: order, the 4xx-no-failover rule, `503 sender_unresolvable` stays queued,
      `429` with a short reset retries the same relay.

**Acceptance:** with the recipient's home relay down, CLI, SDK and web senders all deliver to the
mailbox; with the home relay returning `403 policy_rejected`, none of them try the mailbox. CLI,
SDK, web and relay patch bumps.

### E29-T13 — Recipients: poll every relay, merge, dedup

`apps/cli`, `packages/client-ts`, `apps/web`, per D4.

- [ ] CLI `poweur inbox`: polls home, then mailboxes (all of them on `--all`, otherwise per the
      cadence in D4), each with its own session file
      (`~/.poweur/sessions/<identity>@<relay-host>.toml`). Merge by id, consume per relay.
- [ ] Acks received at a mailbox advance the local journal like acks from home.
- [ ] SDK: `Inbox.poll()` returns merged results with `relay` per message; sessions per relay in
      the session manager.
- [ ] Web: polling loop includes mailboxes per D4. Messages arrive in the same threads; a
      message delivered twice shows once.
- [ ] Histories: the archive record keeps the first relay it came from (debug field only).
- [ ] Unit tests: merge and dedup across relays, a mailbox failure not blocking home results,
      per-relay session refresh on `401 session_expired`.

**Acceptance:** a message sent to the mailbox while home was down appears once in CLI, SDK and
web inboxes and is consumed from the mailbox; the same message also delivered to home (manual
replay) still appears once. CLI, SDK and web patch bumps.

### E29-T14 — Managing backup mailboxes (CLI, SDK, web settings)

- [ ] CLI:
  - `poweur mailbox add <relay-url>`: checks the mailbox's `/health`, signs a new relay set (`seq+1`),
    `PUT`s it to home, and registers at the mailbox;
  - `poweur mailbox ls`: set, last refresh, last poll;
  - `poweur mailbox rm <relay-url>`: new set without it, `DELETE` at the mailbox;
  - `poweur mailbox refresh`.
- [ ] Automatic refresh (D5 triggers): contact accepted or blocked, pin changed, policy changed,
      key rotated, and weekly. The CLI does it on any command that touches those files, the SDK
      exposes `mailboxes.refresh()`, and the web app calls it after those actions and on
      open if older than 7 days.
- [ ] Order of operations when adding: register at the mailbox **first** (it accepts a document
      whose set already lists it, sent in the same request), then publish the set at home. Mail
      never goes to a mailbox that doesn't know the ID.
- [ ] Order when removing: publish the set without it first, keep polling the old mailbox until
      its retention lapses, then `DELETE`.
- [ ] Web Settings → *Backup mailbox*: on/off, which relay, last checked, and a plain sentence:
      *Your backup relay can see who writes to you and your contact list, never what they write.*
- [ ] Multi-device: any device can refresh; the newest `issued_at` wins at the mailbox.
- [ ] Unit tests for ordering and triggers.

**Acceptance:** add, use, refresh and remove a mailbox from each of CLI and web in T16 journeys.
CLI, SDK and web patch bumps.

### E29-T15 — Hosted default: a backup mailbox for every hosted ID

`deploy`, `apps/api`, `apps/web`, per D6.

- [ ] Ops: choose the backup domain, registrar, DNS host and provider (see *Open questions*);
      Ansible role for a `RELAY_MODE=mailbox` instance with its own TLS, backups, monitoring and
      alerting; the runbook in `deploy/README.md`.
- [ ] poweur.net `RECOMMENDED_MAILBOX` set to it; the backup's `MAILBOX_ALLOWLIST` is limited to
      `*.poweur.net` IDs.
- [ ] Onboarding (web, CLI `identity create`): when the home relay's `/health` has `recommended_mailbox`,
      register it and sign the set, unless `--no-mailbox` or the onboarding checkbox is off.
- [ ] Backfill for existing hosted IDs: the web app and CLI offer it once (*Turn on backup
      delivery?*), with the same copy as T14. Never done silently, since it shares the contact
      list with a new host.
- [ ] Capacity estimate (registrations × retention × average inbound) written into the runbook.

**Acceptance:** a new hosted ID created on staging has a two-entry relay set and a live mailbox
registration; stopping the staging home relay still delivers mail between two staging IDs. Relay,
web and CLI patch bumps as applicable.

### E29-T16 — Integration and Playwright journeys

`apps/integration` (real relays, fake DNS); Playwright for web.

- [ ] `RESIL_01` stale contact: resolve once, break DNS for the recipient's name, send → queued
      or delivered from cache; restore → delivered.
- [ ] `RESIL_02` relay sender cache: sender's relay down after the first message; the outbox
      retry is still accepted by the recipient relay.
- [ ] `RESIL_03` home down, mailbox up: deliver via mailbox; the recipient reads it; ack flows
      back.
- [ ] `RESIL_04` poweur.net-style outage: a whole fake-DNS zone fails; the mailbox accepts pinned
      contacts and returns 503 for others; recovery delivers the rest.
- [ ] `RESIL_05` no policy bypass: home `403` → no mailbox attempt.
- [ ] `RESIL_06` dedup: same message at home and mailbox → one inbox entry.
- [ ] `RESIL_07` rotation while home is down: the client re-registers its new document at the
      mailbox with the stored key's signature; old pins in contacts follow `previous_keys`.
- [ ] `RESIL_08` rollback: the relay serves an older document or set → rejected, stored copy
      kept.
- [ ] `RESIL_09` expiry and retention sweeps.
- [ ] `RESIL_10` old-client compatibility: the current release's CLI build verifies and messages
      an ID whose document carries `relay_set`.
- [ ] Playwright: offline app shell (T5), queued send while the contact host is blocked (T4),
      the connectivity banner (T6), mailbox settings (T14).

**Acceptance:** all journeys green in CI.

### E29-T17 — Docs

- [ ] User docs: *What happens when a relay is down* (plain language, hosted and own-domain).
- [ ] Relay docs: running a mailbox-only relay; every new config key in `configuration.md`.
- [ ] CLI reference: `mailbox`, `--max-stale`, `status`.
- [ ] SDK docs: `documentStore`, `connectivity()`, `mailboxes`.

## Open questions

- **Push from a mailbox.** A Web Push subscription is bound to one VAPID key, and a browser
  service worker registration holds one subscription, so a mailbox with its own VAPID key can't
  wake the installed web app. Options: the operator shares its VAPID key with its own backup
  (simple for the hosted default, but the VAPID private key then lives on two hosts), or mailbox mail is
  only seen on the next open or poll. Native push (EPIC-019) has the same question with
  APNs/FCM credentials. **Proposal:** share the key for the operator's own backup only; others
  get poll-only. Decide in T7.
- **Which domain and provider for the hosted backup** (T15). It must not share the registrar,
  DNS host, CDN account or cloud provider with poweur.net, or it fails with it.
- **Groups (EPIC-024).** A group ID's relay set is the group owner's to sign; group fan-out
  (`POST /groups/{group}/messages`) stays on the home relay in this epic. Should members'
  per-member deliveries fail over? Probably yes, through T12 unchanged; confirm in T7.
- **Email bridge (EPIC-023).** Inbound email arrives at the home relay only; no change here.
- **Should the relay set also be published in DNS** (a `_poweur-relays` TXT pointer) for
  resolvers that only get DNS? Probably not: unsigned DNS can't carry a signed set compactly,
  and DNS isn't the failure we're covering. Leave out unless T7 finds a reason.

## Version bumps

Per AGENTS.md, each task bumps the patch version of every package whose shipped behaviour it
changes: relay (`internal/buildinfo.Version` in `apps/api`), CLI (`apps/cli/internal/buildinfo`),
SDK (`packages/client-ts/package.json`), web (`apps/web/package.json`). Docs-only (T7, T17) and
test-only (T16) changes keep versions.
