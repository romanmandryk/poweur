---
id: tls
sidebar_position: 2
title: TLS Configuration
---

# TLS Configuration

All client-to-relay and relay-to-relay traffic uses HTTPS, and identity documents are fetched
from `https://<identity>/.well-known/poweur/id.json`. The relay process itself speaks plain
HTTP (`LISTEN_ADDR`, default `:8080`) and **never terminates TLS**: a reverse proxy in front of
it does. This page covers which certificates you need and the two ways we recommend getting
them. For the full setup, see [Self-hosting a relay](/relay/self-hosting).

## Which certificate you need

**A relay that hosts identities under a domain** (`alice.example.com`, `bob.example.com`, …)
needs a certificate that covers every identity host. Use a **wildcard certificate** for
`*.example.com`, plus the bare `example.com`:

- **One certificate covers every identity.** A new sign-up needs no certificate change.
- **No per-identity issuance**, so no rate-limit or latency cost when people sign up.
- **One-level wildcards are enough.** `*.example.com` covers `alice.example.com` but not
  `deep.alice.example.com`, and hosted identities only ever use the first level.

**A single identity on your own domain** (`alice.com`, served by any relay or from your own web
server) needs only an ordinary certificate for that name, like any website.

## Getting a wildcard certificate

Let's Encrypt issues wildcards only through the **DNS-01** challenge: the issuer proves control
of the domain by creating a temporary `_acme-challenge.example.com` TXT record. HTTP-01 cannot
prove control over `*.example.com`.

### Option A — Caddy with a DNS provider plugin

[Caddy](https://caddyserver.com) obtains and renews the wildcard certificate itself, using your
DNS provider's API for the challenge. It needs a build with the provider's plugin (for example
[caddy-dns/cloudflare](https://github.com/caddy-dns/cloudflare) or
[caddy-dns/hetzner](https://github.com/caddy-dns/hetzner)):

```caddyfile
example.com, *.example.com {
    tls {
        dns cloudflare {env.CF_API_TOKEN}
    }
    reverse_proxy relay:8080 {
        header_up X-Forwarded-For {client_ip}
    }
}
```

Scope the API token to editing DNS in that one zone. Renewal is automatic; Caddy keeps its
certificates in its data volume. [Self-hosting a relay](/relay/self-hosting#3-tls-for-every-hosted-name)
shows how to build the Caddy image.

### Option B — Cloudflare's proxy

With the domain on Cloudflare and the apex and wildcard records proxied, Cloudflare's edge
certificate covers `example.com` and `*.example.com` automatically and forwards traffic to your
server. This is how poweur.net runs.

- Prefer SSL mode **Full (strict)**, with a [Cloudflare origin certificate](https://developers.cloudflare.com/ssl/origin-configuration/origin-ca/)
  (a free wildcard, valid for 15 years) configured in Caddy, so traffic is encrypted all the
  way to your server.
- Configure the proxy to trust Cloudflare's IP ranges for `X-Forwarded-For`, so logs show real
  client addresses. `deploy/infra/caddy/Caddyfile` in the repository is a working example.
- A non-proxied (grey cloud) name must get its own certificate from Caddy as usual.

## What relays check

Relays and clients verify certificates normally when they fetch identity documents or deliver
to another relay. `RELAY_SCHEME=http` and `RESOLVER_ALLOW_PRIVATE=1` exist for local
development and tests only; never set them on a public relay.

## Related

- [Self-hosting a relay](/relay/self-hosting)
- [Security model](/security/model)
- [Web identity](/protocol/web-identity)
