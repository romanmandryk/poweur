---
id: cli-reference
sidebar_position: 2
title: CLI Reference
---

# CLI Reference

The Poweur ID CLI (`poweur`) is a scriptable command-line tool for developers, bots, and automated agents. It provides full access to the Poweur ID Protocol: identity management, message sending, inbox retrieval, and relay diagnostics.

## Installation

The CLI is distributed as a single binary. It can also be run via the monorepo:

```bash
pnpm --filter @poweur/cli start
```

Or built and installed globally:

```bash
pnpm --filter @poweur/cli build
cp dist/poweur /usr/local/bin/
```

## Configuration

Configuration is stored at `~/.poweur/config.toml`. The CLI stores a default identity plus a root keys directory; you can override the identity per command.

```toml
# ~/.poweur/config.toml
relay_url = "https://relay.poweur.net"
identity  = "mybot.poweur.net"
keys_dir  = "~/.poweur/keys"
```

Individual settings can be overridden via environment variables:

| Setting | Env var |
|---------|---------|
| Relay URL | `RELAY_URL` |
| Identity subdomain | `IDENTITY` |
| Keys directory | `KEYS_DIR` |
| Key-file passphrase | `POWEUR_KEY_PASSPHRASE` |

## Global Flags

All commands accept these global flags:

| Flag | Description |
|------|-------------|
| `--json` | Output machine-readable JSON instead of human-friendly text |
| `--use-identity <subdomain>` | Override the active identity for this command |

---

## Commands

### `poweur identity create <name>`

Generate a new long-lived Ed25519 signing keypair **and** an X25519 encryption keypair, then register with the configured relay. Two modes:

**Hosted** (`--hosted`): no DNS token. The client submits a signed identity document; the relay
persists it under `HOSTED_DOMAINS` (e.g. `*.poweur.net` wildcard). Keys are published via
`/.well-known/poweur/` (see [Web identity](/protocol/web-identity)).

```bash
poweur identity create alice --hosted --relay http://127.0.0.1:8080
```

**Self-hosted** (DNS token): the relay writes DNS records using your provider token:

- `_poweur.<identity>` — identity public key
- `_poweur-enc.<identity>` — encryption public key
- `<identity>` — `A`/`CNAME` routing record

```bash
poweur identity create alice --dns-provider cloudflare
```

**Seed-derived** (`--from-seed` or `--seed`): both long-lived keys are derived from a single
32-byte master seed, so the whole identity can be recovered from those 32 bytes alone. See
[Key management & recovery](/security/key-management).

```bash
poweur identity create alice --hosted --from-seed --relay http://127.0.0.1:8080
```

`--from-seed` generates a seed and prints it — **this is the only copy**. `--seed <base64url>`
uses a seed you already hold, and never re-prints it. Without either flag the CLI generates two
independent random keys, as before; those identities cannot produce a recovery seed until they
rotate to one.

Private keys are written to `~/.poweur/keys/` (`<identity>.key` and `<identity>.enc`). Existing
files are never overwritten: a second `create` for a name this machine already holds fails and
points you at `key enroll` / `key recover`. If the relay refuses the registration (name taken,
policy, unreachable), the CLI deletes the key files it just wrote so they cannot shadow a later
enroll or recover.

**Flags:**

| Flag | Description |
|------|-------------|
| `--hosted` | Hosted registration (no DNS token; requires `HOSTED_DOMAINS` on relay) |
| `--invite-code <code>` | Invite code when the relay gate is `invite` |
| `--dns-provider <name>` | DNS provider (`cloudflare` \| `hetzner`) — self-hosted mode |
| `--dns-token <value>` | DNS API token (prefer `CLOUDFLARE_API_TOKEN` / `HETZNER_API_TOKEN`) |
| `--parent-domain <domain>` | Parent domain used when a handle is provided |
| `--relay <url>` | Relay URL override |
| `--from-seed` | Generate a master seed and derive both keys from it; the seed is printed once |
| `--seed <base64url>` | Derive both keys from an existing master seed (never re-printed) |
| `--use-identity <subdomain>` | Override identity for this command |
| `--json` | Machine-readable output |

`--from-seed` and `--seed` are mutually exclusive.

**Example (JSON output):**
```bash
poweur identity create alice --dns-provider cloudflare --json
```

```json
{
  "identity":              "alice.poweur.net",
  "public_key":            "MCowBQYDK2VwAyEAn3a7...",
  "encryption_public_key": "2qsV0x9Ru9v3o_VzH7mHsH-yjwI5sOqO6sRpCVoqXxA",
  "key_path":              "~/.poweur/keys/alice.poweur.net.key",
  "encryption_key_path":   "~/.poweur/keys/alice.poweur.net.enc",
  "relay":                 "https://relay.poweur.net",
  "registered":            true,
  "seed_derived":          false
}
```

With `--from-seed`, the response additionally carries `"seed"` (unpadded base64url). It is
returned exactly once — store it before the process exits:

```bash
poweur identity create alice --hosted --from-seed --json | jq -r .seed > alice.seed
chmod 600 alice.seed
```

---

### `poweur identity show`

Display the current identity's subdomain, identity public key, and (if available) encryption public key.

```bash
poweur identity show
```

---

### `poweur identity dns <identity>`

Perform a live DNS lookup to verify that the identity's `TXT` (public key), `TXT` (encryption key), and `A`/`CNAME` records are propagated.

```bash
poweur identity dns alice.poweur.net
```

---

### `poweur identity lookup <identity>`

Resolve an identity using the **web-first** chain (HTTPS
`/.well-known/poweur/id.json`, then DNS TXT). Prints source (`web` / `dns` /
`both`), keys, the safety number, and the relay. Prefer this over `identity dns`
for hosted identities that have no per-user TXT records.

It also reads the identity's public self-description from the same well-known
route — `profile.json` (display name, bio, locale, avatar, links) and
`capabilities.json` (features and endpoint hints), both served world-readable
out of `poweur-sys/public/`. Both are optional: an identity that publishes
neither still looks up fine, and only an unreachable host (as opposed to a 404)
prints a note on stderr. When there is no `capabilities.json`, the identity
document's own `capabilities` list is shown instead — the same fallback the web
client uses, so the two surfaces degrade to the same answer.

```bash
poweur identity lookup alice.poweur.net
poweur identity lookup alice.poweur.net --json
```

In `--json`, `capabilities` stays the identity document's string list and
`capabilities_document` carries `capabilities.json` when one is published;
`profile` is the profile document or `null`.

---

### `poweur identity use <identity>`

Update the default identity stored in `~/.poweur/config.toml`.

```bash
poweur identity use id2.poweur.net
```

---

### `poweur identity list`

List all identities discovered in the keys directory. The current default is marked with `*`.

```bash
poweur identity list
```

---

### `poweur identity add-encryption-key`

Retrofit an existing identity with an X25519 encryption key. Use this on identities created before E2E encryption was mandatory (they have no `_poweur-enc.<identity>` record and therefore cannot receive messages). The command generates an X25519 keypair on disk, hands the public half and a scoped DNS token to the relay, and waits until the relay confirms the DNS `TXT` record was written.

```bash
poweur identity add-encryption-key --use-identity alice.poweur.net
```

**Flags:**

| Flag | Description |
|------|-------------|
| `--use-identity <identity>` | Identity to retrofit (overrides active identity) |
| `--json` | Machine-readable output |

---

### `poweur send <to> <message>`

Sign and send an end-to-end-encrypted message. The CLI:

1. Loads (or creates) a session for the active identity via `ensure session`.
2. Generates a client-side message id (ULID-shaped) and writes a `queued` entry to the local pending journal (`~/.poweur/pending/<identity>.jsonl`).
3. Looks up the recipient's X25519 encryption public key at `_poweur-enc.<recipient>`. **If no record is found, the send is aborted** with an error that points the recipient at `poweur identity add-encryption-key`. There is no plaintext fallback.
4. Encrypts the payload with ChaCha20-Poly1305 under an X25519-derived key (see [End-to-End Encryption](/protocol/message-format#end-to-end-encryption)).
5. Signs the canonical envelope (including the `id:` line and the `enc:` line) with the **session private key**.
6. Attaches `session_id` and `session_proof` so the recipient's relay can verify without contacting the sender's relay.
7. Resolves the **recipient's** home relay via DNS and POSTs `/messages` directly there (default direct-send model). On `202 Accepted` the journal advances to `delivered_recipient_relay` (tick 1).

```bash
poweur send bob.example.org "Hey Bob, are you there?"
```

**Flags:**

| Flag | Description |
|------|-------------|
| `--use-identity <identity>` | Send from a specific identity (overrides active identity) |
| `--via-home-relay` | Privacy proxy: POST to the configured home relay (`relay_url`) instead of directly to the recipient's relay. The home relay accepts because the sender is locally hosted, then forwards to the recipient relay. Hides the sender's IP from the recipient relay at the cost of an extra hop. |
| `--sign-with <session\|identity>` | Choose the signing key (default `session`). Identity-signed sends omit `session_id`/`session_proof`. |
| `--type <type>` | Envelope message type. Omit for ordinary chat: absent means `chat.text`, and the absent form is what keeps the signed canonical string identical to a pre-typing client's. `sys.*` is reserved — an unregistered one is refused locally. |
| `--thread <id>` | Group this message into a conversation thread. Opaque to the relay. |
| `--expires <rfc3339>` | When the message stops being meaningful. Expired envelopes are refused with HTTP 410. |
| `--attach <file>` | Upload (max 20 MB), grant read to the recipient, and send a `chat.attachment` reference. The caption is optional. |
| `--meta <key=value>` | Envelope metadata, repeatable. **Plaintext** — addressing, not content. A duplicate key is an error rather than a silent overwrite. |
| `--json` | Machine-readable output |

`--type`, `--thread`, `--expires` and `--meta` are validated before the message is encrypted, signed or journalled, so a typo never becomes a recorded send attempt. They cannot be combined with `--anon`: an unsigned envelope binds nothing, so the fields would be routing metadata nobody could trust. See [Typed Messages](/protocol/message-format#typed-messages).

```bash
poweur send bob.example.org "the logo, v3" --thread thr_rebrand --attach ./logo.png
```

If the relay reports the session expired, the CLI silently re-registers a session and retries once. Network failures, 429s and 5xx responses queue an identity-signed encrypted envelope with capped exponential backoff; `poweur listen` retries on reconnect and `poweur outbox list|retry` exposes it explicitly. Permanent 4xx responses still write a sticky `failed` entry.

Note that `cfg.RelayURL` (the configured `relay_url`) is the **home** relay — it is used for inbox polling, ack delivery, identity admin, and (only when `--via-home-relay` is set) outbound sends.

---

### `poweur inbox`

Fetch and display messages from the relay inbox for the active identity. The CLI:

1. Ensures a valid session.
2. Fetches a challenge from the relay, signs it with the session key, and calls `GET /messages/:identity`.
3. Decrypts any envelope with `encryption` metadata using the local X25519 private key. Decrypted messages are prefixed with `🔒` in human output.
4. For every successfully decrypted message, signs and POSTs a `delivered_client` ack to the **original sender's** home relay (DNS-resolved). The local pending journal records this as the source of tick 2 for that conversation partner.
5. Drains the response's `acks` array, advancing the local pending journal to `delivered_client` for any referenced message ids — this is how the sender learns about tick 2.
6. Silently re-registers and retries if the relay reports the session expired.

```bash
poweur inbox
```

**Flags:**

| Flag | Description |
|------|-------------|
| `--use-identity <identity>` | Fetch inbox for a specific identity |
| `--json` | Raw JSON output (including undecrypted envelope) |

---

### `poweur messages status`

Show the local pending journal for the active identity. Each line of
`~/.poweur/pending/<identity>.jsonl` is collapsed to the latest state
per `message_id`, then rendered with WhatsApp-style tick glyphs:

| Glyph | State | Meaning |
|-------|-------|---------|
| `·` | `queued` | Send pipeline started, no relay response yet |
| `✓` | `delivered_recipient_relay` | Recipient relay returned `202 Accepted` (tick 1) |
| `✓✓` | `delivered_client` | Recipient client acked successful decrypt (tick 2) |
| `✗` | `failed` | Send pipeline gave up; sticky |

```bash
poweur messages status
poweur messages status --id msg_01j9xkay7g000000000000000
poweur messages status --json
```

**Flags:**

| Flag | Description |
|------|-------------|
| `--use-identity <identity>` | Read the journal for a specific identity |
| `--id <message_id>` | Show only the entry for the given message id |
| `--json` | Machine-readable JSON output |

See [Delivery Acks](/protocol/delivery-acks) for the full state model.

---

### `poweur session status`

Show the locally cached session for the active identity.

```bash
poweur session status
```

### `poweur session refresh`

Force-delete the cached session and register a new one. Useful when debugging or rotating keys without waiting for the 24h TTL.

```bash
poweur session refresh
```

### `poweur session revoke`

Delete the local session file only (does not call `DELETE /sessions/:id` on the relay). The next send or inbox call will transparently re-register.

```bash
poweur session revoke
```

---

### `poweur dav token`

Mint a WebDAV bearer token signed with the long-lived identity key. Use for rclone,
custom clients, or visitors reading another identity's `/public` tree
(see [WebDAV access](/files/webdav)).

```bash
poweur dav token
poweur dav token --audience=bob.poweur.net --scope=dav:read
poweur dav token --json
```

### `poweur dav mount`

Print a ready-to-paste mount command for the current OS (macOS `mount_webdav`,
Linux `davfs2`/`rclone`, etc.), including a freshly minted token or a reminder to
create an app password for Finder.

```bash
poweur dav mount
```

### `poweur dav password add|list|remove`

Manage named app passwords for Basic-auth WebDAV clients. Hashes are stored at
`poweur-sys/relay/app-passwords.json` on the relay (owner + relay readable).

```bash
poweur dav password add --name=finder
poweur dav password list
poweur dav password remove --name=finder
```

---

### `poweur relay status`

Check relay connectivity. Displays the configured relay endpoint and relay version, and reports whether the relay is reachable.

```bash
poweur relay status
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

### `poweur key rotate`

Replace the identity's signing key, publishing a rotation statement so verifiers accept the new
key while the old one stays valid for a grace period (see
[Web identity](/protocol/web-identity)). Contacts who pinned the old key follow the statement
rather than warning.

```bash
poweur key rotate --grace 168h
```

| Flag | Description |
|------|-------------|
| `--grace <duration>` | How long the previous key stays valid (default `168h`) |
| `--use-identity <subdomain>` | Identity to rotate |
| `--json` | Machine-readable output |

---

### `poweur key derive --seed <base64url>`

Print the public keys a master seed derives — **offline, writing nothing**. Use it to check a
recovery seed against a published identity document before trusting it.

```bash
poweur key derive --seed "$(cat alice.seed)" --json
```

```json
{
  "public_key":            "gq2n...",
  "encryption_public_key": "CJ5t..."
}
```

Compare against what the relay serves:

```bash
curl -s https://relay.poweur.net/identities/alice.poweur.net | jq .public_key
```

| Flag | Description |
|------|-------------|
| `--seed <base64url>` | Master seed (required) |
| `--json` | Machine-readable output |

---

### `poweur key recover <identity> --seed <base64url>`

Rebuild an identity's private key files from its master seed. **Offline** — no relay call, no
network. The relay already holds the public half, so restoring the private half locally is all
that is needed to use the identity again.

```bash
poweur key recover alice.poweur.net --seed "$(cat alice.seed)" \
  --relay https://relay.poweur.net
poweur inbox
```

Writes `~/.poweur/keys/<identity>.key` and `.enc`, and sets the identity, keys directory and
relay in `~/.poweur/config.toml` — so it works on a machine with no prior Poweur config.

| Flag | Description |
|------|-------------|
| `--seed <base64url>` | Master seed (required) |
| `--relay <url>` | Relay to configure (needed on a fresh machine) |
| `--parent-domain <domain>` | Parent domain to configure |
| `--json` | Machine-readable output |

:::caution
Recovery is pure local key derivation, so it always "succeeds" — a wrong seed produces valid
keys that simply are not this identity's. Verify with `poweur key derive` first, or the failure
surfaces later as a rejected request from the relay.
:::

---

### `poweur key kit --seed <seed-or-mnemonic>`

Render a master seed as a recovery kit — 24 BIP39 words plus the base64url seed. Offline: it
converts, it does not generate or store.

```bash
poweur key kit --seed "$(cat alice.seed)"
```

The mnemonic is an **encoding of the same 32 bytes**, not a second secret. It earns its keep
only where a human copies the seed by hand: the checksum catches transcription slips, and the
wordlist avoids the `l/I/1` and `O/0` confusions of base64url. The carrier is up to you — a
printed card, a text file, a password-manager entry are all equally valid.

Every `--seed` flag in the CLI accepts either encoding, so a pasted kit just works.

---

### `poweur key ls`

The "Keys & devices" inventory: every enrollment registered for an identity, with kind, role,
label and last-used time. Metadata only — listing devices needs no access to the wrapped seeds.

```bash
poweur key ls --json
```

An identity with no enrollments says so explicitly, and reminds you the seed is then the only
way back.

---

### `poweur key enroll <identity>` / `key approve` / `key claim`

Move an identity to a new device using a six-digit code — **no camera, no QR, no email**.

On the new device:

```bash
poweur key enroll alice.poweur.net --relay https://relay.poweur.net --label "work laptop"
```

It prints a rendezvous id and a code. On a device that already holds the identity:

```bash
poweur key approve <rendezvous-id> --seed "$(cat alice.seed)" --sas 481920
```

Then back on the new device:

```bash
poweur key claim alice.poweur.net <rendezvous-id> --ephemeral-key <printed-key>
```

`key enroll --wait` collapses the last step by polling until approval.

| Flag | Command | Description |
|------|---------|-------------|
| `--label <text>` | enroll | Device description shown to the approver |
| `--wait` | enroll | Poll until approved, then install the keys |
| `--seed <value>` | approve | Master seed; it lives only on your devices, never on the relay |
| `--sas <digits>` | approve | Refuse to proceed unless the code matches |
| `--ephemeral-key <b64url>` | claim | The private key printed by `key enroll` |

:::caution
**Comparing the code is the authentication step.** Pass `--sas` so a mismatch aborts; without it
the CLI can only print the code and trust the operator to check. Approving without comparing
hands the seed to whoever opened the rendezvous.
:::

The code authenticates the new device's *public* key rather than protecting a secret, so there
is nothing to brute-force offline — see [Key management & recovery](/security/key-management).

---

### `poweur key protect` / `poweur key unprotect`

Encrypt an identity's key files at rest with a passphrase (scrypt + AES-256-GCM), or decrypt
them again. The CLI historically wrote plaintext base64 with mode `0600` — defensible for a bot
on a hardened host, thin for a laptop.

```bash
export POWEUR_KEY_PASSPHRASE='…'
poweur key protect
```

Encrypted and plaintext files are both readable; detection is by shape, so migration needs no
rename or config change. Every command then loads keys transparently as long as
`POWEUR_KEY_PASSPHRASE` is set — the unattended-agent path.

| Flag | Description |
|------|-------------|
| `--passphrase <value>` | Passphrase (prefer the environment variable so it stays out of shell history) |
| `--use-identity <subdomain>` | Identity whose keys to protect |
| `--json` | Machine-readable output |

:::caution
Without the passphrase the keys cannot be loaded. Losing it is equivalent to losing the device —
recover from the seed or another enrolled device.
:::

---

### `poweur contacts <ls|add|request|accept|block|rm> [<identity>]`

Manage the contact list in `poweur-sys/relay/contacts.json`. See
[Contacts & trust](/trust/contacts).

```bash
poweur contacts request bob.poweur.net
poweur contacts accept bob.poweur.net --petname bob
poweur contacts ls
```

| Subcommand | Effect |
|------------|--------|
| `ls` | List contacts with state and petname |
| `add <id>` | Add directly as accepted (no request round-trip) |
| `request <id>` | Send a contact request |
| `accept <id>` | Accept a pending incoming request |
| `block <id>` | Block an identity |
| `rm <id>` | Remove a contact |

| Flag | Description |
|------|-------------|
| `--petname <name>` | Local display name for this contact |
| `--use-identity <subdomain>` | Override identity |

---

### `poweur requests`

List pending incoming contact requests. Accept them with `poweur contacts accept`.

```bash
poweur requests --json
```

---

### `poweur policy <show|set>`

Read or write the inbox policy — who may reach you, and on what terms.

```bash
poweur policy show
poweur policy set contacts_and_requests --anon-allow=true --anon-challenge=pow --anon-bits=20
```

| Mode | Effect |
|------|--------|
| `open` | Anyone may send chat; contact requests still land in the requests queue |
| `contacts_only` | Only accepted contacts |
| `contacts_and_requests` | Contacts, plus strangers who may send a contact request |

| Flag | Description |
|------|-------------|
| `--anon-allow <bool>` | Accept anonymous (unsigned) messages |
| `--anon-challenge <kind>` | `none` \| `pow` \| `verified` \| `payment` |
| `--anon-bits <n>` | Proof-of-work difficulty; each +1 doubles sender work (0 = relay default) |
| `--anon-max-bytes <n>` | Max anonymous payload bytes (0 = default 4096) |
| `--anon-max-per-day <n>` | Max accepted anonymous messages per day (0 = default 20) |
| `--use-identity <subdomain>` | Override identity |
| `--json` | Machine-readable output |

---

### `poweur report <identity>`

Report an identity to the relay operator who hosts them (`sys.abuse.report`). The
report is signed by you and carries message IDs, a reason and an optional note — never
message content, which is end-to-end encrypted and which the operator could not read
anyway.

```bash
poweur report loud.cheapco.test --reason=spam --note="twelve identical messages" --message-ids=m-1,m-2
```

| Flag | Description |
|------|-------------|
| `--reason <kind>` | `spam` \| `harassment` \| `phishing` \| `malware` \| `impersonation` \| `other` |
| `--note <text>` | Free text for the operator (max 2048 bytes) |
| `--message-ids <ids>` | Comma-separated message IDs as evidence (max 32) |
| `--use-identity <subdomain>` | Override identity |
| `--json` | Machine-readable output |

One report per reporter per subject per day counts; repeats answer `duplicate`. A relay
only accepts reports about identities it hosts.

---

### `poweur blocks <export|import>`

Blocklists are block decisions made portable: a signed document a community can pool.
Export publishes `shared/blocks.json` in your own tree, where a
[share](../files/sharing) hands it to a chosen audience. Import verifies the
publisher's signature and merges into your own contacts, where you can see and undo it.

```bash
poweur blocks export --name "my list"
poweur share add /shared/blocks.json --with bob.example.org --perm read
poweur blocks import alice.example.org --dry-run
poweur blocks import --file list.json
```

| Flag | Description |
|------|-------------|
| `--name <text>` | Human label for an exported list |
| `--out <file>` | Also write the signed document locally |
| `--no-publish` | Do not write it into your own tree |
| `--file <path>` | Import from a local file instead of a publisher's tree |
| `--path <tree path>` | Tree path to read from the publisher (default `shared/blocks.json`) |
| `--force` | Also block identities you have accepted as contacts |
| `--dry-run` | Show what would change without writing |
| `--use-identity <subdomain>` | Override identity |
| `--json` | Machine-readable output |

Identities you have accepted as contacts are skipped and named unless `--force`, and an
unsigned or altered list is refused outright.

---

### `poweur anon`

Read the anonymous queue — messages accepted under the policy's `anonymous` block. These are
**unauthenticated**: there is no verified sender and no reply path.

```bash
poweur anon --json
```

To send one, use `poweur send <to> <message> --anon`; the CLI solves the recipient's
proof-of-work challenge if their policy demands one.

---

### `poweur share add <path> --with <id>`

Grant another identity access to a path in your tree, as a signed grant under
`poweur-sys/relay/shares/`. See [Sharing & ACLs](/files/sharing).

```bash
poweur share add /private/reports --with bob.poweur.net --perm rw --expires 2026-12-31T00:00:00Z
```

| Flag | Description |
|------|-------------|
| `--with <id>` | Recipient Poweur ID (repeatable) |
| `--with-group <name>` | Recipient group: an owner-local name (`team`) or a group identity's Poweur ID (`crew.acme.poweur.net`); repeatable |
| `--perm <read\|rw>` | Permission level (default `read`) |
| `--expires <rfc3339>` | Expiry; empty means never |
| `--use-identity <subdomain>` | Identity to share from |
| `--json` | Machine-readable output |

Related: `poweur share ls`, `poweur share revoke <share-id>`, and the owner-local group
commands `poweur share group set <name> --members=<id,id,...>`, `group ls`,
`group remove <name>`.

---

### `poweur group <create|show|add|remove> <group-id>`

Create and administer a **group identity** — a group with its own Poweur ID, usable in any
owner's grants. See [Group identities](/files/group-identities).

```bash
poweur group create crew.acme.poweur.net --member bob.example.org
poweur group show   crew.acme.poweur.net --json
poweur group add    crew.acme.poweur.net --member carol.poweur.net
poweur group remove crew.acme.poweur.net --member bob.example.org
poweur share add /shared/crew-docs --with-group crew.acme.poweur.net --perm read
```

| Flag | Description |
|------|-------------|
| `--member <id>` | Member Poweur ID (repeatable) |
| `--admin <id>` | Admin Poweur ID (repeatable); defaults to the active identity on `create` |
| `--relay <url>` | Relay to register the group on (`create` only) |
| `--use-identity <id>` | Identity performing the operation |
| `--json` | Machine-readable output |

`create` registers the group as a hosted identity, signs its membership document with the
group's own key, and restores the active identity. Membership updates bump the group's
`epoch` only when something actually changed, and the last admin cannot be removed. The
group name must be a full Poweur ID; for an owner-local group use `poweur share group set`.

---

### `poweur sync <pull|push|run|status> <local-dir>`

Synchronise a local directory with your relay-hosted tree over WebDAV plus the changes feed.
See [File sync](/files/sync-protocol).

```bash
poweur sync run ~/poweur --path private --path public
```

| Subcommand | Effect |
|------------|--------|
| `pull` | Fetch remote changes into the local directory |
| `push` | Upload local changes |
| `run` | Continuous two-way sync |
| `status` | Show pending changes without transferring |

| Flag | Description |
|------|-------------|
| `--path <prefix>` | Tree prefix to sync (repeatable; default: `public`, `shared`, `private`, `apps`) |
| `--audience <id>` | Tree owner to sync against — used for trees shared *with* you |
| `--relay <url>` | Relay override |
| `--use-identity <subdomain>` | Identity to authenticate as |

---

### `poweur auth <inspect|sign> <request-file-or-url>`

Inspect or approve a third-party sign-in request. `inspect` shows what is being asked without
signing; `sign` produces the assertion.

```bash
poweur auth inspect ./request.json
poweur auth sign ./request.json --json
```

| Flag | Description |
|------|-------------|
| `--use-identity <subdomain>` | Override identity |
| `--json` | Machine-readable output |

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
