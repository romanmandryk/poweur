# EPIC-021 — Generic OAuth 2.0 / OIDC bridge with IndieAuth compatibility

- **Status:** proposed
- **Priority:** P1 (ecosystem adoption: one bridge unlocks existing auth-capable applications)
- **Depends on:** EPIC-001 (public resolver chain), EPIC-008 (native Poweur Sign-In and verifier),
  EPIC-013 (deployment/observability foundations)
- **Interacts with:** EPIC-015 (browser signer UX), EPIC-018 (host-aware identity front doors),
  EPIC-019 (mobile app and background push), EPIC-009 (typed messages, optional push delivery)
- **Unlocks:** INT-000's auth integration tier; Keycloak, Authentik, oauth2-proxy and other
  standard OIDC integrations; IndieAuth clients and personal-web applications

## Progress

| Task | Status | Notes |
|------|--------|-------|
| E21-T1 Architecture, protocol profile & threat model | **open** | Fix service boundary, trust model, canonical identifiers and OAuth/OIDC profile before code |
| E21-T2 Bridge core + native Poweur authentication | **open** | Generic for any publicly resolvable Poweur ID; browser approval is the primary path |
| E21-T3 OIDC Authorization Code + PKCE provider | **open** | Primary standards surface; discovery, JWKS, code/token/UserInfo, pairwise subjects |
| E21-T4 Browser signer and consent journey | **open** | Same-browser redirect/return first; no QR or messaging required for the normal desktop path |
| E21-T5 Cross-device QR journey | **open** | Phone scans the same short-lived request; desktop polls and resumes |
| E21-T6 IndieAuth compatibility | **open** | Profile discovery, metadata, authorization code flow and canonical `me` URL |
| E21-T7 Optional push-to-approve delivery | **open** | Separate `sys.auth.request` channel; not a contact and not required for OIDC/IndieAuth |
| E21-T8 Packaging, conformance, integrations & operations | **open** | Standalone image, colocated Compose profile, key rotation, security tests and integration recipes |

## Goal

Run a generic service such as `https://oauth.poweur.org` that lets a conventional OAuth 2.0 /
OpenID Connect relying party authenticate **any publicly resolvable Poweur ID**, regardless of
which domain or relay hosts it. The bridge translates a standard authorization request into the
existing native Poweur Sign-In proof, verifies the user's signature through the resolver chain,
then returns a standard authorization code and OIDC identity assertion.

OIDC is the primary compatibility surface because it immediately fits Keycloak, Authentik,
oauth2-proxy and mainstream web applications. IndieAuth is a second adapter over the same bridge
core for personal-web software where the user's profile URL chooses the authorization service.

The default user journey is **same-browser approval in the Poweur web signer**. A QR handoff to
the mobile app is the second path when the identity's keys are not available in that browser.
Delivery through Poweur messaging is an optional later channel, isolated in E21-T7; it must not
be required for login and must not turn the bridge into one of the user's contacts.

## Product and architecture decisions

### One generic bridge can serve the whole network

An issuer deployment is open to Poweur **identities**, not to arbitrary OIDC clients. It accepts
`alice.poweur.net`, `alice.example.com`, or any other valid ID whose identity document and key
resolve through EPIC-001. The ID's relay does not register with the bridge and shares no secret
with it.

Conventional OIDC applications explicitly configure and trust one issuer, for example:

```text
issuer = https://oauth.poweur.org
```

That issuer may authenticate users from any Poweur relay. An organization may instead run the
same bridge at `https://auth.example.com` and configure its applications to trust that issuer.
Changing issuers is visible to an OIDC relying party because the account key is the standard
pair `(iss, sub)`; Poweur must not pretend otherwise.

### Separate service boundary, integrated distribution

The bridge is a standalone service with its own origin, signing keys, client registry,
transaction store, browser sessions and release lifecycle. It may run beside a relay in one
Compose deployment or on a different machine. The relay remains the identity, messaging and
resource server; OAuth browser state and issuer keys do not enter the relay process.

Relay integration is deliberately small and contains no secret:

- optionally advertise a default bridge URL on generated identity/profile pages;
- optionally emit IndieAuth discovery for hosted identities;
- optionally advertise selected auth services in public capabilities;
- continue serving identity documents and the existing native resource-grant endpoint.

No bridge-to-relay credential is introduced. A bridge cannot mint a relay session, send as the
user, or obtain DAV access without a separate user-signed Poweur grant.

### Native Poweur Sign-In is the authentication method

OAuth 2.0 is the authorization framework and OIDC is the assertion format exposed to existing
applications. Inside the bridge, the authentication method remains EPIC-008:

1. The bridge creates a short-lived Poweur Sign-In request with its own origin as `audience`.
2. A Poweur signer verifies bridge metadata, unlocks the user's key and signs the request.
3. The bridge resolves the Poweur ID and verifies the identity or delegated-session signature.
4. The bridge displays the downstream client and requested OIDC/IndieAuth claims for consent.
5. The bridge completes the external authorization-code transaction.

Authentication to the bridge and consent to the downstream client are separate responsibilities.
V1 does not squeeze unstructured downstream OIDC parameters into the native Poweur request's
`statement`. A future structured consent receipt may bind them cryptographically if a concrete
audit use case requires it.

### Messaging is not the protocol transport

The required response travels to a short-lived HTTPS callback at the bridge. Poweur contacts,
inbox policy and durable messages are not prerequisites. E21-T7 may additionally deliver the
same public request as an encrypted typed message so an already enrolled phone receives a push,
but that message is only a notification/handoff. Approval still posts the signed response to the
bridge callback and the initiating browser resumes its existing transaction.

### Resource scopes remain separate

OIDC scopes and bridge access tokens do not silently become Poweur messaging, DAV or share
credentials. V1 exposes authentication and consented identity/profile claims. If an application
requests access to the user's home, the bridge initiates the existing EPIC-008 scoped-grant flow
and the user's relay independently verifies and enforces that signed grant.

## Standards profile

V1 follows:

- [OAuth 2.0 Authorization Framework](https://www.rfc-editor.org/rfc/rfc6749)
- [OAuth 2.0 Authorization Server Metadata](https://www.rfc-editor.org/rfc/rfc8414)
- [OAuth 2.0 Security Best Current Practice](https://www.rfc-editor.org/rfc/rfc9700)
- [Proof Key for Code Exchange (PKCE)](https://www.rfc-editor.org/rfc/rfc7636)
- [OpenID Connect Core](https://openid.net/specs/openid-connect-core-1_0.html)
- [OpenID Connect Discovery](https://openid.net/specs/openid-connect-discovery-1_0.html)
- [IndieAuth](https://indieauth.spec.indieweb.org/)

The mandatory external flow is Authorization Code with PKCE `S256`. The implicit grant is not
implemented. Dynamic OIDC client registration, refresh tokens, OpenID Federation and general
OAuth resource delegation are deferred until a named integration requires them.

## Identifier and claim model

One normative mapping connects the three public representations:

```text
Poweur ID       alice.example.com
IndieAuth me    https://alice.example.com/
did:web         did:web:alice.example.com
```

Normalization must specify lowercase DNS names, trailing-dot removal, HTTPS outside isolated
development, root-path handling, IDNA policy, redirects, and migration/alias behavior. No `www`
or other alias is inferred.

OIDC uses a pairwise opaque `sub`, derived from the canonical Poweur ID and the client's sector
identifier using a persistent per-issuer secret. The public ID is a separate consented claim,
requested with a documented scope such as `poweur_id`:

```json
{
  "iss": "https://oauth.poweur.org",
  "sub": "<pairwise opaque value>",
  "aud": "example-client",
  "amr": ["poweur"],
  "poweur_id": "alice.example.com",
  "poweur_key_fingerprint": "12345 67890 12345 67890"
}
```

The exact claim names and release rules land in E21-T1. A Poweur-aware RP may receive an
optional proof claim containing the verified native response or its digest and independently
verify it; ordinary OIDC clients trust the issuer in the conventional way.

## Primary journeys

### Same-browser approval — primary

```text
RP → bridge /authorize → enter/select Poweur ID
   → identity's web signer → unlock and approve
   → bridge callback → client consent → authorization code
   → RP callback
```

Signer discovery must work for hosted and self-hosted IDs. A hosted identity normally uses its
identity-origin `/app/`; a self-hosted identity may advertise another compatible signer. The
bridge offers an explicit chooser when several transports exist and never receives key material.

### Cross-device QR — secondary

```text
desktop /authorize → bridge shows QR + high-entropy polling handle
phone scans → signer verifies bridge metadata → user approves
phone posts signed response → desktop poll completes → RP callback
```

The QR contains the short-lived request, never a private key. Poll secrets are high entropy,
single-use, browser-session-bound and expire with the request. Copy/paste may remain an
accessibility and CLI fallback, not a promoted primary path.

## Tasks

### E21-T1 — Architecture, protocol profile & threat model

- [ ] Write `apps/docs/docs/auth/oauth-oidc-bridge.md` covering the trust boundary, generic-ID
      resolution, issuer semantics, hosted/self-hosted deployment and native-proof translation
- [ ] Specify the canonical FQDN ↔ IndieAuth profile URL ↔ `did:web` mapping and test vectors
- [ ] Specify OIDC scopes and claims, pairwise `sub`, explicit release of `poweur_id`, account
      linking and what changes when an RP moves to another issuer
- [ ] Define browser transaction, consent, denial, expiry and audit-record state machines
- [ ] Threat model: bridge compromise, forged OIDC assertions, login CSRF, mix-up, open redirect,
      SSRF through arbitrary IDs/client URLs, cookie tossing from hosted subdomains, replay,
      key rotation and malicious/compromised signers
- [ ] Record the deployment boundary: standalone service is normative; colocation is packaging

**Acceptance:** the document is sufficient to implement an independent bridge and says plainly
that generic OIDC RPs trust the bridge, while native Poweur RPs verify the user directly.

### E21-T2 — Bridge core + native Poweur authentication

- [ ] New bridge service with configurable issuer, persistent transaction store and health/build
      metadata; reuse `packages/identity/signin` rather than copying verification logic
- [ ] Accept any valid, publicly resolvable Poweur ID; reuse resolver SSRF, redirect, size,
      timeout, cache and web/DNS mismatch protections
- [ ] Create EPIC-008 auth requests, publish bridge verifier metadata, accept signed callbacks,
      enforce nonce/expiry/single-use rules and support delegated session proofs
- [ ] Browser session begins only after successful native proof; no account/password database
- [ ] Store only transaction, replay, client, pairwise-subject and audit state; never identity
      private keys, DNS tokens, relay admin credentials or DAV credentials
- [ ] Unit tests for invalid IDs, private targets, mismatch, replay, expiry, wrong audience,
      rotation, session revocation and concurrent transactions

**Acceptance:** identities on a Poweur-hosted wildcard, a different public relay and an owned
domain all authenticate to one bridge with zero relay registration or shared secret.

### E21-T3 — OIDC Authorization Code + PKCE provider

- [ ] `/.well-known/openid-configuration`, `/authorize`, `/token`, `/jwks.json` and `/userinfo`
- [ ] Authorization Code only; require PKCE `S256` for public clients and support it for every
      client; exact registered redirect URI matching, `state`, OIDC `nonce` and issuer binding
- [ ] Static/configured client registry first; hash or encrypt confidential client credentials;
      no dynamic registration in v1
- [ ] Short-lived, single-use codes; short-lived access/ID tokens; no refresh tokens in v1
- [ ] Persistent, independently rotated issuer signing keys with overlapping JWKS publication
- [ ] Pairwise subject derivation and consented `poweur_id`/fingerprint claims
- [ ] Optional native-proof claim for Poweur-aware verifiers, without changing standard OIDC
      behavior for ordinary clients
- [ ] Integration coverage against Keycloak, Authentik and oauth2-proxy configurations

**Acceptance:** each named consumer signs in an identity hosted on another relay using only normal
OIDC configuration and sees a stable `(iss, sub)`; the public ID appears only when requested and
approved.

### E21-T4 — Browser signer and consent journey

- [ ] Bridge page to enter or select a Poweur ID, with `login_hint` support and strict
      normalization
- [ ] Discover a compatible web signer; hosted identity-origin `/app/` is the default when
      advertised, with a clear chooser/failure state for self-hosted identities
- [ ] Redirect to the web signer, unlock locally, approve, submit to the bridge and resume the
      original browser transaction without exposing the signed response in URLs
- [ ] Separate downstream consent page showing verified client name, origin, requested claims,
      whether the public Poweur ID will be released, and deny/report controls
- [ ] Host-only `__Host-` cookies (`Secure`, `HttpOnly`, `SameSite`); never a parent-domain cookie;
      no third-party content and `Referrer-Policy: no-referrer` on auth pages
- [ ] Browser tests for success, denial, back/refresh, multiple tabs, expired session, malicious
      redirect and a browser that does not hold the requested identity

**Acceptance:** on a desktop with keys enrolled in the browser signer, “Sign in with Poweur” is a
redirect/unlock/approve/return journey with no QR, mobile app, message or copy/paste step.

### E21-T5 — Cross-device QR journey

- [ ] Bridge renders the same Poweur auth request as a QR and exposes a browser-session-bound
      polling transaction
- [ ] Mobile signer scans, verifies bridge metadata, displays the bridge and action, unlocks and
      submits the signed response directly to the bridge callback
- [ ] Original browser resumes exactly once; polling handles and responses cannot be swapped
      between sessions or users
- [ ] Expiry, cancellation, denial, already-used and camera-unavailable flows
- [ ] Copy/paste request/response remains a documented CLI/accessibility fallback

**Acceptance:** a desktop browser with no Poweur keys completes OIDC login using a phone, and no
private or recovery key appears in the QR, callback, browser history or bridge storage.

### E21-T6 — IndieAuth compatibility

- [ ] IndieAuth metadata plus authorization, token and revocation behavior over the same bridge
      core and browser/QR journeys
- [ ] Canonical `me=https://<poweur-id>/` returned by the code exchange
- [ ] Validate public client-ID URLs and redirect URIs per IndieAuth; apply the resolver-grade
      SSRF and redirect policy to fetched profile/client documents
- [ ] Hosted identity pages advertise the operator's configured default using the standard
      `rel=indieauth-metadata`; self-hosters can publish their chosen bridge without changing ID
- [ ] Login/profile scopes first; no implicit mapping from IndieAuth/Micropub scopes to DAV or
      messaging grants
- [ ] Test with at least two independent IndieAuth clients and one identity delegating to an
      independently hosted bridge

**Acceptance:** an unmodified IndieAuth client starts from `https://alice.example.com/`, discovers
Alice's selected bridge, completes PKCE login and receives that exact canonical `me` URL.

### E21-T7 — Optional push-to-approve delivery channel

This task is additive and does not block T2–T6.

- [ ] Define and register an encrypted, short-lived `sys.auth.request` typed message carrying the
      same request, verified client summary and a transaction-bound number match
- [ ] Bridge operates its own Poweur ID only for delivery; compromise cannot produce a user
      signature, though an OIDC RP already trusts that bridge's issuer key
- [ ] Add `trusted_auth_services` (or equivalent) as a narrow policy distinct from contacts;
      accepting auth requests grants no chat, sharing or other `sys.*` permission
- [ ] Dedicated Sign-in Requests queue/tray; do not archive requests into conversations; states
      are new/viewed/approved/denied/expired
- [ ] Number matching between initiating browser and approving device; never approve from the OS
      notification itself; require unlock/user verification
- [ ] Rate limit, deduplicate, expire and provide deny/block controls to resist push fatigue
- [ ] Foreground SSE/web delivery first; native background notification depends on EPIC-019

**Acceptance:** an enrolled device can approve a request delivered by message while the initiating
browser waits, but disabling the trusted auth service stops delivery without changing contacts or
breaking browser/QR login.

### E21-T8 — Packaging, conformance, integrations & operations

- [ ] Standalone container and documented configuration for issuer URL, database, client registry,
      signer discovery, signing keys, pairwise secret, retention and rate limits
- [ ] One-command Compose profile colocated with a relay, while preserving separate processes,
      origins, keys, storage and health checks
- [ ] Document remote deployment: bridge and relay on different operators/networks with no shared
      secret; private-only identities use a private bridge
- [ ] Back up and rotate issuer keys/pairwise secret safely; define consequences of loss and
      migration; structured audit events without auth payloads or unnecessary IDs
- [ ] Run available OIDC conformance tests and the OAuth security failure matrix; commission an
      external review before presenting the hosted bridge as production authentication
- [ ] Publish Keycloak, Authentik and oauth2-proxy recipes, IndieAuth setup, privacy policy,
      availability expectations and self-host guide
- [ ] Add a live cross-relay journey: external RP → hosted bridge → ID on independent relay → RP

**Acceptance:** the same image runs at `oauth.poweur.org` and an independent issuer; a relay-only
operator need not run it; an auth-only operator need not host identities; documented integrations
work without Poweur-specific patches.

## Non-goals

- Replacing native Poweur Sign-In inside Poweur-aware clients or relay APIs
- Making one mandatory central Poweur identity provider
- Automatically trusting arbitrary OIDC issuers in mainstream relying parties
- Sending login requests through contacts or treating the bridge as a user contact
- Giving OAuth access tokens ambient access to messages, files, shares or identity operations
- Dynamic OIDC client registration, refresh tokens, implicit flow or password grants in v1
- OpenID Federation, SIOPv2/OpenID4VP credential presentation or social-login aggregation in v1
- Building Micropub or an identity website publishing system as part of the bridge

## Open questions to close in E21-T1

- Exact OIDC claim/scope names and whether the optional native proof carries the full response or
  only a digest plus retrieval endpoint
- Signer discovery when an owned-domain identity is resolvable but does not serve a web client
- Pairwise-subject sector rules for clients with multiple redirect hosts
- Whether an IndieAuth login-only response needs any bridge access token beyond the protocol's
  minimum interoperable response
- Account-linking UX when an RP changes issuers while the user retains the same Poweur ID
- Whether the hosted bridge is open registration for OIDC clients or approval-only at launch
