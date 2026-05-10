---
id: interoperability
sidebar_position: 6
title: Interoperability
---

# Interoperability

Poweur ID is intentionally DNS-native: the canonical identifier is the fully qualified domain name itself, such as `alice.poweur.net`. Interoperability should be additive. Poweur ID should not depend on a DID method, an OpenID provider, or a federation hub in order to work, but it should map cleanly into those ecosystems where that improves adoption.

## Canonical Identifier

The canonical Poweur ID identifier is the FQDN:

```text
alice.poweur.net
```

Everything else is a projection of that identity:

- DNS `TXT` record for the public key
- DNS `A` / `CNAME` record for relay routing
- Optional HTTP well-known metadata
- Optional DID document representation

This keeps Poweur ID understandable to operators and easy to verify with standard DNS tooling.

## DID Mapping

### `did:dns`

`did:dns:<fqdn>` is the closest conceptual DID mapping because Poweur ID already anchors identity in DNS. It is a good target for future compatibility, but it should not be the only interop plan because the method is still emerging and is not yet as broadly deployed as more established DID patterns.

### `did:web`

`did:web:<fqdn>` is the practical bridge for current DID-aware tooling. For an identity such as `alice.poweur.net`, the DID document can be served from:

```text
https://alice.poweur.net/.well-known/did.json
```

That DID document should project the same verification material already published in DNS. DNS remains the ground truth; the DID document is a convenience layer for external ecosystems.

## Well-Known Discovery

Poweur ID should define its own metadata endpoint:

```text
https://<identity>/.well-known/poweur.json
```

This document should advertise:

- protocol version
- supported capabilities
- relay endpoint(s)
- authentication handoff methods (`qr`, `universal_link`, `deep_link`)
- optional DID aliases
- optional mobile app link metadata

The same pattern can be used on verifier domains that want to request Poweur ID-based sign-in.

## Messaging Alignment

The current Poweur ID message format is a minimal signed JSON envelope. For broader interoperability, the envelope should evolve toward shapes that are familiar to existing signed-message ecosystems, while keeping the DNS-native trust model intact.

The closest conceptual matches are:

- DIDComm basic messages for signed application-level envelopes
- Nostr-style events for explicit event metadata and end-to-end signed payloads

Recommended additions for future message versions:

- `id`
- `type`
- `nonce`
- `thread_id`
- `expires_at`
- `metadata`

Once the envelope grows beyond the current minimal fields, the signing input should move from newline concatenation to canonical JSON so other implementations can reproduce signatures without protocol-specific string-joining rules.

## Authentication Alignment

The same DNS identity should be usable for third-party sign-up and sign-in. The model is closest to a signed challenge-response wallet flow:

1. A verifier creates a request with `request_id`, `domain`, `audience`, `nonce`, `issued_at`, `expires_at`, `action`, and optional `statement`.
2. The request is handed to the mobile app via QR, universal link, or deep link.
3. The mobile app displays the verifier identity and requested action.
4. After user approval, the app signs the request.
5. The verifier resolves the public key from DNS, or from an equivalent DID document, and verifies the signature.

This is similar in spirit to SIWE / wallet login and SIOPv2-style self-issued flows, but the root identifier remains the DNS name rather than an account address or centralized identity provider.

## Recommended Compatibility Strategy

The safest path is:

1. Keep the DNS name as the only canonical identifier.
2. Publish `/.well-known/poweur.json` for native Poweur ID discovery.
3. Publish `/.well-known/did.json` when DID ecosystem compatibility is needed.
4. Treat `did:dns` as a future-facing alias, not a dependency.
5. Shape auth requests and signed envelopes so an OpenID / wallet bridge can be added later without redesigning the identity layer.

## Related

- [Protocol Overview](/protocol/overview)
- [Identity Model](/protocol/identity-model)
- [DNS Records](/protocol/dns-records)
- [Message Format](/protocol/message-format)
