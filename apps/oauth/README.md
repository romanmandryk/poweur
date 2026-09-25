# poweur-oauth — the OAuth 2.0 / OpenID Connect bridge

A conventional OpenID Provider whose only way to authenticate a person is a
native **Sign in with Poweur ID** approval. It lets Keycloak, Authentik,
oauth2-proxy, Grafana and any OIDC or IndieAuth application sign in **any
publicly resolvable Poweur ID**, on any relay, with ordinary configuration.
It never holds identity keys and shares no secret with any relay.

Design and threat model: [`apps/docs/docs/auth/oauth-oidc-bridge.md`](../docs/docs/auth/oauth-oidc-bridge.md).
Roadmap: [EPIC-022](../../epics/EPIC-022-oauth-oidc-indieauth-bridge.md).

## Run it

```bash
go run ./cmd/poweur-oauth gen-key > oauth-kek.txt        # back this up
export OAUTH_ISSUER=http://oauth.localhost:8090           # https in production; the host needs a dot
export OAUTH_KEY_ENCRYPTION_KEY_FILE=$PWD/oauth-kek.txt
export OAUTH_DEFAULT_SIGNER=http://localhost:8080/app/    # a web signer to offer
export RESOLVER_ALLOW_PRIVATE=1 POWEUR_RESOLVER_SCHEME=http   # local relays only
go run ./cmd/poweur-oauth
```

Then open `http://oauth.localhost:8090/` (or run `scripts/dev.sh`, which also starts a relay). Register an application at `/developers`
(sign in with your Poweur ID) to get a client ID and secret.

Container: `docker build -f apps/oauth/Dockerfile -t poweur-oauth .` from the
repo root. [`compose.example.yml`](compose.example.yml) runs it beside a relay.

## Configuration

| Variable | Default | Meaning |
|----------|---------|---------|
| `OAUTH_ISSUER` | — (required) | The bridge's exact origin; the OIDC `iss` and the native sign-in audience. **Never under a domain that hosts identities.** |
| `OAUTH_KEY_ENCRYPTION_KEY` / `_FILE` | — (required) | 32 bytes, base64. Seals the issuer signing keys in the database. Lose it and the keys are gone. |
| `OAUTH_DATABASE` | `oauth.db` | SQLite file. Holds keys (sealed), the pairwise-subject secret, clients, consents, codes, tokens, audit. |
| `OAUTH_ADDR` | `:8090` | Listen address. |
| `OAUTH_NAME` | `Poweur OAuth bridge` | Shown to signers and on pages. |
| `OAUTH_DEFAULT_SIGNER` | — | Web signer offered when an identity advertises none. |
| `OAUTH_LAUNCHER_URL` | `https://poweur.net` | Where the sign-in page sends visitors without a Poweur ID to create one (a relay serving the web app). The page checks name availability there from the browser. `none` hides the offer. |
| `OAUTH_LAUNCHER_DOMAIN` | launcher host without `id.` | Hosted domain new names are created under (`alice.<domain>`). |
| `OAUTH_FARO_URL` | — | Same-origin path of a Grafana Faro collector, e.g. `/faro/collect`. When set, every page's bundled UI sends anonymous page views (by page name, never the path, which carries transaction codes), errors and web vitals, scrubbed of IDs by `@poweur/faro`. No third-party script; the CSP stays `connect-src 'self'`. |
| `OAUTH_SUBJECT_TYPE` | `pairwise` | `public` makes `sub` the Poweur ID itself. |
| `OAUTH_CLIENT_REGISTRATION` | `open` | Who may register applications at `/developers`: `open` (any signed-in Poweur ID), `allowlist`, or `closed` for a bridge that serves only the operator's own (static) clients. Consent pages name who registered an application and that nobody reviewed it; `clients suspend` stops one at once. |
| `OAUTH_REGISTRATION_ALLOWLIST` | — | CSV of IDs or `*.domain` suffixes for `allowlist`. |
| `OAUTH_MAX_CLIENTS_PER_OWNER` | `10` | Console limit per Poweur ID. |
| `OAUTH_STATIC_CLIENTS` | — | JSON file of operator clients; see [`clients.example.json`](clients.example.json). |
| `OAUTH_URL_CLIENTS` | `indieauth` | URL `client_id`s: `indieauth` (IndieAuth only), `on` (OIDC too), `off`. |
| `OAUTH_SESSION_TTL` | `12h` | How long a browser stays signed in to the bridge. |
| `OAUTH_CONTACT_URI`, `OAUTH_ABUSE_CONTACT` | `/abuse` | Where consent pages send abuse reports. |
| `OAUTH_SECURITY_CONTACT` | abuse contact | Where `/security` asks vulnerability reports to go. |
| `OAUTH_METRICS_ADDR` | off | A second listener for `GET /metrics` (Prometheus text). Bind it to a private address: `127.0.0.1:9464`. |
| `OAUTH_PUSH_CLI`, `OAUTH_PUSH_HOME`, `OAUTH_PUSH_IDENTITY` | — | Enable **Send to my Poweur app**: the `poweur` binary, a home holding only the bridge's identity (`HOME=… poweur identity create …`), and that identity. Users list it under Sign-in services. |
| `OAUTH_RATE_AUTHORIZE` / `_IDENTIFY` / `_CALLBACK` / `_TOKEN` / `_CONSOLE` | 60 / 20 / 60 / 120 / 30 | Requests per minute per client IP; `-1` disables. |
| `OAUTH_TRUST_PROXY` | off | `1` to rate-limit by the last `X-Forwarded-For` hop (only behind a proxy that sets it). |
| `RESOLVER_ALLOW_PRIVATE`, `POWEUR_RESOLVER_SCHEME` | off, `https` | Local development only. |

Relays advertise the bridge with `OAUTH_BRIDGE_URL` (IndieAuth discovery and
`capabilities.json`); they need nothing else.

## Operating it

```bash
poweur-oauth keys list                     # published keys and their state
poweur-oauth keys rotate                   # new signing key; old ones stay published 24h
poweur-oauth clients list
poweur-oauth clients suspend <client_id> impersonation
poweur-oauth clients unsuspend <client_id>
poweur-oauth hash-secret < secret.txt      # client_secret_sha256 for the static file
poweur-oauth prune                         # also runs hourly inside `serve`
poweur-oauth backup /backups/oauth-$(date +%F).db   # consistent copy, server running
```

### Health

`GET /health` answers `200 {"status":"ok"}` only when the database reads and a
signing key is loaded; otherwise `503` with `"error":"database"` or
`"signing key"`. Point your load balancer and uptime check at it.

### Metrics and alerts

With `OAUTH_METRICS_ADDR` set, `GET /metrics` serves request counts and
latency by route, rate-limit refusals, and counters for the audit events
(`token.code_reused`, `native.match_failed`, `push.failed`, …). Labels are
route patterns and event names only — no identities, client ids or addresses.
Sample rules: [`deploy/alerts.yml`](deploy/alerts.yml).

### Backup and restore

`poweur-oauth backup <new-file>` writes a consistent copy while the server
runs (`VACUUM INTO`; it refuses to overwrite). Back it up **with** the
key-encryption key, and keep them apart: the file holds the sealed signing
keys and the pairwise-subject secret.

Restore = copy the file into place and start with the *same*
`OAUTH_KEY_ENCRYPTION_KEY`. Then every user keeps the same `sub` at every
application and old ID tokens still verify — tested in
`TestBackupRestoreKeepsSubjectsAndKeys`. Restoring without the key leaves the
bridge unable to sign; losing the database changes every user's pairwise `sub`
everywhere.

## Connecting applications

Discovery: `https://<issuer>/.well-known/openid-configuration`. Authorization
Code with PKCE `S256` only. Scopes: `openid` (always), `poweur_id` and
`profile` (released only if the user leaves them ticked).

- **oauth2-proxy** (verified live, v7.12 — `apps/web/test/e2e/oauth-live-oauth2-proxy.spec.js`):
  `--provider=oidc --oidc-issuer-url=<issuer> --client-id=… --client-secret=… --code-challenge-method=S256
  --scope="openid poweur_id" --oidc-email-claim=poweur_id --email-domain=* --insecure-oidc-allow-unverified-email`.
  The bridge releases no e-mail, so the Poweur ID stands in for it; restrict who gets in with
  `--authenticated-emails-file` listing Poweur IDs.
- **Keycloak** (identity brokering; verified live, 26.3 — `apps/web/test/e2e/oauth-live-keycloak.spec.js`):
  *OpenID Connect v1.0* provider with the discovery URL above, *Client authentication: Client
  secret sent as basic auth*, *PKCE: S256*, default scopes `openid poweur_id`, and a *Username
  Template Importer* mapper with `${CLAIM.poweur_id}`. A Poweur ID carries no e-mail or name, so
  make those optional in the realm's user profile (or let the review-profile step ask).
- **Authentik** (OAuth source; verified live, 2026.8.2 — `apps/web/test/e2e/oauth-live-authentik.spec.js`):
  *Directory → Federation and Social login → Create → OpenID Connect OAuth Source*, OIDC well-known
  URL above, consumer key/secret from the console, *Additional scopes* `poweur_id`. Two settings
  people miss: add the source to the **identification stage** (`default-authentication-identification`)
  or no button appears on the login page, and keep an **enrollment flow** that asks for a username —
  a Poweur ID carries no `preferred_username` or e-mail, so the enrolling user types one (the spec
  uses the Poweur ID itself). Accounts link by the pairwise `sub`, not by the Poweur ID.
- **Grafana**: `[auth.generic_oauth]` with `use_pkce = true`, `scopes = openid poweur_id`, `login_attribute_path = poweur_id`.

## Pages

The pages are React + Tailwind in [`ui/`](ui), with the web app's design
tokens. Go keeps every route, redirect, cookie and check: a page response is
the built `index.html` with that page's data embedded as a JSON data block
(`bridge/web.go`; the TypeScript twin is `ui/src/lib/page.ts`), so the CSP
stays `script-src 'self'`. The build lands in `bridge/web/dist` and is
embedded at compile time:

```bash
pnpm --filter @poweur/oauth-ui build     # before go run / go build
pnpm --filter @poweur/oauth-ui watch     # while editing; restart the bridge to pick it up
```

Without it the bridge still answers every page with its data behind a plain
"pages not built" notice, which is all the Go tests need. The image builds it.

## Tests

```bash
go test ./apps/oauth/...
cd apps/integration && go test -run TestINT_OAUTH -count=1   # real relays, the CLI as signer, go-oidc as the RP
```
