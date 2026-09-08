---
id: configuration
sidebar_position: 3
title: Configuration
---

# Relay Configuration

The relay is configured via environment variables. On startup, the relay also loads a local `.env` file (if present) so you can run `go run .` without exporting variables manually. All settings have sensible defaults suitable for a single-node development deployment; production deployments should review and override as appropriate.

## Environment Variables

### Core

| Variable | Default | Description |
|----------|---------|-------------|
| `LISTEN_ADDR` | `:8080` | Address the relay binds to |
| `RELAY_ADDRESS` | *(required)* | Public host or IP of this relay (e.g. `relay.poweur.net`). Do not include a port. |
| `RELAY_SCHEME` | `https` | Scheme used for relay-to-relay calls (`http` or `https`) |
| `VERSION` | `0.1.0` | Relay version exposed in `GET /health` |
| `POWEUR_DATA` | *(empty)* | Durable identity store root (`identities/…/id.json`) |
| `HOSTED_DOMAINS` | *(empty)* | Comma-separated parents for hosted registration (e.g. `poweur.net`) |
| `REGISTRATION_GATE` | `open` | `open` or `invite` for hosted registrations |
| `REGISTRATION_INVITE_CODES` | *(empty)* | Comma-separated invite codes when gate=`invite` |
| `RESOLVER_ALLOW_PRIVATE` | `false` | Allow well-known resolve to private IPs (dev/test) |
| `MAX_IDENTITY_BYTES` | `5368709120` (5 GiB) | Per-identity storage quota; `0` = unlimited. Enforced on WebDAV `PUT`/`MKCOL` with `507` |
| `MAX_FILE_BYTES` | `2147483648` (2 GiB) | Max single uploaded file; `0` = unlimited |
| `STORAGE_PROVIDER` | `relay-fs` | File-body backend. v1 supports `relay-fs` only (`POWEUR_DATA` required for DAV) |

### Hosted handle policy

Which names an operator hands out is a deployment decision; the character set is not.
Handles are always ASCII `a-z 0-9 -` — a handle becomes a DNS label and a name under the
wildcard certificate, and non-ASCII letters would also let `аdmin` (Cyrillic `а`)
impersonate `admin`.

| Variable | Default | Description |
|----------|---------|-------------|
| `NAME_MIN_LEN` | `3` | Minimum handle length. The default is the dev/test value; a public deployment should raise it (the shipped compose file sets `6`) |
| `NAME_MAX_LEN` | `24` | Maximum handle length (hard ceiling is the 63-byte DNS label limit) |
| `NAME_ALLOW_HYPHEN` | `true` | Hyphens inside the handle; never leading, trailing or doubled |
| `NAME_ALLOW_DIGITS` | `true` | Digits anywhere in the handle |
| `NAME_RESERVED` | *(empty)* | Extra reserved names, comma-separated. **Adds to** the built-in list, which cannot be shortened by configuration |
| `NAME_BLOCKED_FILE` | *(empty)* | Path to a blocked-terms list, one term per line, `#` comments. A missing file means no blocking and never blocks startup |
| `NAME_BLOCK_MODE` | `substring` | `substring` matches anywhere in the handle, `exact` only the whole handle |

Clients discover all of this from
[`GET /hosted/availability`](/relay/api-reference#get-hostedavailability), which echoes the
policy alongside its verdict — so the app validates as the user types without hardcoding
the rules of the relay it happens to be talking to.

`GET /health` includes a `storage` object when `POWEUR_DATA` is set (`writable`, `free_bytes`).
File trees live under `$POWEUR_DATA/identities/<id>/` and are served at `/dav/<identity>/`
(see [WebDAV access](/files/webdav)).

### DNS Provider

| Variable | Default | Description |
|----------|---------|-------------|
| `DNS_PROVIDER` | *(required)* | Default DNS provider: `cloudflare` or `hetzner` |
| `DNS_PROXY_MODE` | `auto` | Proxy identity routing records in Cloudflare: `auto`, `always`, or `never` |

In the MVP, the DNS provider and its API token are supplied **by the client** during identity registration (`POST /identities`). The `DNS_PROVIDER` setting may be used for operator-side defaults or validation, but clients can supply any supported provider. `DNS_PROXY_MODE=auto` proxies identity routing records only if the relay hostname resolves to a `*.cfargotunnel.com` CNAME.

### Rate Limiting

| Variable | Default | Description |
|----------|---------|-------------|
| `RATE_LIMIT_MINUTE` | `20` | Max messages per sender per minute |
| `RATE_LIMIT_HOUR` | `200` | Max messages per sender per hour |
| `RATE_LIMIT_DAY` | `1000` | Max messages per sender per day |

Set any value to `0` to disable that window's check. Set to `-1` to block all messages (maintenance mode).

### TLS

| Variable | Default | Description |
|----------|---------|-------------|
| `TLS_CERT_FILE` | *(empty)* | Path to TLS certificate PEM file |
| `TLS_KEY_FILE` | *(empty)* | Path to TLS private key PEM file |
| `TLS_ENABLE` | `false` | Enable TLS termination at the relay |

For production deployments, TLS termination is typically handled by a load balancer (see [TLS Configuration](/security/tls)) rather than at the relay process. Set `TLS_ENABLE=false` when TLS is terminated upstream.

### Message Limits

| Variable | Default | Description |
|----------|---------|-------------|
| `MAX_MESSAGE_SIZE` | `524288` | Maximum request body size in bytes (default: 512 KB) |

### Challenge Expiry

| Variable | Default | Description |
|----------|---------|-------------|
| `CHALLENGE_TTL` | `60` | Seconds before an auth challenge expires |

### DNS Cache

| Variable | Default | Description |
|----------|---------|-------------|
| `DNS_CACHE_TTL` | *(follows DNS TTL)* | Override for DNS routing cache TTL in seconds. If unset, uses the DNS record's TTL. |
| `DNS_RESOLVER` | *(system default)* | Custom DNS resolver address (e.g. `1.1.1.1:53`) |

## Example `.env` File

```bash
# Core
LISTEN_ADDR=:8080
RELAY_ADDRESS=relay.poweur.net
RELAY_SCHEME=https
VERSION=0.1.0

# DNS
DNS_PROVIDER=cloudflare

# Rate limits (defaults shown)
RATE_LIMIT_MINUTE=20
RATE_LIMIT_HOUR=200
RATE_LIMIT_DAY=1000

# TLS (terminated at load balancer)
TLS_ENABLE=false
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
Description=Poweur ID Relay
After=network.target

[Service]
Type=simple
User=poweur
EnvironmentFile=/etc/poweur/relay.env
ExecStart=/usr/local/bin/poweur-relay
Restart=on-failure
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
```

Where `/etc/poweur/relay.env` contains the environment variables listed above.

## Related

- [Relay Overview](/relay/overview)
- [DNS Management](/relay/dns-management)
- [TLS Configuration](/security/tls)
