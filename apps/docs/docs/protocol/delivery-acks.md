---
id: delivery-acks
sidebar_position: 7
title: Delivery Acks
---

# Delivery Acks (Three-Tick Model)

Poweur ID tracks message delivery in WhatsApp-style ticks so a sender can
tell, after the fact, whether a message reached the recipient's relay,
whether the recipient's client actually decrypted it, and whether something
went wrong along the way. A third tick distinguishes delivery to a decrypting
client from the user opening the conversation.

## Tick states

| Tick | State | Source | Trigger |
|------|-------|--------|---------|
| Tick 1 | `delivered_recipient_relay` | HTTP | The recipient's relay returned `202 Accepted` to `POST /messages` |
| Tick 2 | `delivered_client` | Recipient client | The recipient successfully decrypted the message and emitted a signed ack |
| Tick 3 | `read` | Recipient client | The user opened the conversation and their inbox policy permits a receipt |

Tick 1 is a property of the HTTP exchange — there is no on-wire ack object.
Ticks 2 and 3 are signed envelopes carried back over `POST /acks`.

The CLI also tracks two local-only states for its own bookkeeping:

- `queued` — write to the journal happened but the network round-trip has
  not yet completed (or the relay has not been reached).
- `failed` — the send pipeline gave up. Sticky: once a message lands in
  `failed` no further state transitions are recorded for that id.

## Ack envelope

Acks are short, signed JSON objects. `state` is `delivered_client` or `read`.

```json
{
  "type":       "ack",
  "id":         "ack_01j...",
  "message_id": "msg_01j...",
  "state":      "delivered_client",
  "sender":     "bob.example.org",
  "recipient":  "alice.poweur.net",
  "timestamp":  "2026-04-26T12:04:05Z",
  "signature":  "<base64>",
  "session_id": "sess_...",
  "session_proof": { ... }
}
```

- `sender` is whoever produced the ack (the recipient of the original
  message — Bob in the table above).
- `recipient` is always the original message's `sender` — the party who
  cares whether tick 2 ever arrives.
- `message_id` is the client-assigned id from the message envelope (see
  [Message Format](/protocol/message-format)). Because it is bound into
  the message's canonical signing string, the message id is itself
  authenticated.
- `session_id` and `session_proof` follow the same rules as on `POST
  /messages`: present when the ack is signed by a session key, omitted
  when the ack is signed directly by the long-lived identity key.

## Canonical signing string

```
ack
<id>
<message_id>
<state>
<sender>
<recipient>
<timestamp>
session:<session_id>
```

The `session:` line is omitted (not blank) when the ack is identity-
signed. The signature is base64 (RFC 4648 §4) of the 64-byte Ed25519
output, exactly like message envelopes.

## Routing

Acks ride the same plumbing as messages, including the relay's
[at-least-one-local rule](/relay/api-reference#at-least-one-local-rule):

1. Bob's client decrypts the message.
2. Bob's client builds the ack and POSTs it directly to **Alice's** home
   relay (DNS-resolved from `alice.poweur.net`).
3. Alice's home relay verifies the signature, applies rate limits,
   confirms that at least one of `sender` or `recipient` is locally
   hosted (Alice is local, so the rule is satisfied), and stores the ack
   keyed by `recipient` (= Alice).
4. The next time Alice runs `poweur inbox`, the relay drains both her
   `messages` and her `acks` arrays in a single round-trip. The CLI
   advances the local pending journal to `delivered_client` for any
   referenced message ids.

When Bob is on a different relay than Alice and prefers to hide his IP
from Alice's relay, he can opt into routing the ack via his own home
relay (the same `--via-home-relay` privacy proxy that exists for sends).
That path is structurally identical: Bob's client posts to Bob's relay,
which accepts because Bob is locally hosted as the ack's `sender`, then
forwards to Alice's relay over HTTP.

## Failure semantics

- **Stuck at tick 1** — the recipient relay accepted the message but the
  recipient never polled their inbox. Sender's journal stays at
  `delivered_recipient_relay` indefinitely. This is the "single tick" UX
  in WhatsApp terms and is the steady state when a recipient is offline.
- **Stuck before tick 1** — the recipient relay never returned 202
  (network error, rate limit, encryption rejection). The CLI records a
  `failed` entry with the underlying reason; this state is sticky.
- **Forged ack** — the relay's signature check (over the canonical
  string above) rejects acks signed by keys it cannot resolve back to the
  declared `sender` identity. The sender's journal does NOT advance, so
  an attacker who guesses a message id cannot fake a tick 2.
- **No negative acks in v1** — there is no `failed_decrypt` ack state.
  When the recipient's client cannot decrypt (e.g. their X25519 key is
  missing) it surfaces an error locally and does NOT emit an ack at all.
  Reading the absence of an ack as "not yet decrypted" is the same UX as
  the offline case above.

## Local journal

The CLI persists every state transition to a per-identity append-only
JSON-Lines file at `~/.poweur/pending/<identity>.jsonl`. One line per
transition; reads collapse the log into the latest state per
`message_id`. The `poweur messages status [--id <msg_id>] [--json]`
command renders this view with WhatsApp-style tick glyphs (`·` queued,
`✓` tick 1, `✓✓` tick 2, `✓✓✓` read, `✗` failed). See
[CLI Reference](/clients/cli-reference) for usage.

Read receipts default on for compatibility. `poweur-sys/relay/inbox-policy.json`
may set `read_receipts.enabled` false globally or list identities under
`read_receipts.disabled_for`; inability to load policy fails closed and emits no read receipt.

## Related

- [Message Format](/protocol/message-format)
- [API Reference](/relay/api-reference)
- [Routing & Send Path](/protocol/routing)
- [Rate Limiting](/protocol/rate-limiting)
