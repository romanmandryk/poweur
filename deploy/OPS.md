# Relay and observability operations

E13 uses the existing VM and Grafana database. Relay → authenticated OTLP/HTTP → Alloy → Prometheus (metrics) and Loki (logs), with Grafana for both dashboards. No new analytics application. Tracking is disabled when `OTEL_EXPORTER_OTLP_ENDPOINT` is empty. Production setup enables it explicitly.

Deployment helpers use Bash with `openssl` (secret generation) and `jq` (JSON health checks). Ansible installs both; on an existing VM, run `sudo apt-get install jq openssl` once. Node.js 22 is used only for CI/development tests, with no npm dependencies or build step. The deployed helpers do not require Node or Python.

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

   **Do not generate replacement Grafana database passwords on an existing database.** The helper preserves imported/existing settings, creates independent intake and hashing secrets, and writes mode `0600`. Re-running it preserves all values. Keep `.observability.env` and `apps/api/.env.prod` on the VM, outside release snapshots and GitHub logs. Back up both encrypted.
3. Capture the currently running release before stopping old services:

   ```sh
   sudo bash deploy/capture-baseline.sh
   sudo chown -R poweur:poweur .releases .current
   ```

   The baseline copies the live web mount, previous infra configuration, and exact running relay image ID. It excludes infra `.env` files. Do not prune that image. On a first E13 upgrade, `release.sh` refuses to replace an existing relay without a recorded baseline.
4. Create a dedicated hosted smoke identity using the Go CLI against the existing relay. Keep its config/keys under `/opt/apps/poweur/.smoke` (the CLI uses `$HOME/.poweur`; run with a temporary home and move its `.poweur` there). For example, from the checkout with Go installed:

   ```sh
   go build -o /tmp/poweur-smoke ./apps/cli
   mkdir -p /tmp/poweur-smoke-home
   HOME=/tmp/poweur-smoke-home /tmp/poweur-smoke identity create deploysmoke.poweur.net --hosted --relay https://relay.poweur.net
   mv /tmp/poweur-smoke-home/.poweur /opt/apps/poweur/.smoke
   chmod -R go-rwx /opt/apps/poweur/.smoke
   ```

   If the name is taken, use another dedicated identity. Do not enable detailed analytics on it. The release image contains the CLI; each deploy runs authenticated `inbox --json`, without registering or sending messages. Ensure `poweur` owns `.smoke`. Its private key is an operational secret.
5. Retire the old foreground Compose systemd units during a short maintenance window; otherwise they can restart old configuration over a new release:

   ```sh
   sudo systemctl disable --now poweur-app.service poweur-infra.service
   sudo systemctl enable docker.service
   # Bring the captured baseline back immediately:
   sudo -u poweur bash deploy/restore.sh "$(readlink .current)"
   ```

   Docker's `restart: unless-stopped` now handles reboot. Do not restart the legacy units or run `docker compose up` from the old checkout or `/opt/infra`. Ansible now prepares the host and secrets; release Actions own application startup.
6. In GitHub Actions secrets configure `DEPLOY_HOST`, `DEPLOY_SSH_KEY` and **`DEPLOY_KNOWN_HOSTS`**. The last value is the server's known_hosts entry, verified against its SSH host-key fingerprint through your provider console. Existing server→GitHub read access must still work. GHCR uses the workflow's token; no image password is committed.
7. Before rollout, ensure `grafana.poweur.net` points to the VM and uses HTTPS. Caddy now obtains its origin certificate too; use DNS-only or a **per-host Cloudflare Full (strict)** rule for Grafana. Then run **Deploy** (manual dispatch or merge to `master`). It runs all CI suites, builds the relay/CLI image, deploys by immutable digest, updates both Compose stacks, checks the exact release hash, stack readiness, and authenticated inbox access. Failed gates restore previous configuration/image. Watch the first rollout, then verify private dashboards.
8. Set `GRAFANA_SMTP_ENABLED`, `GRAFANA_SMTP_HOST`, `GRAFANA_SMTP_USER`, `GRAFANA_SMTP_PASSWORD`, `GRAFANA_SMTP_FROM_ADDRESS` and `ALERT_EMAIL` in `.observability.env`. Redeploy and send a test notification in Grafana → Alerting → Contact points. Alerts exist without SMTP, but email cannot arrive until configured. Enable GitHub Actions failure notifications for **External relay health**; it probes from GitHub every 15 minutes. Scheduled Actions can be delayed or disabled after inactivity; it is a basic external monitor, not an uptime SLA.
9. In Grafana, open **Poweur Growth**, choose **Share → Share externally**, and enable a public link. Share only that generated link. Keep anonymous access disabled. **Poweur Relay Ops**, Explore and Loki remain behind login. The public dashboard has aggregate Prometheus panels only; do not add log panels or actor fields to it.

## DNS: no new record required for the same VM

The generated default endpoint is `http://infra-alloy:4318` on the private Docker bridge, authenticated with the intake credential. No `ingest.poweur.net` DNS record is needed for this path. Prometheus, Loki and Alloy have no host-published ports.

For a remote relay/stack, add **A/AAAA `ingest.poweur.net` pointing to the intake VM**, or a **CNAME to an existing DNS-only hostname that points to it**. Caddy routes `/v1/logs` and `/v1/metrics` and automatically obtains HTTPS certificates; DNS must point to the VM and inbound 80/443 must be reachable. Caddy does **not** create DNS records. The configured reserved names prevent identity registration; reservation is unrelated to DNS creation.

If proxying ingest through Cloudflare, use a **per-host** Full (strict) origin rule, as for Grafana. Do not change the whole zone's TLS mode without migrating the existing HTTP-origin identity hosts. A DNS-only ingest record avoids that complication. Set `OTEL_EXPORTER_OTLP_ENDPOINT=https://ingest.poweur.net` and `TELEMETRY_ALLOW_HTTP=0` for remote export; preserve the intake credentials. `metrics.poweur.net` is reserved but is not a configured intake alias.

Caddy trusts Cloudflare's published IP ranges and rewrites X-Forwarded-For to the resolved client IP. Release scripts trust only Caddy's current Docker address on the relay. If recreating Caddy manually, redeploy the relay to refresh that address. Review Cloudflare range changes periodically. CDN request logs are separate from relay consent mode.

## Routine deployment and rollback

`.current` points to `.releases/<commit>`. That directory holds the matching static web files, stack config and `.relay-image` digest. `docker inspect poweur-relay` and `/health` identify the running release. No application data is stored under the release directory.

Rollback (run as `poweur`):

```sh
cd /opt/apps/poweur
bash deploy/rollback.sh
```

Rollback retains database/data volumes and restores configuration/images, not data/schema changes. Keep at least the current and previous release directories and images; do not use `docker system prune -a` or registry cleanup that deletes their digests. The automated release tests rehearse success and failed-health rollback with command substitutes; `stack-smoke.mjs` boots the real services and verifies ingest/auth/dashboard access. Rehearse the one-liner on your VM after its first successful rollout. A completely new VM has no previous release to restore.

## Fresh VM

Run the Ansible bootstrap, finish its GitHub SSH keys and read-access setup, and initialize `.observability.env` using the helper. For the initial boot only, select a **CI-tested digest** from GHCR and start infra and relay with the shared secrets:

```sh
cd /opt/apps/poweur
export RELAY_IMAGE=ghcr.io/YOUR_OWNER/poweur-relay@sha256:YOUR_TESTED_DIGEST
export RELAY_ENV_FILE=/opt/apps/poweur/apps/api/.env.prod
docker compose -p infra --env-file .observability.env -f deploy/infra/docker-compose.yml up -d
docker compose -p poweur --env-file .observability.env -f docker-compose.prod.yml up -d
```

Then create the smoke identity, capture the baseline with `--infra /opt/apps/poweur/deploy/infra`, and run Deploy. Caddy/Grafana DNS and existing Cloudflare identity routing still need the original host setup. Do not expose internal backend ports.

## Dashboards, retention and failures

- **Poweur Ops:** traffic/errors/latency, forwarding outcomes, inbox/storage pressure, telemetry loss and searchable JSON logs. Query `actor_id` in Loki after login; values reflect consent at collection time. Loki index labels contain service/environment only, never actor or IP.
- **Poweur Growth:** hosted identities, accepted submissions, registrations, completed uploads and storage. Counts describe identities/actions, not unique people or read receipts. Development/test traffic is excluded. Counter increases handle normal restart resets; lost exports and retention mean these are estimates, not a financial ledger. No-data remains a gap, not a fake zero.
- Local container logs rotate at 3 × 10 MB per container. Loki retains 14 days, Prometheus 30 days. Change the committed retention configuration if needed and monitor disk capacity. Historical consented records remain until retention expires; switching off affects subsequent exports and does not delete history.
- Relay export is best effort: a bounded 512-record queue, batches of 128, one-second flush, three-second request timeout, one attempt per log batch, and a bounded shutdown flush. Queue overflow/export failures increment loss metrics. Collector outage does not reject relay requests. Alloy adds its own backend buffering; watch its logs/metrics as well.
- Provisioned alerts cover relay health, missing telemetry, Alloy, sustained 5xx, disk pressure and dropped records. SMTP sends independently of relay messaging. The separate GitHub probe detects VM outages that same-VM Grafana cannot report.

## Backup, restore and moving the stack

Back up `poweur_poweur_data`, `infra_postgres_data`, `infra_grafana_data`, `infra_loki_data`, `infra_prometheus_data`, `infra_alloy_data` and Caddy's data/config volumes, plus encrypted copies of the two environment files and smoke keys. The relay volume includes identity documents, files, inbox/spool and consent preferences; do not rely on the old memory-only assumption. Stop writers for a consistent filesystem snapshot, or use `pg_dump` for PostgreSQL and a provider snapshot strategy. Keep backups off this VM. Restore into the same volume names, then restore matching images/configuration and verify `/health`, authenticated inbox, Grafana queries and a test alert. Never use `down -v` on production.

To move observability, copy only infra volumes/configuration/secrets to the new VM, configure ingest DNS/TLS, and change the relay endpoint to HTTPS. Do not copy or mount relay `POWEUR_DATA` into the analytics stack. Keep the HMAC key on the relay stable if you want historical hashed actors to stay linkable; rotating it intentionally starts new pseudonyms. Keep the Grafana DB credentials consistent when moving its database.
