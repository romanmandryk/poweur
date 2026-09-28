---
id: relay-reputation
sidebar_position: 3
title: Relay reputation & abuse pressure
---

# Relay reputation & abuse pressure

[Recipient consent](contacts) is a wall around one inbox, and it holds. What it cannot
do is see the shape of an attack that is spread across many inboxes, or apply any cost
to the party who made the attack cheap. This page is the design record for that layer:
what Poweur ships today (EPIC-007 E07-T5), the options considered for the phase after
it, and which one we recommend picking up next.

## The asymmetry this layer exists to fix

Hosted registration (EPIC-002) makes identities cheap on purpose — that is the whole
point of "claim your ID in thirty seconds". Cheapness has a price, and it is paid here:

- **Per-identity reputation is weak.** A reputation you can discard by registering
  again is not a reputation. Every per-identity counter in the relay is really a
  counter on one identity's *current* name.
- **The requests queue is the one door open to strangers.** Under
  `contacts_and_requests` a non-contact gets exactly one knock. That is correct for
  one stranger and useless against a thousand: a thousand fresh identities each
  knocking once never trips a per-identity cap, because none of them exceeds it.
- **The relay is the thing that does not change.** An attacker can vary the sender
  name for free and the sending domain for a few dollars. Varying the *relay* means
  standing up infrastructure, holding a DNS name, and having an operator answer for
  it. That is the first field in the envelope with a real cost behind it.

So the unit of accountability for this layer is the **relay**, not the identity.

## What ships today

### 1. Per-sender-relay metering on the requests queue

The recipient's relay charges each contact-request attempt to the relay accountable for
the sender.

| | |
|---|---|
| **Key** | the host serving the sender's identity, from the same lookup (and DNS-TTL cache) the forwarding path already does. When that lookup fails, the sender's parent domain stands in — everyone on a hosted domain still shares one bucket, which is the property that matters. |
| **Scope** | contact-request admissions only. Conversation between accepted contacts is never metered here, and neither is anything else. |
| **Exempt** | this relay's own users. It knows exactly who they are, meters them per identity, and gates their creation (invite codes or proof-of-work). It has levers over its own users; against a peer it has none, which is why the peer gets a budget instead. |
| **Defaults** | 10/minute, 60/hour, 300/day per sending relay (`REQUEST_RELAY_LIMIT_MINUTE` / `_HOUR` / `_DAY`; `0` disables a window). A contact request is a rare event per person; these are deliberately small. |
| **Rejection** | `429` with `{"error":"rate_limit_exceeded","scope":"sender_relay","window","limit","reset_at"}`. The scope says `sender_relay`, not `sender` — a client told "sender" would send its user looking at the wrong thing. |

Attempts are charged, not admissions: a meter that only counted the requests it let
through could be bypassed with requests designed to fail.

**The cost is collective, and that is the mechanism, not a bug.** People on a relay
that lets a flood out share its budget for the minute. That is the pressure: it gives
an operator a reason to notice, and it gives their users a reason to expect them to.
The blast radius is bounded to one relay — a well-run relay's users are never affected
by a neighbour they have never heard of — and the door being rationed is the
stranger-knock, not conversation. Somebody the recipient has already accepted is never
touched by any of this.

**What it does not fix:** a determined attacker with real infrastructure and many
relays, and a big shared relay whose honest users absorb the cost of one bad tenant.
Both are arguments for the phase-2 options below, not against the meter.

### 2. `sys.abuse.report` — a channel to the operator

Nothing before this let a recipient tell the *operator* hosting a sender that something
was wrong, which meant an operator hosting a spammer found out from a blocklist, a
peering complaint, or never.

```
POST /abuse   → 202 {"status": "recorded" | "duplicate"}
```

The body is a signed report document (`packages/identity/abuse.go`):

```json
{"version": 1, "type": "sys.abuse.report",
 "reporter": "alice.example.org", "subject": "loud.cheapco.test",
 "reason": "spam", "message_ids": ["m-1", "m-2"],
 "note": "twelve identical messages overnight",
 "created_at": "…", "signature": "…"}
```

`reason` is a short closed set — `spam`, `harassment`, `phishing`, `malware`,
`impersonation`, `other` — because an operator triaging reports needs to sort them and
free text does not sort.

Four decisions are load-bearing:

- **It is a document POSTed to an endpoint, not a message to an inbox.** Its recipient
  is an operator, who is not an identity and has no encryption key to seal an envelope
  to. The type is registered like the `sys.contact.*` types and rides no envelope.
- **It carries no message content.** The traffic being reported is end-to-end
  encrypted; the operator could not read it anyway, and shipping plaintext would hand a
  third party a copy of the reporter's own private conversation in the name of
  protecting them. Message *IDs* let the operator corroborate against their own
  delivery logs without ever seeing what was said.
- **It must be signed, and it is one report per reporter per subject per day.** An
  anonymous or repeatable complaint stream is itself an abuse vector — it would let one
  attacker manufacture a reputation. The signature names the reporter; the dedup means
  the count reflects people, not clicks.
- **A relay only accepts reports about identities it hosts.** Otherwise the endpoint is
  free storage on every relay in the network. A report about a stranger is answered
  with `403 not_authorized` and a pointer at their relay; `poweur report` resolves that
  relay and posts there.

v1 is **log plus counter**. Nothing suspends an identity, nothing is exported to other
relays, and the response deliberately does not return the count — a probeable number
would turn the endpoint into the reputation oracle it refuses to be. What an operator
does with a count is an operator decision, and building enforcement before anybody has
run the counter would be guessing.

### 3. User-level blocklists: block decisions made portable

A block in `contacts.json` protects exactly one person. That is the right default —
consent is personal — but it means every member of a community pays the full cost of
discovering the same abuser independently, which is the asymmetry spam economics feed
on.

```bash
poweur blocks export --name "my list"            # signs + publishes shared/blocks.json
poweur share add /shared/blocks.json --with bob.example.org --perm read
poweur blocks import alice.example.org           # verifies the signature, then merges
poweur blocks import --file list.json --dry-run  # any other transport works too
```

- **Signed, always.** An unsigned blocklist is an invitation to add names to somebody
  else's list in transit. The importer verifies against the publisher's resolved
  identity key — the same trust chain as everything else here.
- **`shared/`, not `.poweur/relay/`.** The sys zone is owner-and-relay only: the
  permission layer refuses every visitor there, grant or no grant, so a list published
  into it could never be adopted. And not `public/` either — a blocklist names people,
  and published to the world it is a denunciation list. An [EPIC-005 share](../files/storage-v2)
  hands it to a chosen audience.
- **Adoption is a copy, not a subscription.** Importing writes entries into the
  importer's own contacts, tagged `source: "blocklist:<publisher>"`, where they can be
  seen and undone. Nobody ends up carrying a block they cannot explain because a list
  they subscribed to grew overnight.
- **An accepted contact is never blocked silently.** Entries naming somebody the
  importer has accepted are skipped and reported by name unless `--force`. A list that
  quietly cut you off from a person you chose would make adopting one an act of
  self-harm.

## Options for phase 2

### A. Shared, subscribable relay blocklists (DNSBL, but signed)

A publisher — a relay operator, a community, a consortium — publishes a signed list of
**relays** (not identities) at a well-known path. Subscribing relays fetch it on a TTL
and apply it as policy: refuse, or tighten the request meter, for listed peers.

*Buys:* the only mechanism here that scales sub-linearly with the number of attackers,
because one listing covers every identity behind a relay, including the ones not
registered yet. Reuses the document, signature and share machinery already built for
user blocklists.

*Costs:* this is the design with real history behind it, and the history is a warning.
DNSBLs concentrated enormous power in a few unaccountable operators; delisting was
opaque and slow; false positives were paid for by users who had no idea why their mail
vanished, and no way to appeal to anyone. Any Poweur version has to answer the
questions DNSBLs never did — who can publish, how a relay gets off a list, what a user
sees when their message is refused for something their operator did — before it answers
the easy technical ones.

*Failure mode:* small and new relays get listed by default-ish policies and never get
off, and the network re-centralises on the few relays nobody dares list. That is
precisely the outcome Poweur exists to avoid.

### B. Proof-of-work on contact requests from unknown relays

EPIC-014 already ships the primitive and the protocol: stateless HMAC-sealed challenge
tokens, `sha256-lead0`, a `428 challenge_required` envelope, single-use solutions, a
difficulty floor that auto-raises under load, and reference solvers in Go and
TypeScript. It is already consumed by anonymous ingress and by hosted registration
(`REGISTRATION_GATE=pow`, `GET /auth/pow`).

The seam for this layer is specified and not yet wired: `stranger_challenge` /
`stranger_pow_bits` in `inbox-policy.json`, gating the requests queue for *identified*
non-contacts — the same 428 dance the anonymous path does, one step earlier, before the
request is queued. A recipient could set a cost that a person pays once, invisibly, and
a flood pays per identity.

*Buys:* per-request cost imposed by the *recipient's* policy, with no third-party
list, no publisher to trust, and nobody to appeal to because nobody is judging anyone.
It composes with the meter rather than replacing it: the meter rations the relay, PoW
prices the individual request.

*Costs:* work on the sender's device — and, unlike the anonymous path where the sender
is a web visitor at a keyboard, this one taxes ordinary people trying to reach someone.
It must stay at bits that are unnoticeable (≈12–16), which means it prices a flood in
CPU-minutes, not CPU-days. PoW rate-limits; it never authenticates. A GPU outruns the
dial by orders of magnitude.

*Failure mode:* difficulty set high enough to matter against a botnet is high enough to
be a wall for a phone browser. This buys time and raises the floor; it does not end
the argument.

### C. Postage-style deposits

A refundable deposit attached to a contact request, returned when the recipient accepts
and forfeited when they report. `payment` already exists as a policy slot in the
challenge vocabulary and returns a typed envelope with `501`.

**Note only — do not build.** It is the strongest economic answer on paper and the
weakest one in practice for a protocol at this stage: it needs a settlement layer, an
escrow with custody of other people's money, a dispute process, and a jurisdiction. It
also changes what Poweur *is* — "you may pay to talk to a stranger" is a product
decision long before it is a protocol one. Recorded so the vocabulary slot stays
reserved and nobody re-derives it from scratch.

### D. Vouching / transitive trust

Accept a request from a stranger vouched for by an accepted contact. Cheap to state,
and it fits the contacts model exactly.

*Costs:* it leaks the social graph in both directions — precisely the thing
`contacts.json` is kept private to avoid — and one compromised account becomes a
licence to reach everyone it knows. Worth revisiting only with a design that proves
vouches without disclosing who vouched.

## Recommendation for phase 2

**Pick B — wire `stranger_challenge` in front of the requests queue — and pair it with
one non-mechanical change: surface the abuse counter to the operator.**

The reasoning:

- It is the only option whose primitive, protocol, difficulty table, load-shedding
  behaviour and client solvers are already built, tested and documented. The work is
  policy fields, a gate in one branch of `handleMessagesPost`, a CLI/web dial, and a
  mirror in `@poweur/client`.
- It is **decentralised by construction**. Nobody publishes, nobody subscribes, nobody
  is judged, and there is no list to get onto or off. Every other option on this page
  creates an authority; this one creates a price.
- It composes with what shipped rather than replacing it. The relay meter rations the
  peer, PoW prices the individual request, and the two fail differently — which is what
  you want from layered defences.
- It closes an already-specified cross-epic seam (EPIC-014 E14-T3) rather than opening
  a new design surface.

Option A stays on the table but should not be built until we have data — which is the
point of the second half of the recommendation. The abuse counter exists and nothing
reads it back: it already holds per-subject totals, distinct reporter counts and a
reason breakdown, and an operator-facing view of that (plus durability, which memory-only
counters do not have) turns it into evidence about what abuse on this network actually
looks like. Every phase-3 decision, including whether shared relay blocklists are worth
their history, should be made from that evidence rather than from the mail ecosystem's.

Two guardrails hold across all of it:

- **Nothing auto-suspends an identity on a count.** A count is an input to a human
  decision, and building enforcement before anyone has read one is guessing.
- **Reputation is never exported by default.** The moment one relay's opinion of
  another travels automatically, the delisting problem arrives with it, and there is no
  way to un-ship that.

## Configuration reference

| Env var | Default | Meaning |
|---------|---------|---------|
| `REQUEST_RELAY_LIMIT_MINUTE` | 10 | Contact-request attempts per sending relay per minute (`0` = unlimited) |
| `REQUEST_RELAY_LIMIT_HOUR` | 60 | …per hour |
| `REQUEST_RELAY_LIMIT_DAY` | 300 | …per day |

All three at `0` turns the meter off entirely.

## See also

- [Contacts, inbox policy & key pinning](contacts) — the consent layer this one sits behind
- [Anonymous messaging & sender challenges](anonymous-and-challenges) — the PoW primitive and challenge protocol
- [Rate limiting](../protocol/rate-limiting) — the per-sender and global buckets
- [Sharing](../files/storage-v2) — how a blocklist reaches its audience
