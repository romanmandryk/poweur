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
| `relay/blocked-terms.txt` | Terms a hosted handle may not contain (`NAME_BLOCKED_FILE`) |
| `server-bootstrap.sh`, `setup-observability.sh` | Helpers the playbook and first-time setup call |
| `tests/` | Checks that the dashboards and the deploy scripts stay consistent |

The relay itself is one container: [`../docker-compose.prod.yml`](../docker-compose.prod.yml) shows a
production shape (storage in an S3-compatible bucket, hosted domains, name policy, telemetry), and
[`../apps/api/README.md`](../apps/api/README.md) lists everything it reads from the environment.

Operating a relay in production (backups and restores, key and token handling, abuse handling,
suspending or deleting a hosted ID with `poweur-relay identities`) is covered by the relay docs
under `apps/docs/docs/relay/`. The runbook for the hosted service at poweur.net is private.

## Email bridge (EPIC-023)

The bridge ([`apps/email`](../apps/email/README.md)) runs beside the relay as its own Compose
project, `poweur-email` in `/opt/apps/poweur-email`, deployed by
[`deploy-email.yml`](../.github/workflows/deploy-email.yml). The mail server in front of it is
Stalwart at `mail.poweur.net`, configured in the famooss repo. Launch order:

1. Reserved labels are live (relay ≥ 0.2.18): `mta-sts`, `autoconfig`, `autodiscover` and the
   RFC 2142 role names cannot be claimed.
2. Create the bridge identity `email.poweur.net` from a seed you keep, with the operator token
   (see the bridge README).
3. On the VM, `/opt/apps/poweur-email/.env.prod` (0600) holds `EMAIL_BRIDGE_SEED`,
   `EMAIL_HOOK_SECRET`, `EMAIL_LMTP_PASSWORD`, `EMAIL_LMTP_ALLOW` (Stalwart's IP) and
   `EMAIL_SMTP_PASSWORD`; `./certs/lmtp.crt|key` is a certificate for `email.poweur.org`
   (the LMTP STARTTLS name). Open TCP 2525 in the firewall for Stalwart's IP only.
4. DNS: `email.poweur.org` → the VM through Cloudflare (Caddy serves it; `/metrics` stays
   internal). The MX, SPF, DKIM, DMARC, MTA-STS and TLS-RPT records are the mail server's.
5. Stalwart: the RCPT hook to `https://email.poweur.org/stalwart/hook` with
   `Authorization: Bearer <EMAIL_HOOK_SECRET>`; relay `poweur.net` recipients to the bridge's
   LMTP (`<vm>:2525`, STARTTLS, AUTH `stalwart`); a submission account for the bridge allowed
   to send as `*@poweur.net`.
6. Set the repository variable `EMAIL_BRIDGE_DEPLOY=true` (or run the workflow by hand) and
   check `https://email.poweur.org/health`.
7. Add `EMAIL_BRIDGE_ID=email.poweur.net` to `apps/api/.env.prod` and redeploy the relay: users
   now see "Get name@poweur.net". Watch `poweur_email`-prefixed metrics (`email_inbound_total`,
   `email_outbound_total`, `email_hook_total`) in Prometheus.

Back up the `poweur_email_data` volume's `email.db` (send states, suppression list, footer
ledger); blobs in it expire on their own.

## Daily growth digest

`digest/poweur-digest.sh` sends one [ntfy](https://ntfy.sh) push at 08:00 Lisbon time
(`poweur-digest.timer`) with the total hosted IDs and what grew in the last 24h: new IDs,
messages, relay sign-ins (distinct IDs signed in, not session renewals, so they never trigger a push alone), OAuth sign-ins, guestbook sign-ins and
entries, and hello-bot messages. It stays quiet on days nothing grew. Set `ntfy_topic` in
`ansible/secrets.yml` (the topic name is the secret) and run the playbook, or install by hand:
copy the script to `/usr/local/sbin/poweur-digest`, the unit files to `/etc/systemd/system/`, put
`NTFY_URL=https://ntfy.sh/<topic>` in `/etc/poweur-digest/digest.env` (mode 0600) and
`systemctl enable --now poweur-digest.timer`. Try it with `poweur-digest --dry-run` (print only)
or `--force` (send even when nothing grew).
