# EPIC-002 — Relay-only registration, wildcard identities & durable relay storage

- **Status:** partially complete (core shipped 2026-07)
- **Priority:** P0
- **Depends on:** EPIC-001
- **Unlocks:** EPIC-003 (file storage), EPIC-009 (message persistence)

## Progress

| Task | Status | Notes |
|------|--------|-------|
| E02-T1 Durable FS store | **done** (partial) | Identity docs durable; inbox/acks/sessions stay memory-only by design until EPIC-009 |
| E02-T2 Hosted registration | **done** | CLI `--hosted`, web hosted checkbox, integration test |
| E02-T3 Name policy & abuse | **partial** | Reserved names + registration rate limit done; PoW/invite/lease **open** |
| E02-T4 Export & migration | **open** | Deferred — stays on this epic |
| E02-T5 Ops hardening | **open** | Deferred — stays on this epic; quotas expand in EPIC-003 |

**Deferred in the 2026-07 core pass** (plan scope lock): E02-T4, E02-T5, and E02-T3 extras
(PoW / invite-code / lease expiry). Inbox persistence was explicitly deferred to
[EPIC-009](EPIC-009-messaging-upgrades.md) (sessions remain memory-only).

## Goal

Let anyone claim `name.poweur.net` by talking to the relay only — no DNS provider token, no zone
writes. A single wildcard record (`*.poweur.net A <relay>`) routes all hosted identities; keys
are published via the well-known endpoint from EPIC-001. To support this, the relay gets its
first durable storage backend: a per-identity directory on the relay's filesystem, which later
epics grow into the user's synced "home".

## Background (current code)

- Registration (`handleIdentitiesPost` in `apps/api/internal/relay/server.go`) currently
  *requires* `dns_provider` + `dns_token` and performs zone writes through
  `apps/api/internal/dns/providers_cloudflare.go` / `providers_hetzner.go`. This caps the system
  at "people who own a domain and hand an API token to a relay" — fine for self-hosters, a
  non-starter for mass onboarding.
- All relay state is in-memory: `storage.NewIdentityStore()`, `NewInboxStore()`,
  `NewSessionStore()` etc. (`apps/api/internal/storage/`). A restart wipes identities that were
  never written to DNS, so relay-only registration *requires* durability.
- Rate limiting exists (`apps/api/internal/ratelimit/`) and must extend to registration abuse.

## Design direction

- **Two registration modes**, both producing the same signed Identity Document:
  - *self-hosted*: existing flow, user owns DNS, relay optionally writes records (kept as-is);
  - *hosted*: user proves key possession (existing identity-signed admin envelope, see
    `IdentityRequest` in `types.go`), relay allocates `<name>.<relay-domain>` and persists the
    document — zero DNS interaction.
- **Storage layout**: one directory per identity under a configurable root, e.g.
  `$POWEUR_DATA/identities/alice.poweur.net/`. The identity document lives at
  `poweur-sys/public/id.json` inside it — the exact layout EPIC-006 specifies — so the
  well-known handler is just a static-file read. Plain files + a small metadata DB
  (SQLite or bbolt) for indexes; the filesystem is the source of truth so it can later be
  synced to user devices (EPIC-004).
- **Names are leases backed by keys**: the name→key binding is in the signed document;
  the relay enforces uniqueness and an allocation policy, but cannot silently swap keys
  (clients pin keys, see EPIC-007 trust model).

## Tasks

### E02-T1 — Storage abstraction & filesystem backend — DONE (identity docs)

Introduce a `Store` interface that the relay uses for durable state, with the per-identity
directory layout as the primary implementation.

- [x] Define approach: identity documents durable; **inbox / acks / sessions memory-only**
      (documented; inbox durability → EPIC-009)
- [x] Filesystem backend: `identities/<sanitized-id>/poweur-sys/public/id.json`, atomic writes
      (write-temp + rename), path sanitization
- [ ] SQLite (or bbolt) index for fast existence/uniqueness checks and listing —
      **remaining polish** (in-memory map loaded from disk is enough for now)
- [x] Config: `POWEUR_DATA` dir in `apps/api/internal/config/config.go`; in-memory backend kept
      for tests
- [x] Migration note: relays restart without losing hosted identities (integration test:
      register → restart server → resolve + receive message)

**Acceptance:** relay restart preserves hosted identities and their documents; path sanitization
tests show no traversal.

### E02-T2 — Hosted registration endpoint (no DNS writes) — DONE

- [x] Extend `POST /identities`: when `dns_provider`/`dns_token` are absent and the requested
      identity is under the relay's configured hosted domain(s) (`HOSTED_DOMAINS=poweur.net`),
      run the hosted flow: verify identity signature, check name availability, persist signed
      identity document
- [x] Client submits the *signed identity document* in the registration body; relay
      validates signature against the embedded key before accepting
- [x] Keep DNS mode fully working; route by request shape + config
- [x] CLI: `poweur identity create name.poweur.net --hosted` (no token args); web app
      registration flow updated (`apps/web/js/app.js`)
- [x] Update docs: `apps/docs/docs/relay/api-reference.md`, web-identity protocol page
- [ ] `dns-management.md` wording pass ("optional" now) — **remaining**

**Acceptance:** with only a wildcard A record in the fake DNS zone, two fresh identities
register against the relay and exchange E2E-encrypted messages; integration test added
(`TestINT_HOSTED_01`).

### E02-T3 — Name allocation policy & abuse controls — PARTIAL

Mass registration without payment or DNS friction invites squatting and spam-farming.

- [x] Policy module: reserved names (www, admin, relay, mail, …), min/max length, label charset
      (`packages/identity/names.go`)
- [x] Rate limits on registration (reuse `ratelimit` keyed as `register:<identity>`)
- [ ] Proof-of-work or invite-code option behind config for public relays (pluggable
      `RegistrationGate` interface; ship PoW + invite-code + open implementations) — **open**
- [ ] Inactivity/lease policy decision: do hosted names expire? Document the answer and the
      grace/renewal mechanism — **open** (default for now: no expiry)

**Acceptance:** policy unit tests; a public-relay configuration that demonstrably throttles a
registration flood in an integration test (basic rate limit exists; flood integration test /
PoW still open).

### E02-T4 — Identity export & relay migration — OPEN

Hosted users must not be locked in. Because the identity's name contains the relay's domain,
*name portability* differs for hosted vs self-hosted IDs — be explicit about both.

**Stays on this epic** (not moved elsewhere).

- [ ] `GET /identities/{identity}/export` (owner-signed request): returns the full identity
      directory as a tar/zip — document, and later files/messages
- [ ] Self-hosted IDs: document the move-relay procedure (update A/CNAME + `relay` field in
      id.json, signed) and implement `poweur relay set <host>` in the CLI
- [ ] Hosted IDs: spec a signed "moved" tombstone (`id.json` gains `moved_to` field) so
      `alice.poweur.net` can permanently redirect resolvers to `alice.newhome.org`
- [ ] Integration test: identity moves relays, a contact's next message resolves and routes to
      the new relay (leverages existing `resolveRelayHost` DNS caching — verify TTL behavior)

**Acceptance:** documented + tested migration path for both modes; export produces an archive a
new relay can import.

### E02-T5 — Operational hardening for stateful relays — OPEN

The relay was disposable; now it holds user data.

**Stays on this epic.** Per-identity quotas deepen in [EPIC-003](EPIC-003-file-storage-webdav.md).

- [ ] Backup/restore tooling: snapshot `$POWEUR_DATA` consistently, restore drill documented in
      `deploy/`
- [ ] Disk-usage metrics and per-identity quota scaffolding (enforced for real in EPIC-003)
- [ ] Update `docker-compose.prod.yml` + `apps/infra` Terraform for a persistent volume
- [ ] Health endpoint reports storage status (writable, free space) — extend `HealthResponse`

**Acceptance:** prod compose file mounts a volume; kill-and-restore drill documented and
exercised once in CI (integration test with data dir swap).
