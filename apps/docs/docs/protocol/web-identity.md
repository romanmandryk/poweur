---
id: web-identity
sidebar_position: 3
title: Web Identity Resolution
---

# Web Identity Resolution

Poweur identities are discovered primarily over HTTPS at
`https://<identity>/.well-known/poweur/`, with DNS TXT as a fallback for
self-hosted deployments that publish keys in DNS.

## Identity Document (`id.json`)

```json
{
  "version": 1,
  "identity": "alice.poweur.net",
  "public_key": "ed25519:<base64url>",
  "encryption_public_key": "x25519:<base64url>",
  "relay": "relay.poweur.net",
  "capabilities": ["messaging"],
  "updated_at": "2026-07-16T12:00:00Z",
  "signature": "<base64url ed25519 signature>"
}
```

The document is **signed by the identity's Ed25519 private key**. Relays store
and serve documents but never sign them.

### Canonicalization

Signature input is compact JSON with:

- object keys sorted lexicographically
- no insignificant whitespace
- `signature` field omitted
- empty optional fields omitted (`previous_keys`, empty `encryption_public_key`)

### Plain endpoints

| Path | Content |
|------|---------|
| `/.well-known/poweur/id.json` | Full signed document |
| `/.well-known/poweur/pubkey` | `ed25519:<base64url>` text |
| `/.well-known/poweur/enckey` | `x25519:<base64url>` text |

Requests are Host-routed: `Host: alice.poweur.net` selects Alice's document
on a wildcard-hosted relay.

For human-facing browser navigation, the same host serves a generated public
[identity page](/files/identity-pages) at `/`. The well-known identity document
remains the canonical machine-discovery endpoint.

## Resolver chain

1. Fetch `https://<identity>/.well-known/poweur/id.json` (or `http` in local/test)
2. If missing, read `_poweur.<identity>` / `_poweur-enc.<identity>` DNS TXT
3. If **both** succeed, signing keys **must match** or resolution fails closed

### Security limits (HTTPS fetch)

- No redirects
- 16 KB response cap
- 5 second timeout
- IP-literal identities rejected
- Private/loopback targets refused unless explicitly allowed (tests/dev)

`relay` in the document is **advisory** routing metadata; keys are authoritative.

## Hosted vs DNS registration

| Mode | DNS token | Document | Zone writes |
|------|-----------|----------|-------------|
| **Hosted** | No | Required (signed `identity_document`) | None — wildcard A covers routing |
| **DNS** | Yes | Recommended; required if `POWEUR_DATA` is set | Relay writes TXT + A/CNAME |

Hosted identities must fall under `HOSTED_DOMAINS` (e.g. `poweur.net`). Reserved
leftmost labels (`www`, `admin`, `relay`, …) are rejected.

**Lease (v1):** hosted names do not expire automatically.

## Key rotation

Rotation publishes a **new** identity document signed by the **new** key, with the old key
listed in `previous_keys` (grace window via `valid_until`). Continuity is proven by a separate
`rotation_signature` from the **old** key over:

```
identity-rotation
<identity>
<old_public_key>
<new_public_key>
<issued_at>
<nonce>
```

Relay: `POST /identities/{identity}/rotate`. CLI: `poweur key rotate`.

Verifiers should accept signatures under the current `public_key`, or under a `previous_keys`
entry whose `valid_until` is still in the future (`IdentityDocument.KeyValidAt`).

## Migration (`moved_to`)

Hosted names are not portable across parent domains. When leaving a hosted relay, publish a
tombstone document with `moved_to` set to the new identity FQDN (signed by the current key).
Resolvers SHOULD follow `moved_to` once. Self-hosted IDs move by updating DNS + the `relay`
field (`poweur relay set`). Export: `POST /identities/{id}/export` / `poweur identity export`.

## Related

- [Identity Model](/protocol/identity-model)
- [DNS Records](/protocol/dns-records)
- [DNS Management](/relay/dns-management)
- EPIC-001 / EPIC-002

## Credential scope for hosted identities

A hosted identity's passkey is scoped to the registrable domain of its home
(`alice.poweur.net` → `poweur.net`), so the launcher host and the identity's own origin share
one credential. See [credential scope](/security/key-management#credential-scope-rpid) for
what that trades away and why existing credentials are unaffected.
