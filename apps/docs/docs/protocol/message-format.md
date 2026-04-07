---
id: message-format
sidebar_position: 4
title: Message Format
---

# Message Format

An Eurything message is a JSON object that carries the sender identity, recipient identity, payload, and a cryptographic signature. The format is designed to be minimal, self-contained, and verifiable by any party with access to DNS.

## Message Fields

| Field | Type | Description |
|-------|------|-------------|
| `sender` | string | Fully qualified identity subdomain of the sender (`alice.poweur.net`) |
| `recipient` | string | Fully qualified identity subdomain of the recipient (`bob.example.org`) |
| `timestamp` | string | ISO 8601 UTC timestamp of message creation (`2026-03-28T12:00:00Z`) |
| `payload` | string | Message content — plaintext for MVP |
| `signature` | string | Base64-encoded Ed25519 signature over the canonical fields |

All fields are required. No field may be null or omitted.

## Wire Format Example

```json
{
  "sender":    "alice.poweur.net",
  "recipient": "bob.example.org",
  "timestamp": "2026-03-28T12:00:00Z",
  "payload":   "Hey Bob, are you around?",
  "signature": "MEUCIQDzK3Lm9...base64encodedSignature...ABiReF4="
}
```

The wire format is **UTF-8 encoded JSON**. All messages and API responses use `application/json` content type. JSON key order is not significant for parsing, but the canonical signing string (see below) uses a fixed field order.

## Maximum Payload Size

The maximum message payload size is **512 KB** (524,288 bytes). This limit is enforced by the relay at ingress, before any parsing or signature verification, to keep the rejection path cheap. Requests exceeding this limit receive `413 Content Too Large`.

## Canonical Signing String

Before sending, the client constructs a canonical string for signing by concatenating the following four fields **in order**, each separated by a single newline character (`\n`):

```
<sender>\n<recipient>\n<timestamp>\n<payload>
```

For the example message above, the canonical string is:

```
alice.poweur.net
bob.example.org
2026-03-28T12:00:00Z
Hey Bob, are you around?
```

Note: there is no trailing newline. The fields are joined with `\n` separators, not terminated.

## Signing Procedure

1. Construct the canonical string as above.
2. Sign the canonical string using the sender's Ed25519 private key (raw bytes of the UTF-8 encoded canonical string).
3. Base64-encode the 64-byte signature (standard base64, RFC 4648 §4).
4. Place the encoded signature in the `signature` field.

The timestamp is included in the signed payload to prevent trivial replay attacks — a message signed at one time cannot be replayed at another without invalidating the signature.

## Verification Procedure

When a relay receives a message, it verifies the signature as follows:

1. Extract the `sender` field from the message.
2. Query DNS for `_eurything.<sender>` as a `TXT` record to retrieve the sender's public key. Parse the `eurything-pubkey=ed25519:<base64key>` value.
3. As a fallback, if the DNS `TXT` record is not yet propagated or not resolvable, call `GET /identities/:identity` on the peer relay resolved from the sender's `A`/`CNAME` record.
4. Reconstruct the canonical string from `sender`, `recipient`, `timestamp`, and `payload` in the same order used during signing.
5. Decode the `signature` field from base64.
6. Verify the decoded signature against the canonical string using the retrieved Ed25519 public key.
7. If verification fails, reject the message with `401 Unauthorized`.

Relays forwarding messages on behalf of another relay do **not** re-sign. The original signed envelope is forwarded as-is. The receiving relay always verifies against the original sender's public key.

## Inbox Message Format

When a client retrieves messages via `GET /messages/:identity`, each message includes an additional relay-assigned `id` field:

```json
{
  "messages": [
    {
      "id":        "msg_01j9xk7q2f000000000000000",
      "sender":    "bob.example.org",
      "recipient": "alice.poweur.net",
      "timestamp": "2026-03-28T12:00:00Z",
      "payload":   "Hey Alice!",
      "signature": "MEUCIQDzK3Lm9...base64encodedSignature...ABiReF4="
    }
  ]
}
```

The `id` field is assigned by the receiving relay and is not part of the signed canonical string. Clients should verify the signature on received messages before displaying them, even if the relay has already verified it at ingress.

## Future: Encryption

In the MVP, payloads are plaintext. A relay operator (or network observer) can read message content. End-to-end encryption is a planned post-MVP feature. The planned approach is to add an optional `encrypted` boolean field and an `encryption` object carrying the algorithm and key exchange parameters, while keeping the signing scheme unchanged. The canonical signing string would include the ciphertext rather than the plaintext payload, preserving authenticity guarantees.

## Related

- [Identity Model](/protocol/identity-model)
- [Routing](/protocol/routing)
- [API Reference](/relay/api-reference)
