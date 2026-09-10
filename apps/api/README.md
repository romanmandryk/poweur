# Poweur ID Relay (`apps/api`)

## Local development

Create `apps/api/.env`:

```bash
LISTEN_ADDR=:8080
RELAY_ADDRESS=localrelay.poweur.net
RELAY_SCHEME=https
DNS_PROVIDER=cloudflare
RATE_LIMIT_MINUTE=20
RATE_LIMIT_HOUR=200
RATE_LIMIT_DAY=1000
CHALLENGE_TTL=60s
DNS_TTL=300s
VERSION=0.1.1
```

Start the relay:

```bash
cd /Users/romanmandryk/git/poweur/apps/api
go run .
```

### Web client (static SPA)

Build the web UI once (outputs to `apps/api/web/dist`):

```bash
pnpm --filter @poweur/web build
```

Point the relay at that directory so it serves the app under **`/app/`**:

```bash
WEB_STATIC_DIR=/absolute/path/to/poweur/apps/api/web/dist go run .
```

The relay sends permissive **CORS** headers on all routes so browsers can talk to any Poweur ID relay (direct POST to recipient hosts, not only the home relay).

For local development without copying files, run Vite (`pnpm --filter @poweur/web dev`) — it proxies `/messages`, `/sessions`, etc. to `http://127.0.0.1:8080` by default.

## Test coverage

### Unit tests only (this module)

Tests live mostly under `internal/relay`. Measure merged coverage and generate a profile (no extra tools — uses `go test` and `go tool cover` from the Go toolchain):

```bash
cd apps/api
make cover         # runs tests once, writes coverage/cover.out, prints total %
make cover-html     # + coverage/index.html (line-by-line in your browser)
make cover-func     # per-function breakdown (long)
```

This **does not** include `apps/integration` tests. It only reflects code hit by `go test ./...` inside `apps/api`. The Makefile passes **`-coverpkg=github.com/poweur/api/...`**, so code exercised from **`pkg/relay` tests** (e.g. `GET /health`) is counted against **`internal/relay`**, not only tests defined under `internal/relay/`.

As of the last refresh, **`make cover`** (with `-coverpkg` for the whole module) reports on the order of **57%** merged statement coverage for `apps/api`. The **`internal/relay` package alone** (HTTP handlers) is about **~64%** when measured by tests in `internal/relay` (run `go test -cover ./internal/relay/...` for that number). Gaps: the **`main` package**; **Cloudflare/Hetzner** providers; and **`resolveIdentityPublicKey`’s** HTTP fallback to a peer’s `GET /identities/...` (needs a peer test server or integration). The HTML report highlights remaining branches in `server.go`.

### Integration tests (API + CLI together)

End-to-end tests under `apps/integration` run the relay **in-process** (`httptest`) and drive the real CLI packages, so they execute much more of `internal/relay`, `pkg/relay`, and `cli` than unit tests alone. To measure that, run coverage **from the integration module** with `-coverpkg` so dependencies are instrumented:

```bash
cd apps/integration
make cover         # instruments github.com/poweur/api/... and github.com/poweur/cli/...
make cover-html
```

As of the last run, merged **api + cli** statement coverage from integration tests was on the order of **45%** (varies as tests change). The HTML report is the right place to see which paths in `server.go` and `cli.go` are still cold.

## Cloudflare tunnel for HTTPS dev relay

To expose a local relay over HTTPS on `localrelay.poweur.net`, use a Cloudflare tunnel. This keeps the relay reachable at a public hostname while still routing to `localhost:8080`.

Prerequisites:

- `cloudflared` installed
- Cloudflare account authenticated (`cloudflared login`) or `TUNNEL_ORIGIN_CERT` set
- DNS zone `poweur.net` in your Cloudflare account

Run the helper script:

```bash
./scripts/cloudflare-tunnel-dev.sh
```

By default this creates a tunnel named `localrelay`, routes `localrelay.poweur.net` to it, and serves `http://localhost:8080`.

Environment overrides:

```bash
TUNNEL_NAME=localrelay TUNNEL_HOSTNAME=localrelay.poweur.net TARGET_URL=http://localhost:8080 ./scripts/cloudflare-tunnel-dev.sh
```

Once running, keep `RELAY_ADDRESS=localrelay.poweur.net` and `RELAY_SCHEME=https` in the relay `.env` so identity registrations write a valid DNS target.

## CLI sanity check

From `apps/cli`:

```bash
go run . identity create forecopes.poweur.net
go run . send forecopes.poweur.net "hello"
go run . inbox
```
