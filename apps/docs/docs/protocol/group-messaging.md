---
id: group-messaging
sidebar_position: 8
title: Group messaging
---

# Group messaging

A group is an address. `crew.acme.poweur.net` is a
[group identity](../files/group-identities.md) — an ordinary hosted identity with its own
Ed25519 key, made a group by one signed document in its own tree — and this page is what it
means to *send it a message*.

Nothing here introduces new cryptography. Group messaging v1 is the existing 1:1 envelope,
sent N times, with the expansion of "N" moved to the one party that already has to know the
membership: the relay hosting the group.

## The v1 decision: server fan-out, per-member encryption

**The sender's client encrypts the payload separately for every member and posts all of those
envelopes to the group's relay in one request. The relay delivers each envelope to the member
it names.**

```
alice ──┐  encrypt(plaintext, bob.enc_pub)   ─┐
        ├─ encrypt(plaintext, carol.enc_pub)  ├─→ POST /groups/crew.../messages ─→ relay
        └─ encrypt(plaintext, dave.enc_pub)  ─┘        (one HTTP request)            │
                                                                                    ├→ bob's inbox   (local)
                                                                                    ├→ carol's relay (forwarded)
                                                                                    └→ dave's inbox   (local)
```

Concretely:

- **Encryption is unchanged.** Each envelope is X25519 + HKDF + ChaCha20-Poly1305 to that one
  member's published encryption key, exactly as a direct message is. There is no group key, no
  sender key, no ratchet, and no new algorithm identifier.
- **Signatures are unchanged.** Each envelope is signed by the sender over
  `CanonicalMessageEnvelope`, byte-for-byte the string a 1:1 message signs. A member verifies a
  group message the same way they verify any other.
- **The relay's job is expansion, not encryption.** It resolves the group's membership,
  checks that the batch covers exactly that membership, and delivers. It cannot read any
  payload; it can only see who is in the group, which it already had to see to serve the
  document at all.
- **The batch is one HTTP request.** The alternative — the client posting N times to N relays —
  leaks the full membership to every relay on the path via the sender's own traffic and makes
  partial sends the normal case. One request to the group's relay is also what makes the group's
  relay the single place that decides "who is in this group right now".

### What this buys, and what it costs

| | |
|---|---|
| **Buys** | No new crypto to review. No group state to converge. A member needs nothing but their existing encryption key. Removing a member is immediate — the next fan-out simply omits them. Cross-relay works because each envelope is a normal message on the normal forward path. |
| **Costs** | Sender-side work and bandwidth are O(members). No post-compromise security beyond what 1:1 already has. The group's relay learns the membership and the message *sizes* per member. A member added later cannot read older messages (see [Late join](#late-join-and-membership-changes) — we consider this a feature). |

### The revisit threshold

**v1 refuses to fan out to a group with more than 100 members** (`413 group_too_large`). That
number is not a guess about comfort; it is the point at which the O(N) shape stops being a
detail and becomes the design. It is enforced in code so the limit cannot quietly rot.

Sender keys or MLS get picked up when **either**:

1. a real group needs more than 100 members, or
2. a group sustains more than ~10 messages/minute, at which point O(N) per message becomes
   O(N × rate) on the sender's uplink and on the group relay's fan-out loop.

Until one of those is true, the extra machinery buys properties nobody is asking for. The
study of what we would adopt, and what it would demand, is
[MLS adoption study](../future/mls-adoption.md).

## Addressing

A group is addressed by its Poweur ID. There is no group-specific address syntax — if it has a
dot it is an identity, and a group identity is an identity.

**The group's own relay is the fan-out point.** A sender resolves `crew.acme.poweur.net` the
way it resolves any recipient (web-first `/.well-known/poweur/id.json`, then DNS TXT), finds
the relay hosting it, and posts the batch there. That relay reads
`.poweur/relay/group.json` out of the group's own tree and verifies it with the
group's own key.

No relay is ever asked to look up a group hosted somewhere else (see
[sharing a folder with a group](../files/group-identities.md#sharing-a-folder-with-a-group),
where members present the signed roster instead):
the group's membership is only ever read by the group's own relay, on its own disk. The
*members* may be anywhere — a member on another relay is reached over the ordinary
privacy-proxy forward path, which already exists and is already tested.

Two rules follow:

- **A group can only be messaged through the relay that hosts it.** If that relay is down, the
  group is down. A 1:1 message between two members still works; the group does not. This is the
  same availability story a group's `self.json` already has.
- **The at-least-one-local rule is satisfied by the group, not by the sender.** A relay accepts
  a fan-out because *the group* is hosted locally. The sender may be a member on any relay.

### Who may send

The sender must be in the group's **`members`** list.

`members` is the access list; `admins` is an authority list, and they are independent
([group identities](../files/group-identities.md)). An admin who is not a member administers
the roster and does not get a seat in the conversation — they cannot send, and the fan-out
does not deliver to them. This is stated rather than derived because the two lists are easy to
conflate: **for messaging, `members` is the list, alone, in both directions.**

### The roster read

To encrypt per member, a sender needs the membership. `GET /groups/{group}` returns the signed
membership document to a caller who proves they are in `members` or `admins`; everyone else
gets `404 not_found` — the same answer as a group that does not exist, because a
distinguishable rejection turns "is X in this group?" into an enumeration oracle
([limits and privacy](../files/group-identities.md#limits-and-privacy)).

The response is the document as signed, so the client verifies the group's own signature
itself rather than trusting the relay's rendering of it. Member encryption keys are **not** in
the response: the client resolves each member's identity document the normal way, so a group
message is encrypted to keys the sender resolved and pinned, not to keys the group's relay
handed over.

### `epoch` is a precondition, not a hint

The fan-out request carries the `epoch` the client built its batch against. If it does not
equal the group's current epoch, the relay refuses with `409 group_epoch_stale` and returns
the current membership document. The client re-encrypts against the new roster and retries.

This is what makes "removed at 14:00" mean removed. Without it, a client that read the roster
at 13:59 could deliver to someone who left, and the relay — which is the only party that sees
both — would help it. `epoch` moves only on a real membership change, so an idle group never
forces a retry.

## The envelope

**No new envelope field, and no change to the canonical signing string.** Group membership is
carried in two reserved `metadata` keys:

| Key | Value |
|---|---|
| `group` | the group's Poweur ID, lowercase |
| `epoch` | the membership epoch the batch was built against, decimal |

`metadata` is signed, plaintext, and documented as *addressing, not content*
([message format](message-format.md)) — which is exactly what a group name is. Using it means
group messaging adds nothing to `CanonicalMessageEnvelope`: a group message is byte-for-byte a
message any existing implementation already knows how to verify, and a client that has never
heard of groups renders it as a direct message from its actual sender rather than failing.

`metadata.group` is **reserved**, and enforced where it can be: a relay refuses a direct `POST
/messages` carrying `metadata.group` for a group **it hosts**, so nobody can forge their way
into a local group's conversation by sending an ordinary message — that address only accepts
mail through the fan-out endpoint, which checks membership and epoch.

For a group hosted **elsewhere** the check is deliberately not made, and that is not a gap
being papered over. A relay cannot resolve another relay's membership document (cross-relay
group resolution is deferred), so refusing on the strength of a claim it cannot check would
break the legitimate case — a forwarded fan-out is exactly a message carrying
`metadata.group` for a group the receiving relay does not host. So:

> **For a group's own relay, `metadata.group` is enforced. For everyone else it is a hint,
> and the recipient's client is what verifies it.**

Client-side verification is cheap and every member can do it: read the roster (you are a
member, so you are authorized), and confirm the sender is in `members` at the claimed epoch.
`(group, epoch)` is a cache key, so this is one fetch per membership change, not one per
message. A client that cannot verify must render the message as what it provably is — a
direct message from its actual sender — rather than filing it into a group on the strength
of an unchecked label.

An envelope in a fan-out therefore looks like:

```json
{
  "id": "msg_…",
  "sender": "alice.poweur.net",
  "recipient": "bob.example.org",
  "timestamp": "2026-09-10T10:00:00Z",
  "payload": "<ciphertext sealed to bob's encryption key>",
  "thread_id": "crew.acme.poweur.net",
  "metadata": {"group": "crew.acme.poweur.net", "epoch": "3"},
  "encryption": {"alg": "…", "ephemeral_public_key": "…", "nonce": "…"},
  "signature": "<alice, over CanonicalMessageEnvelope>"
}
```

Every envelope in one batch shares `sender`, `timestamp`, `type`, `thread_id` and `metadata`.
Only `id`, `recipient`, `payload` and `encryption` differ — one per member.

### The 512 KB cap

**Unchanged.** 512 KB bounds one envelope, which is what lands in one inbox, and each envelope
in a batch is measured against it individually.

The batch is a transport container for up to 100 such envelopes and has its own, larger cap
(4 MB) — over it, `413 content_too_large`. A group message is therefore bounded by
`payload × members ≤ 4 MB` as well as `payload ≤ 512 KB`; a 40 KB message reaches 100 members,
a 512 KB one reaches 8. Anything bigger is an attachment (E09-T4), which is a reference, not a
payload.

## `thread_id` in groups

**A group message is always threaded, and the default thread is the group itself.**

- With no explicit thread, `thread_id` is set to the group's own Poweur ID —
  `crew.acme.poweur.net`. Every member derives the same value from the address with no
  coordination, so the group's main conversation is one thread on every client without anyone
  having to agree on an identifier.
- A sub-thread is the group ID, a colon, and a client-chosen suffix:
  `crew.acme.poweur.net:design-review`. The relay enforces the prefix — a fan-out whose
  `thread_id` is neither the group ID nor `<group-id>:…` is refused.

The prefix rule is doing real work. `thread_id` is opaque to the relay and chosen by clients,
so without it a group thread and a 1:1 thread could collide, and a client would have no way to
tell from a thread identifier which conversation it belongs to. With it, **`thread_id` alone
answers "which group is this?"**, which is what lets a client render threads without first
opening every message.

`:` is used rather than `/` because `/` is not a legal `thread_id` character
(`ValidateThreadID`); the group ID's own dots make it unambiguous either way.

### Conversation identity

Both sides key a group conversation on the **group**, not on the peer:

- The **recipient** files an inbound message under `metadata.group` rather than under
  `sender`, so all five members' messages land in one conversation.
- The **sender** archives **one** history record for the whole fan-out, with the group as the
  peer — not N records, one per member. The relay never hands a sender their own message back
  (E09-T1), so this local copy is the only record the sender has of what they said, and one
  copy of one message is what they said.

## Ack semantics

**Per-member ticks. There is no aggregate ack on the wire.**

Each envelope has its own `message_id`, so delivery acknowledgement is exactly what it already
is, per member:

- **Tick 1** is per member and comes back in the fan-out response: `POST /groups/{group}/messages`
  returns, for every member, the envelope id and whether that member's relay accepted it.
  Partial success is normal and is reported as such — the response is `202` with a per-member
  breakdown, not a single verdict.
- **Tick 2** (`delivered_client`) is per member and unchanged: each member's client acks the
  envelope it received, addressed to the sender, over `POST /acks`.

**Aggregation is a client-side rollup, deliberately.** A client that wants to show "3 / 5
delivered" groups the acks it holds by the fan-out it sent — it built the batch, so it knows
which N message ids are one message. Nothing new travels for this.

An aggregate ack *on the wire* was considered and rejected. It would have to be produced by
the group's relay, because that is the only party that sees all N deliveries — and it would
therefore be unsigned relay hearsay about other people's clients, exactly the property that
made the `sys.delivery.failed` expiry notice a documented exception rather than a pattern
(E09-T1). Per-member ticks are honest: each one is signed by the client that produced it.

**A failed member does not fail the message.** If carol's relay is unreachable, bob and dave
still have the message, and the response says carol did not. Retry is the sender's business
(and the outbox in E09-T6's), which is the same contract a 1:1 send has.

## Late join and membership changes

**A new member sees nothing sent before they joined. This is forward secrecy by construction,
and it is a feature.**

There is nothing to backfill even if we wanted to. Every historical message was sealed to the
then-members' keys, one envelope each; no envelope exists that the new member can open, and
producing one would mean a member re-encrypting and re-sending history under their own
signature — which is a *new* message from that member quoting the old one, not history.

The consequences, stated so nobody has to infer them:

- **Joining is a floor, not a filter.** A member added at epoch 4 receives every message fanned
  out at epoch 4 and later, and none from epochs 1–3. Their client's group conversation starts
  empty.
- **Leaving is immediate.** A member removed at epoch 5 is not in the epoch-5 roster, so the
  next fan-out does not include them, and a batch built against epoch 4 is refused rather than
  delivered. There is no cache window; the roster is re-read per request, the same
  immediate-revocation pattern grants and app passwords use.
- **A departed member keeps what they already received.** Messages already delivered were
  decrypted with their own key on their own device. Nothing in this design — or in any design
  without trusted hardware — takes that back.
- **Mid-thread changes need no rekey.** There is no group key to roll. The membership change
  *is* the rekey: the next message is simply sealed to a different set of keys.
- **The sender learns the roster changed.** A membership change between reading the roster and
  posting surfaces as `409 group_epoch_stale`, so a client never silently addresses a stale
  group.

## Wire reference

### `GET /groups/{group}`

Read the signed membership document. Authenticated with the same challenge-signed headers as
an inbox pickup (`X-Poweur-Identity`, `X-Poweur-Challenge`, `X-Poweur-Signature`, optionally
`X-Poweur-Session-Id`), except that the caller need not be hosted on this relay — a member
elsewhere must be able to read the roster of a group they are in.

| Status | Meaning |
|---|---|
| `200` | the caller is in `members` or `admins`; body is the signed `ShareGroup` document |
| `401 unauthorized` | challenge missing, expired, or signature invalid |
| `404 not_found` | no such group here, it is not a group identity, **or** the caller is not associated with it |

### `POST /groups/{group}/messages`

```json
{
  "group": "crew.acme.poweur.net",
  "epoch": 3,
  "envelopes": [ { /* a full message envelope per member */ } ]
}
```

The relay checks, in order: the group is hosted here and its `self.json` verifies against its
own key; `epoch` matches; the batch's recipients are exactly `members` minus the sender; every
envelope carries the same sender, thread and metadata, is encrypted, is ≤ 512 KB and verifies
against the sender's key; the sender is in `members`.

| Status | Meaning |
|---|---|
| `202` | accepted; body carries a per-member delivery breakdown (some members may have failed) |
| `400 invalid_group_message` | batch shape wrong: recipients ≠ members, mismatched sender/thread/metadata, bad thread prefix |
| `401 unauthorized` | an envelope's signature did not verify |
| `403 not_a_member` | the sender is not in `members` |
| `404 not_found` | not a group identity hosted here |
| `409 group_epoch_stale` | membership moved; body carries the current document |
| `413 content_too_large` / `413 group_too_large` | batch over 4 MB, an envelope over 512 KB, or the group over 100 members |
| `503 unavailable` | this relay has no file layer, so it cannot resolve a group |

Response body:

```json
{
  "group": "crew.acme.poweur.net",
  "epoch": 3,
  "delivered": [{"recipient": "bob.example.org", "id": "msg_…", "status": "accepted"}],
  "failed": [{"recipient": "dave.example.net", "id": "msg_…", "status": "forward_failed",
              "detail": "…"}]
}
```

## Clients

```bash
# Send to the group. The CLI reads the roster, resolves each member's
# encryption key, seals N payloads and posts one batch.
poweur group send crew.acme.poweur.net "ship it"
poweur group send crew.acme.poweur.net "let's talk layout" --thread design-review

# Read the group conversation out of local history.
poweur group inbox crew.acme.poweur.net
```

The web app renders a group as one conversation row keyed on the group, with each message
labelled by its actual sender. Replies go back to the same `thread_id`.

## Not in v1

- **Sender keys / MLS.** [Studied](../future/mls-adoption.md), deferred, with the thresholds
  above.
- **Group-addressed 1:1 replies.** Replying to a group message replies to the group. Replying
  privately to one member is an ordinary direct message and the client should say which it is
  doing.
- **Groups as senders.** A group identity holds a key and could sign, but nothing emits
  messages *from* a group in v1.
- **Cross-relay group hosting.** A group lives on one relay. Migration is identity migration.
- **Server-side group history.** The relay spools undelivered envelopes and forgets them on
  pickup; the archive is each member's own `.poweur/private/messages/` (E09-T1).
