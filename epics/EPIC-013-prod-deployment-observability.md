# EPIC-013 — Production deployment & observability

- **Status:** implemented (operator rollout steps remain; T5 deferred)
- **Priority:** P1
- **Depends on:** EPIC-002; instruments the relay features added by other epics
- **Unlocks:** reliable releases, searchable errors, private analytics and public growth

## Progress

| Task | Status | Notes |
|------|--------|-------|
| E13-T1 Standard telemetry export & metrics | done | OTel SDK, bounded queue, opt-in export and private metric labels |
| E13-T2 Structured logs, action events & consent modes | done | DAV consent preference, web/native settings, both CLIs and SDK; withdrawal rechecked |
| E13-T3 CI/CD hardening | done | CI gates build; Deploy is manual dispatch: digest + `git reset` + `compose up` + `/health`. No rollback/smoke/baseline. |
| E13-T4 External stack, dashboards & alerts | done | Alloy/Loki, private + aggregate dashboards, alerts, external GitHub probe; operator enables public link/SMTP |
| E13-T5 Federated ecosystem metrics | deferred | Aggregate reporting from other operators, after the basic setup |
| E13-T6 Release versions on every surface | done | Patch bumps, `GET /` build metadata, CLI `--version`, Settings → About |

## Goal

A small-business setup: one VM, Docker Compose, and an optional external telemetry endpoint
on the relay. Send logs, metrics and server action events to a separate intake process at
`ingest.poweur.net` (or `metrics.poweur.net`). Keep it on the same VM initially; moving the
stack later changes configuration, not relay code. Use Grafana for private diagnostics and
analytics, plus a public growth dashboard.

This epic includes analytics and error tracking; no separate analytics epic is needed.
Legal assessment is outside this implementation plan. The two data modes below are product
requirements, not a claim that hashed data is anonymous or exempt from privacy obligations.

## Background (repository review, 2026-09-10)

- `deploy/infra/docker-compose.yml` already runs Caddy, PostgreSQL for Grafana's own
  metadata, Prometheus and Grafana. It has no log backend or telemetry intake process.
- Prometheus's relay scrape job is commented out; Grafana has a datasource but no
  provisioned dashboards. The relay has no metrics endpoint or structured export.
- Existing `log.Printf` calls include identities in stream/spool messages. DAV's
  owner-facing access log is a separate feature; do not ingest that file into analytics.
- The deployment workflow builds/pushes to GHCR and restarts Compose, but does not depend
  on tests, pin the deployed image or perform a health-checked rollback.
- Instrument the actual current product: hosted identities, sessions/recovery/enrollment,
  messaging and SSE, anonymous/PoW actions, files/sync/sharing and background expiry jobs.
  Some older roadmap descriptions predate these implementations.

## Design direction

### Small external stack, standard wire format

```text
Relay -- OTLP/HTTP --> Alloy intake at ingest.poweur.net
                       ├─ metrics --> Prometheus
                       └─ logs/action events --> Loki
                                  Grafana
                       private ops + public aggregate growth
```

Add **Grafana Alloy and Loki** to the existing infra Compose deployment. Alloy is the
intake process; no custom ingestion application or endpoint inside the relay. Use the
OpenTelemetry Go SDK and **OTLP/HTTP protobuf** so backends can be changed later. Action
events are structured OTLP log records, not a separate event protocol.

Use Alloy's OTLP metrics-to-Prometheus exporter and remote-write to internal Prometheus; send logs to Loki. This keeps the relay push-only and avoids a second relay
`/metrics` export path. Select compatible supported versions during implementation.

Keep GHCR, the existing Grafana instance and its database. No event warehouse, separate
public Grafana deployment, distributed tracing or custom analytics service in v1. Loki
queries and Grafana panels provide basic error grouping; a dedicated issue workflow can
be added later if needed. Use Alloy rather than the old Promtail suggestion.

### Configuration and reliability

Implemented environment variables (additional proxy/environment controls are documented in `apps/docs/docs/relay/observability.md`):

| Variable | Default | Meaning |
|----------|---------|---------|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | empty | Master switch; e.g. `https://ingest.poweur.net`; OTLP base URL |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | `http/protobuf` | Standard export format; only supported protocol in v1 |
| `OTEL_EXPORTER_OTLP_HEADERS` | empty | Intake authorization, supplied through deployment secrets |
| `LOG_LEVEL` | `info` | Diagnostic threshold; INFO/WARN/ERROR exported by default |
| `TELEMETRY_HASH_KEY` | empty | Secret HMAC key, required when export is enabled; independent of identity keys |

The base endpoint receives `/v1/logs` and `/v1/metrics`. No URL means no outbound telemetry,
including SDK auto-export through signal-specific overrides. Local structured logs still
work. Action events are independent of `LOG_LEVEL`, so raising it does not disable analytics.
Never log authorization headers, the hash key or endpoint credentials.

Use SDK batching with fixed reasonable defaults rather than many tuning knobs: bounded
memory queue, three-second export timeout, one attempt per log batch and a bounded shutdown flush.
Collector failure must not block requests or make relay health fail. Count dropped records
and log exporter failures locally without recursively exporting them. Delivery is best effort;
no disk queue or exactly-once promise. Record gaps rather than pretending outages were zeros.

Protect intake with HTTPS and a single operator ingest credential, and keep Loki/Prometheus
ports internal. Separate process/Compose configuration lets the stack move to another VM;
same-VM deployment still shares the VM's failure risk. The stack never mounts `POWEUR_DATA`.

### Two data modes

Apply the mode per **authenticated actor**, not globally for the relay:

| Field | No consent / unknown | Consent granted |
|-------|----------------------|-----------------|
| Actor identity | HMAC-SHA256 hash of canonical identity | Raw canonical identity |
| Client IP | Omitted entirely, including IP hashes | Included when associated with that consenting actor's request |
| Timestamp | Included | Included |
| Action, result, duration, error code | Included | Included |

Use a stable **keyed hash**, not plain SHA256 of a guessable handle. Scope it to this relay
operator, keep the secret on the relay and use a different key for dev/test. Hash-key rotation
starts a new analytics history; no need to reconstruct old links. Hashed identities plus
timestamps are **pseudonymous**, and allow per-identity activity analysis in the private view.
They do not identify unique humans: one person or bot can own several identities.

Missing, refused, withdrawn or unverified consent uses the hashed/no-IP mode. Anonymous or
failed-auth requests have no authenticated actor: omit identity and IP rather than trusting
a claimed name. A consenting recipient does not authorize recording the sender's raw identity.
Other identity references are hashed regardless of actor consent; prefer omitting them unless
needed for the event. Background jobs with no actor carry no client IP.

Prepare a small consent interface in the relay (`consentGranted(actor)`), defaulting to false.
Store an optional per-identity preference with version and update timestamp through the
existing authenticated settings conventions; no arbitrary client header can grant consent.
Add a simple analytics switch to web/native settings and a corresponding SDK/CLI operation.
Until that preference is available, every actor uses the no-consent mode. Recheck consent
before exporting queued records; withdrawal changes future export immediately. Historical
records retain their original mode until normal expiry; selective historical deletion is a
follow-up, not silently promised by the switch.

For IPs behind Caddy/Cloudflare, trust forwarded headers only from configured trusted proxies;
otherwise use the direct peer address. Do not mistake another relay's IP for the user's IP in
forwarded messages. Do not propagate consent or client IP between relays.

Apply the same transformation before local logging and export. Raw identities must not leak
through error strings, paths or Host headers in no-consent mode. Use route templates and bounded
error codes; sanitize stacks. Always exclude message/file contents, ciphertext, filenames,
credentials, key material, recovery/enrollment secrets, cookies and full request/response dumps.
IP logging in the CDN/proxy is configured separately; this mode describes relay telemetry.

### Events, metrics and counting

One HTTP middleware records requests by route template, method, status and duration, including
rejections and unmatched routes. Add domain hooks for business outcomes and background jobs:

- Registration/availability, resolution, migration/rotation, sessions and authentication.
- Keystore/enrollment/recovery outcomes, never their secret values.
- Message submission, forwarding, enqueue, pickup/consume/ack/expiry, anonymous/PoW outcomes.
- Contact/policy and validated sharing changes, DAV operations, sync/uploads and quota failures.
- SSE open/close, startup/shutdown, pruning and storage failures.

Keep a short action inventory in the ops docs, extended when endpoints are added. Suggested
record fields: `timestamp`, `kind`, `action`, `outcome`, `error_code`, `route`, `duration_ms`,
`actor_id`, `identity_mode`, optional `client_ip`, and build version. Unknown names/reasons map
to bounded values. Use static error code + sanitized stack frames to group errors by release.
Expected policy/PoW/auth rejections are action outcomes, not automatic server errors.

Metrics are aggregate counters/histograms/gauges only. **No raw or hashed identity, IP, request
ID or raw path in Prometheus or Loki index labels.** Actor fields belong in structured log
metadata/body for private queries. Useful metrics: requests/latency/errors, successful
registrations, accepted submissions, forwarding failures, uploads, quota denials, hosted
identities, storage usage, inbox depth and telemetry drops.

Count registration after creation, not availability checks. Keep origin message submissions
separate from receive/forward/pickup counts. Label them accepted submissions, not unique
messages or human reads. Polling and SSE heartbeats are not growth actions. Public totals
exclude dev/test. Handle process counter resets and missing data; this is not a billing ledger.

## Tasks

### E13-T1 — Optional OTLP exporter and metrics

- [x] Add a small `apps/api/internal/telemetry` package using the OTel SDK, injectable for tests.
- [x] Implement configuration, no-op when unset, batching, bounded failure handling and flush.
- [x] Instrument HTTP request metrics and domain counters/gauges described above.
- [x] Test no-export mode, invalid configuration, unavailable/slow intake, queue overflow,
      metric cardinality and shutdown. Preserve SSE flushing and DAV behavior in middleware.

**Acceptance:** setting one endpoint sends metrics to a test collector; unsetting it makes no
telemetry network calls. Collector failure does not break registration/send/inbox/files.

### E13-T2 — Structured logs, server events and consent modes

- [x] Replace operational `log.Printf` calls with JSON `slog` plus the OTLP logging bridge.
- [x] Cover the action inventory, including background failures and expiry; export normal
      action events even if the diagnostic log threshold is raised.
- [x] Implement keyed identity transformation and consent seam/preference. Add the simple
      settings control and SDK/CLI access; default false until implemented.
- [x] Apply trusted-proxy IP handling only in consent mode; redact before every sink.
- [x] Enable Docker log rotation; keep external logs/events 14 days and metrics 30 days
      initially. These are practical initial defaults, configurable in the stack.
- [x] Test raw/hash/no-IP behavior, forged identity/consent, withdrawal while queued,
      multi-party actions and secrets embedded in errors. Add real-relay integration coverage.

**Acceptance:** the same authenticated action produces raw actor/IP/timestamp with consent,
hashed actor/no IP/timestamp without it. Anonymous and unverified callers export neither actor
nor IP. All current action families have coverage; logs contain no content or credentials.

### E13-T3 — CI/CD hardening (tests gate, pinned deploys, rollback)

- [x] `deploy.yml`: add a `test` job (Go suites + `apps/integration`; web tests when the
      SPA changed) that `build` **needs** — today a red master still deploys
- [x] Deploy by digest/sha, not `:latest`: `RELAY_IMAGE` is the GHCR digest from the
      build job; `docker compose up` on the VM checkout. Auto-rollback and baseline
      snapshots were removed after they restored Grafana 11 over a working 12 stack.
- [x] Post-deploy gate: `wget` `/health` inside the relay container and require
      `versionHash` to match `RELEASE_SHA`. Failure is red but does not compose an
      older stack. Authenticated CLI smoke is not part of deploy. (`bash -s` +
      `docker compose exec` previously swallowed the remote script after Caddy
      reload, so GitHub stayed green while prod froze on 66e969ba / relay 0.1.2.)
- [x] Build the web app + docs artifacts in CI if/when they gain a build step (today
      `apps/web` is static — volume-mounted; keep that, but note it in the ops doc)
- [x] `deploy/README.md` (or extend `BACKUP.md` into `deploy/OPS.md`): the full runbook —
      bootstrap, deploy, rollback, backup/restore, where secrets live (Ansible vars +
      GitHub secrets), metrics/dashboards URLs

**Acceptance:** a PR that breaks tests cannot reach prod; deployed digest visible on the VM;
failed `/health` leaves the compose that just started (no restore of an older stack).

### E13-T4 — External stack, dashboards and alerts

- [x] Add Alloy + Loki to `deploy/infra`; configure the OTLP intake host, TLS/auth,
      internal backend networking, persistent volumes, retention and supported image pins.
- [x] Connect Alloy remote-write to internal Prometheus; provision Loki in Grafana.
- [x] Provision **Relay Ops**: traffic, latency, error groups/builds, policy rejections,
      forwarding, spool, storage and exporter failures. Under login, query individual events
      with hashed or raw actors according to the mode recorded at collection time.
- [x] Provision **Growth**: registrations, hosted identities, accepted submissions/day,
      completed uploads and storage. Private panels may show active identities from logs,
      clearly labeled as identities rather than people.
- [x] Provide only the aggregate Growth dashboard for operator-enabled sharing through Grafana's externally shared
      dashboard feature. No event tables, identity/IP fields, log links or private annotations;
      keep global anonymous access off. Use fixed aggregate queries and test logged-out
      requests cannot retrieve private data. Daily totals are enough; no live identity timeline.
- [x] Add node_exporter for host/disk visibility. Alert on sustained 5xx, missing relay/ingest
      data, disk pressure and queue loss; route alerts independently of relay messaging.
- [x] Add a health probe outside the VM so a VM outage is detectable. Document credentials,
      backup/restore and moving the stack by changing the endpoint/DNS.

**Acceptance:** a clean deployment displays real relay metrics and searchable errors; private
views require login, public access exposes aggregates only. An intake outage does not affect
relay requests and a simulated VM/relay outage is detected. No extra public dashboard service.

### E13-T5 — Federated ecosystem metrics — deferred

Other relay operators reporting ecosystem growth is a later feature, separate from this
operator's external telemetry setup. Do not build a federation collector or relay identity
protocol as part of T1–T4.

- [ ] Specify explicit opt-in aggregate reporting; never share raw logs, identities or IPs.
- [ ] Define source authentication, deduplication/reset behavior, rate limits and simple
      reviewed/unverified source separation. A source signature does not prove counts truthful.
- [ ] Add an ecosystem aggregate dashboard without double-counting send/receive/forward stages.

**Acceptance (later):** two independently configured relays contribute bounded aggregates;
disabling reporting stops it. Untrusted submissions cannot silently inflate reviewed totals.

### E13-T6 — Release versions on every surface

Ship a patch version and a build stamp with every change to a shippable package, and show them where an operator or a user can actually read them. Go modules do not store this module's semver in `go.mod`; bump the `internal/buildinfo.Version` constant instead.

- [x] Agent docs (`AGENTS.md`, `CLAUDE.md`): when a change updates the relay, Go CLI, TS SDK, web app or mobile shell, bump that package's **patch** version in the same change set and stamp the UTC build time (`YYYY-MM-DD HH:MM`).
- [x] Relay `GET /` (JSON) and `GET /health` expose `version`, `buildTime`, `versionHash`. Semver lives in `apps/api/internal/buildinfo`; hash/time come from VCS info, `VERSION_HASH`/`BUILD_TIME`, or image build-args — never overload `VERSION` with a git sha.
- [x] Go CLI `poweur version` / `--version` / `-v` prints the CLI semver and build stamp. TS CLI already has `poweur version`; include the SDK build time.
- [x] Settings → About lists app version, `@poweur/client` version, and the relay of the active identity (or the current identity-host on the web), each with a smaller-font build timestamp.

**Acceptance:** a patched web/mobile build shows its own version and the connected relay's version in About; `curl -H 'Accept: application/json' $RELAY/` returns the three release fields; `poweur --version` (Go and TS) prints a semver.

## Verification and delivery

Ship T1/T2 then T4; T3 can proceed independently. T6 (release versions) is done and independent of the telemetry work. T5 stays deferred. Keep this as one epic.
Run relay unit tests and `apps/integration` for implementation changes, including a capture
collector and a collector-outage scenario. Consent changes touching clients require the usual
web tests, client tests/build/typecheck, canonical specs/vectors when needed, and re-vendoring.
Implementation is complete for T1–T4. Production rollout is an operator step: follow
`deploy/OPS.md` to import existing database secrets, retire the old systemd units, then
run Deploy (manual). Configure SMTP, GitHub failure notifications and the public Growth
share link manually.

Verification: relay/CLI/identity and real-relay integration suites; TS SDK unit/live-relay/
interop tests; web Vitest and Playwright; telemetry race and stalled-collector
shutdown tests. Real isolated Docker smoke (`stack-smoke.mjs`) verifies authenticated intake,
Prometheus/Loki queries, private access denial, public aggregate sharing and an actual
Grafana alert after Alloy stops. Secret-generation tests cover `setup-observability.sh`.
No Python runtime is required.

Implementation choices: same-VM relay uses authenticated `http://infra-alloy:4318` on the
private bridge, so DNS is optional until remote export. The optional public intake hostname
is `ingest.poweur.net` with Caddy TLS. Storage state is sampled at startup and every five
minutes. HTTP events cover resolution/availability, DAV/sync reads and rejection outcomes;
domain hooks cover transitions, forwarding, streams and spool expiry. Sessions and cache
pruning remain represented by the periodic maintenance operation rather than per-entry
identity events. No new cryptographic signing format or federation protocol is introduced.

## Non-goals

- Kubernetes, HA, SSO/enterprise role systems, separate public analytics infrastructure.
- Custom analytics warehouse, tracing/Tempo, session replay, billing-grade event delivery.
- Message/content collection, cross-relay user tracking or automatically enabled federation.
- Legal compliance certification or a consent-management platform in this implementation.

## Technical references

- [OTLP exporter configuration](https://opentelemetry.io/docs/specs/otel/protocol/exporter/)
- [Grafana Alloy](https://grafana.com/docs/alloy/latest/)
- [Loki OpenTelemetry ingestion](https://grafana.com/docs/loki/latest/send-data/otel/)
- [Grafana externally shared dashboards](https://grafana.com/docs/grafana/latest/visualizations/dashboards/share-dashboards-panels/shared-dashboards/)
