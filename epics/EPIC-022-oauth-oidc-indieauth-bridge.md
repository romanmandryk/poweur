# EPIC-022 — Generic OAuth 2.0 / OIDC bridge with IndieAuth compatibility

- **Status:** in progress — bridge implemented in `apps/oauth` (T1–T4, T6, T9 done; T5, T8
  partial; T7 open). Design: [`apps/docs/docs/auth/oauth-oidc-bridge.md`](../apps/docs/docs/auth/oauth-oidc-bridge.md)
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
| E22-T1 Architecture, protocol profile & threat model | **done** | `auth/oauth-oidc-bridge.md`; ID vectors `id-input.json` |
| E22-T2 Bridge core + native Poweur authentication | **done** | `apps/oauth` (bridge 0.1.0); `TestINT_OAUTH_01` signs in IDs from two relays and DNS |
| E22-T3 OIDC Authorization Code + PKCE provider | **done** | go-oidc/x/oauth2 verified; live Keycloak/Authentik runs moved to T8; `poweur_proof` deferred |
| E22-T4 Browser signer and consent journey | **done** | Completion binding, signer discovery, consent, cookies/CSP; relay 0.1.9 publishes `web_signer`; Playwright run through a passkey signer open |
| E22-T5 Cross-device QR journey | **partial** | QR + request code + match code + bound poll; phone camera (EPIC-019) and initiator context open |
| E22-T6 IndieAuth compatibility | **done** | URL clients, `me`, redeem at both endpoints, relay `Link` header; live third-party clients → T8 |
| E22-T7 Optional push-to-approve delivery | **open** | Separate `sys.auth.request` channel; not a contact and not required for OIDC/IndieAuth |
| E22-T8 Packaging, conformance, integrations & operations | **partial** | Image, compose example, operator CLI, recipes, CI; rate limits, conformance suite, live products, prod rollout open |
| E22-T9 Client registry, developer console & user authorizations | **done** | Console, static and URL clients, `/account` |

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
Delivery through Poweur messaging is an optional later channel, isolated in E22-T7; it must not
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
inbox policy and durable messages are not prerequisites. E22-T7 may additionally deliver the
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
- [OAuth 2.0 Authorization Server Issuer Identification](https://www.rfc-editor.org/rfc/rfc9207)
- [OAuth Client ID Metadata Document](https://datatracker.ietf.org/doc/draft-ietf-oauth-client-id-metadata-document/) (draft; URL client IDs, E22-T9)

The mandatory external flow is Authorization Code with PKCE `S256`. The implicit grant is not
implemented. RFC 7591 dynamic client registration, refresh tokens, OpenID Federation and general
OAuth resource delegation are deferred until a named integration requires them. URL client IDs
(E22-T9) are not dynamic registration: nothing is written to the registry.

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

The exact claim names and release rules land in E22-T1. A Poweur-aware RP may receive an
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

### E22-T1 — Architecture, protocol profile & threat model — **done**

- [x] Write `apps/docs/docs/auth/oauth-oidc-bridge.md` covering the trust boundary, generic-ID
      resolution, issuer semantics, hosted/self-hosted deployment and native-proof translation
- [x] Specify the canonical FQDN ↔ IndieAuth profile URL ↔ `did:web` mapping and test vectors
      (`identity.NormalizeIDInput`, `IndieAuthProfileURL`, `DIDWebID`; `vectors/id-input.json`)
- [x] Specify OIDC scopes and claims, pairwise `sub`, explicit release of `poweur_id`, account
      linking and what changes when an RP moves to another issuer
- [x] Define browser transaction, consent, denial, expiry and audit-record state machines
- [x] Threat model: bridge compromise, forged OIDC assertions, login CSRF, mix-up, open redirect,
      SSRF through arbitrary IDs/client URLs, cookie tossing from hosted subdomains, replay,
      key rotation and malicious/compromised signers
- [x] Record the deployment boundary: standalone service is normative; colocation is packaging

**Acceptance:** the document is sufficient to implement an independent bridge and says plainly
that generic OIDC RPs trust the bridge, while native Poweur RPs verify the user directly.

### E22-T2 — Bridge core + native Poweur authentication — **done**

- [x] New bridge service (`apps/oauth`, `poweur-oauth`) with configurable issuer, SQLite store
      and `/health`; reuses `packages/identity/signin` for every verification step
- [x] Accept any valid, publicly resolvable Poweur ID through `identity.Resolve`; its HTTP client
      checks the *dialled* address (no DNS-rebinding window), follows no redirects, caps size
      and time; web/DNS mismatch protection comes with the resolver
- [x] Create EPIC-008 requests, publish bridge metadata, accept signed callbacks, enforce
      nonce/expiry/single-use (the store is the atomic nonce cache), one approval per
      transaction, and the completion binding (resume code + binding cookie / match code)
- [x] Browser session begins only after a verified native proof; no account/password database
- [x] Store only transaction, replay, session, client, consent, code/token hashes, sealed issuer
      keys, pairwise secret and audit state
- [x] Unit tests for invalid IDs, mismatch (another identity's approval), replay, expiry,
      forwarded links, wrong match codes, private addresses, two tabs, identity change
      (`bridge/native_test.go`, `crypto_test.go`). Delegated session proofs are verified by the
      shared verifier (`packages/identity/signin`) and surface as `amr: ["poweur","session"]`;
      *revoked* sessions are only knowable to the relay, which is where scoped grants check them
- [x] Hosted-handle reserved list gains `oauth`, `sso`, `idp`, `openid`, `indieauth` (Go + TS)
- [x] Issuer must be a dotted host (the native audience derives an app namespace) — checked at start

**Acceptance:** met by `TestINT_OAUTH_01`: identities on a hosted wildcard, on a second relay with
another hosted domain, and a DNS-published identity all sign in to one bridge with no relay
registration or shared secret.

### E22-T3 — OIDC Authorization Code + PKCE provider — **done** (live Keycloak/Authentik runs → T8)

- [x] `/.well-known/openid-configuration` (+ RFC 8414 alias), `/authorize`, `/token`,
      `/jwks.json`, `/userinfo`, `/revoke`, `/introspect`
- [x] Authorization Code only; PKCE `S256` required for every client; exact redirect matching
      (loopback any-port for public clients only); `state`, `nonce`, `iss` (RFC 9207);
      duplicate parameters refused; errors before the redirect is trusted render a page
- [x] Client lookup through one registry (static, console, URL)
- [x] Codes: 256-bit, hashed, single-use, 60 s; reuse revokes the first redemption's tokens.
      ID tokens 5 min, access tokens 10 min, no refresh tokens
- [x] RS256 keys sealed with AES-GCM under `OAUTH_KEY_ENCRYPTION_KEY`, kid-bound; rotation keeps
      retired keys published for 24 h (`poweur-oauth keys rotate`); a running server reloads
- [x] Pairwise subject (`HMAC(secret, sector, id)`, sector fixed at creation) and consented
      `poweur_id`/fingerprint/profile claims; `OAUTH_SUBJECT_TYPE=public`
- [x] SSO: session reuse, `prompt=none|login|consent`, `max_age`, `login_hint`, `auth_time`
- [x] Client authentication: `client_secret_basic`/`_post`, `private_key_jwt` (RS256/ES256/EdDSA,
      `jti` replay guard, audience and lifetime checks), `none`
- [ ] Optional native-proof claim — **deferred** (claim name `poweur_proof` reserved; see the
      design's open questions)
- [x] Integration coverage with `coreos/go-oidc` + `x/oauth2` — the libraries oauth2-proxy is
      built on — against real relays (`TestINT_OAUTH_01/02`)
- [ ] Runs against live Keycloak, Authentik and oauth2-proxy containers — **moved to E22-T8**
      (recipes are in `apps/oauth/README.md`)

**Acceptance:** met for the go-oidc stack; the named products are verified in T8.

### E22-T4 — Browser signer and consent journey — **done** (Playwright journey through the web signer open)

- [x] Identify page with `login_hint` and strict normalization (profile URLs accepted)
- [x] Signer discovery: `endpoints.web_signer`, else the identity-origin `/app/` for web-resolved
      IDs, plus the operator default (labelled) and the `poweur://` deep link; nothing is probed
- [x] Redirect to the web signer, approve, POST to the bridge, resume the original browser with
      nothing signed in any URL
- [x] Native completion binding ("Who may complete a sign-in", `auth/sign-in.md`): a delivery
      without a match code finishes only at a single-use `resume_uri` in the browser holding the
      RP's binding cookie; one with the code finishes only through the starting page's poll
      secret. Shared Go helpers `signin.ParseDelivery`/`NewMatchCode`/`NewSecret`/`CheckResumeURI`,
      TS `checkResumeUri`/`normalizeMatchCode`. The guestbook implements it and refuses approvals
      in URLs; the web signer has a code field (required in the shell) and follows `resume_uri`;
      `poweur auth approve --code`. Unit + `TestINT_SIGNIN_02` (forwarded link signs nobody in).
      Web 0.1.18, SDK 0.1.5, CLI 0.1.8
- [x] Relays serve `endpoints.web_signer` (and `endpoints.oauth_bridge`) in `capabilities.json`
      without writing into the user's tree (relay 0.1.9)
- [x] Consent page: client name, return host, "registered by" + "not reviewed" for console
      clients, verified host for URL clients, tick-boxes for `poweur_id`/`profile`, deny, report
- [x] `__Host-` cookies (`Secure`, `HttpOnly`, `SameSite=Lax`) on https; strict CSP with
      `frame-ancestors 'none'`, `Referrer-Policy: no-referrer`, same-origin form-post checks
- [x] Journey tests (Go HTTP harness): success, denial, cancel, refresh/continue, two tabs,
      expiry at each stage, forwarded link, wrong browser, session change before consent,
      cross-site posts
- [ ] Playwright journey through the real web signer with a passkey — open (the same flow is
      covered by the Go harness and the web signer's own unit tests)

**Acceptance:** met by the harness and by hand in the browser (`scripts/dev.sh`).

### E22-T5 — Cross-device QR journey — **partial**

- [x] QR (inline SVG), request code with copy button, and a browser-bound status poll (the
      binding cookie is the poll secret)
- [ ] Mobile signer scans a QR — the shell has no camera flow yet (EPIC-019); it can paste the
      request code and type the match code today
- [x] Original browser resumes exactly once; handles cannot be swapped between browsers
- [x] Number matching: the approving device must send the code the starting screen shows; one
      attempt, then the transaction fails
- [ ] Initiator context (coarse location, browser, elapsed time) shown on the signer — open;
      needs a signer-readable context endpoint
- [x] Expiry, cancellation, denial and already-used flows
- [x] Copy/paste request code remains the CLI/accessibility path (`poweur auth approve --code`)

**Acceptance:** met with the CLI as the other device (`TestINT_OAUTH_02`); a phone scanning the QR
waits on EPIC-019.

### E22-T6 — IndieAuth compatibility — **done** (interop runs against third-party clients → T8)

- [x] Metadata at `/.well-known/oauth-authorization-server`; authorization, code redemption at
      the authorization endpoint *and* the token endpoint, introspection, revocation
- [x] Canonical `me=https://<poweur-id>/` (+ `profile` when released); no access token for
      login/profile-only grants
- [x] Client-ID URL rules (https, path, no dot segments, no credentials/fragment, no IP but
      loopback); client metadata fetched with the safe client; redirect URIs same-origin with the
      client or listed in its metadata (and then only same-origin or loopback)
- [x] Relays with `OAUTH_BRIDGE_URL` send `Link: …; rel="indieauth-metadata"` on hosted identity
      roots; self-hosters publish their own
- [x] Login/profile scopes only; nothing maps to DAV or messaging
- [ ] Run two independent IndieAuth clients against it — **moved to E22-T8**

**Acceptance:** met at protocol level by `bridge/indieauth_test.go`; live clients in T8.

### E22-T7 — Optional push-to-approve delivery channel — **open**

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

### E22-T8 — Packaging, conformance, integrations & operations — **partial**

- [x] Standalone container (`apps/oauth/Dockerfile`, non-root, healthcheck) and documented
      configuration (`apps/oauth/README.md`)
- [x] Compose example with a relay beside it (`apps/oauth/compose.example.yml`), separate
      processes, origins, keys and storage; `scripts/dev.sh` for local work
- [ ] Document remote deployment across operators — partly in the design doc; no runbook yet
- [x] Operator CLI: `keys list|rotate`, `clients list|suspend|unsuspend`, `prune`, `gen-key`,
      `hash-secret`; hourly pruning with retention; backup guidance and loss consequences;
      structured audit events without payloads
- [ ] Rate limiting on `/authorize`, `/t/*/identify` and `/token` (identify triggers outbound
      fetches) — open
- [ ] OIDC conformance suite, OAuth security failure matrix, external review — open (human)
- [x] Keycloak, Authentik, oauth2-proxy and Grafana recipes (untested against live products)
- [ ] Live runs of those products and of two IndieAuth clients; privacy policy; production
      deployment at `oauth.poweur.org` (ansible/Caddy) — open
- [x] Cross-relay journey in CI: RP → bridge → IDs on independent relays → RP (`TestINT_OAUTH_01`)

**Acceptance:** the same image serves any issuer; relays need only `OAUTH_BRIDGE_URL`; live
product verification and the hosted rollout remain.

### E22-T9 — Client registry, developer console & user authorizations — **done**

Three ways a client reaches the bridge, one registry interface, specified in the
[design](../apps/docs/docs/auth/oauth-oidc-bridge.md#clients):

- [x] **Developer console** at `/developers` — *the main path*: owner signs in with native Poweur
      Sign-In; create client (name, redirect URIs, sector host, auth method); secret shown once,
      stored as SHA-256; rotate with 7-day overlap; retire; edit; delete (typed confirmation);
      co-owner IDs; audit events per change
- [x] Static clients from `OAUTH_STATIC_CLIENTS` (plaintext or `client_secret_sha256`), with
      `first_party` consent skip for `openid` only
- [x] **URL client IDs** for IndieAuth (`OAUTH_URL_CLIENTS=indieauth` by default; `on` admits
      Client ID Metadata Document clients on the OIDC surface; `off`)
- [x] Operator posture: `OAUTH_CLIENT_REGISTRATION=open|allowlist|closed`, allowlist with
      `*.domain` suffixes, per-owner limit, 5 creations/hour, `poweur-oauth clients suspend`
- [x] Consent labels as specified; report link on every consent page; `/abuse`
- [x] **User `/account`**: authorized apps with released claims and last use, revoke (deletes
      consent, revokes access tokens), recent sign-ins, sign out
- [x] Sector fixed at creation; editing redirect URIs keeps `sub` (`TestConsoleClientLifecycle`)
- [x] Server-rendered pages, strict CSP, no third-party content; lifecycle tests from create to
      token to revoke

**Acceptance:** met: a console client signs in a user from another identity and redeems with its
secret; URL clients sign in without registration; revoking from `/account` kills tokens and
brings consent back.

## Non-goals

- Replacing native Poweur Sign-In inside Poweur-aware clients or relay APIs
- Making one mandatory central Poweur identity provider
- Automatically trusting arbitrary OIDC issuers in mainstream relying parties
- Sending login requests through contacts or treating the bridge as a user contact
- Giving OAuth access tokens ambient access to messages, files, shares or identity operations
- RFC 7591 dynamic client registration, refresh tokens, implicit flow or password grants in v1
- A web admin UI for issuer keys and operator actions (CLI/config in v1)
- OpenID Federation, SIOPv2/OpenID4VP credential presentation or social-login aggregation in v1
- Building Micropub or an identity website publishing system as part of the bridge

## Open questions to close in E22-T1

Proposed answers are in the design draft; confirm them in review before closing T1.

- ~~Exact OIDC claim/scope names~~ → `openid`, `poweur_id`, `profile`; native proof claim
  `poweur_proof` reserved, not in v1
- Signer discovery when an owned-domain identity is resolvable but does not serve a web client
  → `endpoints.web_signer`, else app / QR / default-signer chooser; in-browser paste flow decided in T4
- ~~Pairwise-subject sector rules for clients with multiple redirect hosts~~ → sector host fixed
  at client creation
- Whether an IndieAuth login-only response needs any bridge access token beyond the protocol's
  minimum interoperable response (T6)
- ~~Account-linking UX when an RP changes issuers~~ → only via a released `poweur_id`; documented,
  not automated
- ~~Open registration or approval-only at launch~~ → operator setting; hosted bridge launches
  `open` with URL clients on (E22-T9)
