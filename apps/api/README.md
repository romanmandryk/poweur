# Eurything Relay (`apps/api`)

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
VERSION=0.1.0
```

Start the relay:

```bash
cd /Users/romanmandryk/git/eurything/apps/api
go run .
```

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
