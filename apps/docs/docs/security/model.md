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
| Verify message authenticity using public keys from DNS | Read encrypted message payloads (E2E encrypted in the MVP) |
| Rate-limit and reject suspected spam | Impersonate a user (no private keys held) |
| Write DNS records ephemerally, with a client-supplied token | Retain DNS write credentials after a registration request |
| Drop or delay messages | Prove that it delivered a message (no delivery receipts in MVP) |
| Cache short-lived session public keys in memory | Derive the session private key or the long-lived identity private key |
| See routing metadata (sender, recipient, timestamp) | Rotate or revoke an identity's key pair |

## Where Secrets Live

| Secret / Data | Stored in | Can leave? |
|---------------|-----------|:----------:|
| Long-lived identity private key (Ed25519) | Device secure enclave (iOS / Android); key file on CLI | Never |
| Long-lived encryption private key (X25519) | Device secure storage; key file on CLI | Never |
| Session private key (Ed25519, ≤24h) | Device local storage (mobile app / CLI `~/.eurything/sessions/`) | Never; discarded on revoke or expiry |
| DNS provider API token | iOS Keychain / Android Keystore | Only during DNS writes |
| Identity public key | DNS `_eurything.<identity>` TXT record | Public — visible to all |
| Encryption public key | DNS `_eurything-enc.<identity>` TXT record | Public — visible to all |
| Session public key | Relay in-memory session cache | Lost on relay restart |
| Relay endpoint | App config | Not a secret |
| Rate limit counters | Relay in-memory | Lost on restart |
| DNS routing cache | Relay in-memory | Lost on restart |
| Inbox messages (ciphertext only) | Relay in-memory | Lost on restart |

## Threat Model

### Compromised relay

A relay that is fully compromised by an attacker:

- **Can** drop, delay, or selectively deliver messages.
- **Can** observe routing metadata: sender, recipient, timestamp, approximate message size.
- **Can** extract the cached session public keys and observed envelopes, but not the session private keys (never transmitted).
- **Cannot** read message payloads — they are encrypted end-to-end with ChaCha20-Poly1305 using a key derived from X25519 ECDH between an ephemeral sender key and the recipient's long-lived encryption key.
- **Cannot** forge a message from any identity — signatures require the sender's session or identity private key.
- **Cannot** impersonate a user — no private keys are held, and session registration requires a fresh identity-key signature.
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

- **Can** observe message metadata: sender, recipient, timestamp, relay addresses, approximate message size.
- **Cannot** read message payloads — they are encrypted end-to-end (X25519 + HKDF-SHA256 + ChaCha20-Poly1305). TLS provides an additional transport-layer confidentiality boundary, but the protocol does not rely on it for payload confidentiality.
- **Cannot** forge messages or impersonate identities.

Metadata protection (who-talks-to-whom, when) is **not** in scope for the MVP. That would require mixnet-style routing or onion layers and is a future concern.

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

## MVP Scope Notes

- **Metadata is visible to the relay.** Sender, recipient, timestamp, and message size are not hidden from the relay. Hiding routing metadata (who talks to whom) is out of scope for the MVP.
- **Forward secrecy is partial.** Each message uses a fresh ephemeral X25519 key, so compromising a recipient's long-lived encryption key does not reveal past messages once the ephemeral key is deleted. A dedicated double-ratchet session scheme (Signal-style) is a future improvement.
- **Plaintext fallback.** If a recipient does not publish an encryption public key, the sender may opt in to sending plaintext. CLI warns on this path; mobile refuses. Operators should treat missing encryption keys as a hard error in production deployments.
- **Session keys are relay-local.** A relay seeing only an unfamiliar `session_id` cannot verify a forwarded message unless the envelope also carries `session_proof` (which the sending relay attaches automatically when forwarding).

## Related

- [TLS Configuration](/security/tls)
- [DNS Records](/protocol/dns-records)
- [Identity Model](/protocol/identity-model)
- [Relay Overview](/relay/overview)
