---
id: dns-records
sidebar_position: 3
title: DNS Records
---

# DNS Records

DNS is the authoritative data store for the Eurything Protocol. Every identity is anchored by DNS records that carry the public key and the relay routing address. This page defines all DNS record formats used or reserved by the protocol.

## Record Summary

| Record | Purpose | Required |
|--------|---------|----------|
| `TXT` at `_eurything.<identity>` | Public key for the identity | Yes |
| `A` or `CNAME` at `<identity>` | Relay routing — points to the relay server | Yes |
| `TXT` at `_eurything-caps.<identity>` | Capability advertisement (future) | No |

---

## 1. Public Key Record (`TXT`)

The identity's Ed25519 public key is stored in a `TXT` record at the `_eurything` subdomain prefix:

```
_eurything.alice.poweur.net.  300  IN  TXT  "eurything-pubkey=ed25519:<base64-encoded-public-key>"
```

### Format

```
eurything-pubkey=<algorithm>:<base64url-public-key>
```

- **algorithm** — `ed25519` in the MVP. Additional algorithms (e.g. `p256`) may be added in future versions.
- **base64url-public-key** — the raw 32-byte Ed25519 public key, base64url-encoded (RFC 4648 §5, no padding).

### Example

```
; Alice's Ed25519 public key
_eurything.alice.poweur.net.  300  IN  TXT  "eurything-pubkey=ed25519:MCowBQYDK2VwAyEAn3a7Lr2Y5mFk8vX1Pm9D4tN6oQ2WqRhZ3cBsKjUdYeI"
```

### TTL recommendation

A TTL of 300 seconds (5 minutes) is recommended. Short TTLs allow key rotation to propagate quickly; very short TTLs (< 60s) can cause excessive DNS lookups at the relay.

### Relay lookup behaviour

When verifying a message signature, the relay resolves `_eurything.<sender-subdomain>` as a `TXT` query. If the record contains a `eurything-pubkey` value, the relay extracts the public key and verifies the signature. As a fallback (e.g. during initial propagation), the relay may call `GET /identities/:identity` on the peer relay resolved from the sender's routing `A`/`CNAME` record.

---

## 2. Relay Routing Record (`A` / `CNAME`)

The identity subdomain itself must have either an `A` record pointing directly to the relay's IPv4 address, or a `CNAME` record pointing to the relay's hostname.

### Option A — Direct `A` record

Use when the relay is identified by a static IP address:

```
alice.poweur.net.  300  IN  A  95.217.142.10
```

### Option B — `CNAME` to relay hostname

Use when multiple identities share a relay and the relay address may change:

```
alice.poweur.net.  300  IN  CNAME  relay.poweur.net.
relay.poweur.net.  300  IN  A      95.217.142.10
```

The `CNAME` approach is recommended for operators hosting many identities — a single relay IP change only requires updating the `relay.poweur.net` `A` record.

### Resolution behaviour

When a relay needs to forward a message to `alice.poweur.net`, it resolves the `A` record for that subdomain (following `CNAME` chains) to obtain the destination relay's IP address, then sends the message to `https://<relay-ip>/messages`.

The relay caches resolved addresses for the duration of the DNS record's TTL to avoid redundant lookups on every forwarded message.

---

## 3. Full Example — New Identity

The following DNS records would be written by the relay upon successful registration of the identity `alice.poweur.net`:

```
; ── Identity: alice.poweur.net ──────────────────────────────────────

; Public key (Ed25519, base64url-encoded, no padding)
_eurything.alice.poweur.net.  300  IN  TXT  "eurything-pubkey=ed25519:MCowBQYDK2VwAyEAn3a7Lr2Y5mFk8vX1Pm9D4tN6oQ2WqRhZ3cBsKjUdYeI"

; Relay routing — option A: direct IP
alice.poweur.net.             300  IN  A     95.217.142.10

; Relay routing — option B: CNAME (alternative to A record above)
; alice.poweur.net.           300  IN  CNAME relay.poweur.net.
```

---

## 4. Capability Advertisement Record (`TXT`) — Future

A reserved `TXT` record format allows identities to advertise supported capabilities via DNS, enabling clients to discover what services an identity supports without contacting any relay.

```
_eurything-caps.alice.poweur.net.  300  IN  TXT  "eurything-caps=messaging,publishing"
```

### Format

```
eurything-caps=<capability>[,<capability>...]
```

Capabilities are comma-separated short identifiers. Defined capability identifiers (future):

| Identifier | Meaning |
|------------|---------|
| `messaging` | Identity accepts Eurything Protocol messages |
| `publishing` | Identity publishes signed content |
| `payments` | Identity has published payment addresses |

Clients can inspect capability records to decide whether to initiate contact with an identity (e.g. confirm messaging is supported before sending).

### Example

```
; Alice supports messaging and publishing
_eurything-caps.alice.poweur.net.  300  IN  TXT  "eurything-caps=messaging,publishing"

; A payment-accepting identity
_eurything-caps.merchant.example.com.  300  IN  TXT  "eurything-caps=messaging,payments"
```

See [Future Capabilities](/future/capabilities) for the full capability advertisement roadmap.

---

## DNS Zone Security

Because DNS records are the ground truth for public keys and routing, DNS zone security is important:

- **Restrict API token scopes.** The DNS API token supplied during identity registration should be scoped to the minimum required zone and permissions (create/update `TXT` and `A`/`CNAME` records in the target zone only). Never supply a token with delete rights or cross-zone write access.
- **Enable DNSSEC where possible.** DNSSEC prevents cache poisoning attacks that could redirect routing or substitute a public key. Cloudflare and Hetzner both support DNSSEC for managed zones.
- **Rotate tokens regularly.** The relay never stores DNS write credentials at rest; the risk window for a compromised relay is limited to the duration of an active registration request.

## Related

- [Identity Model](/protocol/identity-model)
- [DNS Management](/relay/dns-management)
- [Future Capabilities](/future/capabilities)
