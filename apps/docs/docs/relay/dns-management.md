---
id: dns-management
sidebar_position: 4
title: DNS Management
---

# DNS Management

The relay supports **two registration modes**. DNS API writes are **optional** — only the
self-hosted path uses a client-supplied DNS token. Hosted identities need no per-user DNS
records beyond the operator's wildcard.

See also [Web identity](/protocol/web-identity) and [API reference](/relay/api-reference).

## Registration modes

### Hosted (no DNS writes)

When `HOSTED_DOMAINS` includes the parent (e.g. `poweur.net`) and the client omits
`dns_provider` / `dns_token`, `POST /identities` runs the **hosted** flow:

1. Validate name policy, identity signature, and signed `identity_document`.
2. Enforce registration gate / rate limits (see [Configuration](/relay/configuration)).
3. Persist `id.json` under `$POWEUR_DATA/identities/<id>/poweur-sys/public/`.
4. Return `201 Created` — **no** Cloudflare/Hetzner API calls.

Routing uses a single operator-managed wildcard, for example:

```
*.poweur.net.   300  IN  A  <relay-ip>
```

Keys are published at `https://<identity>/.well-known/poweur/id.json` (Host-routed on the relay).

**Lease policy (v1):** hosted names do **not** expire automatically. A name remains allocated
until the owner migrates (`moved_to` / export) or an operator revokes it. Renewal is not required.

### Self-hosted (DNS writes)

When the request includes `dns_provider` and `dns_token`, the relay writes DNS records on behalf
of the client (existing path below). The identity may still store a signed document on the relay
when `POWEUR_DATA` is configured.

## DNS write path (self-hosted only)

When a client calls `POST /identities` with a DNS provider type (`cloudflare` or `hetzner`) and a
scoped API token:

1. Validates the request (public key format, identity subdomain syntax, supported provider).
2. Constructs the required DNS records.
3. Calls the DNS provider API using the client-supplied token to create (or update) those records.
4. If writes succeed, registers the identity as locally known and returns `201 Created`.
5. If any write fails, returns `502 Bad Gateway` with a description of the DNS API error.
6. **Discards the token** regardless of success or failure.

The token is used in-process for the duration of the registration request and never written to
disk, logged, or stored in memory after the request completes.

## Records written (self-hosted)

For a registration of `alice.example.org`:

```
; Public key TXT record
_poweur.alice.example.org.  300  IN  TXT  "poweur-pubkey=ed25519:<base64url-pubkey>"

; Optional encryption key
_poweur-enc.alice.example.org.  300  IN  TXT  "poweur-enckey=x25519:<base64url>"

; Relay routing (A or CNAME)
alice.example.org.             300  IN  A    <this-relay-ip>
```

The `A` record IP is the relay's own public IP address, as configured in `RELAY_ADDRESS`. If the
relay is behind a load balancer, use the load balancer IP. Operators may prefer `CNAME` routing.

## Supported DNS Providers

### Cloudflare

The Cloudflare provider uses the [Cloudflare DNS API v4](https://developers.cloudflare.com/api/operations/dns-records-for-a-zone-create-dns-record).

**API calls made:**

1. `GET /zones?name=<parent-domain>` — resolve the zone ID for the parent domain.
2. `POST /zones/<zone-id>/dns_records` — create the `TXT` record at `_poweur.<identity>`.
3. `POST /zones/<zone-id>/dns_records` — create the `A` record at `<identity>`.

If a record with the same name and type already exists, the relay issues a `PUT` (update) instead
of `POST` (create).

**Required token permissions:** `Zone:DNS:Edit` on the target zone.

### Hetzner DNS

The Hetzner DNS provider uses the [Hetzner DNS API](https://dns.hetzner.com/api-docs).

**API calls made:**

1. `GET /zones?name=<parent-domain>` — resolve the zone ID.
2. `POST /records` — create the `TXT` record.
3. `POST /records` — create the `A` record.

**Required token permissions:** Read + write access on the target zone.

## Provider Interface

```go
type DNSProvider interface {
    WriteRecord(ctx context.Context, zone, name, recordType, value string, ttl int) error
    DeleteRecord(ctx context.Context, zone, name, recordType string) error
}
```

## Security Considerations

**Token scoping.** Clients should create DNS API tokens scoped to the minimum required
permissions and the specific zone.

**Ephemeral handling.** The relay never logs, stores, or returns the DNS token.

**Hosted vs DNS.** Hosted registration never receives a DNS token. Key publication is via the
signed identity document and well-known endpoints, not TXT writes.

**Key rotation.** Formal rotation uses a new signed identity document and `previous_keys`
(see [Web identity](/protocol/web-identity)). Re-registration with DNS update remains available
for self-hosted operators.

## Related

- [Web identity](/protocol/web-identity)
- [DNS Records](/protocol/dns-records)
- [Configuration](/relay/configuration)
- [Security Model](/security/model)
