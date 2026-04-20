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

### `eurything send <to> <message>`

Sign and send a message. By default the CLI:

1. Loads (or creates) a session for the active identity via `ensure session`.
2. Looks up the recipient's encryption public key from DNS.
3. Encrypts the payload with ChaCha20-Poly1305 if the recipient has a published encryption key. If not, it prints a warning and sends plaintext.
4. Signs the message with the **session private key**.
5. Attaches `session_id` and `session_proof` so the recipient's relay can verify without contacting the sender's relay.

```bash
eurything send bob.example.org "Hey Bob, are you there?"
```

**Flags:**

| Flag | Description |
|------|-------------|
| `--use-identity <identity>` | Send from a specific identity (overrides active identity) |
| `--no-encrypt` | Skip encryption and send plaintext (not recommended) |
| `--json` | Machine-readable output |

If the relay reports the session expired, the CLI silently re-registers a session and retries once before failing.

---

### `eurything inbox`

Fetch and display messages from the relay inbox for the active identity. The CLI:

1. Ensures a valid session.
2. Fetches a challenge from the relay, signs it with the session key, and calls `GET /messages/:identity`.
3. Decrypts any envelope with `encryption` metadata using the local X25519 private key. Decrypted messages are prefixed with `🔒` in human output.
4. Silently re-registers and retries if the relay reports the session expired.

```bash
eurything inbox
```

**Flags:**

| Flag | Description |
|------|-------------|
| `--use-identity <identity>` | Fetch inbox for a specific identity |
| `--json` | Raw JSON output (including undecrypted envelope) |

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
