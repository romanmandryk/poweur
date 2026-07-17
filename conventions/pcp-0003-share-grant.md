# PCP-0003 — Share grant & group documents

- **Status:** experimental (shipped with EPIC-005)
- **Owner:** poweur core
- **Registry entries:** `sys.share.offer`, `sys.share.accept`, `sys.share.revoked` (reserved)

## Convention

- Grants: `poweur-sys/relay/shares/<share-id>.json`, schema
  `conventions/schemas/poweur-sys/share-grant.schema.json`, signed by the owner identity
  key over the canonical string in `packages/identity/grants.go`. Paths only under
  `/shared` or `/apps`; audience = direct ids and/or owner-local groups; permissions
  `read`/`write` (write ⇒ read); whole-subtree inheritance; revocation = file delete.
- Groups: `poweur-sys/relay/groups/<name>.json`, schema `share-group.schema.json`,
  owner-signed member list (≤ 1000 members).

Full spec: `apps/docs/docs/files/sharing.md`.

## Compatibility

The `link` audience key is reserved for capability-URL shares (EPIC-005 T4). Relays MUST
ignore (and log) grants they cannot verify — never fail open.
