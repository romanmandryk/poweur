# PCP-0002 — Identity document (`id.json`)

- **Status:** experimental (shipped since EPIC-001/002)
- **Owner:** poweur core
- **Registry entries:** none (reserved path `poweur-sys/public/id.json`)

## Convention

`poweur-sys/public/id.json` — the signed, machine-readable description of a Poweur ID,
served at `/.well-known/poweur/id.json` (web-first resolution) and mirrored by DNS TXT.
Shape and canonical signing: `packages/identity/doc.go` (`IdentityDocument`,
`CanonicalBytes`). Written only via registration/rotation endpoints — the relay rejects
owner DAV writes to this path (relay-managed file).

Fields: `version`, `identity`, `public_key` (`ed25519:` base64url), optional
`encryption_public_key` (`x25519:`), `relay`, `capabilities`, `previous_keys`
(rotation statements consumed by key pinning, PCP-0004), `moved_to`, `updated_at`,
`signature`.

## Compatibility

Resolvers fail closed on key mismatch between web and DNS. Unknown fields preserved.
