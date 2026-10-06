# Deploying Poweur

This directory is the infrastructure a Poweur host is built from. To run your own relay, start with
the [self-hosting guide](../apps/docs/docs/relay/self-hosting.md) and the
[relay configuration reference](../apps/docs/docs/relay/configuration.md); this is what sits behind
them.

| Path | What it is |
|------|------------|
| `ansible/` | Playbook that prepares a fresh Ubuntu host (Docker, firewall, users, secrets, optional backup). Copy `inventory.example.yml` to `inventory.yml` and `secrets.example.yml` to `secrets.yml` (both stay out of git) |
| `infra/` | The monitoring and edge stack: Caddy, Prometheus, Loki, Alloy, Grafana with provisioned dashboards and alerts, Postgres for Grafana. Start it with `docker compose` from this directory |
| `backup/` | Encrypted off-site backup to a Hetzner Storage Box with restic. See [`backup/README.md`](backup/README.md) |
| `betterstack/` | Optional Better Stack uptime and log shipping. Easy to remove |
| `relay/blocked-terms.txt` | Terms a hosted handle may not contain (`NAME_BLOCKED_FILE`) |
| `server-bootstrap.sh`, `setup-observability.sh` | Helpers the playbook and first-time setup call |
| `tests/` | Checks that the dashboards and the deploy scripts stay consistent |

The relay itself is one container: [`../docker-compose.prod.yml`](../docker-compose.prod.yml) shows a
production shape (storage in an S3-compatible bucket, hosted domains, name policy, telemetry), and
[`../apps/api/README.md`](../apps/api/README.md) lists everything it reads from the environment.

Operating a relay in production (backups and restores, key and token handling, abuse handling,
suspending or deleting a hosted ID with `poweur-relay identities`) is covered by the relay docs
under `apps/docs/docs/relay/`. The runbook for the hosted service at poweur.net is private.

## Daily growth digest

`digest/poweur-digest.sh` sends one [ntfy](https://ntfy.sh) push at 08:00 Lisbon time
(`poweur-digest.timer`) with the total hosted IDs and what grew in the last 24h: new IDs,
messages, relay sign-ins (CLI and web sessions together), OAuth sign-ins, guestbook sign-ins and
entries, and hello-bot messages. It stays quiet on days nothing grew. Set `ntfy_topic` in
`ansible/secrets.yml` (the topic name is the secret) and run the playbook, or install by hand:
copy the script to `/usr/local/sbin/poweur-digest`, the unit files to `/etc/systemd/system/`, put
`NTFY_URL=https://ntfy.sh/<topic>` in `/etc/poweur-digest/digest.env` (mode 0600) and
`systemctl enable --now poweur-digest.timer`. Try it with `poweur-digest --dry-run` (print only)
or `--force` (send even when nothing grew).
