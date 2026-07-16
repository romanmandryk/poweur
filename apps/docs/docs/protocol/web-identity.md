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

## Related

- [Identity Model](/protocol/identity-model)
- [DNS Records](/protocol/dns-records)
- EPIC-001 / EPIC-002
