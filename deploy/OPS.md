# Relay and observability operations

E13 uses the existing VM and Grafana database. Relay → authenticated OTLP/HTTP → Alloy → Prometheus (metrics) and Loki (logs), with Grafana for both dashboards. An optional second OTLP sink (`TELEMETRY_OTLP_SECONDARY_ENDPOINT`) can fan the same privacy-filtered events to another vendor (currently the Better Stack collector on `infra_net`). Tracking is disabled when both OTLP endpoints are empty. Production setup enables Alloy explicitly.

`setup-observability.sh` uses `openssl` to generate secrets. Ansible installs it; on an existing VM, `sudo apt-get install openssl` once is enough. Node.js 22 is used only for CI/development tests. Production deploy is `git reset` plus `docker compose up`.

Local deployment checks:

```sh
node --test deploy/tests/*.test.mjs
node deploy/tests/stack-smoke.mjs  # requires Docker; isolated containers and volumes
```

## One-time upgrade checklist (existing VM)

1. **Back up first:** the relay volume, Grafana/Postgres volumes, and existing `/opt/infra/.env`. Keep credentials private. See Backup below. Use the existing `infra` and `poweur` Compose project names so volumes retain their names.
2. Get this revision's deployment scripts onto `/opt/apps/poweur`. As an administrator, import existing credentials, then assign the generated file to the deploy user:

   ```sh
   cd /opt/apps/poweur
   sudo bash deploy/setup-observability.sh --file .observability.env --import-env /opt/infra/.env
   sudo chown poweur:poweur .observability.env
   ```

   **Do not generate replacement Grafana database passwords on an existing database.** The helper preserves imported/existing settings, creates independent intake and hashing secrets, and writes mode `0600`. Re-running it preserves all values. Keep `.observability.env` and `apps/api/.env.prod` on the VM, outside git and GitHub logs. Back up both encrypted.
3. Retire the old foreground Compose systemd units during a short maintenance window; otherwise they can restart old configuration over a new release:

   ```sh
   sudo systemctl disable --now poweur-app.service poweur-infra.service
   sudo systemctl enable docker.service
   # Start the current compose files (Grafana 12 stack), not an old snapshot:
   sudo docker compose -p infra --env-file .observability.env -f deploy/infra/docker-compose.yml up -d
   sudo docker compose -p poweur --env-file .observability.env -f docker-compose.prod.yml up -d
   ```

   Docker's `restart: unless-stopped` now handles reboot. Do not restart the legacy units or run `docker compose up` from the old checkout or `/opt/infra`. Ansible now prepares the host and secrets; release Actions own application startup.
4. In GitHub Actions secrets configure `DEPLOY_HOST`, `DEPLOY_SSH_KEY` and **`DEPLOY_KNOWN_HOSTS`**. The last value is the server's known_hosts entry, verified against its SSH host-key fingerprint through your provider console. Existing server→GitHub read access must still work. GHCR uses the workflow's token; no image password is committed.
5. Before rollout, ensure `grafana.poweur.net` is routed to Grafana (HTTP origin via Caddy, or DNS-only / per-host Full strict). A push to `master` (or manual dispatch) runs **Deploy**: relay image, `git reset --hard` on the VM, `docker compose up`. No smoke identity. A red `/health` check does not compose an older stack.
6. Set `GRAFANA_SMTP_ENABLED`, `GRAFANA_SMTP_HOST`, `GRAFANA_SMTP_USER`, `GRAFANA_SMTP_PASSWORD`, `GRAFANA_SMTP_FROM_ADDRESS` and `ALERT_EMAIL` in `.observability.env`. Redeploy and send a test notification in Grafana → Alerting → Contact points. Alerts exist without SMTP, but email cannot arrive until configured. The GitHub **External relay health** schedule is paused (see Actions minutes below). Same-VM Grafana cannot report a total VM outage; Better Stack uptime checks can.
7. In Grafana, open **Poweur Growth**, choose **Share → Share externally**, and enable a public link. Share only that generated link. Keep anonymous access disabled. **Poweur Relay Ops**, Explore and Loki remain behind login. The public dashboard has aggregate Prometheus panels only; do not add log panels or actor fields to it.

## DNS: no new record required for the same VM

The generated default endpoint is `http://infra-alloy:4318` on the private Docker bridge, authenticated with the intake credential. No `ingest.poweur.net` DNS record is needed for this path. Prometheus, Loki and Alloy have no host-published ports.

For a remote relay/stack, add **A/AAAA `ingest.poweur.net` pointing to the intake VM**, or a **CNAME to an existing DNS-only hostname that points to it**. Caddy routes `/v1/logs` and `/v1/metrics` and automatically obtains HTTPS certificates; DNS must point to the VM and inbound 80/443 must be reachable. Caddy does **not** create DNS records. The configured reserved names prevent identity registration; reservation is unrelated to DNS creation.

If proxying ingest through Cloudflare, use a **per-host** Full (strict) origin rule, as for Grafana. Do not change the whole zone's TLS mode without migrating the existing HTTP-origin identity hosts. A DNS-only ingest record avoids that complication. Set `OTEL_EXPORTER_OTLP_ENDPOINT=https://ingest.poweur.net` and `TELEMETRY_ALLOW_HTTP=0` for remote export; preserve the intake credentials. `metrics.poweur.net` is reserved but is not a configured intake alias.

Caddy trusts Cloudflare's published IP ranges and rewrites X-Forwarded-For to the resolved client IP. Deploy sets `TELEMETRY_TRUSTED_PROXIES` to Caddy's current Docker bridge address. If you recreate Caddy by hand, compose-up the relay again so it picks up the new address. Review Cloudflare range changes periodically. CDN request logs are separate from relay consent mode.

## Routine deployment

Push to `master` (or manual dispatch) runs Deploy: GHCR digest, `git reset --hard` on `/opt/apps/poweur`, `docker compose up` for `infra` then `poweur`. `GET /health` and `docker inspect poweur-relay` identify the running image. There is no smoke step, `.releases` pointer, or rollback. Leftover `.releases/` or `.smoke/` on the VM can be deleted.

**Actions minutes.** Automatic runs are build and deploy only. CI (Go, `@poweur/client`, web, Playwright, observability) does not run on push or pull request. The OAuth bridge deploy skips its tests the same way. Both deploy jobs run when the image build succeeded, including when the test job was skipped. **External relay health** is not on a cron.

| Want | Do |
|---|---|
| Full CI once | Actions → **CI** → Run workflow |
| Tests before this deploy | Actions → **Deploy** (or **Deploy OAuth bridge**) → Run workflow → **Run tests** |
| Tests before every deploy again | Repository variable `RUN_CI` = `true` (Settings → Secrets and variables → Actions → Variables). No commit. |
| Pull-request CI again | Uncomment `pull_request` in `.github/workflows/ci.yml`. Leave `push` commented so master does not run the suite twice. |
| External health cron again | Uncomment `schedule` in `.github/workflows/health-monitor.yml`. Every 15 minutes is about 2,900 minutes/month. |

**Web app:** the relay image carries the built client at `/web` (`WEB_STATIC_DIR`) and serves it at `/app/` on every host; nothing is mounted from the checkout.

## Fresh VM

Run the Ansible bootstrap, finish its GitHub SSH keys and read-access setup, and initialize `.observability.env` using the helper. For the initial boot only, select a **CI-tested digest** from GHCR and start infra and relay with the shared secrets:

```sh
cd /opt/apps/poweur
export RELAY_IMAGE=ghcr.io/YOUR_OWNER/poweur-relay@sha256:YOUR_TESTED_DIGEST
export RELAY_ENV_FILE=/opt/apps/poweur/apps/api/.env.prod
docker compose -p infra --env-file .observability.env -f deploy/infra/docker-compose.yml up -d
docker compose -p poweur --env-file .observability.env -f docker-compose.prod.yml up -d
```

Then push to `master` (or dispatch Deploy). Caddy/Grafana DNS and existing Cloudflare identity routing still need the original host setup. Do not expose internal backend ports.

## Dashboards, retention and failures

- **Poweur Host:** node-exporter CPU/memory/load/disk/network plus the blackbox probe of `https://relay.poweur.net/health`. Same-VM Grafana cannot report a total VM outage. The GitHub probe is manual until its schedule is restored; Better Stack uptime can cover that gap.
- **Optional Better Stack overlay** (server side only; easy to remove): host collector in `deploy/betterstack/` (needs `deploy/betterstack/.env`, gitignored). Relay dual-export goes straight to the Better Stack OpenTelemetry source: `TELEMETRY_OTLP_SECONDARY_ENDPOINT=https://<ingesting host>` and `TELEMETRY_OTLP_SECONDARY_HEADERS=Authorization=Bearer%20<source token>` in `.observability.env` (the collector's config is managed from the Better Stack dashboard and has no OTLP intake). Relay panics and HTTP 5xx go to Better Stack Errors via `SENTRY_DSN` (Sentry-compatible DSN, `sentry-go`; no tracing). Uptime: create the HTTP checks in `deploy/betterstack/uptime.json` in the Better Stack UI, and/or set `TELEMETRY_UPTIME_URL` to a Heartbeat URL. No browser loads anything from Better Stack. To drop the vendor: `docker compose ... down` and unset the env vars and `SENTRY_DSN`.
- **Browser telemetry (Grafana Faro, first-party):** the web app, the mobile shell, the OAuth bridge's pages, the website and the docs send page views, errors and web vitals to `/faro/collect` on their own origin. Caddy's `faro` snippet forwards it to Alloy's `faro.receiver` (port 12347, internal) without the client IP, and answers CORS only for the mobile shell (`capacitor://localhost`, `https://localhost`). Alloy labels it `{source="faro", kind, app}` and redacts any `*.poweur.net` from entries without `user_id` before Loki (14-day retention). Anonymous by default: `@poweur/faro` scrubs IDs, domains, emails, URL queries and fragments in the browser and stores nothing there. A user's ID is attached only after they tick Settings → Diagnostics → "Include my ID". Dashboard: **Poweur Web: analytics & errors** (`deploy/infra/grafana/dashboards/poweur-web.json`, generated by `deploy/infra/grafana/scripts/web-dashboard.py`). Switches: `FARO_COLLECT_URL` on the relay (default `/faro/collect` in `docker-compose.prod.yml`; empty turns it off), `OAUTH_FARO_URL` on the bridge, and the `<script src="…/faro.js">` tags on the static sites (rebuild with `pnpm --filter @poweur/faro bundle`).
- **Poweur Ops:** traffic/errors/latency, forwarding outcomes, inbox/storage pressure, telemetry loss and searchable JSON logs. Query `actor_id` in Loki after login; values reflect consent at collection time. Loki index labels contain service/environment only, never actor or IP.
- **Poweur Growth:** public, built-in-public board: identity curve with daily registrations, new this week, messages in the last 24h, an hourly message pulse and 48h heartbeat strip by envelope type (chat/app/anonymous/`sys.*`), a 7-day type mix, profile completion (name/avatar/bio/links/language), most-changed settings, inbox-policy choices, opt-ins, and storage/uploads. Every panel reads a `poweur_growth_*` recording rule; `deploy/tests/growth-dashboard.test.mjs` enforces that. Counts describe identities/actions, not unique people or read receipts. Development/test traffic is excluded. Counter increases handle normal restart resets; lost exports and retention mean these are estimates, not a financial ledger. No-data remains a gap, not a fake zero.
- Local container logs rotate at 3 × 10 MB per container. Loki retains 14 days, Prometheus 30 days. Change the committed retention configuration if needed and monitor disk capacity. Historical consented records remain until retention expires; switching off affects subsequent exports and does not delete history.
- Relay export is best effort: a bounded 512-record queue, batches of 128, one-second flush, three-second request timeout, one attempt per log batch, and a bounded shutdown flush. Queue overflow/export failures increment loss metrics. Collector outage does not reject relay requests. Alloy adds its own backend buffering; watch its logs/metrics as well.
- Provisioned alerts cover relay health, missing telemetry, Alloy, sustained 5xx, disk pressure and dropped records. SMTP sends independently of relay messaging. A total VM outage is invisible to same-VM Grafana; the GitHub probe only sees it when someone runs **External relay health**, or when its schedule is turned back on.

## Operator IDs (`support.poweur.net` and other reserved names)

Reserved names (`support`, `help`, `admin`, …) and handles shorter than `NAME_MIN_LEN` can't be
claimed by users, but the operator can create them. Once created they are ordinary IDs:
anyone can message them.

1. On the server, add `OPERATOR_TOKEN=<openssl rand -hex 32>` to `apps/api/.env.prod`
   and recreate the relay (`docker compose ... up -d relay`).
2. On your laptop, keys are generated locally and never touch the server:

   ```bash
   POWEUR_OPERATOR_TOKEN=<token> poweur identity create support.poweur.net --hosted --from-seed --relay https://poweur.net
   ```

   It prints the **recovery kit** (seed and 24-word mnemonic). Store it in your password
   manager. It is the only way back, and the relay can't recover it.
3. Use it in a browser: open `https://support.poweur.net/app/`, choose **Add this device**, and
   approve the code from the laptop with `poweur key approve <code> --use-identity support.poweur.net`.
4. An agent or support system gets it with `poweur key recover support.poweur.net --seed <kit>`
   (or `@poweur/client` with the seed).
5. Set `QUOTA_CONTACT=support.poweur.net` so a full drive tells people to message it.
6. Remove `OPERATOR_TOKEN` again when you're done creating names.

## Backup, restore and moving the stack

Production uses Hetzner's automatic server backups (nightly, 7 days). An off-site encrypted
restic backup to a Hetzner Storage Box is built but off until configured: see
[BACKUP.md](BACKUP.md) (`deploy/backup/`, installed by the Ansible playbook). Loki and Prometheus data are not
backed up. The relay volume is the relay's whole durable state (storage v2): encrypted drives
under `drives/`, and the identity index, undelivered mail and key backups under `relay/`. Never run
`docker compose down -v` in production.

### Storage v2 layout and the v1 migration

`POWEUR_DATA` holds `drives/<id>/…` (journal, snapshots, versions, pages, chunks and the
`.poweur` system files) and `relay/{identities,spool,keystore,group-shares,group-rosters}/` plus
`relay/storage-quotas.json` (quota overrides: `docker exec poweur-relay /relay quotas`, `quotas set
<id> 2GiB`, `quotas unset <id>`; picked up within a minute). The relay writes nothing else. With `STORAGE_PROVIDER=s3`
the same keys live in the bucket and `POWEUR_DATA` is not needed. Chunks and journal segments are
immutable once written, so a live backup is consistent up to the last few seconds of commits.

A relay volume still in the v1 layout (`identities/<id>/poweur-sys/…`, `spool/`, `keystore/`) is
converted once, with the relay stopped:

```bash
docker stop poweur-relay
docker run --rm -v poweur_poweur_data:/data --entrypoint /relay "$RELAY_IMAGE" migrate-v1 --data /data --dry-run
docker run --rm -v poweur_poweur_data:/data --entrypoint /relay "$RELAY_IMAGE" migrate-v1 --data /data
docker start poweur-relay
```

To move a relay from its disk to an S3 bucket, stop it, copy every object into the bucket (idempotent;
each copy is read back and compared), then switch the relay's environment to S3 and start it:

```bash
docker stop poweur-relay
docker run --rm -v poweur_poweur_data:/data --env-file apps/api/.env.prod -e STORAGE_PROVIDER=s3 \
  --entrypoint /relay "$RELAY_IMAGE" copy-store --from /data
# add STORAGE_PROVIDER=s3, S3_ENDPOINT, S3_BUCKET, S3_REGION, S3_ACCESS_KEY, S3_SECRET_KEY and
# S3_PRESIGN=0 (Hetzner does not enforce presigned checksums) to apps/api/.env.prod, then:
docker compose -p poweur --env-file .observability.env -f docker-compose.prod.yml up -d relay
```

Afterwards the relay keeps nothing on the volume; the old `drives/` and `relay/` on it can go once
the bucket-backed relay is verified. Production moved to the Hetzner bucket `poweur` (hel1) on
2026-09-29. New Hetzner credentials can be refused by some gateways for several minutes after
creation; wait until a burst of requests all succeeds before cutting over.

It moves identity documents, profiles and avatars, capabilities, contacts, policy, analytics,
devices, connected apps, group rosters, key backups and undelivered mail; v1 files, shares, links
and history are not migrated. Rerunning is safe. The v1 trees are renamed `*.v1-backup`; delete them
once the new relay has been verified. Production was migrated on 2026-09-29 (7 identities).

To move observability, copy only infra volumes/configuration/secrets to the new VM, configure ingest DNS/TLS, and change the relay endpoint to HTTPS. Do not copy or mount relay `POWEUR_DATA` into the analytics stack. Keep the HMAC key on the relay stable if you want historical hashed actors to stay linkable; rotating it intentionally starts new pseudonyms. Keep the Grafana DB credentials consistent when moving its database.

## Website and docs (`tmpwww.poweur.org`, docs at `/docs`)

The poweur.org website (`apps/site`, plain static files) and the docs (`apps/docs`, Docusaurus with `baseUrl: /docs/`) are one static tree served by infra Caddy straight from disk: `/opt/apps/poweur-web/www`, bind-mounted read-only at `/srv/web`, with the docs in its `docs/` folder. The directory sits outside the relay checkout, so the relay deploy's `git reset --hard` never touches it. The host is temporary while the site is new, so it answers with `X-Robots-Tag: noindex`. Unknown site paths redirect to `/`; unknown `/docs/` paths get the docs' own 404 page.

**Deploys** run from `.github/workflows/deploy-web.yml` when `apps/site/**` or `apps/docs/**` change on `master` (or on manual dispatch): build the docs with `SITE_URL=https://tmpwww.poweur.org`, copy the site without `social/`, `scripts/` and `README.md`, add the docs as `docs/`, copy the tree to the VM, unpack it beside the live one and swap it in with a rename, then fetch `/` and `/docs/clients/js-sdk` through Cloudflare. The Caddy route and the `/srv/web` mount ship with the relay's Deploy, like every other Caddy change; that deploy also creates `/opt/apps/poweur-web` as `poweur` so Docker does not create it root-owned.

**DNS.** Add a proxied Cloudflare record `tmpwww.poweur.org` pointing at the VM (A record, or a CNAME to `oauth.poweur.org`). poweur.org is in Full TLS mode, so Caddy must hold a certificate for the name; it obtains one on first request once DNS points here. Until then Cloudflare shows 525.

**Analytics.** The site and the docs load `faro.js` (`apps/site/assets/faro.js`, `apps/docs/static/js/faro.js`, generated by `pnpm --filter @poweur/faro bundle`; skipped on localhost): anonymous page views, errors and web vitals to `/faro/collect` on the same host, which the Caddy site block imports. See "Browser telemetry" above.

**Moving to the real name** (`poweur.org`, with `www.poweur.org` redirecting to it): add those names to the Caddy site block, drop the `X-Robots-Tag` header, and set `SITE_URL` in `deploy-web.yml` to `https://poweur.org`.

## OAuth/OIDC bridge (`oauth.poweur.org`)

The bridge (`apps/oauth`, EPIC-022) runs on the same VM as its own Compose project, `poweur-oauth`, from `/opt/apps/poweur-oauth` — a directory of its own, so the relay deploy's `git reset --hard` of `/opt/apps/poweur` never touches it. Caddy routes `oauth.poweur.org` (HTTP and HTTPS, like Grafana) to `poweur-oauth:8090` on `infra_net`; Prometheus scrapes `poweur-oauth:9464` and probes `https://oauth.poweur.org/health` as `blackbox-oauth`; Grafana alerts `oauth-down`, `oauth-code-reuse` and `oauth-server-errors`.

**Deploys** run from `.github/workflows/deploy-oauth.yml` only when `apps/oauth/**`, `packages/identity/**` or `go.work` change on `master` (or on manual dispatch): the `poweur-oauth` GHCR image stamped with the commit, `apps/oauth/deploy/docker-compose.prod.yml` copied to `/opt/apps/poweur-oauth/docker-compose.yml`, `docker compose up`, then `/health` must report this commit's `versionHash` — inside the container and publicly through Cloudflare. Bridge unit tests and `TestINT_OAUTH` run only when the dispatch checks **Run tests**, or when `RUN_CI=true`. Caddy, Prometheus and Grafana changes still ship with the relay's Deploy (infra `compose up` and a Caddy reload); Prometheus and Grafana read their files at start, so after changing them run `docker restart infra-prometheus infra-grafana`.

**Configuration.** Non-secret settings are in the compose file. `/opt/apps/poweur-oauth/.env.prod` (owner `poweur`, mode `0600`, never committed) holds:

| Variable | |
|---|---|
| `OAUTH_KEY_ENCRYPTION_KEY` | 32 bytes, base64 (`poweur-oauth gen-key`). Seals the signing keys. **Lose it and the bridge cannot sign; every application must be reconfigured.** |
| `OAUTH_CLIENT_REGISTRATION`, `OAUTH_REGISTRATION_ALLOWLIST` | Optional. Registration is `open` (any Poweur ID) unless set to `allowlist` (with a CSV of IDs or `*.domain`) or `closed`. |
| `OAUTH_ABUSE_CONTACT`, `OAUTH_SECURITY_CONTACT` | Published on `/abuse`, `/privacy`, `/security`. |
| `OAUTH_FARO_URL` | Set in the compose file: `/faro/collect`. Anonymous Faro telemetry from the bundled UI on every page, same origin (no third-party script, CSP unchanged). |

After editing it: `cd /opt/apps/poweur-oauth && OAUTH_IMAGE=$(docker inspect -f '{{.Config.Image}}' poweur-oauth) docker compose -p poweur-oauth up -d`.

**Operator commands** run inside the container, which already has the configuration:

```sh
docker exec poweur-oauth poweur-oauth keys list
docker exec poweur-oauth poweur-oauth clients list
docker exec poweur-oauth poweur-oauth clients suspend <client_id> impersonation
```

**Backup.** A root cron (`/etc/cron.d/poweur-oauth-backup`) writes a consistent copy daily with `poweur-oauth backup` into the volume's `backups/` directory and keeps 14 days. That protects against a corrupt database, not against losing the VM: copy `/var/lib/docker/volumes/poweur_oauth_data/_data/backups/` off the machine with the other volumes, and keep the key-encryption key in a separate place. Restore = stop the container, put a backup in place as `oauth.db`, start with the **same** key: every application then sees the same subjects and keys.
