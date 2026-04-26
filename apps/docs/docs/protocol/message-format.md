---
id: message-format
sidebar_position: 4
title: Message Format
---

# Message Format

An Eurything message is a JSON object that carries the sender identity, recipient identity, payload, and a cryptographic signature. The payload is always end-to-end encrypted between sender and recipient: the relay never sees plaintext, and any message that reaches the relay without an `encryption` envelope is rejected with `400 encryption_required` before routing.

## Message Fields

| Field | Type | Description |
|-------|------|-------------|
| `id` | string | **Required.** Client-assigned unique message id (ULID-shaped). Bound into the signed canonical string and used as the inbox storage key on the recipient relay so acks can refer back to a verifiable identifier |
| `sender` | string | Fully qualified identity subdomain of the sender (`alice.poweur.net`) |
| `recipient` | string | Fully qualified identity subdomain of the recipient (`bob.example.org`) |
| `timestamp` | string | ISO 8601 UTC timestamp of message creation (`2026-03-28T12:00:00Z`) |
| `payload` | string | Base64url of the AEAD ciphertext (always — plaintext is rejected) |
| `signature` | string | Base64-encoded Ed25519 signature over the canonical fields |
| `session_id` | string | Optional. Identifies the short-lived session whose key signed this message |
| `session_proof` | object | Optional. Self-contained proof that the session key was authorized by the identity key. See [Session Proof](#session-proof) |
| `encryption` | object | **Required.** End-to-end encryption envelope (`alg`, `ephemeral_public_key`, `nonce`). See [End-to-End Encryption](#end-to-end-encryption) |

`id`, `sender`, `recipient`, `timestamp`, `payload`, `signature`, and `encryption` are always required. `session_id` is optional at the protocol level but present on every routine CLI- and mobile-produced message; identity-signed sends omit it.

To keep the format extensible, future protocol revisions may reserve additional envelope fields such as `type`, `nonce`, `thread_id`, `expires_at`, and `metadata`. MVP relays and clients must ignore unknown top-level fields unless a newer protocol version marks them as mandatory.

## Wire Format Example

Encrypted, session-signed message (the common case):

```json
{
  "id":        "msg_01j9xkay7g000000000000000",
  "sender":    "alice.poweur.net",
  "recipient": "bob.example.org",
  "timestamp": "2026-03-28T12:00:00Z",
  "payload":   "b29LaWxvNC4xN...base64url ciphertext...",
  "signature": "MEUCIQDzK3Lm9...base64 signature...",
  "session_id": "sess_01j9xk...",
  "session_proof": {
    "session_public_key":  "sHxPq...base64url...",
    "issued_at":           "2026-03-28T08:00:00Z",
    "expires_at":          "2026-03-29T08:00:00Z",
    "nonce":               "mE3k...",
    "identity_signature":  "cVz...base64..."
  },
  "encryption": {
    "alg":                  "x25519-chacha20-poly1305",
    "ephemeral_public_key": "kY0u...base64url...",
    "nonce":                "iNv1...base64url..."
  }
}
```

The wire format is **UTF-8 encoded JSON**. All messages and API responses use `application/json` content type.

## Maximum Payload Size

The maximum message payload size is **512 KB** (524,288 bytes), measured on the ciphertext. This limit is enforced by the relay at ingress, before any parsing or signature verification, to keep the rejection path cheap. Requests exceeding this limit receive `413 Content Too Large`.

## Canonical Signing String

Before sending, the client constructs a canonical string by concatenating the following lines in order, each separated by a single newline (`\n`):

```
<sender>
<recipient>
<timestamp>
<payload>
id:<message_id>
session:<session_id>                              # if session_id is present
enc:<alg>:<ephemeral_public_key>:<nonce>          # if encryption is present
```

The `id` line is always present (the client-assigned message id is mandatory). Lines for absent optional fields are **omitted**, not included as empty strings. In normal operation the `enc:` line is always present (encryption is mandatory) and the `session:` line is present whenever the client uses the session-signed path. Identity-signed sends drop only the `session:` line.

For the encrypted example above the canonical string is:

```
alice.poweur.net
bob.example.org
2026-03-28T12:00:00Z
b29LaWxvNC4xN...base64url ciphertext...
id:msg_01j9xkay7g000000000000000
session:sess_01j9xk...
enc:x25519-chacha20-poly1305:kY0u...base64url...:iNv1...base64url...
```

Note: there is no trailing newline.

## Signing

1. Construct the canonical string as above.
2. Sign the canonical string with:
   - the **session private key** when `session_id` is set (the normal MVP path), or
   - the **long-lived identity private key** when no session is in use (headless agents that opt out of sessions).
3. Base64-encode the 64-byte signature (standard base64, RFC 4648 §4).
4. Place the encoded signature in the `signature` field.

The timestamp is included in the signed payload to prevent trivial replay. Binding `session_id`, `encryption`, and the ciphertext into the canonical string prevents an attacker from re-using a signature for a different session, cipher state, or ciphertext.

### Choosing a signing key

The sender picks the signing key per message. Relays MUST accept either path: the presence of `session_id` in the envelope is sufficient to disambiguate which public key the relay verifies against, and no explicit discriminator field is required.

| Path | When | Envelope shape | Verifying key |
|------|------|----------------|---------------|
| Session-signed (default) | Routine interactive use; mobile | `session_id` present, optional `session_proof` | Session Ed25519 public key (from cache or `session_proof`) |
| Identity-signed | Headless agents, rarely-sent messages, offline-prepared envelopes | `session_id` omitted, `session_proof` omitted | Long-lived identity Ed25519 public key (from DNS `_eurything.<sender>` or peer relay) |

The CLI exposes this choice via `eurything send --sign-with=session|identity` (default `session`). Identity-signed sends skip session registration entirely — no `POST /sessions` round-trip, no passkey prompt on mobile, and nothing is written to the local session cache. The trade-off is that every identity-signed message is cryptographically bound to the long-lived key, which forgoes the forward-secrecy benefit of rotating short-lived session keys.

## Verification

When a relay receives a message, it verifies as follows:

1. Extract `sender` and (optionally) `session_id` from the message.
2. Resolve the verifying public key:
   - If `session_id` is present, look it up in the session cache. If not found **and** `session_proof` is present, verify the proof against the sender's long-lived identity key (DNS: `_eurything.<sender>`) and cache the resulting session. If the session is missing and no proof is supplied, reject with `401 session_expired` so the client can re-register.
   - If `session_id` is absent, fetch the sender's long-lived public key directly from DNS (with a peer-relay fallback via `GET /identities/:identity`).
3. Reconstruct the canonical string exactly as the sender did, including the `session:` and `enc:` lines when the corresponding fields are present.
4. Verify the signature with the resolved public key.
5. If verification fails, reject with `401 Unauthorized`.

Relays forwarding messages on behalf of another relay do **not** re-sign. The sending relay MUST attach `session_proof` to a session-signed envelope before forwarding it to a peer relay, so the peer can verify without sharing session state. Clients MAY also attach the proof directly so the first hop can verify without any cross-relay API call.

## Session Proof

`session_proof` is the set of inputs that were used to register the session, plus the identity signature produced over them. Any relay can verify it by fetching the sender's long-lived key from DNS and checking the signature over the canonical session-registration string:

```
session-registration
<identity>
<session_public_key>
<issued_at>
<expires_at>
<nonce>
```

The proof is produced once at session registration and re-used on every outbound message until the session expires. See [Session & Passkey Flow](/protocol/identity-model#sessions) for details.

## End-to-End Encryption

Messages are **always** end-to-end encrypted. The recipient must have a published X25519 encryption key at `_eurything-enc.<recipient>`; if they do not, senders refuse to send and relays reject the envelope. The relay only ever sees ciphertext plus routing metadata.

The encryption suite:

- **Key agreement:** X25519 ECDH between a sender-generated ephemeral keypair and the recipient's long-lived X25519 public key.
- **Key derivation:** HKDF-SHA256 with `salt = ephemeral_public_key || recipient_public_key` and `info = "eurything/msg/v1"`, producing a 32-byte key.
- **Cipher:** ChaCha20-Poly1305 with a random 12-byte nonce and additional authenticated data `"eurything/msg/v1\n" || ephemeral_public_key || recipient_public_key`.
- **Envelope:** ciphertext (base64url) in `payload`; `ephemeral_public_key` and `nonce` (base64url) in `encryption`; `alg` is the fixed string `x25519-chacha20-poly1305`.

There is no plaintext fallback. When the recipient's `_eurything-enc.<identity>` TXT record is missing, clients abort with an error that points the user at `eurything identity add-encryption-key <recipient>` (or the mobile equivalent). Relays additionally enforce this on the server side: `POST /messages` without `encryption.alg`, `encryption.ephemeral_public_key`, and `encryption.nonce` is rejected with `400 encryption_required` before signature verification or rate-limiting runs.

Future versions may replace `x25519-chacha20-poly1305` with a stronger suite. The `alg` string is the version marker; clients must reject envelopes whose `alg` they do not implement.

## Inbox Message Format

When a client retrieves messages via `GET /messages/:identity`, each message preserves the original client-assigned `id` (relays do not rewrite it). All envelope fields (including `encryption` and `session_id`) are preserved verbatim, and the response also carries an `acks` array drained alongside `messages` (see [Delivery Acks](/protocol/delivery-acks)):

```json
{
  "messages": [
    {
      "id":        "msg_01j9xk7q2f000000000000000",
      "sender":    "bob.example.org",
      "recipient": "alice.poweur.net",
      "timestamp": "2026-03-28T12:00:00Z",
      "payload":   "b29LaWxvNC4xN...base64url ciphertext...",
      "signature": "MEUCIQDzK3Lm9...base64 signature...",
      "session_id": "sess_01j9xk...",
      "encryption": {
        "alg":                  "x25519-chacha20-poly1305",
        "ephemeral_public_key": "kY0u...base64url...",
        "nonce":                "iNv1...base64url..."
      }
    }
  ],
  "acks": []
}
```

The `id` field is the client-assigned id from the original send and IS part of the signed canonical string, so receiving clients can rely on it as a stable, signature-bound message identifier. Clients should still verify the signature on received messages before displaying them, even if the relay has already verified it at ingress, and decrypt with their local X25519 private key.

## Related

- [Identity Model](/protocol/identity-model)
- [DNS Records](/protocol/dns-records)
- [Interoperability](/protocol/interoperability)
- [Routing](/protocol/routing)
- [API Reference](/relay/api-reference)
