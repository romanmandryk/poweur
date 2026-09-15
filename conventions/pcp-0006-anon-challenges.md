# PCP-0006 — Anonymous ingress & sender challenges

- **Status:** experimental (shipped with EPIC-014)
- **Owner:** poweur core
- **Registry entries:** `anonymous` / `stranger_challenge` fields of the inbox-policy
  schema; challenge envelope

## Convention

- Inbox-policy `anonymous` block (schema `inbox-policy.schema.json`): opt-in for
  unsigned senders with `challenge` = `none | pow | verified | payment`, recipient
  difficulty dial `pow_bits`, and hard caps `max_bytes` / `max_per_day`. Absent = deny.
- Challenge envelope (HTTP 428): `{"error":"challenge_required","challenge":{"type",
  "algo","token","bits","expires_at"}}` — clients render unknown-but-well-formed types
  gracefully. `verified` and `payment` are reserved slots.
- PoW scheme `sha256-lead0`: solution nonce s.t. `sha256(token + "." + nonce)` has ≥
  `bits` leading zero bits; tokens are HMAC-sealed `{purpose, bits, expires_at,
  nonce}`, purpose-bound, single-use, 10-min TTL. Reference: `packages/identity/pow.go`
  + `solvePow` in `packages/client-ts`.
- Anonymous messages: envelope without `sender`/`signature`, E2E-encrypted with an
  ephemeral key, delivered to a dedicated queue (`GET /anon/{identity}`), never the
  signed inbox.

Full spec: `apps/docs/docs/trust/anonymous-and-challenges.md`.

## Compatibility

Relays that don't implement anonymous ingress reject unsigned envelopes as invalid —
identical to the pre-PCP behavior. New challenge types extend the envelope's `type`
without breaking existing clients (they refuse and explain).
