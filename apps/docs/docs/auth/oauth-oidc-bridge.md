---
id: oauth-oidc-bridge
sidebar_position: 6
title: OAuth 2.0 / OIDC bridge
---

# OAuth 2.0 / OIDC bridge

> **Status: implemented in `apps/oauth` (EPIC-022).** Native authentication, the OIDC provider,
> the developer console, `/account`, IndieAuth and the QR/code journey are built and tested
> against real relays with `go-oidc` as the relying party (`apps/integration`, `TestINT_OAUTH_*`).
> Still open: push-to-approve (E22-T7), production rollout and recipes verified against live
> Keycloak/Authentik (E22-T8). Operator guide: [`apps/oauth/README.md`](https://github.com/poweur/poweur/blob/main/apps/oauth/README.md).

Native [Sign in with Poweur ID](./sign-in.md) needs no issuer: a relying party verifies the
user's signature itself. Most existing software cannot do that — Keycloak, Authentik,
oauth2-proxy, Grafana, Gitea and thousands of web apps speak OpenID Connect and nothing else.
The bridge is a conventional OpenID Provider whose *only* authentication method is a native
Poweur Sign-In proof. It lets those applications accept **any publicly resolvable Poweur ID**,
on any relay, with ordinary OIDC configuration.

The one sentence to keep in mind: **a native Poweur RP verifies the user; an OIDC RP verifies
the bridge.** An OIDC relying party trusts the issuer it configured, exactly as it trusts Google
or Keycloak. Nothing here changes that, and nothing here pretends otherwise.

## Roles and trust boundary

```
 OIDC relying party            bridge (issuer)                 web / mobile signer      user's relay
 (Keycloak, oauth2-proxy…)     https://oauth.poweur.org        (holds the keys)         (identity doc)
        │                              │                              │                      │
        │ 1. /authorize (PKCE)         │                              │                      │
        ├─────────────────────────────►│ 2. ask for a Poweur ID       │                      │
        │                              │ 3. native sign-in request    │                      │
        │                              │    audience = issuer origin  │                      │
        │                              ├──── ?auth= redirect ────────►│ 4. fetch issuer's    │
        │                              │◄─── GET /.well-known/poweur.json (verifies origin)   │
        │                              │                              │ 5. unlock, approve   │
        │                              │◄─── POST signed response ────┤                      │
        │                              │ 6. resolve + verify ─────────────────────────────────►
        │                              │◄─── browser resumes (resume code + cookie) ─┘       │
        │                              │ 7. consent to *this client*  │                      │
        │◄──── code + state + iss ─────┤                              │                      │
        │ 8. /token (code_verifier)    │                              │                      │
        ├─────────────────────────────►│ 9. id_token (pairwise sub)   │                      │
```

| Party | Holds | Never holds |
|-------|-------|-------------|
| Signer | identity/session private keys | issuer keys, client secrets |
| Bridge | issuer signing keys, pairwise secret, client registry, transactions, consents | any identity private key, relay credentials, DAV tokens, DNS tokens |
| User's relay | identity document, the user's home | anything about the bridge — it is not told a bridge exists |
| OIDC RP | its client credentials, `(iss, sub)` | the ability to verify the user without trusting the issuer |

No bridge↔relay credential exists. A compromised bridge can mint ID tokens for *its own*
relying parties — every OP carries that risk — but cannot sign as the user, send a message,
read a file, or produce a native approval another Poweur RP would accept (native approvals are
bound to the audience that requested them).

## Deployment boundary

The bridge is a **standalone service** (`apps/oauth`, binary `poweur-oauth`): its own origin,
database, keys, cookies and release cycle. Running it in the same Compose file as a relay is
packaging, not architecture (E22-T8).

Rules for the origin:

- **Never under a hosted identity domain.** `oauth.poweur.net` sits beside `alice.poweur.net`
  in one cookie site and one wildcard certificate. Host the issuer on a registrable domain that
  issues no identities (`poweur.org`, `auth.example.com`). As defence in depth every bridge
  cookie uses the `__Host-` prefix, and the hosted-handle reserved list gains `oauth`, `sso`,
  `idp`, `openid` and `indieauth` (E22-T2) so no user can claim the name of the bridge.
- **The issuer is an exact URL** — `https://oauth.poweur.org`, no path, no trailing slash. It is
  also the native Sign-In `audience`, so one string names the bridge in both protocols — and
  therefore needs a dotted host (the audience derives an app namespace), so local development
  uses `http://oauth.localhost:8090`, never bare `localhost`.
- An organization may run its own issuer. Its RPs see a different `iss` and therefore different
  accounts; see [Changing issuers](#changing-issuers).

## Identifiers

One canonical form, three projections:

| Form | Value for `alice.example.com` |
|------|-------------------------------|
| Poweur ID (canonical) | `alice.example.com` |
| IndieAuth `me` / profile URL | `https://alice.example.com/` |
| DID | `did:web:alice.example.com` |

Normalization of user input, in order:

1. Trim whitespace; strip a leading `@`.
2. If it parses as a URL: scheme must be `https` (`http` only with the development flag); no
   userinfo, port, query or fragment; path must be empty or `/`. Take the host.
3. Lowercase; remove one trailing dot.
4. Must pass `identity.ValidateIdentityName` — ASCII only; an IDN is accepted only as its
   `xn--` form. No Unicode-to-punycode conversion is done on the user's behalf.
5. No alias is inferred: `www.` is never stripped or added, and redirects are never followed
   (the resolver chain already refuses them).

Conformance vectors (`packages/identity/testdata/vectors/id-input.json`, generated by `identity.NormalizeIDInput`):

| Input | Result |
|-------|--------|
| `Alice.Example.com` | `alice.example.com` |
| `alice.example.com.` | `alice.example.com` |
| `@alice.example.com` | `alice.example.com` |
| `https://alice.example.com` | `alice.example.com` |
| `https://alice.example.com/` | `alice.example.com` |
| `https://alice.example.com/blog` | reject: path |
| `https://alice.example.com:8443/` | reject: port |
| `http://alice.example.com/` | reject: scheme (dev flag: accept) |
| `alice` | reject: not a FQDN (a bridge knows no default domain) |
| `аlice.example.com` (Cyrillic а) | reject: non-ASCII |
| `xn--80ak6aa92e.example` | accept (IDN, already punycode) |

## Authentication: the bridge is a native relying party

The bridge uses `packages/identity/signin` unmodified — `Verifier{Origin: issuer}` with a
shared, atomic nonce cache (a unique index in the bridge database).

It publishes ordinary RP metadata:

```json
{
  "poweur_auth": "1",
  "origin": "https://oauth.poweur.org",
  "name": "Poweur OAuth bridge",
  "response_uris": ["https://oauth.poweur.org/poweur/callback"],
  "transports": ["redirect", "qr", "deeplink"],
  "contact_uri": "https://oauth.poweur.org/abuse"
}
```

The native request is **login-only**: `action: "signin"`, no `scopes`, and a fixed statement
naming the bridge. Downstream client names and OIDC claims are **not** placed in `statement`:
the signer shows the bridge, the bridge then shows the client. Authentication to the bridge and
consent to a client are separate screens with separate responsibilities.

### Finding a signer

After the user enters an ID (or `login_hint` supplies one), the bridge resolves the identity
and picks where to send the browser:

1. `endpoints.web_signer` from the identity's `/.well-known/poweur/capabilities.json`. A relay
   that serves the web app fills in `https://<id>/app/` when the user's own file does not say
   otherwise, and adds `endpoints.oauth_bridge` when its operator sets `OAUTH_BRIDGE_URL`.
2. Failing that, `https://<id>/app/` when the identity resolved over HTTPS (its relay serves the
   app beside the document).
3. The operator's default web signer (`OAUTH_DEFAULT_SIGNER`), labelled plainly as "works only
   if this browser already holds your keys there"; the `poweur://` deep link; and, for another
   device, a QR code, the request code and the match code.

The bridge never embeds or frames a signer and never receives key material.

### Same-browser return without the response in a URL

Today's web signer POSTs the approval to `response_uri`, then offers a "Continue" link that
repeats the approval as `?response=` in a URL. The bridge refuses responses in URLs (they land
in history, logs and `Referer`). The flow instead:

1. Signer POSTs the encoded response to `/poweur/callback` (`text/plain`, as today).
2. The bridge verifies it and answers `200 {"status": "ok", "resume_uri": "https://oauth.poweur.org/poweur/resume?code=<code>"}`.
   The resume code is 256-bit random, single-use, valid for 60 seconds, and bound to the
   transaction.
3. The signer navigates the same browser to `resume_uri` (it must be same-origin with the
   audience; the signer checks).
4. The bridge continues only if the resume code **and** the transaction's `__Host-` browser
   cookie both match.

Requiring both is what defeats a forwarded login link: an attacker who starts a transaction and
tricks Alice into approving it never receives the resume code (it goes to Alice's signer), and
Alice's browser, which does, lacks the attacker's cookie. The `resume_uri` reply is an additive
extension of the native callback — E22-T4 lands it in [sign-in.md](./sign-in.md) and teaches
the web signer to prefer it over `?response=`.

Cross-device (QR) approval cannot use a resume code and keeps the residual risk every QR login
has; see the threat model.

## Transactions

A transaction is created by a valid `/authorize` call and lives at most **10 minutes**.

```
             invalid client / redirect_uri ──► error page (never redirect)
/authorize ──► identify ──► awaiting_signature ──► authenticated ──► resumed ──► consent
                  ▲               │ (native request ≤ 5 min;              │          │
                  └── retry ──────┘  a new one may be issued)             │     approve │ deny
                                                                          │          ▼   ▼
                                               existing session + stored consent ──► code_issued ──► redeemed
                                                                                        │
                                                                     access_denied ◄────┘ (deny)
Any state ──► expired (TTL) │ failed (verification error, shown to the user, not redirected)
```

- `redirect_uri` and `client_id` are validated **before** anything is shown. An invalid pair
  renders an error page and never redirects (open-redirect guard).
- Errors after that point redirect with `error`, `state` and `iss` (RFC 9207), never with
  details of why verification failed.
- Authorization codes: 256-bit random, stored hashed, single-use, **60 seconds**, bound to
  client, redirect URI, PKCE challenge, nonce and identity. A second redemption revokes the
  tokens issued by the first.
- ID tokens: 5 minutes. Access tokens: 10 minutes, opaque, good only at `/userinfo`. No refresh
  tokens in v1.

### Browser session and SSO

After a successful resume the bridge sets `__Host-poweur_session` (`Secure`, `HttpOnly`,
`SameSite=Lax`, 12 hours, bound to one identity). With a live session and a stored consent that
covers the requested claims, a later `/authorize` issues a code without another signature.
`prompt=login` or an exceeded `max_age` forces a new native proof; `prompt=none` without a
session or consent returns `login_required` / `consent_required`. `auth_time` is the time of
the native proof, not of the session cookie.

## OIDC profile

**Endpoints**

| Path | Purpose |
|------|---------|
| `/.well-known/openid-configuration` | discovery (RFC 8414 alias at `/.well-known/oauth-authorization-server`) |
| `/authorize` | Authorization Code; `response_type=code` only |
| `/token` | `authorization_code` grant only |
| `/jwks.json` | issuer public keys, current + previous |
| `/userinfo` | claims for an access token |
| `/revoke` | RFC 7009 access-token revocation |
| `/introspect` | RFC 7662, for the client that owns the token |
| `/.well-known/poweur.json`, `/poweur/callback`, `/poweur/resume` | the native RP side |
| `/t/{id}`, `/t/{id}/status`, `/t/{id}/continue` | a transaction's pages and its browser-bound status poll |
| `/login`, `/account`, `/developers` | bridge sign-in, the user's authorizations, the developer console |

**Requirements**

- PKCE `S256` required for **every** client, confidential or public. `plain` is refused.
- `state` required. `nonce` required when `openid` is requested.
- `iss` returned on the authorization response (RFC 9207) so a client talking to several
  issuers can detect mix-up.
- Exact string match on `redirect_uri`, except loopback redirects (`http://127.0.0.1:<any port>/…`,
  `http://[::1]:<any port>/…`) for public clients, per RFC 8252.
- ID tokens signed **RS256** by default — the one algorithm OIDC Core requires every RP to
  accept. ES256/EdDSA may be published in addition once the named integrations are tested
  against them.
- Signing keys rotate on a schedule; a retired key stays in JWKS for at least 2× the ID-token
  lifetime plus the longest RP JWKS cache the recipes document (24 h).

**Scopes and claims**

| Scope | Claims | Released |
|-------|--------|----------|
| `openid` | `iss`, `sub`, `aud`, `exp`, `iat`, `auth_time`, `nonce`, `amr` | always |
| `poweur_id` | `poweur_id`, `poweur_key_fingerprint`, `poweur_id_url` | shown ticked on consent; released only if left ticked |
| `profile` | `name`, `picture`, `profile` from the identity's public profile | shown ticked on consent; released only if left ticked |

`amr` is `["poweur"]`, with `"session"` appended when a delegated session key signed. `email`
is not offered: a Poweur ID is not an email address, and inventing one would be a lie some RP
would eventually believe. `profile` claims are read from the *public* profile at consent time
and marked in the consent screen as "public anyway"; `picture` is the public avatar URL on the
identity origin, never proxied through the bridge.

### Pairwise subject

```
sub = base64url( HMAC-SHA256( pairwise_secret,
        "poweur-oidc-sub-v1\n" + sector + "\n" + poweur_id ) )
```

- `pairwise_secret` is 32 random bytes per issuer, backed up with the signing keys. **Losing it
  changes every `sub` for every RP** — it is the most important secret the bridge holds.
- `sector` is fixed per client **when the client is created** and never changes afterwards, so
  editing redirect URIs cannot silently re-key a user's account:
  - URL client IDs: the `client_id` URL's host.
  - Registered and static clients: the host of the first redirect URI, or an explicit sector
    host chosen at creation. Two clients from one vendor that should see the same `sub` share
    a sector deliberately.
- The public ID is never derivable from `sub`. An RP that needs to know *who* someone is asks
  for `poweur_id` and gets it only if the user agrees.

A Poweur ID is a public DNS name, so the case for pairwise subjects is weaker here than for an
email-shaped identity: the argument is not that the name is secret but that *publishable* and
*handed to every site automatically* are different things. Pairwise costs almost nothing to
implement and keeps a login-only RP from learning the name, so it stays the default. Two honest
drawbacks: an RP that asks for nothing but `openid` shows its user as an opaque string, which is
why the integration recipes request `poweur_id`; and losing the pairwise secret re-keys every
account at every RP. `OAUTH_SUBJECT_TYPE=pairwise|public` lets a deployment — typically a
company bridge in front of its own tools — set `sub` to the canonical Poweur ID instead, gaining
human-readable logs and cross-app account portability at the cost of correlation.

### Changing issuers

`(iss, sub)` is the OIDC account key, so moving an RP from one bridge to another creates new
accounts. The only portable link is the `poweur_id` claim: an RP that stored it can offer
"link your existing account" after the user signs in via the new issuer and releases it. The
bridge documents this; it does not attempt to export or synchronize subjects between issuers.

## Clients

A client can reach the bridge in three ways, and the console is the one the documentation leads
with. All three feed one registry interface, and all three show the user who they are dealing
with before any claim is released.

### Why not one shared client ID

The tempting shortcut is one `client_id` and secret for the whole bridge, with each app passing
its own `redirect_uri`. It matches the native protocol's "nobody registers" feel, and it fails
for four structural reasons:

- **The redirect allowlist is the entire boundary.** With a redirect URI nobody authenticated,
  anyone may run the flow with the shared credentials and `redirect_uri=https://evil.example/cb`,
  collect an authorization code for a user they phished through the bridge, and exchange it. RFC
  9700 requires exact, pre-registered redirect URIs for exactly this reason.
- **`aud` stops separating applications.** One client ID means every app receives tokens with the
  same audience, so an ID token minted for app A verifies at app B. Any RP that accepts an
  assertion it did not fetch itself (mobile and SPA backends, API gateways) becomes
  impersonatable from any other app. Audience separation is the only structural defence.
- **Consent cannot name the app.** The bridge would know nothing about the caller but an
  attacker-supplied URL. Native Sign-In resists phishing *because* the signer fetches the RP's
  metadata over TLS and shows a verified name; a shared client throws that away one layer up.
- **A shared secret is not a secret.** It would live in every self-hoster's config, every doc and
  every screenshot, so confidential-client authentication would be theatre — and per-app
  suspension, per-app rate limits and a per-app pairwise `sub` all become impossible.

What survives is per-client identity: redirect URIs are either registered through the console or
authenticated by the client's own TLS-served document (IndieAuth's URL client IDs). Either way
the bridge knows *which application it is talking about* before it shows a consent screen, and
that is the property everything else depends on.

### 1. Registered clients — the developer console

Most software that speaks OIDC expects a `client_id` and `client_secret` typed into a config
field: Keycloak identity brokering, Authentik sources, oauth2-proxy, Grafana, Nextcloud, CMS
plugins. The bridge serves a self-service console at `/developers` for them, and this is the
path the documentation leads with.

- **Sign in to the console with your Poweur ID.** The console is a native relying party of its
  own origin — no password, no account table, no email verification. The owner of a client *is*
  a Poweur ID, which is a more accountable and more revocable statement than a developer signup.
- Create a client: name, redirect URIs (exact HTTPS, or loopback for native apps), optional
  sector host. The bridge returns `client_id` (`pwc_…`) and a `client_secret` **shown once**;
  secrets are 256-bit random and stored as SHA-256 (a slow hash buys nothing against that much
  entropy).
- Rotate with overlap: a second secret is issued and the old one keeps working until the owner
  retires it or 7 days pass. Edit the name and redirect URIs; delete the client.
- Co-owners: further Poweur IDs that may manage the client (a group identity, EPIC-005 E05-T5,
  as owner comes later).
- `token_endpoint_auth_method`: `client_secret_basic`, `client_secret_post`, or `private_key_jwt`
  with a pasted or linked JWKS.
- Every change is an audit event attributed to the owner ID.

Consent shows the client's name, **"registered by `alice.example.com`"** and the redirect host.
A signed, resolvable owner is not a verified brand, and the label says so rather than implying a
review that did not happen.

Abuse controls: a per-owner client limit (default 10), rate-limited creation, a report link on
every consent page, and operator suspension (`poweur-oauth clients suspend`). Operators choose
the posture:

```
OAUTH_CLIENT_REGISTRATION = open | allowlist | closed
```

The hosted `oauth.poweur.org` launches `open`; a company bridge usually runs `closed` with
static clients only.

### 2. Static clients — operator configuration

A JSON file (`OAUTH_STATIC_CLIENTS`) in the registered-client schema, read at start. Shown
read-only in the console. A static client may be marked `first_party: true`, which skips the
consent screen for `openid` alone (never for `poweur_id` or `profile`). This is what a private
deployment in front of its own Grafana and Forgejo uses.

RFC 7591 **dynamic registration** stays out of v1: it is an anonymous write endpoint into the
registry, and the console covers the same need with an accountable owner.

### 3. URL client IDs — because IndieAuth requires them

In IndieAuth a client *is* a URL: the client identifier is the application's own address, and
its redirect URIs are discovered from the document served there. E22-T6 cannot be implemented
without that fetcher, so the bridge has one — and the same mechanism is the IETF
[OAuth Client ID Metadata Document](https://datatracker.ietf.org/doc/draft-ietf-oauth-client-id-metadata-document/)
draft that some MCP clients are adopting.

It is **not** the general path. Asking an integrator to publish and maintain a JSON file on
their own site is more work than pasting a secret, not less, and it moves client identity into a
document the bridge must fetch, cache and invalidate. Default posture:

```
OAUTH_URL_CLIENTS = indieauth | on | off     # default: indieauth
```

Where accepted, the rules are strict:

```json
{
  "client_id": "https://notes.example.com/oauth/client.json",
  "client_name": "Example Notes",
  "redirect_uris": ["https://notes.example.com/auth/callback"],
  "token_endpoint_auth_method": "none"
}
```

- Fetched with resolver-grade rules: HTTPS only, no redirects, no private addresses, 16 KiB
  cap, 5 s timeout; cached per HTTP cache headers, clamped to 5 minutes – 24 hours.
- `client_id` inside the document must equal the URL it was fetched from.
- `redirect_uris` must be same-origin with the `client_id`, or loopback.
- `none` (public, PKCE) or `private_key_jwt` with a same-origin `jwks_uri`. `client_secret_*` is
  refused — there is nowhere to have agreed a secret.
- `logo_uri` must be same-origin, loaded under the consent page's CSP or not at all.
- Consent shows the name **and the verified host**: "Example Notes — notes.example.com".

Redirect URIs travel with the client rather than living in the registry, but they are
authenticated by the client's own TLS-served document: dynamic and authenticated, not dynamic
and trusted.

## What the user manages

At `/account`, signed in with their Poweur ID, a user sees:

- **Authorized apps** — every client they consented to: name, host or owner, claims released,
  first and last use. **Revoke** deletes the stored consent, revokes live access tokens and
  forces a new consent next time. (There are no refresh tokens to revoke.) It cannot delete an
  account the RP created; the page says so.
- **Recent sign-ins** — time, client, whether a session key signed, coarse user agent.
- **Sign out** of the bridge session.

This is the bridge's own record. It is deliberately **not** written into the user's home: the
bridge holds no credential for it, and a login that silently wrote into your tree would be
exactly the ambient access the design refuses. Native connected apps
([Connected apps](./connected-apps.md)) remain the in-home record for resource grants.

## Operator management

Issuer signing keys, the pairwise secret, client suspension and retention are **CLI and
configuration**, not web UI — `poweur-oauth keys rotate`, `poweur-oauth clients suspend <id>`,
`poweur-oauth hash-secret` for static clients.
A web admin surface is one more authenticated attack surface on the most sensitive service an
operator runs; v1 does without it (E22-T8 packages the commands).

## Storage

SQLite by default (Postgres behind the same interface for multi-instance). Tables: `clients`,
`client_secrets` (hashed), `transactions`, `codes` (hashed), `access_tokens` (hashed),
`consents`, `nonces` (unique index — the atomic replay guard), `url_client_cache`,
`audit_events`, `signing_keys` (encrypted at rest with an operator-supplied key).

Retention: transactions, codes and nonces are deleted after expiry; audit events 90 days by
default; consents until revoked or 1 year unused.

## Threat model

| Threat | Mitigation |
|--------|------------|
| **Bridge compromise** | Attacker can mint ID tokens to this issuer's RPs — inherent to any OP; keys encrypted at rest, rotation and incident runbook (E22-T8). Cannot forge native approvals for other audiences, cannot touch relays or homes. |
| **Forged OIDC assertions** | RS256 with published JWKS; `aud`, `iss`, `exp`, `nonce` required; code bound to PKCE and client. |
| **Login CSRF / session swap** | `state` + PKCE + nonce on the OIDC leg; resume code + `__Host-` cookie on the native leg (see [Same-browser return](#same-browser-return-without-the-response-in-a-url)). |
| **Mix-up** | `iss` in the authorization response (RFC 9207); one issuer per bridge. |
| **Open redirect** | Invalid `client_id`/`redirect_uri` never redirect; exact match; loopback only for public clients. |
| **SSRF via arbitrary IDs, client URLs, JWKS and profile documents** | One fetcher for all of them with the resolver's rules: HTTPS, no redirects, private-address block after DNS resolution, size and time caps. |
| **Cookie tossing from hosted subdomains** | Issuer on a domain that issues no identities; `__Host-` cookies; bridge labels reserved. |
| **Replay of native approvals** | Existing single-use nonce keyed by audience; atomic unique index; 5-minute window. |
| **Signed response leakage** | Never accepted in URLs; `Referrer-Policy: no-referrer`; no third-party content on auth pages; strict CSP. |
| **Phishing clients** | Consent shows verified host (URL clients) or owner ID + redirect host (registered); report link; operator suspension; `open` registration is an explicit operator choice. |
| **QR relay ("scan this to log in")** | Residual, as in every QR login: short TTL, the signer shows the bridge origin and a number that must match the initiating screen (E22-T5), copy explaining not to scan codes sent by others. |
| **Key rotation during login** | The verifier already accepts `previous_keys` inside the rotation grace window. |
| **Compromised or malicious signer** | Out of scope for the bridge, as for native Sign-In; session-key approvals are marked in `amr`. |
| **Pairwise secret loss** | Every RP sees new users. Backed up with signing keys; documented as unrecoverable. |
| **Correlation by the bridge itself** | The bridge sees which IDs sign in to which clients. Minimized retention; the privacy policy states it; organizations that object run their own issuer. |

## Open questions

- **Native proof claim.** Whether an OIDC token may carry the verified native response (or its
  digest) for Poweur-aware RPs. That response's audience is the *bridge*, so it proves the user
  approved the bridge, not the RP — useful for audit, not as a substitute for trusting `iss`.
  Claim name `poweur_proof` is reserved; not in v1.
- **Owned-domain IDs without a web signer.** The chooser falls back to app/QR/default signer;
  whether the bridge should also offer the paste-code flow in the browser is decided in E22-T4.
- **Custom-scheme redirects for native apps** (RFC 8252 private-use schemes): deferred until a
  named native client needs them.
- **Logos for registered clients**: v1 shows an initial, not an uploaded image, to avoid the
  bridge hosting user content.
- **CIMD is an IETF draft.** Track it; the fields above are the stable core shared with
  IndieAuth, so drift should be small.
- **Minimal IndieAuth response**: whether a profile-only IndieAuth exchange needs any token
  (E22-T6).
