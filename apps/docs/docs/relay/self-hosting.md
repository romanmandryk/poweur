---
id: self-hosting
sidebar_position: 0
title: Self-hosting a relay
---

# Self-hosting a relay

A relay hosts Poweur IDs under a domain you control (`alice.example.com`,
`bob.example.com`), keeps their inboxes and files, and serves the web app. It federates with
every other relay, so your users can message and share with anyone on poweur.net or any other
relay.

You do **not** need a relay just to use your own domain as your ID. A single ID on your domain
can live on any existing relay: see [Web identity](/protocol/web-identity).

## What you need

- A Linux server with Docker and Docker Compose, and ports 80 and 443 open. A small VM
  (2 vCPU, 2–4 GB RAM) is plenty for a family or a team. Disk is whatever your users' files
  need.
- A domain, for example `example.com`, where you can create DNS records.
- A way to get TLS certificates for **every name under the domain**, because each hosted ID
  is its own host name (`alice.example.com`). Pick one of the two options in
  [Step 3](#3-tls-for-every-hosted-name).

## How the pieces fit

```text
                    ┌───────────────── your server ─────────────────┐
alice.example.com ─▶│ Caddy (TLS) ──▶ relay :8080 ──▶ /data volume  │
example.com/app/  ─▶│                 (serves the web app at /app/) │
                    └───────────────────────────────────────────────┘
```

The relay speaks plain HTTP on port 8080 and never terminates TLS itself; a reverse proxy in
front of it does. Everything durable (identity documents, inbox spool, files, shares) lives
under one directory, `POWEUR_DATA`.

## 1. DNS

Create two records pointing at your server:

| Name | Type | Value | Why |
|------|------|-------|-----|
| `example.com` | A / AAAA | your server | The web app (`https://example.com/app/`) and sign-up |
| `*.example.com` | A / AAAA | your server | Every hosted ID, e.g. `alice.example.com` |
| `relay.example.com`, `id.example.com` | covered by the wildcard | | The relay's own name (written into identity documents) and the sign-up host |

The wildcard is what makes hosted IDs work without one DNS change per user.

## 2. Build the image

The image contains the relay and the web app. Build it from the repository root:

```bash
git clone https://github.com/romanmandryk/poweur.git && cd poweur
docker build -f apps/api/Dockerfile -t poweur-relay \
  --build-arg VERSION_HASH=$(git rev-parse HEAD) .
```

## 3. TLS for every hosted name

Browsers and other relays reach each ID over HTTPS at its own name, so you need a certificate
that covers `*.example.com` as well as `example.com`.

**Option A — Caddy with a DNS plugin (wildcard certificate).** Caddy proves control of the
domain through your DNS provider's API and gets a single wildcard certificate from Let's
Encrypt. You need a Caddy build that includes your provider's plugin, for example
[caddy-dns/cloudflare](https://github.com/caddy-dns/cloudflare), built with
[`xcaddy`](https://github.com/caddyserver/xcaddy):

```dockerfile
# caddy.Dockerfile
FROM caddy:2-builder AS builder
RUN xcaddy build --with github.com/caddy-dns/cloudflare
FROM caddy:2
COPY --from=builder /usr/bin/caddy /usr/bin/caddy
```

```caddyfile
# Caddyfile
example.com, *.example.com {
    tls {
        dns cloudflare {env.CF_API_TOKEN}
    }
    reverse_proxy relay:8080 {
        header_up X-Forwarded-For {client_ip}
    }
}
```

The API token needs only *Zone → DNS → Edit* on that one zone.

**Option B — Cloudflare's proxy in front.** Put `example.com` on Cloudflare and proxy both
records (orange cloud). Cloudflare's edge certificate covers `example.com` and
`*.example.com`, and forwards to your server. This is how poweur.net runs. Use SSL mode
*Full (strict)* with a Cloudflare origin certificate on Caddy, or keep the origin on HTTP only
if the server accepts traffic from Cloudflare's IP ranges alone. With the proxy on, tell Caddy
to trust Cloudflare's addresses so logs see real client IPs (`deploy/infra/caddy/Caddyfile` in
the repository is a working example).

## 4. Configure the relay

Create `relay.env`:

```bash
RELAY_ADDRESS=relay.example.com   # this relay's public name, written into identity documents
RELAY_SCHEME=https
HOSTED_DOMAINS=example.com        # IDs are created as <name>.example.com
POWEUR_DATA=/data
NAME_MIN_LEN=3                    # shortest handle you allow (poweur.net uses 6)

# A private relay: only people with a code can create an ID.
REGISTRATION_GATE=invite
REGISTRATION_INVITE_CODES=family-2026,team-alpha
```

Every setting is described in [Configuration](/relay/configuration). Leave
`REGISTRATION_GATE` at its default `open` for a public relay.

## 5. Run it

```yaml
# compose.yml
services:
  relay:
    image: poweur-relay
    restart: unless-stopped
    env_file: relay.env
    volumes:
      - poweur_data:/data
  caddy:
    image: your-caddy-with-dns-plugin   # or caddy:2 with option B
    restart: unless-stopped
    ports: ["80:80", "443:443"]
    environment:
      CF_API_TOKEN: ${CF_API_TOKEN}
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy_data:/data
volumes:
  poweur_data:
  caddy_data:
```

```bash
docker compose up -d
curl https://example.com/health     # {"status":"ok", …}
```

Open `https://example.com/app/` and create the first ID. Confirm it resolves from outside:

```bash
curl https://alice.example.com/.well-known/poweur/id.json
```

Then send a message from it to an ID on another relay (for example a poweur.net one) to check
that federation works in both directions.

## Back up and upgrade

- **Back up** the `poweur_data` volume (identity documents, inbox spool, files, shares) and
  your `relay.env`. Stop the relay for a consistent file snapshot, or snapshot the volume at
  the storage level. Caddy's volume only holds certificates, which it can re-issue.
- **Upgrade** by pulling the repository, rebuilding the image and running
  `docker compose up -d`. Data formats are versioned; read the release notes before jumping
  several versions.
- **Monitor** with `GET /health`, which reports version, storage writability and free space.
  For logs and metrics, see [Observability](/relay/observability).

## Optional extras

- **Sign in to other apps** with your users' IDs: run the [OAuth/OIDC bridge](/auth/oauth-oidc-bridge)
  (`apps/oauth`) and set `OAUTH_BRIDGE_URL` on the relay so hosted IDs advertise it.
- **Separate web app host.** `LAUNCHER_HOST` / `LAUNCHER_HOSTS` choose which host shows the
  "create an ID" flow (default `id.<first hosted domain>` and the bare domain).
- **Name policy.** Reserve or block names with `NAME_RESERVED` and `NAME_BLOCKED_FILE`.

## Related

- [Relay overview](/relay/overview) — what the relay does and what it stores
- [Configuration](/relay/configuration) — every environment variable
- [TLS](/security/tls) — certificates for hosted identities in more detail
- [Claim your ID](/web/claim-your-id) — the sign-up flow your users will see
