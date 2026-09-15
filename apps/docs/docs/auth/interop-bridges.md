---
id: interop-bridges
sidebar_position: 5
title: DID and OpenID interoperability
---

# DID and OpenID interoperability

Poweur IDs remain DNS names. Interoperability layers project the same identity and key
material into formats other systems already understand; they do not create a second source of
identity truth.

## `did:web`: ship

For `alice.example.com`, the deterministic projection is `did:web:alice.example.com`, served
at `https://alice.example.com/.well-known/did.json`. The relay generates it from the current,
verified Poweur identity document:

- the Ed25519 signing key becomes a `JsonWebKey2020` verification method used for
  `authentication`, `assertionMethod`, and `capabilityInvocation`;
- the X25519 encryption key becomes the `keyAgreement` method;
- the Poweur relay is advertised as a service endpoint.

Key bytes use base64url JWK `x` values, so the projection can be compared mechanically with
`/.well-known/poweur/id.json`. Updates and rotation need no second write: a fresh request is
projected from the current stored document. This follows the
[did:web resolution path](https://w3c-ccg.github.io/did-method-web/) and the
[DID Core data model](https://www.w3.org/TR/did-core/).

## OIDC bridge: planned

An optional bridge can make a Poweur identity acceptable to an ordinary OpenID Connect relying
party:

1. An RP sends a normal authorization request to a bridge it chose.
2. The bridge creates a Poweur Sign-In request whose audience is the bridge's verified origin
   and carries the OIDC transaction in bridge-local state.
3. The user approves in any Poweur signer.
4. The bridge verifies the Poweur response through the normal resolver chain.
5. The bridge issues a short-lived, signed OIDC `id_token`; `sub` is a stable pairwise value,
   while the Poweur ID is an explicit claim released with consent.

The bridge needs conventional OP machinery: discovery, authorization and token endpoints,
JWKS rotation, PKCE, state/nonce validation, redirect-URI registration, pairwise subject
derivation, and an auditable claim-release policy. It may keep only transaction/replay state;
it never receives an identity private key or becomes the canonical directory.

### Boundaries we do not bend

- No mandatory or central Poweur OP. Any operator can self-host a bridge and native Poweur RPs
  bypass it.
- No bearer token substitutes for a Poweur identity signature at a relay.
- No bridge-owned account is required and no bridge database becomes identity truth.
- OIDC scopes do not silently translate to home scopes. A resource grant remains a separate,
  visibly approved exchange with the user's relay.

### Recommendation

Implementation is now planned in
[EPIC-021](https://github.com/poweur/poweur/blob/main/epics/EPIC-021-oauth-oidc-indieauth-bridge.md):
a generic, independently
self-hostable bridge, with OIDC/OAuth 2.0 as the primary standards surface and IndieAuth over
the same authentication core. It remains a separate service because it adds issuer signing
keys, browser sessions, client registration, redirects and account-correlation concerns that
do not belong in the relay. Same-browser Poweur approval is the primary journey, cross-device
QR is second, and typed-message push is an optional delivery channel rather than a prerequisite.

## SIOPv2 and OpenID4VP survey

SIOPv2 overlaps with Poweur Sign-In in useful vocabulary: a self-issued assertion, audience
and nonce binding, short validity, and holder-controlled keys. OpenID4VP adds presentation
requests and response modes useful when a relying party asks for credentials rather than only
proof of a name. We should reuse those terms in bridge documentation and keep our response
shape easy to translate.

We should not import the wallet stack into the core protocol today. Presentation Exchange,
credential-format negotiation, wallet metadata, request-object signing/encryption, and the
many OpenID response modes solve credential-wallet interoperability that a Poweur name proof
does not need. If verifiable-credential presentation becomes a product requirement, implement
it at the bridge boundary first and keep the existing canonical Poweur Sign-In bytes stable.
