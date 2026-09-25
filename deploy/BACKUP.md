# Backing up and restoring Poweur

**Today:** production relies on **Hetzner's automatic server backups**: a full image of
the VM every night (about 22:35 UTC), kept for **7 days**. That is what the privacy policy
promises, so change both together. Restore = roll the server back, or create a new server
from a backup image and copy what you need out of it. Hetzner's backups cover the server's
own disk only, **not attached Cloud Volumes**.

**Ready but switched off:** an off-site, encrypted, file-level backup to a Hetzner Storage
Box with [restic](https://restic.net/) ([`backup/poweur-backup.sh`](backup/poweur-backup.sh)).
It survives losing the server or the account, restores single files, and verifies itself
weekly. The Ansible playbook installs it only once `storage_box_user` is set in
`secrets.yml`. If you turn it on, choose its retention (`backup_keep_within`) and update the
privacy policy's backup row to match. The rest of this page describes it.

## What is in a snapshot

| Path in the snapshot | Source | How |
|---|---|---|
| `/backup/relay` | `poweur_poweur_data`: identity documents, files, inbox/ack spool, consent preferences | Read live. The relay writes whole files, so no pause |
| `/backup/staging/oauth` | `poweur_oauth_data`: the OAuth bridge's SQLite database | Copied while `poweur-oauth` is **paused** for a few seconds, so the database and its WAL agree |
| `/backup/staging/postgres.sql.gz` | `infra-postgres` (Grafana's database) | `pg_dumpall` |
| `/backup/caddy` | `infra_caddy_data`: TLS certificates | Read live |
| `/backup/grafana` | `infra_grafana_data` | Read live |
| `/backup/staging/config/…` | `.env.prod`, `.observability.env`, `/opt/infra/.env`, the bridge's `.env.prod` | Copied; the whole repository is encrypted |

Not backed up: Loki and Prometheus (14- and 30-day telemetry that rebuilds itself) and
sessions (memory-only by design). Never run `docker compose down -v` in production.

## Setting it up (once)

1. **Order a Storage Box** in the Hetzner console (BX11, 1 TB, is plenty; pick the same
   region as the VM). In its settings, **enable SSH support**.
2. **Add to `deploy/ansible/secrets.yml`** (see `secrets.example.yml`):
   `storage_box_user`, `storage_box_host`, `storage_box_path`, `restic_password`
   (`openssl rand -base64 32`), and optionally `backup_heartbeat_url`.
   **Put `restic_password` in your password manager too.** Without it no backup can ever be
   read, and it is the one secret that must not exist only on the server.
3. **Run the playbook.** It installs the script (`/usr/local/sbin/poweur-backup`), the
   nightly timer (03:30 UTC ± 20 min), the config under `/etc/poweur-backup/`, and a new SSH
   key, then prints a one-liner to install that key on the Storage Box:

   ```bash
   echo '<printed key>' | ssh -p 23 u123456@u123456.your-storagebox.de install-ssh-key
   ```

4. **Run the playbook again.** It records the Storage Box host key and creates the
   repository. Check the host key against the fingerprints in the Hetzner docs if you
   want to be strict.
5. **Take the first backup and look at it:**

   ```bash
   sudo poweur-backup run
   sudo poweur-backup snapshots
   ```

6. **Alerting:** create a heartbeat check (Better Stack Heartbeats, healthchecks.io, …)
   that expects one ping a day with a few hours' grace, and put its URL in
   `backup_heartbeat_url`. Every successful night pings it and every failure pings
   `<url>/fail`, so both a failed and a missed night alert you. Logs:
   `journalctl -u poweur-backup`.

## Every night

`poweur-backup run` stages the database copies in a temporary Docker volume, backs
everything up in one snapshot tagged `nightly`, deletes snapshots older than 30 days
(`forget --keep-within 30d --prune`, relative to the newest snapshot), and on Sundays
verifies a random 10 % of the stored data. Any failure unpauses the bridge, removes the
staging volume and pings the heartbeat's `/fail`.

## Restoring

Practise this before launch, on a scratch VM, and write down how long it took.

1. **Get the data out:**

   ```bash
   sudo poweur-backup snapshots
   sudo poweur-backup restore latest /root/restore      # or a snapshot ID
   ```

   The snapshot lands in `/root/restore/backup/…`, laid out as in the table above.

2. **Stop what you're replacing** (relay and bridge; Caddy and Grafana if restoring those):

   ```bash
   cd /opt/apps/poweur && docker compose -f docker-compose.prod.yml stop relay
   cd /opt/apps/poweur-oauth && docker compose stop oauth
   ```

3. **Put each volume back** with a throwaway container:

   ```bash
   docker run --rm -v poweur_poweur_data:/dst -v /root/restore/backup/relay:/src:ro alpine \
     sh -c 'rm -rf /dst/* && cp -a /src/. /dst/'
   docker run --rm -v poweur_oauth_data:/dst -v /root/restore/backup/staging/oauth:/src:ro alpine \
     sh -c 'rm -rf /dst/* && cp -a /src/. /dst/'
   docker run --rm -v infra_caddy_data:/dst -v /root/restore/backup/caddy:/src:ro alpine \
     sh -c 'cp -a /src/. /dst/'
   ```

   Postgres (Grafana's database), into a running `infra-postgres`:

   ```bash
   gunzip -c /root/restore/backup/staging/postgres.sql.gz | docker exec -i infra-postgres psql -U postgres
   ```

   Environment files are in `/root/restore/backup/staging/config/`; copy back what you need.

4. **Start and verify:** start the services again, then check `/health` (storage writable),
   that a known identity resolves, an authenticated inbox read, a WebDAV file, and a sign-in
   through the bridge. `TestINT_OPS_01_RestoreDataDir` covers the relay's side of this.

5. **Delete `/root/restore`** when you're done: it holds everyone's data in the clear.

## Deleting someone's data

When an ID is deleted, its data is gone from the server at once and from the backups
after at most 7 days (Hetzner's rotation), as the privacy policy says. With the restic
backups on, it is their retention instead; keep the policy in step. Never restore a deleted ID from a backup,
and if you restore the whole volume after a deletion, delete that ID again.
