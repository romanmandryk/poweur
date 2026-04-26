---
id: routing
sidebar_position: 5
title: Routing
---

# Routing

The Eurything Protocol uses DNS as its routing layer. Every identity subdomain points to the relay that handles messages for that identity ("its home relay"). By default a sending client posts each outbound message **directly** to the recipient's home relay — the sender's own home relay is uninvolved in outbound traffic. Any client anywhere in the world can deliver to any identity by performing a standard DNS lookup; no central routing registry and no relay coordination is required.

## Default: Direct Send

The full delivery path for a message from Alice (`alice.poweur.net`) to Bob (`bob.example.org`) under the default direct-send model:

```mermaid
sequenceDiagram
    participant AC as Alice's Client<br/>(Mobile/CLI)
    participant DNS as Public DNS
    participant BR as Bob's Relay<br/>(relay.example.org)
    participant BC as Bob's Client<br/>(Mobile/CLI)
    participant AR as Alice's Relay<br/>(home; for inbox/acks only)

    AC->>AC: Generate id, sign canonical<br/>(includes id:<message_id>)
    AC->>DNS: A bob.example.org
    DNS-->>AC: 198.51.100.42
    AC->>BR: POST /messages<br/>{id, sender, recipient, timestamp, payload, signature, encryption}
    BR->>BR: Rate-limit (sender + global)<br/>encryption envelope check
    BR->>DNS: TXT _eurything.alice.poweur.net
    DNS-->>BR: "eurything-pubkey=ed25519:..."
    BR->>BR: Verify signature, apply<br/>at-least-one-local rule (Bob is local),<br/>store in Bob's inbox keyed by id
    BR-->>AC: 202 Accepted (tick 1)
    BC->>BR: GET /messages/bob.example.org<br/>(challenge-response auth)
    BR-->>BC: {messages: [...], acks: [...]}
    BC->>BC: Verify signature, decrypt
    BC->>DNS: A alice.poweur.net
    DNS-->>BC: ...AR
    BC->>AR: POST /acks {state: delivered_client, ...}
    AR->>AR: At-least-one-local (Alice is local), store ack
    AC->>AR: GET /messages/alice.poweur.net
    AR-->>AC: {messages: [], acks: [tick 2]}
```

The home relay's role under the default model is symmetric: it receives **inbound** messages addressed to its locally hosted identities, and **inbound** acks addressed to its locally hosted identities (the original message senders). It is not on the outbound path.

## Optional: `--via-home-relay` (privacy proxy)

A client may opt into routing its outbound traffic through its own home relay so the recipient relay sees the home relay's IP rather than the client's IP. The home relay accepts because the **sender** is locally hosted ([at-least-one-local rule](/relay/api-reference#at-least-one-local-rule)), then forwards the original signed envelope to the recipient relay. This is the only sanctioned forwarding path; relays never act as open forwarders for unrelated parties.

```mermaid
sequenceDiagram
    participant AC as Alice's Client
    participant AR as Alice's Home Relay
    participant BR as Bob's Relay
    AC->>AR: POST /messages (sender local, recipient remote)
    AR-->>AC: 202 Accepted
    AR->>BR: POST /messages (forwarded; original envelope)
    BR-->>AR: 202 Accepted
```

The signed envelope is unchanged on the wire; relays never re-sign forwarded traffic.

## Step-by-Step Breakdown (default direct send)

### 1. Client constructs and signs

Alice's client generates a client-side message id (ULID/UUID), constructs the canonical string (including `id:<message_id>` and the encryption envelope), and signs with the session key (or long-lived identity key for identity-signed sends).

### 2. Client resolves recipient relay

Alice's client resolves `bob.example.org`'s `A`/`CNAME` directly to find Bob's relay address.

### 3. Client posts directly

Alice's client POSTs the signed envelope to Bob's relay's `/messages`. Alice's home relay is not contacted.

### 4. Recipient relay accepts

Bob's relay applies rate limits (per-sender + global), verifies the signature against Alice's DNS-published public key, applies the [at-least-one-local rule](/relay/api-reference#at-least-one-local-rule) (Bob is local — accept), and stores the message in Bob's inbox keyed by the client-assigned id. It returns `202 Accepted` (tick 1).

### 5. Bob fetches and decrypts

Bob's client polls `GET /messages/bob.example.org`, which returns both `messages` and `acks` arrays. Bob's client verifies signatures, decrypts the payload, and emits a signed `delivered_client` ack.

### 6. Recipient client posts the tick-2 ack

Bob's client resolves Alice's home relay via DNS and POSTs the ack to **Alice's** home relay's `/acks`. Alice's home relay accepts because Alice is locally hosted (at-least-one-local satisfied via `recipient`).

### 7. Alice sees tick 2

On Alice's next inbox poll, the `acks` array carries the `delivered_client` ack. Her CLI advances the local pending journal accordingly. See [Delivery Acks](/protocol/delivery-acks).

## Local vs. Remote: the at-least-one-local rule

A relay accepts a message or ack iff the sender or the recipient is a locally hosted identity. The three accepted cases are:

- **Recipient-local** — store in inbox (the normal direct-send ingress).
- **Sender-local, recipient-remote** — forward to the recipient relay (the privacy-proxy case for `--via-home-relay`).
- **Both local** — note-to-self; store in inbox.

Anything else returns `403 not_authorized`. Relays never re-sign forwarded envelopes; the original sender's signature travels end-to-end and is verified independently at every hop that handles it.

## DNS Routing Cache

To avoid a DNS lookup on every message, relays maintain an in-memory cache of resolved relay addresses, keyed by recipient subdomain. Cache entries expire at the DNS record's TTL. On a cache miss, the relay performs a fresh DNS resolution.

The cache is ephemeral — it is lost on relay restart. On restart, the first message to each remote identity triggers a fresh DNS lookup. This is acceptable; the overhead is a single DNS round-trip.

## Horizontal Scaling

Because the relay is stateless and routing is DNS-driven, horizontal scaling requires no coordination:

1. Add relay instances behind a load balancer.
2. The load balancer's IP is what DNS `A` records point to.
3. Any relay instance can accept, verify, and forward any message.

Inbox messages are held in-memory per-instance in the MVP. For production workloads, a shared in-memory store (e.g. Redis) or a message queue would be substituted for the local inbox — but this is a post-MVP concern.

## Operator Setup

For an operator hosting multiple identities on a single relay, all identity subdomains should point to the same relay (via a shared `CNAME` target or the same `A` record). For example:

```
; All identities on relay.poweur.net
alice.poweur.net.  300  IN  CNAME  relay.poweur.net.
bob.poweur.net.    300  IN  CNAME  relay.poweur.net.
carol.poweur.net.  300  IN  CNAME  relay.poweur.net.
relay.poweur.net.  300  IN  A      95.217.142.10
```

A single wildcard TLS certificate (`*.poweur.net`) covers all identity subdomains. See [TLS Configuration](/security/tls) for details.

## Related

- [DNS Records](/protocol/dns-records)
- [Rate Limiting](/protocol/rate-limiting)
- [Relay Overview](/relay/overview)
- [API Reference](/relay/api-reference)
