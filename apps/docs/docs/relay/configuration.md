---
id: configuration
sidebar_position: 3
title: Configuration
---

# Relay Configuration

The relay is configured via environment variables. On startup, the relay also loads a local `.env` file (if present) so you can run `go run .` without exporting variables manually. All settings have defaults suitable for a single-node development deployment; for a production setup, start from [Self-hosting a relay](/relay/self-hosting).

Durations use Go syntax with a unit: `60s`, `5m`, `720h`. A bare number such as `60` is not a valid duration and is ignored, so the default applies. Counts and byte sizes are plain integers.

## Environment Variables

### Core

| Variable | Default | Description |
|----------|---------|-------------|
| `LISTEN_ADDR` | `:8080` | Address the relay binds to. The relay speaks HTTP only; terminate TLS in a reverse proxy (see [TLS](/security/tls)) |
| `RELAY_ADDRESS` | *(required)* | This relay's public host name (e.g. `relay.poweur.net`), without a port. Written into identity documents as their home relay |
| `RELAY_SCHEME` | `https` | Scheme for relay-to-relay calls (`http` only for local tests) |
| `POWEUR_DATA` | *(empty)* | Root of durable state: identity documents, inbox spool, and the filesystem drive store. Without it the relay cannot host identities. An S3 drive does not require it |
| `HOSTED_DOMAINS` | *(empty)* | Comma-separated parents for hosted registration (e.g. `poweur.net` → `alice.poweur.net`) |
| `WEB_STATIC_DIR` | *(empty; `/web` in the Docker image)* | Directory of the built web app (`apps/web/dist`), served at `/app/` |
| `LAUNCHER_HOST` | `id.<first hosted domain>` | The host that serves the "create an ID" flow |
| `LAUNCHER_HOSTS` | `id.<domain>` and `<domain>` for each hosted domain | Comma-separated set of such hosts; wins over `LAUNCHER_HOST` |
| `OAUTH_BRIDGE_URL` | *(empty)* | Public URL of an [OAuth/OIDC bridge](/auth/oauth-oidc-bridge). Hosted identities advertise it in `capabilities.json` and as IndieAuth metadata; the relay never calls it |
| `REGISTRATION_GATE` | `open` | `open`, or `invite` to require a code for hosted registration |
| `REGISTRATION_INVITE_CODES` | *(empty)* | Comma-separated invite codes when the gate is `invite` |
| `OPERATOR_TOKEN` | *(empty: off)* | Lets the operator register a hosted name the name policy holds back from everyone else (reserved like `support`, or shorter than `NAME_MIN_LEN`). The client sends it as `X-Poweur-Operator-Token` on `POST /identities` (`poweur identity create … --operator-token`); keys are still generated and held by the client, and the domain, signatures and name shape are still checked. A wrong token is refused with `401 invalid_operator_token`. Use a long random value and unset it when you're not creating names |
| `RESOLVER_ALLOW_PRIVATE` | `false` | Allow identity resolution to private IP addresses (local development and tests only) |
| `VERSION` | *(build's version)* | Relay semver in `GET /` and `GET /health`. Override for tests only |
| `BUILD_TIME` | *(VCS time)* | Build timestamp, advertised as `buildTime` |
| `VERSION_HASH` | *(VCS revision)* | Git revision, advertised as `versionHash` |

### Storage and delivery

| Variable | Default | Description |
|----------|---------|-------------|
| `STORAGE_PROVIDER` | `fs` | Drive object store: `fs` (under `POWEUR_DATA`) or `s3`. `relay-fs` was removed |
| `MAX_IDENTITY_BYTES` | `5368709120` (5 GiB) | Configured quota per identity; `0` = unlimited. Drive enforcement is being restored in EPIC-020 |
| `STORAGE_QUOTAS_FILE` | `$POWEUR_DATA/storage-quotas.json` | Per-identity quotas that override `MAX_IDENTITY_BYTES`: a JSON object from identity to bytes or a size string (`{"alice.example.com": "2GB"}`, `0` = unlimited). A relay with a store keeps them there instead (`relay/storage-quotas.json`, edited with `poweur-relay quotas set <id> <size>` / `unset <id>`, re-read within a minute); this file applies only while the store has none. Either way no restart is needed and an invalid edit keeps the last good version |
| `QUOTA_CONTACT` | — | Who to ask for more space, usually a Poweur ID. Over-quota `507` responses and `GET /files/{identity}/quota` name it (`contact`) |
| `MAX_FILE_BYTES` | `2147483648` (2 GiB) | Largest single uploaded file; `0` = unlimited |
| `S3_ENDPOINT` | *(required for `s3`)* | `host:port`, or an `http`/`https` URL. The URL scheme overrides `S3_SECURE` |
| `S3_BUCKET` | *(required for `s3`)* | Bucket that already exists. The relay does not create it |
| `S3_PREFIX` | *(empty)* | Key prefix inside the bucket. Object layout is `<prefix>/drives/…` |
| `S3_REGION` | `us-east-1` | Region passed to the S3 client. Cloudflare R2 accepts `auto` |
| `S3_ACCESS_KEY` | *(empty)* | Access key. Empty uses `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`. Set together with `S3_SECRET_KEY` |
| `S3_SECRET_KEY` | *(empty)* | Secret key |
| `S3_SESSION_TOKEN` | *(empty)* | Optional session token for temporary credentials |
| `S3_SECURE` | `true` | Use TLS when `S3_ENDPOINT` has no scheme |
| `S3_PRESIGN` | `true` | `true` returns presigned chunk URLs. `false` sends chunk bytes through the relay. Conditional writes are required either way |
| `MAX_INBOX_PER_IDENTITY` | `50` | Undelivered messages that may wait for one identity; further mail is refused until they collect it |
| `MAX_ACKS_PER_IDENTITY` | `50` | Pending delivery receipts per identity |
| `SPOOL_TTL` | `720h` (30 days) | How long undelivered mail waits for a recipient who never returns; `0` disables expiry |
| `MAX_STREAMS_PER_IDENTITY` | `8` | Concurrent push (SSE) streams per identity; `0` = unlimited |
| `STREAM_IDLE_TIMEOUT` | `1h` | Closes a push stream regardless of traffic; clients reconnect and catch up from their cursor |
| `CHALLENGE_TTL` | `60s` | How long an authentication challenge stays valid |
| `DNS_TTL` | `300s` | How long resolved identities and relay addresses are cached |

Message and upload body sizes are fixed limits in the relay, not settings.

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

`GET /health` includes a `storage` object when a durable store is configured (`writable`, and
`free_bytes` for the filesystem provider).

Everything durable lives in the object store selected above — `$POWEUR_DATA` for `fs`, the
bucket prefix for `s3`: each identity's drive under `drives/<id>/` (journal, snapshots,
versions, chunks and the `.poweur` system files), and the relay's own registries under
`relay/` (`identities/`, `spool/messages/`, `spool/acks/`, `keystore/`). Back up that one
location. An S3 relay needs no `POWEUR_DATA` at all. See [Storage v2](/files/storage-v2).

### DNS registration

| Variable | Default | Description |
|----------|---------|-------------|
| `DNS_PROXY_MODE` | `auto` | For DNS-mode registration on Cloudflare: proxy identity routing records `auto`, `always` or `never`. `auto` proxies only when the relay host is a `*.cfargotunnel.com` CNAME |

DNS-mode registration (an identity on a domain whose DNS the relay writes) uses a provider and
API token **supplied by the client** in `POST /identities`; the relay has no DNS credentials of
its own. See [Cloudflare](#cloudflare-dns-provider-setup) and [Hetzner](#hetzner-dns-provider-setup)
below. Hosted identities need no DNS writes at all.

### Rate Limiting

| Variable | Default | Description |
|----------|---------|-------------|
| `RATE_LIMIT_MINUTE` | `20` | Max messages per sender per minute |
| `RATE_LIMIT_HOUR` | `200` | Max messages per sender per hour |
| `RATE_LIMIT_DAY` | `1000` | Max messages per sender per day |

| `GLOBAL_RATE_LIMIT_MINUTE` | `1000` | Max messages through this relay per minute, all senders together |
| `GLOBAL_RATE_LIMIT_HOUR` | `100000` | …per hour |
| `GLOBAL_RATE_LIMIT_DAY` | `1000000` | …per day |

Set any value to `0` to disable that window's check. Set a per-sender value to `-1` to block all messages (maintenance mode).

#### Per-sender-relay request metering

Contact-request admissions are additionally metered against the relay accountable for
the sender — cheap identities cluster on relays, so a flood of one-request strangers is
invisible to a per-identity cap and obvious to a per-relay one. This relay's own users
are exempt (it meters and gates them directly), and conversation between accepted
contacts is never metered here. See
[Relay reputation & abuse pressure](../trust/relay-reputation).

| Variable | Default | Description |
|----------|---------|-------------|
| `REQUEST_RELAY_LIMIT_MINUTE` | `10` | Contact-request attempts per sending relay per minute |
| `REQUEST_RELAY_LIMIT_HOUR` | `60` | …per hour |
| `REQUEST_RELAY_LIMIT_DAY` | `300` | …per day |

`0` disables a window; all three at `0` turns the meter off. Rejections are `429` with
`"scope": "sender_relay"`.

### Observability

Logs, metrics, error tracking and the browser analytics tag are configured with the
`OTEL_*`, `TELEMETRY_*`, `SENTRY_DSN`, `FARO_COLLECT_URL` and `LOG_LEVEL` variables,
described in [Observability](/relay/observability). All are off when unset.

## Example `.env` File

A hosted relay for `example.com` behind a TLS reverse proxy:

```bash
LISTEN_ADDR=:8080
RELAY_ADDRESS=relay.example.com
RELAY_SCHEME=https
POWEUR_DATA=/data
HOSTED_DOMAINS=example.com
WEB_STATIC_DIR=/web            # already set in the Docker image

# Handles of at least 6 characters, like poweur.net
NAME_MIN_LEN=6

# Rate limits (defaults shown)
RATE_LIMIT_MINUTE=20
RATE_LIMIT_HOUR=200
RATE_LIMIT_DAY=1000
```

For local development, see `apps/api/README.md` in the repository.

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

## Optional telemetry

See [Observability](observability.md) for OTLP export configuration, trusted proxies and per-identity analytics preferences.
