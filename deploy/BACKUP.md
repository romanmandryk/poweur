# Backing up and restoring Poweur relay data

Hosted identities are durable under `$POWEUR_DATA` (compose: volume `poweur_data` → `/data`).

## What to back up

Snapshot the entire `POWEUR_DATA` directory (or Docker volume). The important tree is:

```
$POWEUR_DATA/identities/<sanitized-id>/poweur-sys/public/id.json
```

Inbox, acks, and sessions are **memory-only** today (see EPIC-009) — they are not in this tree.

## Consistent snapshot

1. Prefer a filesystem/volume snapshot while the relay is stopped, **or**
2. Use a crash-consistent copy (e.g. `rsync -a` / `tar`) of `$POWEUR_DATA` while the relay
   runs — identity docs are written atomically (temp + rename), so a mid-write copy may skip a
   brand-new registration but should not corrupt an existing `id.json`.

Example:

```bash
# stop relay (compose)
docker compose -f docker-compose.prod.yml stop relay

# archive the volume mount or bind path
tar -C /var/lib/docker/volumes/poweur_poweur_data/_data -czf poweur-data-$(date +%F).tar.gz .

docker compose -f docker-compose.prod.yml start relay
```

## Restore drill

1. Stop the relay.
2. Replace `$POWEUR_DATA` with the backup contents.
3. Start the relay with the same `HOSTED_DOMAINS` / `RELAY_ADDRESS`.
4. Confirm `GET /health` reports `storage.writable: true`.
5. Confirm `GET /identities/<id>` returns a known identity (integration: `TestINT_OPS_01_RestoreDataDir`).

## Quotas

`MAX_IDENTITY_BYTES` is scaffolding for per-identity disk limits (enforced in EPIC-003). It does
not yet reject writes.
