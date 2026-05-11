---
id: dns-records
sidebar_position: 3
title: DNS Records
---

# DNS Records

DNS is the authoritative data store for the Poweur ID Protocol. Every identity is anchored by DNS records that carry the public key and the relay routing address. This page defines all DNS record formats used or reserved by the protocol.

## Record Summary

| Record | Purpose | Required |
|--------|---------|----------|
| `TXT` at `_poweur.<identity>` | Long-lived Ed25519 identity public key | Yes |
| `TXT` at `_poweur-enc.<identity>` | Long-lived X25519 encryption public key | Yes |
| `A` or `CNAME` at `<identity>` | Relay routing — points to the relay server | Yes |
| `TXT` at `_poweur-caps.<identity>` | Capability advertisement (future) | No |

Short-lived **session keys** are **not** published in DNS. They live only in the client's local storage and the issuing relay's in-memory cache. Their authorization is carried in the message envelope as `session_proof` when needed (see [Message Format](/protocol/message-format#session-proof)).

---

## 1. Public Key Record (`TXT`)

The identity's Ed25519 public key is stored in a `TXT` record at the `_poweur` subdomain prefix:

```
_poweur.alice.poweur.net.  300  IN  TXT  "poweur-pubkey=ed25519:<base64-encoded-public-key>"
```

### Format

```
poweur-pubkey=<algorithm>:<base64url-public-key>
```

- **algorithm** — `ed25519` in the MVP. Additional algorithms (e.g. `p256`) may be added in future versions.
- **base64url-public-key** — the raw 32-byte Ed25519 public key, base64url-encoded (RFC 4648 §5, no padding).

### Example

```
; Alice's Ed25519 public key
_poweur.alice.poweur.net.  300  IN  TXT  "poweur-pubkey=ed25519:MCowBQYDK2VwAyEAn3a7Lr2Y5mFk8vX1Pm9D4tN6oQ2WqRhZ3cBsKjUdYeI"
```

### TTL recommendation

A TTL of 300 seconds (5 minutes) is recommended. Short TTLs allow key rotation to propagate quickly; very short TTLs (< 60s) can cause excessive DNS lookups at the relay.

### Relay lookup behaviour

When verifying a message signature, the relay resolves `_poweur.<sender-subdomain>` as a `TXT` query. If the record contains a `poweur-pubkey` value, the relay extracts the public key and verifies the signature. As a fallback (e.g. during initial propagation), the relay may call `GET /identities/:identity` on the peer relay resolved from the sender's routing `A`/`CNAME` record.

---

## 2. Encryption Public Key Record (`TXT`)

The identity's long-lived X25519 encryption public key is stored in a `TXT` record at the `_poweur-enc` subdomain prefix. Senders use it to derive a shared secret via X25519 ECDH and end-to-end encrypt the message payload (see [End-to-End Encryption](/protocol/message-format#end-to-end-encryption)).

```
_poweur-enc.alice.poweur.net.  300  IN  TXT  "poweur-enckey=x25519:<base64url-public-key>"
```

### Format

```
poweur-enckey=<algorithm>:<base64url-public-key>
```

- **algorithm** — `x25519` in the MVP.
- **base64url-public-key** — the raw 32-byte X25519 public key, base64url-encoded (RFC 4648 §5, no padding).

### Presence

The encryption record is written at the same time as the identity public key when an identity is created. Existing identities that predate E2E support can add one later via `poweur identity add-encryption-key`. The record is effectively mandatory for inbound messaging: senders that cannot find a recipient's encryption record abort with a clear error (there is no plaintext fallback), and relays reject any `POST /messages` whose envelope lacks encryption metadata with `400 encryption_required`.

---

## 3. Relay Routing Record (`A` / `CNAME`)

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

This same lookup is performed by **two different parties**:

- **Sending clients** resolve the recipient identity to its relay and POST `/messages` directly there (the default send path; see [Routing → Default: Direct Send](/protocol/routing#default-direct-send)).
- **Recipient clients** resolve the *original sender's* identity to that sender's home relay and POST `/acks` there to deliver tick-2 acks.
- **Relays in privacy-proxy mode** (when a client opts into `--via-home-relay`) resolve the recipient's relay and forward the signed envelope over HTTPS.

The relay caches resolved addresses for the duration of the DNS record's TTL to avoid redundant lookups on every forwarded message.

---

## 4. Full Example — New Identity

The following DNS records would be written by the relay upon successful registration of the identity `alice.poweur.net`:

```
; ── Identity: alice.poweur.net ──────────────────────────────────────

; Identity public key (Ed25519, base64url-encoded, no padding)
_poweur.alice.poweur.net.       300  IN  TXT  "poweur-pubkey=ed25519:MCowBQYDK2VwAyEAn3a7Lr2Y5mFk8vX1Pm9D4tN6oQ2WqRhZ3cBsKjUdYeI"

; Encryption public key (X25519, base64url-encoded, no padding)
_poweur-enc.alice.poweur.net.   300  IN  TXT  "poweur-enckey=x25519:2qsV0x9Ru9v3o_VzH7mHsH-yjwI5sOqO6sRpCVoqXxA"

; Relay routing — option A: direct IP
alice.poweur.net.                  300  IN  A     95.217.142.10

; Relay routing — option B: CNAME (alternative to A record above)
; alice.poweur.net.                300  IN  CNAME relay.poweur.net.
```

---

## 5. Capability Advertisement Record (`TXT`) — Future

A reserved `TXT` record format allows identities to advertise supported capabilities via DNS, enabling clients to discover what services an identity supports without contacting any relay.

```
_poweur-caps.alice.poweur.net.  300  IN  TXT  "poweur-caps=messaging,publishing"
```

### Format

```
poweur-caps=<capability>[,<capability>...]
```

Capabilities are comma-separated short identifiers. Defined capability identifiers (future):

| Identifier | Meaning |
|------------|---------|
| `messaging` | Identity accepts Poweur ID Protocol messages |
| `publishing` | Identity publishes signed content |
| `payments` | Identity has published payment addresses |

Clients can inspect capability records to decide whether to initiate contact with an identity (e.g. confirm messaging is supported before sending).

### Example

```
; Alice supports messaging and publishing
_poweur-caps.alice.poweur.net.  300  IN  TXT  "poweur-caps=messaging,publishing"

; A payment-accepting identity
_poweur-caps.merchant.poweur.net.  300  IN  TXT  "poweur-caps=messaging,payments"
```

See [Future Capabilities](/future/capabilities) for the full capability advertisement roadmap.

---

## DNS Zone Security

Because DNS records are the ground truth for public keys and routing, DNS zone security is important:

- **Restrict API token scopes.** The DNS API token supplied during identity registration should be scoped to the minimum required zone and permissions (create/update `TXT` and `A`/`CNAME` records in the target zone only). Never supply a token with delete rights or cross-zone write access.
- **Enable DNSSEC where possible.** DNSSEC prevents cache poisoning attacks that could redirect routing or substitute a public key. Cloudflare and Hetzner both support DNSSEC for managed zones.
- **Rotate tokens regularly.** The relay never stores DNS write credentials at rest; the risk window for a compromised relay is limited to the duration of an active registration request.

## HTTP Discovery and DID Interop

Some interoperability features are better expressed over HTTPS than in DNS records. Identities and relays may additionally expose:

- `https://<identity>/.well-known/poweur.json` — Poweur ID metadata, supported protocol versions, auth capabilities, and app-link information.
- `https://<identity>/.well-known/did.json` — a `did:web`-style DID document for DID-aware tooling.
- `https://<identity>/.well-known/did-configuration.json` — optional domain-to-DID binding metadata.

Poweur ID keeps DNS as the ground truth for public keys and routing. These HTTP documents are compatibility layers for ecosystems that already expect well-known endpoints and DID documents.

`did:dns` is the closest conceptual DID mapping to Poweur ID, but because that method is not yet broadly established, `did:web` is the more practical interop bridge for early implementations.

## Related

- [Identity Model](/protocol/identity-model)
- [Interoperability](/protocol/interoperability)
- [DNS Management](/relay/dns-management)
- [Future Capabilities](/future/capabilities)
