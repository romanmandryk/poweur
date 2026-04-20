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

Generate a new Ed25519 key pair and register the identity `<name>.<parent-domain>` with the configured relay.

```bash
eurything identity create alice
```

The relay writes the DNS records and registers the identity.

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
  "identity":   "alice.poweur.net",
  "public_key": "MCowBQYDK2VwAyEAn3a7...",
  "relay":      "relay.poweur.net",
  "registered": true
}
```

---

### `eurything identity show`

Display the current identity's subdomain and public key.

```bash
eurything identity show
```

**Output:**
```
Identity:   alice.poweur.net
Public key: MCowBQYDK2VwAyEAn3a7...
Key file:   ~/.eurything/keys/alice.poweur.net.key
### `eurything identity use <identity>`

Update the default identity stored in `~/.eurything/config.toml`.

```bash
eurything identity use id2.poweur.net
```

### `eurything identity list`

List all identities discovered in the keys directory. The current default is marked with `*`.

```bash
eurything identity list
```
```

**JSON output (`--json`):**
```json
{
  "identity":   "alice.poweur.net",
  "public_key": "MCowBQYDK2VwAyEAn3a7...",
  "relay":      "https://relay.poweur.net",
  "key_path":   "~/.eurything/keys/alice.poweur.net.key"
}
```

---

### `eurything identity check <subdomain>`

Perform a live DNS lookup to verify that the identity's `TXT` and `A` records are propagated.

```bash
eurything identity check alice.poweur.net
```

**Output:**
```
Checking DNS for alice.poweur.net...
  TXT _eurything.alice.poweur.net → eurything-pubkey=ed25519:MCowBQYDK2Vw... ✓
  A   alice.poweur.net            → 95.217.142.10 ✓
DNS propagation confirmed.
```

---

### `eurything send <to> <message>`

Sign and send a message to the given identity address. The message is signed with the active identity's private key and submitted to the configured relay.

```bash
eurything send bob.example.org "Hey Bob, are you there?"
```

**Flags:**

| Flag | Description |
|------|-------------|
| `--from <identity>` | Send from a specific identity (overrides active identity) |

**Output:**
```
Message sent to bob.example.org
  Relay:     relay.poweur.net
  Message ID: msg_01j9xk7q2f000000000000000
  Timestamp: 2026-03-28T12:00:00Z
```

**JSON output (`--json`):**
```json
{
  "status":     "accepted",
  "message_id": "msg_01j9xk7q2f000000000000000",
  "sender":     "alice.poweur.net",
  "recipient":  "bob.example.org",
  "timestamp":  "2026-03-28T12:00:00Z"
}
```

---

### `eurything inbox`

Fetch and display messages from the relay inbox for the active identity. The CLI performs the challenge–response authentication flow automatically.

```bash
eurything inbox
```

**Output:**
```
Inbox for alice.poweur.net (3 messages)

  [1] From: bob.example.org  |  2026-03-28T12:00:00Z
      Hey Alice!

  [2] From: carol.poweur.net  |  2026-03-28T11:55:00Z
      Are you joining the call?

  [3] From: r2d2-bot.poweur.net  |  2026-03-28T11:50:00Z
      Status report: all systems nominal.
```

**Flags:**

| Flag | Description |
|------|-------------|
| `--limit <n>` | Show only the most recent `n` messages |
| `--from <identity>` | Filter messages from a specific sender |
| `--since <timestamp>` | Show only messages after this ISO 8601 timestamp |
| `--verify` | Verify each message signature before displaying (default: true) |

**JSON output (`--json`):**
```json
{
  "identity": "alice.poweur.net",
  "messages": [
    {
      "id":        "msg_01j9xk7q2f000000000000000",
      "sender":    "bob.example.org",
      "recipient": "alice.poweur.net",
      "timestamp": "2026-03-28T12:00:00Z",
      "payload":   "Hey Alice!",
      "signature": "<base64-encoded signature>",
      "verified":  true
    }
  ]
}
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
