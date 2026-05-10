---
id: capabilities
sidebar_position: 1
title: Future Capabilities
---

# Future Capabilities

The Poweur ID Protocol is designed to grow. Because identity is expressed in DNS, new capabilities can be advertised by adding new DNS record types — without modifying the core protocol, without changing the relay, and without any central registry update.

## DNS Capability Advertisement Pattern

The pattern for advertising a new capability is to define a new `TXT` record at a well-known subdomain prefix of the identity:

```
_eurything-caps.<identity>.  300  IN  TXT  "poweur-caps=<cap1>,<cap2>,..."
```

Clients can inspect capability records to determine whether to attempt a capability-specific interaction with an identity before initiating it. This is analogous to DNS `SRV` records, but uses a more compact single-record format.

### Example

```
; Alice supports messaging and publishing
_eurything-caps.alice.poweur.net.  300  IN  TXT  "poweur-caps=messaging,publishing"

; A payment-accepting merchant
_eurything-caps.shop.poweur.net.  300  IN  TXT  "poweur-caps=messaging,payments"

; An automated agent
_eurything-caps.r2d2.poweur.net.   300  IN  TXT  "poweur-caps=messaging,bot"
```

Future capabilities can define their own dedicated `TXT` record formats for richer metadata (e.g. a payment record with a structured currency and address list). The capability record serves as a discovery hint; the detailed record format is capability-specific.

---

## Roadmap

### Messaging (MVP — Active)

Signed, verified, relay-routed messages between any two Poweur ID identities. Covered in full in this documentation.

**Planned improvements:**
- End-to-end encryption (the most pressing post-MVP priority)
- Delivery receipts
- WebSocket push delivery (currently polling)
- Persistent message storage (currently relay in-memory only)
- Group messaging

### Publishing

Signed content (posts, articles, announcements) published under an identity's subdomain. Content is signed with the identity's private key, so authenticity is verifiable by anyone.

**DNS capability record:**
```
_eurything-caps.alice.poweur.net.  300  IN  TXT  "poweur-caps=publishing"
```

**Publishing record (proposed):**
```
_eurything-pub.alice.poweur.net.   300  IN  TXT  "poweur-pub-url=https://alice.poweur.net/feed"
```

### Payments

Payment address advertisement under an Poweur ID identity. Instead of sharing a crypto address or bank account number, a payer can look up a recipient's DNS identity and discover all advertised payment methods.

**DNS capability record:**
```
_eurything-caps.alice.poweur.net.  300  IN  TXT  "poweur-caps=payments"
```

**Payment record (proposed format, not final):**
```
_eurything-pay.alice.poweur.net.  300  IN  TXT  "poweur-pay=btc:bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"
_eurything-pay.alice.poweur.net.  300  IN  TXT  "poweur-pay=lightning:alice@getalby.com"
_eurything-pay.alice.poweur.net.  300  IN  TXT  "poweur-pay=iban:GB29NWBK60161331926819"
```

Multiple payment records can be combined in a single `TXT` value or across multiple `TXT` records in the same RRset.

### Authentication

Allow Poweur ID identities to authenticate to third-party services without passwords. A service can issue a challenge, the user signs it with their Poweur ID private key, and the service verifies the signature against the public key in DNS.

This is essentially the same challenge-response flow used in the relay API (`GET /auth/challenge`), generalized to any service and wrapped in a mobile-friendly approval UX.

**DNS capability record:**
```
_eurything-caps.alice.poweur.net.  300  IN  TXT  "poweur-caps=auth"
```

**Recommended verifier discovery:**
```
https://service.example/.well-known/poweur.json
```

The verifier metadata should describe its domain, supported protocol versions, callback mechanism, and whether it supports QR-based or deep-link based handoff to the mobile signer.

**Recommended request object fields:**

- `request_id`
- `domain`
- `audience`
- `nonce`
- `issued_at`
- `expires_at`
- `action` (`signup`, `signin`, `link`, ...)
- `statement`
- `response_uri`

**Interoperability direction:**

- Keep the DNS name as the canonical identity.
- Support `did:web` as the pragmatic DID bridge today.
- Leave room for `did:dns` if that method stabilizes and gains broad support.
- Keep the signed response shape close to wallet and signed-message ecosystems so an OIDC / SIOP bridge can be layered on later.

### File Sharing

Allow an Poweur ID identity to act as a portable file-sharing address for systems similar to Google Drive, Dropbox, or Nextcloud.

There are two distinct levels of support:

1. **Native Poweur ID file-sharing capability.** A future Poweur ID-compatible storage provider could let users share files and folders directly to a DNS identity, with verification and discovery handled through Poweur ID records and signatures.
2. **Mapping to existing provider identities.** Existing platforms can often be integrated only indirectly by publishing a mapping from the Poweur ID DNS identity to the provider's native account identifier.

**Important interoperability note:**

- **Google Drive** sharing is tied to Google-native identities such as Google account email addresses, Google groups, Workspace domains, or public links. A DNS record can advertise which Google account corresponds to an Poweur ID identity, but Google Drive does not natively resolve DNS identities in its ACL model.
- **Dropbox** sharing similarly relies on Dropbox account email addresses or Dropbox account IDs. A DNS record can publish that mapping, but Dropbox does not support granting access directly to a DNS identity.
- **Nextcloud** is the closest fit because it already has a federated identity model (`<user>@<instance>`). An Poweur ID identity could advertise a mapping to a Nextcloud Federated Cloud ID, making lookup and client-side translation more natural. Even here, the native share target remains the Nextcloud federated ID, not the Poweur ID DNS name itself.

This means the practical first step is not "replace Google Drive or Dropbox identity models with DNS." The practical first step is "let a DNS identity advertise where it can receive file shares on existing systems."

**DNS capability record:**
```
_eurything-caps.alice.poweur.net.  300  IN  TXT  "poweur-caps=files"
```

**Provider mapping record (proposed):**
```
_eurything-files.alice.poweur.net.  300  IN  TXT  "poweur-files=nextcloud:alice@cloud.poweur.net"
_eurything-files.alice.poweur.net.  300  IN  TXT  "poweur-files=dropbox:alice@poweur.net"
_eurything-files.alice.poweur.net.  300  IN  TXT  "poweur-files=gdrive:alice@poweur.net"
```

The intent of this record is discovery and interoperability:

- a client can look up where an identity accepts file shares
- a bridge service can translate the DNS identity into a provider-specific target account
- a sender can choose the best supported platform before initiating the share

It does **not** imply that the third-party provider will verify Poweur ID signatures or treat the DNS identity itself as a first-class ACL subject.

**Native Poweur ID file-sharing direction:**

A future Poweur ID-native file-sharing capability could define:

- a signed file manifest format
- capability records that advertise storage endpoints or sync providers
- optional encrypted file transfer or envelope keys
- recipient-based sharing to DNS identities rather than platform-specific accounts

That would make the Poweur ID identity itself the true share target, with Google Drive / Dropbox / Nextcloud mappings treated as compatibility bridges rather than the primary model.

### Key Rotation and Revocation

A formal protocol for rotating or revoking the key pair associated with an identity without losing the subdomain:

1. Generate a new key pair.
2. Publish the new public key in DNS.
3. Optionally publish a signed rotation statement (`_eurything-rotate.<identity>` TXT record) signed by the old key, providing proof of continuity.
4. The old key is considered deprecated after a grace period.

Key revocation would involve publishing a `_eurything-revoke.<identity>` TXT record and notifying known contacts.

### Federation and Relay Peering

Relays may maintain persistent connections or trust relationships with known peer relays — a "federation" model similar to ActivityPub. This would reduce per-message DNS lookups and improve delivery reliability for high-volume identity pairs.

### Additional DNS Providers

The relay's DNS provider interface is designed for extension. Planned additional provider support:

- AWS Route 53
- Porkbun
- Namecheap
- Google Cloud DNS

Each requires only a new implementation of the two-method `DNSProvider` Go interface.

---

## Backwards Compatibility

New capability records are additive — they do not change the core protocol. Clients and relays that do not understand a capability record simply ignore it. This ensures the protocol can evolve without breaking existing deployments.

Core protocol changes (message format, signing scheme, API endpoints) will be versioned using the relay's `version` field in `GET /health` responses.

## Related

- [DNS Records](/protocol/dns-records)
- [Identity Model](/protocol/identity-model)
- [Interoperability](/protocol/interoperability)
- [Protocol Overview](/protocol/overview)
