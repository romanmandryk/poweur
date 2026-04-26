---
id: cli-reference
sidebar_position: 2
title: CLI Reference
---

# CLI Reference

The Eurything CLI (`eurything`) is a scriptable command-line tool for developers, bots, and automated agents. It provides full access to the Eurything Protocol: identity management, message sending, inbox retrieval, and relay diagnostics.

## Installation

The CLI is distributed as a single binary. It can also be run via the monorepo:

```bash
pnpm --filter @eurything/cli start
```

Or built and installed globally:

```bash
pnpm --filter @eurything/cli build
cp dist/eurything /usr/local/bin/
```

## Configuration

Configuration is stored at `~/.eurything/config.toml`. The CLI stores a default identity plus a root keys directory; you can override the identity per command.

```toml
# ~/.eurything/config.toml
relay_url = "https://relay.poweur.net"
identity  = "mybot.poweur.net"
keys_dir  = "~/.eurything/keys"
```

Individual settings can be overridden via environment variables:

| Setting | Env var |
|---------|---------|
| Relay URL | `RELAY_URL` |
| Identity subdomain | `IDENTITY` |
| Keys directory | `KEYS_DIR` |

## Global Flags

All commands accept these global flags:

| Flag | Description |
|------|-------------|
| `--json` | Output machine-readable JSON instead of human-friendly text |
| `--use-identity <subdomain>` | Override the active identity for this command |

---

## Commands

### `eurything identity create <name>`

Generate a new long-lived Ed25519 signing keypair **and** an X25519 encryption keypair, then register both public keys (and the relay routing record) with the configured relay.

```bash
eurything identity create alice
```

The relay writes three DNS records:

- `_eurything.<identity>` — identity public key
- `_eurything-enc.<identity>` — encryption public key
- `<identity>` — `A`/`CNAME` routing record

Private keys are written to `~/.eurything/keys/` (`<identity>.key` and `<identity>.enc`).

**Flags:**

| Flag | Description |
|------|-------------|
| `--dns-provider <name>` | DNS provider (`cloudflare` \| `hetzner`) |
| `--dns-token <value>` | DNS API token (prefer `CLOUDFLARE_API_TOKEN` / `HETZNER_API_TOKEN`) |
| `--parent-domain <domain>` | Parent domain used when a handle is provided |
| `--relay <url>` | Relay URL override |
| `--use-identity <subdomain>` | Override identity for this command |

**Example (JSON output):**
```bash
eurything identity create alice --dns-provider cloudflare --json
```

```json
{
  "identity":              "alice.poweur.net",
  "public_key":            "MCowBQYDK2VwAyEAn3a7...",
  "encryption_public_key": "2qsV0x9Ru9v3o_VzH7mHsH-yjwI5sOqO6sRpCVoqXxA",
  "key_path":              "~/.eurything/keys/alice.poweur.net.key",
  "encryption_key_path":   "~/.eurything/keys/alice.poweur.net.enc",
  "relay":                 "https://relay.poweur.net",
  "registered":            true
}
```

---

### `eurything identity show`

Display the current identity's subdomain, identity public key, and (if available) encryption public key.

```bash
eurything identity show
```

---

### `eurything identity dns <identity>`

Perform a live DNS lookup to verify that the identity's `TXT` (public key), `TXT` (encryption key), and `A`/`CNAME` records are propagated.

```bash
eurything identity dns alice.poweur.net
```

---

### `eurything identity use <identity>`

Update the default identity stored in `~/.eurything/config.toml`.

```bash
eurything identity use id2.poweur.net
```

---

### `eurything identity list`

List all identities discovered in the keys directory. The current default is marked with `*`.

```bash
eurything identity list
```

---

### `eurything identity add-encryption-key`

Retrofit an existing identity with an X25519 encryption key. Use this on identities created before E2E encryption was mandatory (they have no `_eurything-enc.<identity>` record and therefore cannot receive messages). The command generates an X25519 keypair on disk, hands the public half and a scoped DNS token to the relay, and waits until the relay confirms the DNS `TXT` record was written.

```bash
eurything identity add-encryption-key --use-identity alice.poweur.net
```

**Flags:**

| Flag | Description |
|------|-------------|
| `--use-identity <identity>` | Identity to retrofit (overrides active identity) |
| `--json` | Machine-readable output |

---

### `eurything send <to> <message>`

Sign and send an end-to-end-encrypted message. The CLI:

1. Loads (or creates) a session for the active identity via `ensure session`.
2. Generates a client-side message id (ULID-shaped) and writes a `queued` entry to the local pending journal (`~/.eurything/pending/<identity>.jsonl`).
3. Looks up the recipient's X25519 encryption public key at `_eurything-enc.<recipient>`. **If no record is found, the send is aborted** with an error that points the recipient at `eurything identity add-encryption-key`. There is no plaintext fallback.
4. Encrypts the payload with ChaCha20-Poly1305 under an X25519-derived key (see [End-to-End Encryption](/protocol/message-format#end-to-end-encryption)).
5. Signs the canonical envelope (including the `id:` line and the `enc:` line) with the **session private key**.
6. Attaches `session_id` and `session_proof` so the recipient's relay can verify without contacting the sender's relay.
7. Resolves the **recipient's** home relay via DNS and POSTs `/messages` directly there (default direct-send model). On `202 Accepted` the journal advances to `delivered_recipient_relay` (tick 1).

```bash
eurything send bob.example.org "Hey Bob, are you there?"
```

**Flags:**

| Flag | Description |
|------|-------------|
| `--use-identity <identity>` | Send from a specific identity (overrides active identity) |
| `--via-home-relay` | Privacy proxy: POST to the configured home relay (`relay_url`) instead of directly to the recipient's relay. The home relay accepts because the sender is locally hosted, then forwards to the recipient relay. Hides the sender's IP from the recipient relay at the cost of an extra hop. |
| `--sign-with <session\|identity>` | Choose the signing key (default `session`). Identity-signed sends omit `session_id`/`session_proof`. |
| `--json` | Machine-readable output |

If the relay reports the session expired, the CLI silently re-registers a session and retries once before failing. If the relay returns `400 encryption_required` the CLI surfaces the error — this indicates a client bug, since the CLI always encrypts. Network/HTTP errors during the send write a `failed` entry to the journal (sticky).

Note that `cfg.RelayURL` (the configured `relay_url`) is the **home** relay — it is used for inbox polling, ack delivery, identity admin, and (only when `--via-home-relay` is set) outbound sends.

---

### `eurything inbox`

Fetch and display messages from the relay inbox for the active identity. The CLI:

1. Ensures a valid session.
2. Fetches a challenge from the relay, signs it with the session key, and calls `GET /messages/:identity`.
3. Decrypts any envelope with `encryption` metadata using the local X25519 private key. Decrypted messages are prefixed with `🔒` in human output.
4. For every successfully decrypted message, signs and POSTs a `delivered_client` ack to the **original sender's** home relay (DNS-resolved). The local pending journal records this as the source of tick 2 for that conversation partner.
5. Drains the response's `acks` array, advancing the local pending journal to `delivered_client` for any referenced message ids — this is how the sender learns about tick 2.
6. Silently re-registers and retries if the relay reports the session expired.

```bash
eurything inbox
```

**Flags:**

| Flag | Description |
|------|-------------|
| `--use-identity <identity>` | Fetch inbox for a specific identity |
| `--json` | Raw JSON output (including undecrypted envelope) |

---

### `eurything messages status`

Show the local pending journal for the active identity. Each line of
`~/.eurything/pending/<identity>.jsonl` is collapsed to the latest state
per `message_id`, then rendered with WhatsApp-style tick glyphs:

| Glyph | State | Meaning |
|-------|-------|---------|
| `·` | `queued` | Send pipeline started, no relay response yet |
| `✓` | `delivered_recipient_relay` | Recipient relay returned `202 Accepted` (tick 1) |
| `✓✓` | `delivered_client` | Recipient client acked successful decrypt (tick 2) |
| `✗` | `failed` | Send pipeline gave up; sticky |

```bash
eurything messages status
eurything messages status --id msg_01j9xkay7g000000000000000
eurything messages status --json
```

**Flags:**

| Flag | Description |
|------|-------------|
| `--use-identity <identity>` | Read the journal for a specific identity |
| `--id <message_id>` | Show only the entry for the given message id |
| `--json` | Machine-readable JSON output |

See [Delivery Acks](/protocol/delivery-acks) for the full state model.

---

### `eurything session status`

Show the locally cached session for the active identity.

```bash
eurything session status
```

### `eurything session refresh`

Force-delete the cached session and register a new one. Useful when debugging or rotating keys without waiting for the 24h TTL.

```bash
eurything session refresh
```

### `eurything session revoke`

Delete the local session file only (does not call `DELETE /sessions/:id` on the relay). The next send or inbox call will transparently re-register.

```bash
eurything session revoke
```

---

### `eurything relay status`

Check relay connectivity. Displays the configured relay endpoint and relay version, and reports whether the relay is reachable.

```bash
eurything relay status
```

**Output:**
```
Relay:    https://relay.poweur.net
Status:   ✓ OK
Version:  0.1.0
Latency:  42ms
```

**JSON output (`--json`):**
```json
{
  "relay":   "https://relay.poweur.net",
  "status":  "ok",
  "version": "0.1.0",
  "latency_ms": 42
}
```

---

## Environment Variables

| Variable | Purpose |
|----------|---------|
| `DNS_SERVER` | Override the system resolver for identity lookups. Accepts `host`, `host:port`, or an IPv4/IPv6 literal (e.g. `1.1.1.1`). Useful when the local network caches negative DNS responses. |
| `DEBUG_HTTP` | When set to a non-empty value, dumps every relay HTTP request and response to stderr. Sensitive material (session and identity signatures) appears in these dumps — only use for local debugging. |

---

## Exit Codes

| Code | Meaning |
|------|---------|
| `0` | Success |
| `1` | General error |
| `2` | Configuration error (missing or invalid config) |
| `3` | Network error (relay unreachable) |
| `4` | Authentication error (signature or challenge failure) |
| `5` | Rate limit exceeded |

## Related

- [Clients Overview](/clients/overview)
- [API Reference](/relay/api-reference)
- [Identity Model](/protocol/identity-model)
