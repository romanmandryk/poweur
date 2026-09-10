# Backing up and restoring Poweur

Use the [operations runbook](OPS.md#backup-restore-and-moving-the-stack) for the current deployment, including observability backups and immutable release rollback.

The relay's `poweur_poweur_data` volume contains identity documents, files, consent preferences and durable inbox/ack spool data. Sessions remain memory-only. Do not back up only `id.json` or assume messages are memory-only. Snapshot the entire volume while writers are stopped, and keep encrypted backups off the VM. Never run production `docker compose down -v`.

After restoring the volume with the matching release and configuration, verify `/health` storage writability, a known identity, authenticated inbox retrieval, and a DAV file. `TestINT_OPS_01_RestoreDataDir` covers the relay data-directory restore path. Grafana/Postgres and metrics/log backends have their own volumes and credentials; restoring relay storage alone does not restore those dashboards/history.
