# EPIC-001 — Web-based identity resolution (`/.well-known/poweur/`)

- **Status:** proposed
- **Priority:** P0 (foundation for everything else)
- **Depends on:** —
- **Unlocks:** EPIC-002 (relay-only registration), EPIC-008 (sign-in)

## Goal

Allow a Poweur ID's public keys, relay endpoint and capabilities to be discovered over HTTPS at
`https://<identity>/.well-known/poweur/…` in addition to DNS TXT records. DNS remains a valid
(and for self-hosters, preferred) publication channel, but it is no longer the only one. This
makes wildcard hosting possible: `*.poweur.net` points at a relay once, and millions of
identities resolve without a single per-user DNS write.

## Background (current code)

- Verification today is DNS-only: `resolveIdentityPublicKey` in
  `apps/api/internal/relay/server.go` falls back to a `_poweur.<identity>` TXT lookup, and the
  CLI does the same in `apps/cli/internal/identity/dns.go` (`LookupDNS`, `LookupEncryptionKey`).
  An earlier HTTP fallback to peer relays was removed as an SSRF vector — the new HTTP path must
  address that concern explicitly (fixed scheme, fixed path, size limits, no redirects to
  private ranges).
- Routing today: the recipient's relay host is found by resolving the identity's A/CNAME record
  (`resolveRelayHost`). With a wildcard A record this part keeps working unchanged — which is
  exactly why wildcard + well-known is a natural fit.
- The docs already anticipate `/.well-known/poweur.json` for sign-in verifier metadata
  (`apps/docs/docs/future/capabilities.md`).

## Design direction

Introduce a signed **Identity Document** as the canonical machine-readable description of an
identity, and define a **resolver chain** every verifier must implement:

1. `https://<identity>/.well-known/poweur/id.json` (HTTPS, TLS-validated)
2. `_poweur.<identity>` / `_poweur-enc.<identity>` DNS TXT records (existing format)

Either source alone is sufficient; when both exist they MUST agree on the signing key, otherwise
resolution fails closed. The identity document is self-signed by the identity key, so the HTTPS
server (typically a relay) cannot tamper with it — TLS protects integrity in transit, while the
signature protects integrity at rest and enables third-party re-serving/caching.

Proposed `id.json` (v1):

```json
{
  "version": 1,
  "identity": "alice.poweur.net",
  "public_key": "ed25519:<base64url>",
  "encryption_public_key": "x25519:<base64url>",
  "relay": "relay.poweur.net",
  "capabilities": ["messaging", "files", "auth"],
  "previous_keys": [],
  "updated_at": "2026-06-12T00:00:00Z",
  "signature": "<base64url ed25519 signature over canonical JSON>"
}
```

Also exposed as plain files for trivial integrations:
`/.well-known/poweur/pubkey` (text, `ed25519:<base64url>`) and
`/.well-known/poweur/enckey` (`x25519:<base64url>`).

## Tasks

### E01-T1 — Specify the Identity Document and resolver chain

Write the normative spec for `id.json`: canonical JSON serialization for signing (sorted keys,
no insignificant whitespace — reuse the canonical-string discipline from
`apps/docs/docs/protocol/message-format.md`), required/optional fields, signature rules, TTL and
caching semantics (`Cache-Control`, max staleness), and the DNS↔HTTPS agreement rule. Define
error behavior: key mismatch between sources, expired documents, malformed signatures.

- [ ] Spec page `apps/docs/docs/protocol/web-identity.md` covering document format, resolver
      chain order, caching, and failure modes
- [ ] Canonicalization rules with test vectors (valid doc, tampered doc, key-mismatch pair)
- [ ] Decide and document the trust rule for `relay` changes (relay endpoint is *advisory*
      routing data, keys are *authoritative* — relay change must be signed by identity key)
- [ ] Security analysis section: SSRF, redirect handling (no redirects followed), response size
      cap (16 KB), content-type requirements, IP-literal identities rejected

**Acceptance:** spec merged with test vectors; reviewed against the existing
`identity-model.md` and `dns-records.md` pages with cross-links.

### E01-T2 — Relay serves `/.well-known/poweur/` for hosted identities

The relay answers well-known requests for any identity it hosts. Because identities are
virtual-hosted on a wildcard (`alice.poweur.net` resolves to the relay), the relay must route by
`Host` header: `GET https://alice.poweur.net/.well-known/poweur/id.json` → alice's document.

- [ ] New handler in `apps/api/internal/relay/` for `/.well-known/poweur/{id.json,pubkey,enckey}`
      keyed on the request `Host`
- [ ] Documents are generated from the identity store and signed at registration time by the
      *client* (relay stores, never signs — it doesn't have the private key)
- [ ] Correct `Cache-Control`, `ETag`, and CORS headers (public, immutable until rotation)
- [ ] `GET /identities/{identity}` response (`IdentityResponse` in
      `apps/api/internal/relay/types.go`) extended to include the signed document
- [ ] Unit tests + integration test in `apps/integration/` covering Host-based routing

**Acceptance:** with a wildcard DNS entry in the fake zone, a registered identity's keys are
fetchable via HTTPS well-known and verify against the registration signature.

### E01-T3 — Resolver-chain client library (Go)

Implement the resolver chain once, in a shared package consumable by both the relay and the CLI
(currently the lookup logic is duplicated between `apps/api/internal/dns/resolver.go` and
`apps/cli/internal/identity/dns.go`).

- [ ] New module/package (e.g. `pkg/identity-resolver`) with `Resolve(ctx, identity) (IdentityDoc, error)`
- [ ] HTTPS fetch with hard limits: 5s timeout, 16 KB body cap, no redirects, HTTPS only,
      reject private/loopback IPs unless explicitly configured (test mode)
- [ ] DNS TXT path folded in behind the same interface; agreement check when both resolve
- [ ] Pluggable cache with TTL honoring `Cache-Control` (relay reuses its existing
      `cacheMu`/TTL pattern from `server.go`)
- [ ] Swap `resolveIdentityPublicKey` (relay) and `LookupEncryptionKey` (CLI) to the new
      resolver behind a feature flag/env (`POWEUR_RESOLVER=chain|dns`)

**Acceptance:** integration suite passes with `POWEUR_RESOLVER=chain` for both DNS-published and
web-published identities; SSRF tests prove private-range fetches are refused.

### E01-T4 — Web client & CLI use the resolver chain

- [ ] `apps/web/js/api.js`: resolve recipient keys via well-known with DNS-over-HTTPS TXT as
      fallback (browser can't do raw DNS); document the trade-off
- [ ] CLI `poweur lookup <identity>` command prints the full resolution result (source used,
      keys, relay, capabilities) — extends the existing DNS status output
- [ ] Docs: update `apps/docs/docs/clients/` for the new lookup behavior

**Acceptance:** sending a message from web/CLI to a web-resolved identity works end to end in
the integration environment.

### E01-T5 — Key rotation & history in the Identity Document

Today key rotation is only sketched in `future/capabilities.md` (`_poweur-rotate` TXT). The
identity document makes rotation tractable: `previous_keys` carries old keys with validity
windows, and a rotation statement signed by the *old* key proves continuity.

- [ ] Spec: rotation statement format, grace-period semantics, verifier behavior for messages
      signed by a previous key within its window
- [ ] Relay: accept a rotation request (new doc signed by old key + new key), update stored doc
- [ ] CLI: `poweur key rotate` generating the statement and updating both web doc and
      (if DNS-published) TXT records
- [ ] Integration test: rotate a key, verify old-signed sessions are rejected after grace period

**Acceptance:** an identity rotates keys without losing its name; contacts resolve and verify
the new key with continuity proof.
