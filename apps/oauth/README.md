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
| `OAUTH_SUBJECT_TYPE` | `pairwise` | `public` makes `sub` the Poweur ID itself. |
| `OAUTH_CLIENT_REGISTRATION` | `open` | Who may use `/developers`: `open`, `allowlist`, `closed`. |
| `OAUTH_REGISTRATION_ALLOWLIST` | — | CSV of IDs or `*.domain` suffixes for `allowlist`. |
| `OAUTH_MAX_CLIENTS_PER_OWNER` | `10` | Console limit per Poweur ID. |
| `OAUTH_STATIC_CLIENTS` | — | JSON file of operator clients; see [`clients.example.json`](clients.example.json). |
| `OAUTH_URL_CLIENTS` | `indieauth` | URL `client_id`s: `indieauth` (IndieAuth only), `on` (OIDC too), `off`. |
| `OAUTH_SESSION_TTL` | `12h` | How long a browser stays signed in to the bridge. |
| `OAUTH_CONTACT_URI`, `OAUTH_ABUSE_CONTACT` | `/abuse` | Where consent pages send abuse reports. |
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
```

Back up the database **and** the key-encryption key together. Restoring the
database without the key leaves the bridge unable to sign; losing the database
changes every user's pairwise `sub` at every application.

## Connecting applications

Discovery: `https://<issuer>/.well-known/openid-configuration`. Authorization
Code with PKCE `S256` only. Scopes: `openid` (always), `poweur_id` and
`profile` (released only if the user leaves them ticked).

- **oauth2-proxy**: `--provider=oidc --oidc-issuer-url=<issuer> --client-id=… --client-secret=… --code-challenge-method=S256 --scope="openid poweur_id"`
- **Keycloak** (identity brokering): *OpenID Connect v1.0* provider, discovery URL above, *Client authentication: Client secret sent as basic auth*, *PKCE: S256*, default scopes `openid poweur_id`.
- **Authentik** (OAuth source): *OpenID Connect* type, *OIDC Well-known URL* above, consumer key/secret from the console, scopes `openid poweur_id`.
- **Grafana**: `[auth.generic_oauth]` with `use_pkce = true`, `scopes = openid poweur_id`, `login_attribute_path = poweur_id`.

## Tests

```bash
go test ./apps/oauth/...
cd apps/integration && go test -run TestINT_OAUTH -count=1   # real relays, the CLI as signer, go-oidc as the RP
```
