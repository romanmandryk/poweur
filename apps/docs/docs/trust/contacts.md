---
id: contacts
sidebar_position: 1
title: Contacts, inbox policy & key pinning
---

# Contacts, inbox policy & key pinning

Poweur's spam answer is **recipient consent**, not reputation: hosted identities are
cheap, so per-identity reputation is weak — instead, your relay only accepts what your
policy allows, and strangers get exactly one knock on the door. This page specifies the
contact model (EPIC-007), the enforcement flow, and the key-pinning trust model.

## The two policy files

Both live in the relay-readable zone (`poweur-sys/relay/`), are owner-written over DAV
(so they sync across devices like any file), schema-validated on write, and are **never
visible to other users**.

**`contacts.json`** ([schema](https://github.com/romanmandryk/poweur/blob/master/conventions/schemas/poweur-sys/contacts.schema.json)):

```json
{"version": 1, "contacts": [
  {"identity": "bob.example.org", "state": "accepted",
   "pinned_key": "ed25519:…", "petname": "Bob", "added_at": "…", "source": "request"}]}
```

States: `requested` (an open request exists), `accepted`, `blocked`.

**`inbox-policy.json`**: `{"version": 1, "mode": "contacts_and_requests"}`

| mode | behavior |
|------|----------|
| `open` | any valid Poweur ID may message (pre-policy behavior; **the default when no file exists**, for compatibility) |
| `contacts_only` | accepted contacts only; everyone else `policy_rejected` — including contact requests |
| `contacts_and_requests` | contacts message normally; a stranger's first `sys.contact.request` lands in the **requests queue**; everything else is rejected until accepted |

Clients SHOULD write `contacts_and_requests` for human identities (`poweur policy set
contacts_and_requests`). Blocked senders are rejected in **every** mode — and receive the
same generic `policy_rejected` as strangers, so a block is indistinguishable from a
closed inbox. Your own identity always reaches your own inbox (multi-device).

## The request lifecycle

```
none ──sys.contact.request──► pending (requests queue, ONE slot per sender)
pending ──recipient accepts──► accepted (both sides write contacts.json, keys pinned)
pending ──recipient ignores──► re-request only after 7-day cooldown
any     ──recipient blocks───► blocked (silent)
```

- The request rides messaging as a typed envelope: `type: "sys.contact.request"`
  (plaintext type — the relay routes on it without reading the E2E-encrypted intro,
  which is capped at 4 KB). The type is **bound into the message signature**
  (`CanonicalMessageTyped`), so it cannot be forged onto a signed message.
- The queue is separate from the inbox (`GET /requests/{identity}`, challenge-signed) —
  requests never pollute the message stream, and `poweur requests` lists them.
- `sys.contact.accept` is only accepted from a peer the recipient lists as `requested` —
  an unsolicited "accept" from a stranger is rejected.
- Enforcement happens on the **recipient's relay**, which is where cross-relay forwarded
  traffic arrives too — a sender's relay cannot bypass policy.

CLI: `poweur contacts request/accept/block/rm/ls`, `poweur requests`, `poweur policy
show/set`.

Web app (EPIC-015 E15-T2): **Contacts** lists the same document with its states and
petnames; **Messages → Requests** merges the relay's request queue with contact requests
that arrived in the inbox — under the default `open` policy the very same envelope is
delivered as a normal typed message, so a tray that read only the queue would be empty
for most users. Accept/Block act there, and a message from someone you hold no entry for
carries a one-tap **Add**.

## Key pinning (the safety-number model)

Accepting (or adding) a contact pins their current signing key in `contacts.json`. On
every `poweur send`, the resolved key is compared against the pin:

- **Match** → send.
- **Mismatch, but the new identity document lists the pinned key in `previous_keys`**
  (a signed rotation statement, PCP-0002) → legitimate rotation: re-pin automatically,
  note to the user.
- **Mismatch with no rotation statement** → **refuse to send**, print both safety
  numbers and both keys, and require explicit `--accept-new-key` after out-of-band
  verification. This is what a compromised relay or registrar swapping a contact's key
  looks like.

The web app runs the same three-way check before every send; the mismatch case is a
blocking dialog showing both safety numbers, and "Trust new key" is its
`--accept-new-key`.

### Safety numbers (the fingerprint format)

A pin is only worth what the out-of-band comparison that bootstrapped it is worth, and
nobody compares 43 characters of base64url. Every Poweur client therefore renders a key
as a **safety number**: four groups of five digits.

```
safety number: 56963 45073 70021 85367
```

Derivation — canonical, identical in Go, `@poweur/client` and the web app, and pinned by
the `fingerprints` conformance vectors:

```
canonical   = "poweur-fingerprint-v1" LF <normalized key string>
digest      = SHA-256(canonical)
group i     = uint40(digest[5i .. 5i+5]) mod 100000, zero-padded to 5 digits
fingerprint = groups 0..3, joined by single spaces
```

The normalized key string keeps its algorithm prefix (`ed25519:…`, `x25519:…`), so a
signing key and an encryption key with identical bytes never share a safety number, and
every base64 variant of one key converges on one value. 20 digits is ≈66 bits — a
second-preimage search an attacker must run against one specific victim's pin.

**Why digits and not emoji.** Emoji short-auth-strings are friendlier on a phone screen
and denser per symbol, but they lose everywhere this protocol needs them to hold:
Poweur's primary comparison surface is a terminal (emoji there range from correct to
double-width-misaligned to tofu, and the CLI cannot tell which it got); the comparison is
usually *spoken*, and digits are pronounceable identically in any language two contacts
share while emoji names are not; digits can be typed back in, searched for in a log, and
written on paper; and an emoji alphabet is a versioned dependency — adding or reordering
one symbol silently changes everybody's fingerprint. Digits need no such table.

Where they appear: `poweur contacts ls`, `poweur contacts add/accept` (at pin time, when
verification is still cheap), `poweur identity lookup`, the send-time mismatch refusal,
the web contact panel, and the web key-mismatch dialog.

Pinning is client-side defense-in-depth: it fails open when contacts are unreachable
(the resolver chain still applies), and it fails **closed** on an actual mismatch.

## Privacy analysis

- Contact lists are sensitive. Readers: the owner's devices and the enforcing relay —
  never other users, never other relays. The E2EE design study (E03-T7) covers the
  longer-term option of hiding them from the relay too; today the relay must read them
  to enforce.
- Whether you blocked someone, and your policy mode, are not observable from rejection
  responses (uniform `policy_rejected`).
- A sender learns only: delivered, rejected-by-policy, or request-queued/cooldown —
  the minimum needed for honest UX.

## Deferred (tracked in EPIC-007)

- `sys.contact.*` client auto-processing (the accept notification is delivered but
  clients handle it manually — both clients pin on accept, neither acts on an inbound
  `sys.contact.accept` by itself).
- E07-T5 abuse pressure: per-sender-relay request metering, shared blocklists,
  `sys.abuse.report` handling (type reserved in the registry), PoW on requests from
  unknown relays (primitive from EPIC-014).
