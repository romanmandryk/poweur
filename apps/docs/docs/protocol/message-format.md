---
id: message-format
sidebar_position: 4
title: Message Format
---

# Message Format

An Eurything message is a JSON object that carries the sender identity, recipient identity, payload, and a cryptographic signature. In the MVP the payload is also end-to-end encrypted between sender and recipient whenever both sides publish an encryption key; the relay never sees plaintext.

## Message Fields

| Field | Type | Description |
|-------|------|-------------|
| `sender` | string | Fully qualified identity subdomain of the sender (`alice.poweur.net`) |
| `recipient` | string | Fully qualified identity subdomain of the recipient (`bob.example.org`) |
| `timestamp` | string | ISO 8601 UTC timestamp of message creation (`2026-03-28T12:00:00Z`) |
| `payload` | string | Base64url ciphertext when `encryption` is set, UTF-8 plaintext otherwise |
| `signature` | string | Base64-encoded Ed25519 signature over the canonical fields |
| `session_id` | string | Optional. Identifies the short-lived session whose key signed this message |
| `session_proof` | object | Optional. Self-contained proof that the session key was authorized by the identity key. See [Session Proof](#session-proof) |
| `encryption` | object | Optional. Present when the payload is end-to-end encrypted. See [End-to-End Encryption](#end-to-end-encryption) |

`sender`, `recipient`, `timestamp`, `payload`, and `signature` are always required. The remaining fields are optional in the protocol but in practice every CLI- and mobile-produced message in the MVP carries `session_id` and, when the recipient has a published encryption key, `encryption`.

To keep the format extensible, future protocol revisions may reserve additional envelope fields such as `id`, `type`, `nonce`, `thread_id`, `expires_at`, and `metadata`. MVP relays and clients must ignore unknown top-level fields unless a newer protocol version marks them as mandatory.

## Wire Format Example

Encrypted, session-signed message (the common case):

```json
{
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
session:<session_id>                              # if session_id is present
enc:<alg>:<ephemeral_public_key>:<nonce>          # if encryption is present
```

Lines for absent optional fields are **omitted**, not included as empty strings. This keeps the canonical string backward compatible with the plaintext, session-less envelope (first four lines only).

For the encrypted example above the canonical string is:

```
alice.poweur.net
bob.example.org
2026-03-28T12:00:00Z
b29LaWxvNC4xN...base64url ciphertext...
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

In the MVP, messages are end-to-end encrypted whenever the recipient has a published X25519 encryption key (`_eurything-enc.<recipient>`). The relay only sees ciphertext plus routing metadata.

The encryption suite:

- **Key agreement:** X25519 ECDH between a sender-generated ephemeral keypair and the recipient's long-lived X25519 public key.
- **Key derivation:** HKDF-SHA256 with `salt = ephemeral_public_key || recipient_public_key` and `info = "eurything/msg/v1"`, producing a 32-byte key.
- **Cipher:** ChaCha20-Poly1305 with a random 12-byte nonce and additional authenticated data `"eurything/msg/v1\n" || ephemeral_public_key || recipient_public_key`.
- **Envelope:** ciphertext (base64url) in `payload`; `ephemeral_public_key` and `nonce` (base64url) in `encryption`; `alg` is the fixed string `x25519-chacha20-poly1305`.

Clients MAY fall back to plaintext when the recipient has no published encryption key. In that case `encryption` is omitted and `payload` is UTF-8 plaintext. CLI clients warn the user; mobile clients refuse to send.

Future versions may replace `x25519-chacha20-poly1305` with a stronger suite. The `alg` string is the version marker; clients must reject envelopes whose `alg` they do not implement.

## Inbox Message Format

When a client retrieves messages via `GET /messages/:identity`, each message includes an additional relay-assigned `id` field. All other envelope fields (including `encryption` and `session_id`) are preserved verbatim:

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
  ]
}
```

The `id` field is assigned by the receiving relay and is not part of the signed canonical string. Clients should verify the signature on received messages before displaying them, even if the relay has already verified it at ingress, and decrypt with their local X25519 private key.

## Related

- [Identity Model](/protocol/identity-model)
- [DNS Records](/protocol/dns-records)
- [Interoperability](/protocol/interoperability)
- [Routing](/protocol/routing)
- [API Reference](/relay/api-reference)
