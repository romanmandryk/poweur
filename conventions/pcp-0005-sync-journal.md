# PCP-0005 — Sync journal record & changes cursor

- **Status:** experimental (shipped with EPIC-004)
- **Owner:** poweur core
- **Registry entries:** `sys.sync.changed` (reserved)

## Convention

Per-identity changes journal record (served by `GET /sync/{identity}/changes`):
`{change_id, op: put|mkdir|delete, path, etag, size, mtime, actor, time}`.
`change_id` is a per-identity monotonic counter; cursors are opaque strings; a delete
covers its subtree; moves are journaled as delete + put/mkdir per entry. Compaction
forces `full_resync: true` → rebuild from `GET /sync/{identity}/manifest` (NDJSON).

Full spec: `apps/docs/docs/files/sync-protocol.md`.

## Compatibility

Consumers MUST treat unknown `op` values as "full resync required" rather than guessing.
