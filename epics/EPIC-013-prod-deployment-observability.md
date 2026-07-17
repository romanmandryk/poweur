# EPIC-013 — Production deployment & observability

- **Status:** proposed
- **Priority:** P1 (ops debt compounds; metrics are also the project's public growth story)
- **Depends on:** EPIC-002 (relay ops); interacts with every epic that adds endpoints
- **Unlocks:** confident releases, public growth dashboards, federated ecosystem metrics

## Goal

Keep the deployment story **boring on purpose** — GitHub Actions + one Hetzner VM + docker
compose, nothing else — while making it *safe* (tests gate deploys, health-checked rollout,
one-command rollback) and making the relay **observable**: structured logs, Prometheus
metrics for everything the relay does on behalf of users, Grafana dashboards provisioned as
code, and a public growth dashboard. Second half: **federated ecosystem metrics** — third-
party relay operators share aggregate counters (never logs) with a central
`analytics.poweur.net` collector by default-config, with abuse resistance built on the
identity system we already have.

## Background (what exists today — more than you'd think)

- **CI/CD already runs**: `.github/workflows/deploy.yml` on master push builds the
  `apps/api` image → pushes `ghcr.io/<owner>/poweur-relay:{latest,sha}` → SSHes to the
  Hetzner VM → `git pull` + `docker compose -f docker-compose.prod.yml pull && up -d`.
  Gaps: **no tests run before deploying**, the compose file pins `:latest` (deployed sha is
  not what compose records → no rollback story), no post-deploy health gate, web/docs not
  built in CI.
- **Infra compose exists** (`deploy/infra/docker-compose.yml`): Caddy (Cloudflare
  terminates TLS), Postgres (Grafana backing store), **Prometheus** (30 d retention;
  `deploy/infra/prometheus/prometheus.yml` has a commented-out `poweur-relay` scrape job
  waiting for a `/metrics` endpoint), **Grafana** at grafana.poweur.net. Ansible playbook +
  `server-bootstrap.sh` + `BACKUP.md` cover provisioning.
- **The relay has no `/metrics` and no structured logging.** `log.Printf` here and there;
  the only per-user records are the owner-facing DAV access logs (EPIC-003) — those are a
  user feature, not ops observability.

### Registry decision (answering the standing question)

Hetzner does **not** offer a managed container registry. Options considered:
self-hosted `registry:2` on the VM (another service to run, back up and secure — negative
value at this scale) vs **GitHub Container Registry (ghcr.io)**: free for public repos,
already wired into the workflow, images live next to the code. **Decision: stay on
ghcr.io.** Revisit only if GitHub rate limits ever bite (the VM pulls a few times a day —
they won't).

Third-party SaaS observability: **Grafana Cloud's free tier** (10k metric series, 50 GB
logs, 14 d retention) would work and is the fallback if VM maintenance becomes a burden —
but Prometheus+Grafana are already provisioned on the VM, self-hosting keeps data ownership
aligned with the project's whole thesis, and the public-dashboard need is served by
Grafana's built-in public dashboards. **Decision: self-host (existing infra); note Grafana
Cloud as the escape hatch.**

## Design direction

- **Metrics are aggregate counters only — never per-identity labels.** A
  `messages_sent_total{identity="alice"}` label is a privacy leak *and* a cardinality bomb.
  Per-user visibility belongs to the user (their access log / changes journal), not to ops.
- **One HTTP middleware, not N instrumented handlers**: wrap the router once for
  request count / duration / status by route pattern; add explicit domain counters only
  where a route doesn't say enough (rejection *reasons*, forwarding, policy denials).
- `/metrics` binds to a **separate internal port** (`METRICS_ADDR`, default `:9091`),
  reachable only on the compose network — never through Caddy. Public sharing happens via
  Grafana dashboards, not raw Prometheus.
- **Logs are operational, metrics are shareable.** Logs stay on the VM (json + docker
  rotation); only metrics ever leave a relay, and only as aggregates.
- Federated metrics trust model: **a relay's identity is its domain** — reuse the resolver
  chain instead of inventing API keys. Garbage can't be *prevented* at a public endpoint,
  but it can be made costly (domain ownership) and non-polluting (verified/unverified
  split, clamping, quarantine). See E13-T5.

## Tasks

### E13-T1 — Relay metrics instrumentation (`/metrics`)

- [ ] Add `prometheus/client_golang`; `internal/metrics` package with a `promhttp` server
      on `METRICS_ADDR` (default `:9091`, empty = disabled); config + docs
- [ ] Router middleware: `http_requests_total{route,method,code}` and
      `http_request_duration_seconds{route}` using the mux *pattern* (never the raw path —
      identities in paths must not become label values)
- [ ] Domain counters (names indicative):
      `registrations_total{result}`, `sessions_created_total`, `auth_failures_total{surface}`,
      `messages_total{direction=sent|received|forwarded,result=accepted|rejected}`,
      `message_rejections_total{reason=signature|rate_limit|policy|size|anon}`,
      `dav_ops_total{method,root,result}`, `sync_requests_total{endpoint}`,
      `uploads_total{result}`, `shares_active` (gauge), `grant_denials_total`,
      `rate_limit_rejections_total{bucket}`, `dav_tokens_minted_total{kind=owner|visitor}`
- [ ] Gauges from existing state: hosted identities count, storage bytes used (sum over
      identities, sampled by a slow ticker — reuse `runPruner` cadence), inbox depth
- [ ] Uncomment the relay scrape job in `deploy/infra/prometheus/prometheus.yml`; add the
      metrics port to `docker-compose.prod.yml` (infra_net only)
- [ ] Unit tests: middleware records the route pattern, not the path; rejection reasons
      increment the right counter (table-driven against the running test relay)

**Acceptance:** `curl :9091/metrics` on a dev relay shows request + domain metrics;
Prometheus on the VM scrapes it; no metric has an identity-valued label (grep-able test).

### E13-T2 — Structured logging

- [ ] Replace `log.Printf` with `log/slog` JSON handler to stdout; request-scoped fields
      (route, status, duration, remote relay for forwards); level via `LOG_LEVEL`
- [ ] Log hygiene rules documented in the ops doc: no message bodies, no file contents, no
      tokens/passwords, identities only where operationally required (auth failures,
      registration) — logs are *not* shared off-VM, but write them as if they might leak
- [ ] Docker log rotation in both compose files (`max-size`, `max-file`) — today logs grow
      unbounded
- [ ] Optional (defer unless free): Loki + promtail container in `deploy/infra` for
      searchable logs in Grafana; decide after living with `docker logs` + rotation

**Acceptance:** relay logs are one-JSON-object-per-line; a grep of a day's logs finds no
message content or credentials; disk usage bounded.

### E13-T3 — CI/CD hardening (tests gate, pinned deploys, rollback)

- [ ] `deploy.yml`: add a `test` job (Go suites + `apps/integration`; web tests when the
      SPA changed) that `build` **needs** — today a red master still deploys
- [ ] Deploy by digest/sha, not `:latest`: compose reads `RELAY_IMAGE` from `.env` on the
      VM; the deploy step writes the new sha there → `docker compose ps` shows exactly
      what runs, and rollback = write previous sha + `up -d` (document as a one-liner;
      keep last N shas in ghcr via retention policy)
- [ ] Post-deploy gate: workflow curls `/health` (and one authenticated smoke call) after
      `up -d`; failure exits non-zero so the run is red and auto-rolls back to the prior sha
- [ ] Build the web app + docs artifacts in CI if/when they gain a build step (today
      `apps/web` is static — volume-mounted; keep that, but note it in the ops doc)
- [ ] `deploy/README.md` (or extend `BACKUP.md` into `deploy/OPS.md`): the full runbook —
      bootstrap, deploy, rollback, backup/restore, where secrets live (Ansible vars +
      GitHub secrets), metrics/dashboards URLs

**Acceptance:** a PR that breaks tests cannot reach prod; deployed sha visible on the VM;
rollback rehearsed once and documented; failed health gate leaves the previous version
running.

### E13-T4 — Grafana dashboards as code + alerting + public growth dashboard

- [ ] Dashboard provisioning dir (`deploy/infra/grafana/provisioning/dashboards/` +
      JSON models in-repo): **Relay Ops** (request rates, latencies, error rates,
      rejections by reason, rate-limit hits, disk/quota) and **Growth** (identities,
      messages/day, active DAV/sync users, shares created, storage) — growth uses only
      counters that are safe to show publicly
- [ ] Node/host metrics: add `node_exporter` (+ `cadvisor` optional) to `deploy/infra`
      so disk-full is visible before it takes the relay down
- [ ] Grafana alerting (provisioned): relay down (scrape fail), 5xx rate, disk > 80 %,
      cert/domain resolution failure from the blackbox check below; route to email or a
      messaging webhook —**alerts must not depend on the relay being up**
- [ ] Blackbox probe (blackbox_exporter): external-view checks of `https://relay.poweur.net/health`
      and one hosted identity's `/.well-known/poweur/id.json`
- [ ] Public growth dashboard: Grafana **public dashboard** feature on the Growth board
      (read-only, no login), linked from the project README/site

**Acceptance:** both dashboards render from a fresh `docker compose up` with zero manual
clicks; a simulated relay outage fires an alert; the growth dashboard is reachable
logged-out.

### E13-T5 — Federated ecosystem metrics (`analytics.poweur.net`) — design + v1

Third-party relay operators should be able to share **aggregate metrics only** (never
logs, never identities) so the ecosystem's growth is visible in one place. Sharing must be
easy (default config), honest about being optional (documented opt-out, `METRICS_FEDERATION=off`),
and abuse-aware.

- [ ] Spec `apps/docs/docs/ops/federated-metrics.md`: reporting payload = small JSON of
      **monotonic counters + a few gauges** (hosted identities, messages sent/received/
      forwarded, rejections, DAV/sync request totals, storage bytes, relay version),
      reported every 15 min; explicitly enumerate what is *never* sent (logs, paths,
      identity names, IPs)
- [ ] **Sender authenticity — reuse the identity machinery, not API keys:** a submission is
      signed by the relay's key and names the relay's public address; the collector
      verifies by fetching `https://<relay-address>/.well-known/poweur/relay.json`
      (relay identity document — small addition to the well-known handler) and checking
      the signature. Sybil cost = a real domain + reachable HTTPS endpoint per fake relay.
      First submission puts a relay in **quarantine** (recorded, not displayed) for N days
- [ ] **Garbage resistance** (accept that a public endpoint can't fully prevent lying —
      contain it): per-relay rate limits; plausibility clamps (max delta per interval,
      e.g. a 10-identity relay "suddenly" reporting 10 M messages gets clamped + flagged);
      monotonicity checks on counters; totals always displayed as **verified relays**
      (manually promoted from quarantine) vs **reported, unverified** — garbage can
      inflate only the unverified line, and a poisoned relay is one delete away.
      Document the residual risk honestly: a determined liar with a domain can inflate
      unverified numbers; that line is labeled accordingly
- [ ] Collector v1: small Go service (`apps/analytics` or a handler on the main relay
      behind `analytics.poweur.net` via Caddy) that verifies, clamps, stores into
      Prometheus via remote-write or a simple exposition endpoint Prometheus scrapes
      (`/metrics` with `relay` label = relay address; bounded label cardinality =
      registered relays only)
- [ ] Relay-side reporter: `METRICS_FEDERATION` config (`on` by default **with a startup
      log line + docs stating exactly what is shared and how to disable**), jittered
      reporting loop, no retries beyond next tick (fire-and-forget)
- [ ] Ecosystem dashboard in Grafana fed by collector data: relays online, identities and
      message volume across the network, verified vs unverified split; public-shared like
      the Growth board

**Acceptance:** two dev relays report to a local collector; a forged submission (wrong
signature, unreachable claimed address, absurd deltas) is rejected or quarantined; the
ecosystem dashboard shows both relays; disabling via config verifiably stops reporting.

## Non-goals

- Kubernetes, Terraform, multi-VM HA — one VM + compose is the point (revisit at real load).
- Log aggregation off the VM (Loki is optional and local; logs never leave the operator).
- Billing-grade accuracy in federated metrics — the numbers are directional by design.
