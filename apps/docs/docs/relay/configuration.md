---
id: configuration
sidebar_position: 3
title: Configuration
---

# Relay Configuration

The relay is configured via environment variables. All settings have sensible defaults suitable for a single-node development deployment; production deployments should review and override as appropriate.

## Environment Variables

### Core

| Variable | Default | Description |
|----------|---------|-------------|
| `EURYTHING_HOST` | `0.0.0.0` | IP address the relay binds to |
| `EURYTHING_PORT` | `8080` | TCP port the relay listens on |
| `EURYTHING_RELAY_URL` | *(required)* | Public HTTPS URL of this relay (e.g. `https://relay.poweur.net`) |
| `EURYTHING_LOG_LEVEL` | `info` | Log verbosity: `debug`, `info`, `warn`, `error` |
| `EURYTHING_ENV` | `production` | Environment tag: `development`, `staging`, `production` |

### DNS Provider

| Variable | Default | Description |
|----------|---------|-------------|
| `EURYTHING_DNS_PROVIDER` | *(required)* | Default DNS provider: `cloudflare` or `hetzner` |

In the MVP, the DNS provider and its API token are supplied **by the client** during identity registration (`POST /identities`). The `EURYTHING_DNS_PROVIDER` setting may be used for operator-side defaults or validation, but clients can supply any supported provider.

### Rate Limiting

| Variable | Default | Description |
|----------|---------|-------------|
| `EURYTHING_RATE_LIMIT_MINUTE` | `20` | Max messages per sender per minute |
| `EURYTHING_RATE_LIMIT_HOUR` | `200` | Max messages per sender per hour |
| `EURYTHING_RATE_LIMIT_DAY` | `1000` | Max messages per sender per day |

Set any value to `0` to disable that window's check. Set to `-1` to block all messages (maintenance mode).

### TLS

| Variable | Default | Description |
|----------|---------|-------------|
| `EURYTHING_TLS_CERT_FILE` | *(empty)* | Path to TLS certificate PEM file |
| `EURYTHING_TLS_KEY_FILE` | *(empty)* | Path to TLS private key PEM file |
| `EURYTHING_TLS_ENABLE` | `false` | Enable TLS termination at the relay |

For production deployments, TLS termination is typically handled by a load balancer (see [TLS Configuration](/security/tls)) rather than at the relay process. Set `EURYTHING_TLS_ENABLE=false` when TLS is terminated upstream.

### Message Limits

| Variable | Default | Description |
|----------|---------|-------------|
| `EURYTHING_MAX_MESSAGE_SIZE` | `524288` | Maximum request body size in bytes (default: 512 KB) |

### Challenge Expiry

| Variable | Default | Description |
|----------|---------|-------------|
| `EURYTHING_CHALLENGE_TTL` | `60` | Seconds before an auth challenge expires |

### DNS Cache

| Variable | Default | Description |
|----------|---------|-------------|
| `EURYTHING_DNS_CACHE_TTL` | *(follows DNS TTL)* | Override for DNS routing cache TTL in seconds. If unset, uses the DNS record's TTL. |
| `EURYTHING_DNS_RESOLVER` | *(system default)* | Custom DNS resolver address (e.g. `1.1.1.1:53`) |

## Example `.env` File

```bash
# Core
EURYTHING_PORT=8080
EURYTHING_RELAY_URL=https://relay.poweur.net
EURYTHING_LOG_LEVEL=info
EURYTHING_ENV=production

# DNS
EURYTHING_DNS_PROVIDER=cloudflare

# Rate limits (defaults shown)
EURYTHING_RATE_LIMIT_MINUTE=20
EURYTHING_RATE_LIMIT_HOUR=200
EURYTHING_RATE_LIMIT_DAY=1000

# TLS (terminated at load balancer)
EURYTHING_TLS_ENABLE=false
```

## Cloudflare DNS Provider Setup

When clients use `dns_provider: "cloudflare"` in `POST /identities`, they supply a Cloudflare API token. The relay uses this token to call the Cloudflare DNS API to write records.

The token must have the following permissions on the target zone:

- **Zone:DNS:Edit** — to create and update `TXT`, `A`, and `CNAME` records.

Clients should create a scoped token from the [Cloudflare API Tokens](https://dash.cloudflare.com/profile/api-tokens) page, selecting "Edit zone DNS" and restricting to the specific zone (e.g. `poweur.net`).

```
Token scope:
  Zone → DNS → Edit
  Zone → Specific Zone: poweur.net
```

The relay does **not** require a Cloudflare account itself — it uses the client-supplied token ephemerally for the duration of the registration request.

## Hetzner DNS Provider Setup

When clients use `dns_provider: "hetzner"` in `POST /identities`, they supply a Hetzner DNS API token. The relay uses the [Hetzner DNS API](https://dns.hetzner.com/api-docs) to write records.

The token must have access to create and update records in the target zone. Create a token from the [Hetzner DNS Console](https://dns.hetzner.com/) under `Access` → `API Tokens`.

The relay writes the same record types as with Cloudflare: `TXT` for the public key and `A`/`CNAME` for routing.

## Systemd Service Example

For a production deployment on Linux, a minimal systemd unit file:

```ini
[Unit]
Description=Eurything Relay
After=network.target

[Service]
Type=simple
User=eurything
EnvironmentFile=/etc/eurything/relay.env
ExecStart=/usr/local/bin/eurything-relay
Restart=on-failure
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
```

Where `/etc/eurything/relay.env` contains the environment variables listed above.

## Related

- [Relay Overview](/relay/overview)
- [DNS Management](/relay/dns-management)
- [TLS Configuration](/security/tls)
