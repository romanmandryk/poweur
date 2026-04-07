---
id: model
sidebar_position: 1
title: Security Model
---

# Security Model

The fundamental principle of the Eurything security model is: **trust is rooted in the device's secure enclave; the relay is an untrusted forwarder**.

Every design decision in the protocol follows from this principle. The relay can be observed, replaced, or fully compromised without exposing user private keys, allowing message forgery, or enabling identity impersonation.

## Trust Hierarchy

```
┌─────────────────────────────────────────────────────────┐
│   TRUSTED                                               │
│   Device secure enclave (iOS Secure Enclave /           │
│   Android StrongBox / TEE)                              │
│   ↳ Holds private keys                                  │
│   ↳ Accessed only via WebAuthn/FIDO2                    │
└─────────────────────────────────┬───────────────────────┘
                                  │
┌─────────────────────────────────▼───────────────────────┐
│   SEMI-TRUSTED (integrity via cryptographic proof)      │
│   DNS records                                           │
│   ↳ Public keys (TXT records)                           │
│   ↳ Routing (A/CNAME records)                           │
│   ↳ Controlled by domain owner; visible to all          │
└─────────────────────────────────┬───────────────────────┘
                                  │
┌─────────────────────────────────▼───────────────────────┐
│   UNTRUSTED (verify, don't trust)                       │
│   Relay servers                                         │
│   Network transport                                     │
│   ↳ Can forward or drop messages                        │
│   ↳ Cannot forge signatures or hold private keys        │
└─────────────────────────────────────────────────────────┘
```

## What the Relay Can and Cannot Do

| The relay can... | The relay cannot... |
|-----------------|---------------------|
| Forward messages between identities | Forge a message from any identity |
| Verify message authenticity using public keys from DNS | Read encrypted message content *(post-MVP; plaintext in MVP)* |
| Rate-limit and reject suspected spam | Impersonate a user (no private keys held) |
| Write DNS records ephemerally, with a client-supplied token | Retain DNS write credentials after a registration request |
| Drop or delay messages | Prove that it delivered a message (no delivery receipts in MVP) |
| Read plaintext message payloads *(MVP limitation)* | Rotate or revoke an identity's key pair |

## Where Secrets Live

| Secret / Data | Stored in | Can leave? |
|---------------|-----------|:----------:|
| Private key | Device secure enclave (iOS / Android) | Never |
| DNS provider API token | iOS Keychain / Android Keystore | Only during DNS writes |
| Public key | DNS TXT record | Public — visible to all |
| Relay endpoint | App config | Not a secret |
| Rate limit counters | Relay in-memory | Lost on restart |
| DNS routing cache | Relay in-memory | Lost on restart |
| Inbox messages | Relay in-memory | Lost on restart |

## Threat Model

### Compromised relay

A relay that is fully compromised by an attacker:

- **Can** read plaintext message payloads (MVP limitation — encryption is post-MVP).
- **Can** drop, delay, or selectively deliver messages.
- **Cannot** forge a message from any identity — signatures require the sender's private key.
- **Cannot** impersonate a user — no private keys are held.
- **Cannot** exfiltrate DNS write credentials — tokens are discarded after use.
- **Cannot** register fraudulent identities without a valid, scoped DNS provider token from a legitimate user.

### DNS zone compromise

An attacker who gains write access to the DNS zone for an identity subdomain:

- **Can** substitute the identity's public key (TXT record) — allowing them to forge messages that appear to be from that identity for as long as the malicious record persists.
- **Can** redirect the identity's routing (A/CNAME record) — allowing them to receive messages intended for that identity.
- **Cannot** access the original private key (stored in the device secure enclave).

This makes DNS zone security a first-class concern. See [mitigations](#dns-zone-security-mitigations) below.

### Network observer (passive)

An observer who can read network traffic:

- **Can** read plaintext message payloads in the MVP (TLS protects transport, but relay operators can read plaintext).
- **Can** observe message metadata: sender, recipient, timestamp, relay addresses.
- **Cannot** forge messages or impersonate identities.

End-to-end encryption is a planned post-MVP feature that would prevent relay operators and network observers from reading message content.

### Replay attacks

A valid message cannot be replayed with a different timestamp because the timestamp is included in the signed canonical string. An attacker who captures a signed message cannot resubmit it with a modified timestamp — the signature would fail. An exact replay (same sender, recipient, timestamp, payload) would result in a duplicate delivery, which clients should handle by deduplicating on message ID or timestamp.

### Man-in-the-middle

TLS protects the transport between clients and relays, and between relays. Certificate validation is required. A MITM attacker who can intercept and modify TLS traffic would need to compromise the relay's TLS certificate — but even then, they could not forge signatures without the sender's private key.

## DNS Zone Security Mitigations

Because DNS zone integrity is critical:

1. **Scope API tokens.** DNS provider tokens supplied during identity registration should be scoped to the minimum required zone and permissions (create/update TXT and A/CNAME records only, no delete, no cross-zone).
2. **Enable DNSSEC.** DNSSEC signs DNS records cryptographically, preventing cache poisoning attacks. Both Cloudflare and Hetzner support DNSSEC. Operators should enable it for zones hosting Eurything identities.
3. **Monitor DNS records.** Operators and users should monitor their DNS records for unexpected changes.
4. **Rotate tokens regularly.** The relay never stores tokens, so the risk from a compromised relay is bounded to the duration of an active registration request.

## MVP Disclosure

The MVP has one important security limitation that users should understand:

**Message payloads are plaintext.** Until end-to-end encryption is implemented, relay operators and network observers (including ISPs, VPN providers, and anyone with access to relay infrastructure) can read message content. Signatures guarantee authenticity and integrity — they do not provide confidentiality. Users should be made aware of this limitation before sending sensitive content.

## Related

- [TLS Configuration](/security/tls)
- [DNS Records](/protocol/dns-records)
- [Identity Model](/protocol/identity-model)
- [Relay Overview](/relay/overview)
