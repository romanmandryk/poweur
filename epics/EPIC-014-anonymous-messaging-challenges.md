# EPIC-014 — Anonymous messaging & sender challenges (proof-of-work)

- **Status:** core complete (T1–T3 + CLI + registration gate shipped; web page + stranger-challenge seam open)
- **Priority:** P2 (after EPIC-007 lands the inbox-policy surface it extends)
- **Depends on:** EPIC-007 (inbox policy + requests queue), EPIC-006 (policy schema);
  feeds EPIC-012 (web contact forms), EPIC-002 (deferred PoW registration gate)
- **Unlocks:** contact forms and open inboxes without opening the spam floodgates; a
  reusable PoW primitive for every "stranger wants in" surface

## Progress

| Task | Status | Notes |
|------|--------|-------|
| E14-T1 Spec | **done** | [`apps/docs/docs/trust/anonymous-and-challenges.md`](../apps/docs/docs/trust/anonymous-and-challenges.md) + PCP-0006; measured difficulty table committed (native: 16 bits ≈ 40 ms, 20 ≈ 0.6 s, 24 ≈ 9 s); coordinated with E07's schema (the `anonymous` block extends `inbox-policy.schema.json`) |
| E14-T2 PoW primitive | **done** | `packages/identity/pow.go` (HMAC-sealed stateless tokens, purpose-bound, bit dial clamped [8, 30], cancellable solve, benchmark) + the JS solver, since moved into `packages/client-ts/src/pow.ts` by E15-T6 (was `apps/web/js/pow.js`) |
| E14-T3 Relay enforcement | **done** | Unsigned envelopes → opt-in check → 428 challenge → verified single-use solution → dedicated anon queue (`GET /anon/{identity}`); encrypt-only holds (ephemeral keys); size/daily caps + per-IP rate limit; load auto-raises the difficulty floor (+4 bits over 120 challenges/min); `verified`/`payment` return typed envelopes with 501 on attempts. Full rejection-path matrix in `anon_test.go`. **Open:** `stranger_challenge` gate for identified non-contacts (seam specified; lands with E07-T5) |
| E14-T4 Client UX | **partial** | CLI shipped: `poweur send --anon` (auto-solve with progress), `poweur anon` (decrypting drain with ANONYMOUS marker + trust warning), `poweur policy set --anon-*`; `TestINT_ANON_01` end to end. **Deferred (EPIC-012):** web anon-send page + difficulty slider + anon-queue view |
| E14-T5 Second consumers | **done** (E07 hook open) | `REGISTRATION_GATE=pow` + `REGISTRATION_POW_BITS` + `GET /auth/pow` + CLI auto-solve (`TestINT_ANON_02`) — **closes the EPIC-002 deferral**; PCP-0006 filed; E07-T5 contact-request PoW remains with EPIC-007 |

## Goal

Let a recipient *opt in* to receiving messages from senders **without a Poweur ID
signature** (anonymous web visitors, contact forms, one-off tips) — default stays **deny**
— and put a recipient-chosen **cost ladder** in front of any non-contact sender, anonymous
or identified: nothing / **proof-of-work with recipient-tunable difficulty** / verified
account / payment. This epic drives the PoW implementation end to end; verified-account
and payment land as designed slots with stub enforcement.

## Background (current code & related epics)

- Today the relay **requires** a valid signature on every message (`handleMessagesPost`
  verifies sender sig; anonymous ingress does not exist at all). The only anti-abuse tools
  are rate limits (`apps/api/internal/ratelimit/`).
- **EPIC-007** defines `poweur-sys/relay/inbox-policy.json` (`contacts_only` |
  `contacts_and_requests` | `open`) and the requests queue for *identified* strangers —
  this epic extends that same policy file rather than adding a second policy surface.
  E07-T5 already wishes for "proof-of-work on contact requests from unknown relays": it
  consumes the primitive built here.
- **EPIC-012** (identity websites) needs anonymous contact-form ingress and explicitly
  deferred the policy vocabulary to EPIC-007/here.
- **EPIC-002** deferred a PoW gate on hosted registration — same primitive, second
  consumer.
- A challenge store already exists (`apps/api/internal/storage/challenges.go`, used for
  auth); PoW challenges follow the same issue-verify-expire pattern.

## Design direction

- **Anonymous ≠ unaccountable-free-for-all.** Anon messages are opt-in per recipient,
  land in a **separate anon queue** (never the main inbox), are size-capped harder, and
  carry no sender identity — the client UX must render them visibly differently.
- **One policy surface.** Extend `inbox-policy.json` (E07-T1 schema) with:

```json
{
  "mode": "contacts_and_requests",
  "anonymous": {
    "allow": true,
    "challenge": "pow",            // "none" | "pow" | "verified" | "payment"
    "pow_bits": 20,                 // recipient-tuned difficulty
    "max_bytes": 4096,
    "max_per_day": 20
  },
  "stranger_challenge": "pow",     // optional extra cost for identified non-contacts
  "stranger_pow_bits": 12
}
```

- **PoW scheme — Hashcash-style, tunable in single bits:** challenge is
  `sha256(challenge_token || solution_nonce)` needing `pow_bits` leading zero bits.
  Bits (not hex digits) give a smooth ×2-per-step dial: 12 bits ≈ milliseconds,
  20 bits ≈ ~1 s, 26 bits ≈ ~1 min on a laptop (publish a measured table; JS/WebCrypto
  is ~5–10× slower than native — cap effective bits so mobile browsers stay usable).
  The relay issues the challenge **stateless** (HMAC-sealed token carrying difficulty +
  recipient + expiry, keyed by a relay secret) so no store grows under flood; solved
  tokens are single-use (small seen-cache until expiry).
- **Flow:** unsigned `POST /messages` → `428 challenge_required` with
  `{type:"pow", token, bits, algo:"sha256-lead0", expires_at}` → sender retries with
  `{challenge_token, solution}` attached → relay verifies in O(1), enforces policy caps,
  stores to the anon queue. Identified strangers get the same dance when
  `stranger_challenge` is set — *before* their contact request is queued.
- **Difficulty is the recipient's dial, floor is the relay's.** Relay config sets a
  minimum/maximum bits window and may **auto-raise the floor under load** (global anon
  QPS threshold) — the Hashcash answer to distributed flooding.
- **Verified account / payment are policy slots, not v1 features:** `verified` = sender
  presents a third-party attestation over their ID (design the attestation format —
  who signs what — implementation deferred); `payment` = HTTP 402 with a payment
  descriptor (format reserved, deferred). Both reuse the same challenge envelope so
  clients already know how to render "this recipient requires X".

## Tasks

### E14-T1 — Spec: anon ingress, challenge protocol & policy vocabulary

- [x] `apps/docs/docs/trust/anonymous-and-challenges.md`: the policy JSON above (as an
      extension to E07-T1's schema), the 428 challenge envelope, PoW algorithm + bit
      difficulty table (measured native + browser), single-use + expiry semantics,
      anon-queue semantics (separate from inbox and requests; no acks; hard caps)
- [x] Define the challenge envelope generically (`pow` | `verified` | `payment`) so
      clients render unknown-but-well-formed challenge types gracefully
- [x] Threat analysis: pre-computation (token binds recipient + expiry), replay
      (single-use cache), GPU asymmetry (honest about it — PoW rate-limits, it doesn't
      authenticate), privacy of anon senders (relay sees IP; document what is logged —
      counters only per EPIC-013 rules)
- [x] Coordinate with E07-T1 so both land in one schema (the `anonymous` block extends
      the shipped `inbox-policy.schema.json`)

**Acceptance:** spec merged; difficulty table backed by a committed benchmark.

### E14-T2 — PoW primitive (shared package, both sides)

- [x] `packages/identity/pow.go` (shared so relay verifies and CLI/web solve):
      `NewChallenge(secret, recipient, bits, ttl)` → sealed token;
      `Solve(token, bits)` (with context cancel); `Verify(secret, token, solution)`;
      constant-time, allocation-light verify
- [x] Difficulty in bits with a fixed window ([8, 30], constants in the package);
      benchmark test emitting the bits→duration table (feeds the spec)
- [x] JS solver for the web app (now `packages/client-ts/src/pow.ts`; was `apps/web/js/pow.js` until E15-T6 — WebCrypto sha256, chunked so the
      UI stays responsive; Worker deferred with the web UI pass)
- [x] Table-driven tests: solve/verify roundtrip across bit range, tamper (wrong
      recipient/bits/expiry), expiry, wrong-secret rejection

**Acceptance:** Go and JS solvers both satisfy a 20-bit challenge the Go verifier
accepts; benchmarks committed.

### E14-T3 — Relay enforcement: anon queue + challenge gate

- [x] `handleMessagesPost`: unsigned messages allowed **only** when the recipient's
      policy says so → 428 challenge → verified retry lands in the **anon queue**
      (parallel to E07's requests queue; reuse its storage pattern), with
      `anonymous.max_bytes` / `max_per_day` enforced and single-use solution cache
- [ ] `stranger_challenge`: same gate wired in front of E07-T2's contact-request path
      for identified non-contacts — **open** (seam specified in the spec; lands with
      E07-T5's abuse-pressure pass)
- [x] Global anon load shedding: relay-wide anon QPS threshold auto-raises the
      effective bits floor (log + metric when it kicks in — EPIC-013 counters:
      `message_rejections_total{reason=anon}`, `pow_challenges_total{result}`)
- [x] `verified` / `payment` modes: policy accepted, enforcement returns the typed
      challenge envelope with `501 not_implemented` semantics documented (clients can
      explain "recipient requires payment — not yet supported")
- [x] Integration tests: default-deny; opt-in without challenge; PoW happy path;
      wrong/expired/replayed solution; per-day cap; queue isolation (anon messages
      never appear in the signed inbox); difficulty floor under simulated flood

**Acceptance:** anon message with valid PoW reaches the anon queue of an opted-in
recipient and nowhere else; every rejection path tested; E02's registration-gate PoW
deferral gets a note pointing here once the primitive exists.

### E14-T4 — Client UX (CLI + web)

- [x] CLI: `poweur send --anon <recipient> <msg>` (solves the challenge, shows
      difficulty/ETA, respects context cancel); `poweur inbox --anon` to read the anon
      queue; `poweur policy` subcommand to view/set the anonymous block of
      `inbox-policy.json` (write via DAV like `share`/`dav password`)
- [ ] **Deferred (EPIC-012):** web app anon-queue view (visually distinct, no reply
      affordance); settings panel for the policy block with a difficulty slider showing
      human terms ("~1 s on a laptop, ~10 s on a phone")
- [ ] **Partial:** anonymous *sending* page ties into EPIC-012's contact form — the JS
      solver + form snippet EPIC-012 can embed (that epic owns the website shape)

**Acceptance:** two-browser demo: recipient enables anon+PoW, visitor sends without any
identity, message appears in the anon queue; slider changes measurably change solve time.

### E14-T5 — Second consumers of the primitive (follow-through)

- [x] EPIC-002 deferred item: optional PoW on hosted registration
      (`REGISTRATION_GATE=pow`, bits from config) using the same package — closes that
      deferral
- [ ] E07-T5 hook: PoW on contact requests from unknown relays — **open** (design note + seam;
      implement with EPIC-007 if it has landed)
- [x] Registry entries (EPIC-006 PCP) for the challenge envelope + policy fields
      (PCP-0006)

**Acceptance:** `REGISTRATION_GATE=pow` works end to end in the integration suite.

## Non-goals

- PoW as sybil-proof identity (it is a rate limiter; the contacts model from EPIC-007
  remains the actual spam answer for identified senders).
- Payment rails and attestation issuers — formats reserved here, built when a real
  consumer exists.
- Memory-hard PoW (argon2/equihash): sha256 keeps JS/native gap acceptable and the
  verifier trivial; revisit only if GPU farming becomes an observed problem. **Now
  scheduled** — the GPU-vs-mobile asymmetry (~10⁴× on sha256) is picked up in
  [EPIC-016](EPIC-016-pow-v2-and-pay-to-send.md), which makes the algorithm pluggable and
  adds a memory-hard default plus a pay-to-send fast-lane.
- Payment rails: the `payment` slot is reserved here (501); real settlement (x402, L402,
  Stripe/Revolut/SEPA) and the recipient-priced fee land in EPIC-016.
