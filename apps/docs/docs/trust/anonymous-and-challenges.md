---
id: anonymous-and-challenges
sidebar_position: 2
title: Anonymous messaging & sender challenges
---

# Anonymous messaging & sender challenges

Recipients can *opt in* to messages from senders **without any Poweur identity** —
web visitors, contact forms, one-off tips — behind a recipient-chosen cost ladder
(EPIC-014). The default is, and stays, **deny**. This page specifies the policy block,
the challenge protocol, and the proof-of-work scheme.

## The policy block

`poweur-sys/relay/inbox-policy.json` gains an `anonymous` object (absent = deny):

```json
{"version": 1, "mode": "contacts_and_requests",
 "anonymous": {"allow": true, "challenge": "pow", "pow_bits": 20,
               "max_bytes": 4096, "max_per_day": 20}}
```

| field | meaning | default |
|-------|---------|---------|
| `allow` | accept unsigned messages at all | `false` |
| `challenge` | `none` \| `pow` \| `verified` \| `payment` | `none` |
| `pow_bits` | PoW difficulty (leading zero bits; each +1 doubles the work) | relay default (16), clamped to [8, 30] |
| `max_bytes` | payload cap per anonymous message | 4096 |
| `max_per_day` | accepted anonymous messages per day | 20 |

CLI: `poweur policy set contacts_only --anon-allow --anon-challenge pow --anon-bits 20`.

`verified` (third-party attestation over an ID) and `payment` (HTTP-402-style) are
**designed slots**: the relay answers with the typed challenge envelope so clients can
explain the requirement, but v1 cannot verify them (`501 not_implemented` on attempts).

## The flow

An anonymous message is a normal envelope with **no `sender` and no `signature`** —
still with a client-generated `id`, RFC3339 `timestamp`, and **mandatory E2E
encryption** (an ephemeral X25519 key needs no identity; there is no plaintext excuse).

```
POST /messages  {id, recipient, timestamp, payload, encryption}
 → 403 policy_rejected                     recipient hasn't opted in (the default)
 → 428 challenge_required                  + challenge envelope (below)
 → 202 {"status": "anon_accepted"}         challenge "none", or valid solution attached

challenge envelope:
 {"error": "challenge_required",
  "challenge": {"type": "pow", "algo": "sha256-lead0", "token": "…",
                "bits": 20, "expires_at": "…"}}

retry:  same message + {"challenge_token": "…", "challenge_solution": "…"}
 → 202 | 403 challenge_failed | 403 challenge_replayed | 413 | 429 anon_daily_cap
```

Accepted messages land in a **dedicated anonymous queue** (`GET /anon/{identity}`,
challenge-signed owner drain — same auth as the inbox), never the signed inbox or the
contact-requests queue. Clients MUST render them visibly differently
(`poweur anon` prints an `ANONYMOUS` marker and a trust warning; the web app's
**Anonymous** tray drops the avatar and sender line entirely and offers no reply or
add-contact affordance — there is nobody to reply to or add).

## Proof-of-work (`sha256-lead0`)

Find an ASCII `solution` such that `sha256(token + "." + solution)` has at least
`bits` leading zero bits. Reference implementations: `packages/identity/pow.go`
(solve + verify) and `packages/client-ts/src/pow.ts` (browser solve, chunked for UI
responsiveness).

**Measured cost** (Go, single Apple-Silicon core; browser JS is ~5–10× slower):

| bits | expected time | feel |
|------|---------------|------|
| 8 | ~0.6 ms | free |
| 12 | ~2.5 ms | free |
| 16 | ~40 ms | unnoticeable (relay default) |
| 20 | ~0.6 s | "one moment" |
| 24 | ~9 s | deliberate effort |
| 26 | ~36 s | hostile-sender territory |

Recipient dials are clamped into **[8, 30] bits**. Above ~20 bits, phone browsers get
uncomfortable — UIs should say so on the slider. The web app's policy panel does: it
labels each stop with browser-side seconds (the table above ×5–10) and warns past 20.

### Token mechanics

- Tokens are **HMAC-sealed and stateless**: `base64url(claim) + "." + base64url(hmac)`
  where the claim is `{purpose, bits, expires_at, nonce}`. A challenge flood therefore
  stores nothing server-side. The sealing key is memory-only; a relay restart
  invalidates outstanding challenges (clients re-request).
- `purpose` binds a token to one surface (`msg:<recipient>`, `registration`) — a
  solution mined for bob cannot be spent on alice, nor a message token on registration.
- Solutions are **single-use**: the claim nonce enters a seen-cache until token expiry;
  replays get `403 challenge_replayed`.
- Tokens expire after 10 minutes.

### Difficulty under load

The relay auto-raises the effective difficulty floor by +4 bits while challenge
issuance exceeds ~120/minute — the Hashcash answer to distributed flooding. Hard caps
stay on regardless: per-IP rate limits, per-recipient `max_bytes` and `max_per_day`.

## Second consumers of the primitive

- **Hosted registration** (`REGISTRATION_GATE=pow`, closes the EPIC-002 deferral):
  `GET /auth/pow?purpose=registration` → solve → include `pow_token`/`pow_solution`
  in the registration request. The CLI does this automatically;
  `REGISTRATION_POW_BITS` sets the difficulty.
- **Contact requests from unknown relays** (E07-T5): the seam is specified —
  `stranger_challenge` / `stranger_pow_bits` policy fields gate the request queue for
  *identified* non-contacts — implementation lands with the EPIC-007 abuse-pressure
  pass.

## Threat notes

- **PoW rate-limits; it does not authenticate.** Nothing about a solved challenge says
  who the sender is — hence the separate queue, the caps, and the UX warning.
- **Pre-computation:** tokens bind recipient + expiry; nothing useful can be mined in
  advance of a challenge.
- **GPU asymmetry:** a GPU miner outruns the dial by orders of magnitude. The caps
  (`max_per_day`, per-IP limits, load floor) bound the damage; sha256 is kept anyway
  because the verifier stays trivial and the JS/native gap acceptable. Memory-hard PoW
  is a revisit-if-observed item.
- **Privacy:** the relay sees the anonymous sender's IP (used for rate limiting, logged
  per EPIC-013 hygiene rules — counters, not identities). Payload content is E2E
  encrypted to the recipient.
