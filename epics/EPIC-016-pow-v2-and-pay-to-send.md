# EPIC-016 — Sender-challenge v2: pluggable, memory-hard PoW & pay-to-send

- **Status:** proposed
- **Priority:** P2 (hardening + pay-to-send pass over the EPIC-014 challenge ladder; not
  blocking, but the PoW fairness bug below is a correctness issue once anon ingress sees real
  spam)
- **Depends on:** EPIC-014 (challenge ladder, `inbox-policy.json` `anonymous` block, the
  `verified`/`payment` reserved slots, the stateless HMAC-sealed token), EPIC-007 (inbox
  policy surface), EPIC-006 (PCP registry + schema); consumes/extends INT-002
  (`payments.json` handles, LNURL/x402 relay endpoints), coordinates with EPIC-013 (metrics)
- **Unlocks:** anon/stranger ingress that a phone can actually satisfy without a GPU farm
  out-spamming it 10⁴×; a recipient-priced **pay-to-send** fast-lane (crypto + fiat) that
  lets recipients price their own attention, without taxing self-hosters
- **Shared gateway:** the payment gateway, settlement callback and cut accounting built here are reusable for other paid grants and subscriptions — one gateway, not two. An active paid entitlement may lower (never bypass abuse controls on) registration/anon PoW difficulty.

## Progress

| Task | Status | Notes |
|------|--------|-------|
| E16-T1 Algorithm abstraction (pluggable `algo`, one difficulty dial) | **open** | envelope already carries `algo`; generalize policy + solver/verifier registry |
| E16-T2 Memory-**latency**-hard PoW (`argon2id` and/or `seqmem`) | **open** | the actual fix for the 10⁴× mobile-vs-GPU gap; see algorithm note |
| E16-T3 Payment challenge protocol (`payment` slot → real) | **open** | recipient-priced, provider-agnostic descriptor + settlement callback |
| E16-T4 Provider adapters: x402 (crypto) + L402 (Lightning) | **open** | HTTP-402-native rails; deepest fit, reuses INT-002 endpoints |
| E16-T5 Provider adapters: Stripe / Revolut / SEPA (fiat) | **open** | hosted-gateway rails; where Poweur's cut lives |
| E16-T6 Fee/cut model (hosted cut, self-host = no cut) + accounting | **open** | cut applies only when Poweur operates the gateway |
| E16-T7 Tiered ladder: partial payment reduces PoW difficulty | **open** | the "pay half, solve ~1 s instead of ~5 s" middle tier |
| E16-T8 Client UX (CLI + web): pick-your-cost, pay button, receipts | **open** | sender chooses PoW **or** pay; recipient sets price in policy |

## Goal

Two coupled upgrades to the EPIC-014 sender-challenge ladder:

1. **Make the proof-of-work fair across devices and swappable without a client migration.**
   Today's `sha256-lead0` Hashcash gives a GPU/ASIC-class miner a multiple-orders-of-magnitude
   edge over the honest mobile browser it is supposed to gate — so the difficulty that stops a
   spammer is a difficulty that bricks a phone. Move to a **memory-latency-hard** puzzle, and
   make the algorithm a **relay-side pluggable choice** behind a single recipient-facing
   "difficulty" dial, so we can change algorithms and parameters later with zero policy-file or
   client churn.

2. **Turn the reserved `payment` slot into a real pay-to-send fast-lane.** Let an anonymous or
   stranger sender **choose**: solve the PoW, or **pay a recipient-set fee** to send instantly
   (or at reduced difficulty). Price is the recipient's dial. On **hosted** Poweur IDs, where
   Poweur operates the payment gateway, Poweur takes a configurable cut; when a user **self-hosts
   and brings their own gateway/wallet**, settlement never touches Poweur infrastructure and there
   is no cut. Support crypto-native rails (x402 stablecoin, L402/Lightning) and conventional fiat
   (Stripe, Revolut, SEPA) behind one provider-agnostic descriptor.

Verified-account attestation (the third `verified` slot) is **out of scope** here — it stays a
reserved slot; this epic finishes PoW and payment only.

## Background (current code)

- **The PoW primitive** is `packages/identity/pow.go`: `sha256-lead0`, difficulty in leading-zero
  **bits** (`PowMinBits=8`, `PowMaxBits=30`, `PowDefaultBits=16`), stateless HMAC-sealed token
  `{purpose, bits, expires_at, nonce}`, `SolvePow`/`VerifyPowSolution`. JS twin in
  `packages/client-ts/src/pow.ts` (the web app consumes it; `apps/web/js/pow.js` is gone). Spec + measured table in
  `apps/docs/docs/trust/anonymous-and-challenges.md`; PCP-0006 registers the envelope.
- **The challenge envelope already carries `algo`** (`"sha256-lead0"`, `idpkg.PowAlgo`) and a
  typed `type` (`pow` | `verified` | `payment`) — the extension seam for a second algorithm and a
  real payment type is already on the wire (`apps/api/internal/relay/anon.go:writeChallengeRequired`).
- **The policy block** is `AnonymousPolicy` in `packages/identity/inboxpolicy.go`
  (`challenge`, `pow_bits`, `max_bytes`, `max_per_day`) + `conventions/schemas/poweur-sys/inbox-policy.schema.json`.
  `payment` is an accepted `challenge` value that today returns `501 not_implemented`
  (`anon.go:handleAnonMessage` default branch).
- **The enforcement path** (`anon.go`) is provider-agnostic in shape already: 428 →
  single-use nonce cache → daily cap → anon queue. Registration reuses the same primitive
  (`handleAuthPow`, `checkRegistrationPow`).
- **Payments groundwork** lives in **INT-002**: a `payments.json` convention (LNURL/Lightning
  address, x402/eth, Open Payments, Revolut/Wise/IBAN handles) and a planned relay
  `/.well-known/lnurlp/` endpoint. This epic is the **inbound** counterpart: INT-002 publishes
  *how alice gets paid in general*; E16 spends that to *gate a message on a payment to alice*.
- **No payment code exists yet.** There is no gateway integration, no ledger, no webhook handler.

### The bug this epic fixes (why `sha256-lead0` is not enough)

A prior review flagged that a mobile client can be **~10⁴× disadvantaged** versus a GPU VM
running the CLI solver. `sha256` is embarrassingly parallel and compute-bound — precisely the
workload GPUs and ASICs devour. So any bit-difficulty high enough to cost a spam farm real money
is a difficulty that makes a phone spin for minutes. Hashcash rate-limits a *single honest CPU*;
it does not rate-limit a *budgeted attacker*, and it punishes the weakest honest device most.

**The subtle part — do not just "switch to Argon2id" and call it fixed.** Argon2id (the natural
first suggestion, and a genuine improvement) is memory-**bandwidth**-hard: it streams a large
buffer, which is *also* what GPUs are built for. Field reports put the GPU edge at only ~10–20×
even at 32 MB — better than sha256's 10⁴×, but not closed, and rentable at scale. The stronger
answer is a memory-**latency**-hard, inherently **sequential** puzzle: each step's memory address
depends on the previous step's result (pointer-chasing over a small buffer), so a hash cannot be
parallelized *within itself*. A GPU's thousands of cores just stall waiting on each other's
dependent reads; a normal consumer CPU stays competitive with rented datacenter silicon. This is
the direction to target for the default; Argon2id is an acceptable, well-audited interim with
honest limits. (See `## Algorithm note` below and the Sources at the bottom.)

## Design direction

### A. Pluggable algorithm behind one difficulty dial (E16-T1)

The recipient must never have to reason about hash internals. Keep **one** human-facing dial and
let the relay own the algorithm and its parameters.

- **Policy stays abstract.** Replace the algo-specific `pow_bits` with a single
  `pow_difficulty` (integer level, e.g. 1–10, or better a **target solve-time band** the relay
  maps to params) plus an optional `pow_algo` override the recipient rarely sets. Keep `pow_bits`
  as a **deprecated alias** honored for old policy files (migration, not a break). The recipient's
  intent is "make a stranger spend about *this much* effort"; the relay decides how.
- **The envelope already speaks algo.** Generalize it to carry everything the solver needs and
  nothing it must guess:

  ```json
  {"error": "challenge_required",
   "challenge": {"type": "pow",
                 "algo": "argon2id",              // or "sha256-lead0" | "seqmem-v1"
                 "token": "…",
                 "params": {"m_kib": 65536, "t": 2, "p": 1, "target_bits": 20},
                 "expires_at": "…"}}
  ```

  Clients run whatever `algo`+`params` the envelope names; an unknown `algo` is rendered as
  "this recipient requires a challenge your client can't solve yet — update" (graceful, same
  posture as unknown `type`).
- **Registry of algorithms** in `packages/identity`: an interface (`PowAlgorithm` with
  `Params(difficulty) → params`, `Solve(ctx, token, params)`, `Verify(token, solution, params)`)
  with `sha256-lead0` as the first registrant and the memory-hard algo(s) as the second. The
  relay config picks the **default algo** and the difficulty→params mapping table; recipients set
  a level. Swapping the default later is a one-line relay change, no client or policy migration.
- **The token seals the params.** `PowChallenge` gains `Algo string` and `Params` so a solution
  is bound to the exact algorithm+cost it was minted for (no downgrade attack: a spammer cannot
  present a cheap-algo solution against an expensive-algo policy).

### B. Memory-hard, GPU-resistant PoW (E16-T2)

- Ship **at least one memory-hard algorithm** with a WASM/browser solver at parity with the Go
  solver (WASM lands within ~10% of native C, so honest browsers are not penalized relative to a
  native CLI — the whole point).
  - **`argon2id`** — RFC 9106, well-audited, `m_kib` 64–256 MB, `t=1–3`, `p=1`. The safe interim.
  - **`seqmem-v1`** (target default) — a memory-**latency**-hard sequential pointer-chase over a
    ~256–512 KiB buffer with causal hash binding, difficulty = chain length. Narrows the
    device gap the most; the verifier stays cheap (recompute or a checkpointed proof). Spec it as
    its own PCP with test vectors before implementing.
- **Difficulty → params mapping** is a committed, benchmarked table (like E14's bit table):
  target ~1–2 s on a mid-range phone for a normal message, more for suspected-bulk. **Benchmark
  on real targets** — mid-range Android, mobile Safari WASM, the Go CLI — before locking, and
  publish the table in the spec. WASM Argon2 varies more across browsers than native does; the
  dial must be conservative enough that mobile Safari is still usable.
- **Honesty in the spec, unchanged from E14:** memory-hardness *narrows* the asymmetry, it does
  not erase it. A funded spammer renting many small-RAM VMs still buys a linear multiplier. PoW is
  one layer; the caps (`max_per_day`, per-IP), the load floor, the contacts model, and the
  **paid fast-lane below** are the rest of defense-in-depth. Keep `sha256-lead0` registered for
  low-value/high-volume surfaces (e.g. registration) where the verifier's triviality matters more
  than device fairness — the pluggability makes that a per-surface choice.

### C. Pay-to-send: the `payment` slot becomes real (E16-T3)

Turn `challenge:"payment"` from a 501 into a working alternative to PoW. Provider-agnostic by
construction; concrete rails are adapters (D/E).

- **Policy** — the `anonymous` block (and later `stranger_*`) gains a `payment` sub-object:

  ```json
  {"anonymous": {"allow": true, "challenge": "pow_or_payment",
    "pow_difficulty": 5,
    "payment": {"amount": "0.02", "currency": "USD",
                "providers": ["x402", "lightning", "stripe"],
                "memo": "priority contact"}}}
  ```

  New `challenge` values: `payment` (pay only) and **`pow_or_payment`** (sender's choice — the
  key UX from the suggestion). Recipient sets **amount + currency + which rails they accept**;
  the accepted rails are the intersection of what the recipient published in `payments.json`
  (INT-002) and what the relay can settle.
- **Wire flow** — a payment-typed challenge envelope carries a **payment descriptor** (not a
  bespoke format — reuse the rails' own): for x402 the HTTP-402 `PAYMENT-REQUIRED`
  fields; for L402 the macaroon + Lightning invoice; for Stripe a Checkout/PaymentIntent
  client-reference. The relay answers `402 Payment Required` (finally the honest status code) with:

  ```json
  {"error": "payment_required",
   "challenge": {"type": "payment", "provider": "x402",
                 "amount": "0.02", "currency": "USDC",
                 "descriptor": { …provider-native fields… },
                 "payment_id": "…", "expires_at": "…"}}
  ```

  Sender pays via the named provider, retries the message with a **settlement proof**
  (`{"payment_id": "…", "payment_proof": "…"}`); the relay **verifies settlement**
  (facilitator/webhook/invoice-settled) exactly like it verifies a PoW solution — single-use
  `payment_id`, bound purpose/recipient, expiry — then admits the message to the anon queue.
- **Settlement is verified, never trusted from the client.** The `payment_proof` is only an
  index into a relay-confirmed settlement (x402 facilitator receipt, Lightning
  invoice-settled event, Stripe webhook), mirroring the single-use-nonce discipline PoW already
  has. A `payment_id` is consumed once.
- **Reserved-slot compatibility:** clients that only know PoW keep working — a recipient who
  offers `pow_or_payment` still accepts a valid PoW solution, so no client is locked out by
  price.

### D. Crypto rails — x402 & L402 (E16-T4)

These are the deepest fit because they are **HTTP-402-native**, agent-friendly, and
self-custodial — and INT-002 already plans the outbound half.

- **x402** (Coinbase/Cloudflare open standard, HTTP-402 + stablecoin over EIP-3009/Permit2;
  USDC/EURC gasless; CAIP-2 chain IDs; **zero protocol fee**; facilitator verifies+settles). Fit:
  the relay emits an x402 `PAYMENT-REQUIRED` for the recipient's on-chain address (from
  `payments.json`), the sender's wallet/agent signs a `PAYMENT-SIGNATURE`, a **facilitator**
  verifies+settles. **Hosted:** Poweur runs (or contracts) the facilitator and can add its cut as
  a fee split. **Self-hosted:** the user points at their own address + any facilitator (incl.
  Coinbase's free tier) — Poweur is not in the loop, no cut. Excellent for AI-agent senders, which
  is a first-class Poweur audience.
- **L402** (Lightning HTTP-402, macaroon + LN invoice; sub-cent; **no facilitator/third party**;
  settles in BTC). Fit: the relay (or the user's own LND/LNbits, see INT-002's `/.well-known/lnurlp/`)
  issues an invoice + macaroon; message admitted on invoice-settled. Lowest minimums (true
  sub-cent), fully self-sovereign — the natural self-hosted default; hosted Poweur can run a node
  and take a routing/service cut.
- Ship **one** end to end first (recommend x402 for agent reach *or* L402 for self-host purity —
  pick per the first real consumer) and stub the other behind the same adapter interface.

### E. Fiat rails — Stripe / Revolut / SEPA (E16-T5)

For senders without crypto. These are **hosted-gateway** rails by nature — which is exactly where
Poweur's cut lives.

- **Stripe** — Checkout/PaymentIntent + **webhook** settlement; Stripe Connect models the
  application fee (Poweur's cut) natively; also natively supports SEPA/iDEAL/Bancontact/etc. via
  Payment Methods, covering the "local European payment methods" ask with one integration.
- **Revolut / Revolut Merchant** — the sender is deep-linked to a hosted pay page (pairs with the
  INT-002 `revolut:@handle` directory entry); Revolut Merchant API confirms settlement for a cut.
- **Raw SEPA / IBAN** — for self-hosted users who publish an `iban:` handle: the message can carry
  a payment *request*, but settlement is out-of-band (no instant confirmation), so this rail is
  **manual-confirm / trust-on-payment**, not an instant gate. Documented as such.
- **The cut only exists on hosted gateways.** When Poweur operates the Stripe/Revolut account, the
  cut is an application fee. If a self-hoster brings their **own** Stripe/Revolut keys, Poweur
  neither sees nor splits the money — same principle as x402/L402.

### F. Tiered ladder & the cut model (E16-T6, E16-T7)

- **Three tiers from the suggestion, expressed in policy:**
  - *Free:* solve the memory-hard PoW (tuned to ~1–2 s mobile) → send.
  - *Paid:* pay the recipient's fee → send instantly, no PoW.
  - *Middle:* **partial payment reduces required difficulty proportionally** — pay half → solve a
    PoW calibrated to a fraction of the time. Implement as: the relay mints a **cheaper** PoW
    challenge once a partial `payment_id` is confirmed (the payment discount just lowers the
    difficulty param in the freshly-minted token). Clean because it composes the two existing
    mechanisms rather than inventing a third.
- **Cut model (E16-T6):**
  - Recipient sets the **price they receive**. Poweur's cut is a **relay-config** percentage/flat
    fee **added on top** (sender pays price + cut) or **split from** (recipient nets price − cut) —
    make it explicit and shown in the UX; recommend *added on top* so the recipient's dial means
    what it says.
  - Cut applies **iff the relay operates the gateway for a hosted identity**. Detection: the
    settlement path runs through Poweur-held credentials/facilitator. Self-hosted identity or
    user-supplied gateway keys ⇒ `cut = 0`, and the relay documents that it took nothing.
  - **Accounting:** a minimal per-identity ledger of gated payments (amount, rail, cut, message
    id) under the relay's durable store, exposed to the owner (receipts) and to ops (EPIC-013
    metrics: `paid_messages_total{rail}`, `payment_cut_total`). No custody of recipient funds
    beyond what the rail requires; prefer rails that settle **directly to the recipient** with the
    cut split at settlement (Stripe Connect application fee, x402 fee split) over Poweur holding
    balances.

### Algorithm note (record the reasoning so we don't relitigate)

- `sha256-lead0`: compute-bound, GPU/ASIC edge ~10⁴× → **unfair to mobile**. Keep only for
  surfaces where verifier triviality > device fairness (registration).
- `argon2id`: memory-**bandwidth**-hard, GPU edge ~10–20× at 32 MB → **better, not solved**;
  well-audited, good WASM libs → **acceptable interim default**.
- `seqmem-v1` (memory-**latency**-hard, sequential pointer-chase, causal-hash-bound): can't be
  parallelized within a hash → **best device fairness** (consumer CPU ≈ rented datacenter GPU) →
  **target default**, but newer/less-audited → needs its own PCP + test vectors + review before it
  becomes default.
- Pluggability (A) is what lets us ship `argon2id` now and promote `seqmem-v1` later without
  touching a single policy file.

## Tasks

### E16-T1 — Algorithm abstraction & one difficulty dial

- [ ] `packages/identity`: `PowAlgorithm` registry interface
      (`Name`, `Params(level)`, `Solve(ctx, token, params)`, `Verify(token, solution, params)`);
      register `sha256-lead0` unchanged behind it
- [ ] Seal `Algo` + `Params` into `PowChallenge`; envelope carries `algo` + `params` (extend
      `writeChallengeRequired` / `handleAuthPow`); solution binds to minted algo+params (no
      downgrade)
- [ ] Policy: add `pow_difficulty` (abstract level / target-time band) + optional `pow_algo`;
      keep `pow_bits` as an honored deprecated alias; relay config picks default algo + the
      level→params table; update `inbox-policy.schema.json` + `AnonymousPolicy` + validators
- [ ] JS: `packages/client-ts/src/pow.ts` dispatches on `algo` from the envelope
- [ ] Tests: unknown-algo graceful render; alias migration; downgrade-attack rejection

**Acceptance:** a recipient sets a single difficulty; the relay can change the default algorithm
via config with no policy-file or client change; old `pow_bits` policies still work.

### E16-T2 — Memory-hard PoW algorithm(s)

- [ ] `argon2id` algorithm (Go verify/solve + WASM/browser solver at ~parity); params
      `m_kib`/`t`/`p`; conservative mobile-safe default mapping
- [ ] (Target) `seqmem-v1` memory-latency-hard PCP with test vectors, then Go + WASM impls
- [ ] Committed benchmark → difficulty table across CLI + at least one real mobile browser
      (published in the spec, like E14's bit table)
- [ ] Spec update in `apps/docs/docs/trust/anonymous-and-challenges.md`: algorithms, the honest
      GPU-asymmetry comparison (sha256 vs argon2id-bandwidth vs seqmem-latency), device-fairness
      rationale; PCP-0006 amended / new PCP for `seqmem-v1`

**Acceptance:** on the benchmark box, the memory-hard algo shrinks the GPU-VM-vs-mobile advantage
by ≥100× versus `sha256-lead0` at an equivalent honest-mobile solve time; browser solver within
~10% of native.

### E16-T3 — Payment challenge protocol (provider-agnostic core)

- [ ] Policy: `payment` sub-block (`amount`, `currency`, `providers`, `memo`) + new `challenge`
      values `payment` and `pow_or_payment`; schema + `AnonymousPolicy` + validation
- [ ] Relay: `402 Payment Required` with a typed payment descriptor; retry path verifying a
      single-use, purpose/recipient-bound, expiring **settlement proof**; admit to anon queue on
      confirmed settlement — same discipline as `consumeNonce`
- [ ] Provider adapter interface (`Quote`, `Descriptor`, `VerifySettlement`) so D/E are
      drop-ins; accepted rails = recipient's `payments.json` ∩ relay-supported
- [ ] Tests: pay-only happy path (stub rail); unpaid → 402; replayed/expired proof; recipient
      offering `pow_or_payment` still accepts a valid PoW

**Acceptance:** with a stub rail, an anonymous message gated by `payment` is admitted only after a
verified, single-use settlement; `pow_or_payment` admits on *either* proof.

### E16-T4 — Crypto rails: x402 + L402

- [ ] x402 adapter: emit `PAYMENT-REQUIRED`/`402`, verify+settle via facilitator (hosted:
      Poweur/contracted facilitator + fee split; self-host: user address + any facilitator, no
      cut); USDC/EURC on a supported chain
- [ ] L402 adapter: macaroon + LN invoice (relay node or user's own LNbits/LND via INT-002
      `/.well-known/lnurlp/`); admit on invoice-settled; sub-cent minimums
- [ ] Ship one end-to-end against its test network; stub the other behind the interface
- [ ] Docs: rail comparison (x402 agent-reach + ~$0.01 floor vs L402 self-sovereign sub-cent),
      when each is the right default

**Acceptance:** a message is admitted after a real testnet settlement on the shipped rail; the
self-hosted path demonstrably routes money without Poweur infrastructure.

### E16-T5 — Fiat rails: Stripe / Revolut / SEPA

- [ ] Stripe adapter: Checkout/PaymentIntent + webhook settlement; Stripe Connect application
      fee = Poweur cut on hosted IDs; SEPA/iDEAL/etc. via Stripe Payment Methods
- [ ] Revolut Merchant adapter (or deep-link + confirm) tied to the INT-002 `revolut:` handle
- [ ] Raw `iban:`/SEPA documented as manual-confirm/trust-on-payment (no instant gate)
- [ ] Tests against Stripe test mode incl. the application-fee split and a self-host (own keys,
      zero cut) case

**Acceptance:** Stripe test-mode payment admits a message; the application fee is taken only when
Poweur operates the account, and is provably zero with user-supplied keys.

### E16-T6 — Fee/cut model & accounting

- [ ] Relay config: cut policy (percentage/flat, added-on-top vs split; default added-on-top);
      per-identity hosted-vs-self-host detection driving `cut = 0` for self-hosters
- [ ] Minimal durable payment ledger (amount, rail, cut, message id, settlement ref); owner
      receipts view; EPIC-013 metrics `paid_messages_total{rail}` / `payment_cut_total`
- [ ] Prefer settle-direct-to-recipient-with-split over Poweur holding balances; document custody
      posture and the "self-host ⇒ we take nothing" guarantee prominently
- [ ] Tests: cut math (on-top vs split), zero-cut self-host, ledger + receipt correctness

**Acceptance:** hosted paid message records a correct cut and a recipient receipt; the same flow
on a self-hosted identity records zero cut and no Poweur settlement leg.

### E16-T7 — Tiered ladder: partial payment ↓ difficulty

- [ ] `pow_or_payment` middle tier: a confirmed **partial** payment causes the relay to mint a
      **reduced-difficulty** PoW token (compose payment + PoW, no third mechanism)
- [ ] Policy expresses the discount curve (e.g. fraction paid → fraction of difficulty)
- [ ] Tests: full pay → no PoW; partial pay → measurably easier PoW; no pay → full PoW

**Acceptance:** paying half the fee yields a PoW that solves in a measurably smaller fraction of
the full-difficulty time.

### E16-T8 — Client UX (CLI + web)

- [ ] CLI: `poweur send --anon` learns `pow_or_payment` — prints "solve (~Ns) or pay
      €X via {rails}", `--pay <rail>` to choose payment, shows the receipt; `poweur policy set`
      grows `--pay-amount/--pay-currency/--pay-rails` and the abstract `--pow-difficulty`
- [ ] Web (coordinates with EPIC-015): anon-send page offers **solve vs pay**; policy/settings
      panel sets price + accepted rails + difficulty with human labels ("~1 s phone / ~5 s pay
      €0.02"); receipts in the owner's view
- [ ] Tests: integration (`apps/integration`) for pick-your-cost against a stub rail; web e2e for
      the choice UI

**Acceptance:** two-party demo — recipient sets "solve ~2 s **or** pay €0.02"; one sender solves,
another pays; both messages reach the anon queue; the paid one shows a receipt and (hosted) a cut.

## Non-goals

- **Verified-account attestation** (the `verified` slot) — stays reserved; a separate epic.
- **PoW as sybil-proof identity** — unchanged from EPIC-014: it is a rate limiter; contacts
  (EPIC-007) remain the spam answer for identified senders.
- **Custodial balances / being a money transmitter** — prefer rails that settle directly to the
  recipient with the cut split at settlement; Poweur is a gate + optional facilitator, not a bank.
  Regulatory/KYC posture for fiat rails is a prerequisite spike before E16-T5 ships in production,
  not designed here.
- **Outbound payment handles** — that is INT-002 (`payments.json`, LNURL, pay-me directory); this
  epic *consumes* those to price inbound messages, it does not re-specify them.
- **On-chain settlement mechanics / new tokens** — reuse x402/L402/Stripe as-is; no Poweur coin,
  no mining rewards (the suggestion's explicit rejection of the mining incentive model).

## Sources (research grounding, July 2026)

- x402 (Coinbase/Cloudflare open HTTP-402 stablecoin standard; USDC/EURC via EIP-3009, zero
  protocol fee, facilitator model): <https://www.x402.org/x402-whitepaper.pdf>,
  <https://docs.cdp.coinbase.com/x402/welcome>, Stripe x402 support
  <https://docs.stripe.com/payments/machine/x402>
- L402 (Lightning HTTP-402, no facilitator, sub-cent, self-sovereign):
  <https://docs.lightning.engineering/the-lightning-network/l402>,
  agent-commerce comparison <https://getalby.com/blog/agentic-commerce-a-guide-to-l402-x402-and-mpp>
- Memory-hard PoW in the browser: Argon2id is memory-**bandwidth**-hard (GPU edge ~10–20×, RFC
  9106 <https://datatracker.ietf.org/doc/rfc9106/>, WASM libs
  <https://github.com/openpgpjs/argon2id>); the memory-**latency**-hard, sequential
  pointer-chase direction (consumer CPU ≈ rented GPU) per the "Sandglass/Browsercoin" redesign
  writeup <https://coinspectator.com/mainstream/2026/07/19/browsercoin-dev-update-we-redesigned-our-proof-of-work-from-scratch-to-be-browser-first-after-gpu-farms-took-over-our-browser-based-coin/>
</content>
</invoke>
