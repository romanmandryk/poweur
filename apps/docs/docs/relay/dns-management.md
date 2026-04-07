---
id: dns-management
sidebar_position: 4
title: DNS Management
---

# DNS Management

The relay writes DNS records on behalf of registering clients. This page describes how that write path works, which DNS providers are supported, and what records are created.

## Write Path

When a client calls `POST /identities`, the request body includes a DNS provider type (`cloudflare` or `hetzner`) and a scoped API token for the relevant DNS zone. The relay:

1. Validates the request (public key format, identity subdomain syntax, supported provider).
2. Constructs the two required DNS records.
3. Calls the DNS provider API using the client-supplied token to create (or update) those records.
4. If both writes succeed, registers the identity as locally hosted and returns `201 Created`.
5. If any write fails, returns `502 Bad Gateway` with a description of the DNS API error.
6. **Discards the token** regardless of success or failure.

The token is used in-process for the duration of the registration request and never written to disk, logged, or stored in memory after the request completes.

## Records Written

For a registration of `alice.poweur.net`:

```
; Public key TXT record
_eurything.alice.poweur.net.  300  IN  TXT  "eurything-pubkey=ed25519:<base64url-pubkey>"

; Relay routing record (A record pointing to this relay)
alice.poweur.net.             300  IN  A    <this-relay-ip>
```

The `A` record IP is the relay's own public IP address, as configured in `EURYTHING_RELAY_URL`. If the relay is behind a load balancer, the IP is the load balancer's IP.

Alternatively, if the relay operator prefers `CNAME` routing, this can be configured at the operator level; the default write for new identities uses an `A` record.

## Supported DNS Providers

### Cloudflare

The Cloudflare provider uses the [Cloudflare DNS API v4](https://developers.cloudflare.com/api/operations/dns-records-for-a-zone-create-dns-record).

**API calls made:**

1. `GET /zones?name=<parent-domain>` — resolve the zone ID for the parent domain (e.g. `poweur.net`).
2. `POST /zones/<zone-id>/dns_records` — create the `TXT` record at `_eurything.<identity>`.
3. `POST /zones/<zone-id>/dns_records` — create the `A` record at `<identity>`.

If a record with the same name and type already exists, the relay issues a `PUT` (update) instead of `POST` (create). This allows re-registration of an identity to update its public key and relay address.

**Required token permissions:**

- `Zone:DNS:Edit` on the target zone.

### Hetzner DNS

The Hetzner DNS provider uses the [Hetzner DNS API](https://dns.hetzner.com/api-docs).

**API calls made:**

1. `GET /zones?name=<parent-domain>` — resolve the zone ID.
2. `POST /records` — create the `TXT` record.
3. `POST /records` — create the `A` record.

If a record already exists (identified by name + type), the relay issues a `PUT /records/<record-id>` to update it.

**Required token permissions:**

- Read + write access on the target zone. Hetzner DNS API tokens are scoped to all zones in the account; use a dedicated Hetzner DNS account if tighter scoping is needed.

## Provider Interface

The relay's DNS management logic is abstracted behind a Go interface, making it straightforward to add new providers:

```go
type DNSProvider interface {
    WriteRecord(ctx context.Context, zone, name, recordType, value string, ttl int) error
    DeleteRecord(ctx context.Context, zone, name, recordType string) error
}
```

Any struct that implements this interface can be wired in as a DNS provider. Adding support for a new provider (e.g. Route 53, Porkbun, Namecheap) requires only a new implementation of this interface — no changes to relay core logic.

## Security Considerations

**Token scoping.** Clients should create DNS API tokens scoped to the minimum required permissions and the specific zone. A token with write access to all zones or delete rights creates unnecessary risk.

**Ephemeral handling.** The relay never logs, stores, or returns the DNS token. It exists only as an in-memory variable during the request lifecycle. Inspect relay logs and ensure no debug logging inadvertently captures request bodies.

**Re-registration updates.** Because the relay issues an update (not a create) if a record already exists, a client can call `POST /identities` again with a new token to rotate their public key or change relay. This is the mechanism for key rotation in the MVP (a more formal key rotation protocol is a post-MVP concern).

## Related

- [DNS Records](/protocol/dns-records)
- [Configuration](/relay/configuration)
- [Security Model](/security/model)
