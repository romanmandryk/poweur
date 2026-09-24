# PCP-0003 — Share grant & group documents

- **Status:** experimental (shipped with EPIC-005)
- **Owner:** poweur core
- **Registry entries:** `sys.share.offer`, `sys.share.accept`, `sys.share.revoked`, `sys.share.claim` (experimental)

## Convention

- Grants: `poweur-sys/relay/shares/<share-id>.json`, schema
  `conventions/schemas/poweur-sys/share-grant.schema.json`, signed by the owner identity
  key over the canonical string in `packages/identity/grants.go`. Paths only under
  `/shared` or `/apps`; audience = direct ids and/or owner-local groups, or one capability
  token; permissions `read`/`write` (write ⇒ read), plus `create` only for a file-request
  link; whole-subtree inheritance; revocation = file delete.
- Groups: `poweur-sys/relay/groups/<name>.json`, schema `share-group.schema.json`,
  owner-signed member list (≤ 1000 members).
- Lifecycle bodies are versioned JSON carried inside end-to-end encrypted messages:
  `sys.share.offer` includes the complete signed grant and `offered_at`;
  `sys.share.accept` includes `share_id`, owner, recipient, recipient-local `mount_path`,
  and `accepted_at`; `sys.share.revoked` includes `share_id`, owner and `revoked_at`;
  `sys.share.claim` includes the source share/token, claimant, completed anonymous action and
  `claimed_at`.
  The enclosing sender/recipient identities MUST match the body roles.
- Accepting writes a non-authoritative, credential-free pointer at
  `shared/<owner>/<name>/.poweur-mount.json`. It records version, share id, owner,
  source path, permissions, acceptance time and optional grant expiry. Clients resolve
  the owner and mint fresh visitor authority; the pointer never embeds a token.
- Offers MUST carry signed envelope metadata `share_id`, an expiry no more than seven days
  out, and an encrypted payload no larger than 64 KiB. A non-contact offer is a consent
  request under inbox policy, not permission to start a message stream.

Full spec: `apps/docs/docs/files/sharing.md`.

## Compatibility

The `link` audience key is used for capability-URL shares (EPIC-005 T4); link grants are not
sent as identity offers. Relays MUST ignore (and log) grants they cannot verify — never fail
open.

A `link.file_request` object makes the token a strict create-only capability. The grant MUST
carry exactly `permissions:["create"]`; ordinary download links MUST carry exactly
`permissions:["read"]`. A request holder may create a new, relay-uniquely-named object under
the granted folder, but MUST NOT list, read, overwrite, rename or delete any object. The
signed request may cap upload count, aggregate bytes, per-object bytes and accepted media
types. This is the v1 compatibility subset of EPIC-020's granular `create` permission.
