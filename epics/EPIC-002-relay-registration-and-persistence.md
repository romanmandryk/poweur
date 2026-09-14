# EPIC-002 — Relay-only registration, wildcard identities & durable relay storage

- **Status:** complete (PoW gate shipped via EPIC-014: `REGISTRATION_GATE=pow`); one polish item open — E02-T1's SQLite/bbolt lookup index is not built
- **Priority:** P0
- **Depends on:** EPIC-001
- **Unlocks:** EPIC-003 (file storage), EPIC-009 (message persistence)

## Progress

| Task | Status | Notes |
|------|--------|-------|
| E02-T1 Durable FS store | **done** | Identity docs durable; SQLite index deferred polish; inbox → EPIC-009 |
| E02-T2 Hosted registration | **done** | CLI/web + docs |
| E02-T3 Name policy & abuse | **done** | Reserved names, rate limit, invite gate, lease=no-expiry; PoW gate shipped via EPIC-014 (`REGISTRATION_GATE=pow`, `TestINT_ANON_02`) |
| E02-T4 Export & migration | **done** | export API/CLI, `relay set`, `moved_to` + resolve follow |
| E02-T5 Ops hardening | **done** | volume, health storage, backup doc + restore integration |

Inbox persistence remains [EPIC-009](EPIC-009-messaging-upgrades.md). The PoW registration
gate shipped via the shared challenge primitive in
[EPIC-014](EPIC-014-anonymous-messaging-challenges.md) (E14-T5): `REGISTRATION_GATE=pow`.

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
      **deferred polish** (in-memory map loaded from disk is enough; not blocking)
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
- [x] `dns-management.md` wording pass (hosted vs DNS dual-mode; lease = no expiry)

**Acceptance:** with only a wildcard A record in the fake DNS zone, two fresh identities
register against the relay and exchange E2E-encrypted messages; integration test added
(`TestINT_HOSTED_01`).

### E02-T3 — Name allocation policy & abuse controls — DONE

- [x] Policy module: reserved names, min/max length, charset
- [x] Rate limits on registration (`register:<identity>` + `register:flood`)
- [x] `RegistrationGate`: `open` | `invite` (`REGISTRATION_GATE`, `REGISTRATION_INVITE_CODES`)
- [x] Proof-of-work gate — shipped via EPIC-014 (`REGISTRATION_GATE=pow`,
      `REGISTRATION_POW_BITS`; CLI auto-solves; `TestINT_ANON_02_PowRegistrationGate`)
- [x] Lease decision: **no automatic expiry** (documented in dns-management / web-identity)

**Tests:** `TestINT_REG_01_InviteRequired`, `TestINT_REG_02_RegistrationFloodRateLimit`.

### E02-T4 — Identity export & relay migration — DONE

- [x] `POST /identities/{identity}/export` (owner-signed) → tar.gz; CLI `poweur identity export`
- [x] Self-hosted: `poweur relay set <url>` + docs (update DNS + `relay` field)
- [x] Hosted: `moved_to` on identity document; resolver follows once
- [x] Integration: `TestINT_EXPORT_01_IdentityExportArchive`

### E02-T5 — Operational hardening for stateful relays — DONE

- [x] Backup/restore: [`deploy/BACKUP.md`](../deploy/BACKUP.md); `TestINT_OPS_01_RestoreDataDir`
- [x] Quota scaffolding: `MAX_IDENTITY_BYTES` (enforce in EPIC-003)
- [x] `docker-compose.prod.yml` mounts `poweur_data` → `POWEUR_DATA=/data`
- [x] `/health` reports `storage` (writable, free_bytes)
