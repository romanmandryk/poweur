# PCP-0008 — Drive share offers

- **Status:** experimental (EPIC-020 E20-T7; replaces PCP-0003's lifecycle bodies)
- **Owner:** poweur core
- **Registry entries:** `sys.share.offer`, `sys.share.accept`, `sys.share.revoked`

## Convention

A storage-v2 share lives on the owner's relay (a signed `drive.Share`, see
`apps/docs/docs/files/storage-v2.md`). These messages tell the people involved about it. They
are end-to-end encrypted typed messages; **none of them grants anything** — the relay decides
every request from the share itself.

Bodies are JSON with `format: 2` (Go `packages/identity/drive/offer.go` is authoritative; TS twin
`packages/client-ts/src/drive/offer.ts`):

- `sys.share.offer` (owner → member): `share` (the complete signed share, whose `member` is the
  recipient), `relay` (host of the drive's relay), `kind` (`file`/`folder`), optional `name`
  (the shared node's display name — the member cannot decrypt a shared root's name, which is
  sealed to its parent) and `offered_at`.
- `sys.share.accept` (member → owner): `drive`, `share_id`, `accepted_at`.
- `sys.share.revoked` (owner → member): `drive`, `share_id`, `revoked_at`.

The enclosing sender and recipient MUST match the body: an offer's sender is the share's
issuer and its recipient the share's member. Recipients verify the share's signature with the
issuer's resolved key, and that the issuer is the drive owner or holds `admin` through a share
that is itself valid, before showing or accepting it.

Offers MUST carry envelope metadata `share_id`, an expiry no more than seven days out, and an
encrypted payload no larger than 64 KiB. A non-contact's offer is a consent request under the
recipient's inbox policy, not permission to start a message stream.

Accepting records a mount in the member's own drive at `.poweur/private/mounts.json`
(`{format: 1, mounts: [{drive, relay, node, share_id, name, kind, role, mounted}]}`),
encrypted like any private file; clients show it as `Shared/<owner>/<name>`. A revocation
notice (or the relay answering `403`) removes it. Links are never offered: they travel as URLs.

## Compatibility

`format: 1` bodies (PCP-0003 path grants) are obsolete and MUST be ignored.
