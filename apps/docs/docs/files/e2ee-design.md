---
id: e2ee-design
sidebar_position: 3
title: E2EE storage design
---

# Relay-blind (E2EE) storage — design study

**Status: design only (E03-T7). Nothing here is implemented; this document exists so v1
storage decisions don't preclude an encrypted mode later.**

The v1 storage model ([storage-model.md](./storage-model.md)) is **relay-visible**: the
`relay-fs` provider reads and writes plaintext bytes, which is what makes relay-mediated
features (WebDAV to third parties, `/pub` web serving, share enforcement, content-hash ETags)
straightforward. This study answers: *what would it take for the relay to store bytes it
cannot read, and which parts of the tree can ever work that way?*

## 1. Survey of prior art

### Cryptomator (per-file envelope encryption over dumb storage)

- **Model:** a client-side virtual filesystem. Each file is encrypted independently:
  a random file key wrapped by a masterkey (scrypt-derived from a passphrase), content
  encrypted in 32 KiB AES-GCM chunks with a per-file header. File and directory **names are
  also encrypted** (AES-SIV), and the real directory hierarchy is flattened into
  hash-addressed shards (`d/AB/CDEF.../`), so the storage server learns almost nothing about
  structure.
- **Lessons for Poweur:**
  - Works over *any* dumb backend (WebDAV included) — the server needs zero changes. That
    property is exactly what keeps our `StorageProvider` interface untouched.
  - Chunked AEAD means seekable reads and resumable writes without decrypting whole files.
  - Name encryption breaks server-side listings, sync UX, and per-path ACLs — everything the
    relay does for `/public` and `/shared`. Full Cryptomator-style opacity is therefore only
    viable for owner-only roots.

### Nextcloud E2EE folders (lessons, mostly negative)

- **Model:** opt-in per-folder E2EE; a folder metadata file (encrypted) maps obfuscated blob
  names to real names; per-user key pairs are certified by the server.
- **Lessons:**
  - Early versions had serious breaks (server could inject public keys, metadata rollback,
    key-recovery flaws — see the 2020 audits). Root cause: the **server distributed and
    certified keys** it also stored data for. Poweur avoids this class by construction:
    identity keys are already distributed and verified out-of-band via DNS/well-known
    (EPIC-001), never minted by the relay.
  - Mixing E2EE and server-mediated features on the *same folder* (previews, search, sharing
    UI) caused most of the complexity. Conclusion: make relay-blind a **per-root/per-folder
    mode with reduced features**, not a transparent overlay.
  - Sync clients need stable, server-visible *names* for delta detection even when content is
    opaque — our `change_id` journal already gives us that without readable names.

### age / streaming AEAD formats

- **age** (X25519 + ChaCha20-Poly1305, STREAM construction over 64 KiB chunks) is the closest
  fit for our primitives: Poweur identities already hold X25519 encryption keys, and messaging
  already uses `x25519-chacha20-poly1305`. Encrypting a file "to" a set of recipients is the
  same operation as encrypting a message payload.
- STREAM-style chunking (age, Tink, Miscreant) provides: seekable decryption, truncation
  detection, and streaming without buffering whole files — required for large blobs and for
  a future sync protocol to resume mid-file.
- **Recommendation:** adopt an age-compatible envelope (or age itself) rather than inventing
  a format: recipient stanzas map 1:1 onto Poweur identity/device enc keys.

### Key distribution via Poweur messaging

Sharing an encrypted folder means delivering the folder key to each recipient. Poweur already
has an authenticated, encrypted channel between any two identities (relay messaging,
EPIC-002). Design intent:

- Folder key wrapped to each recipient's X25519 key, delivered as a structured message
  (`kind: key-grant`) — reusing exactly the message envelope, signatures, and resolver chain.
- Revocation = re-encrypt folder key (lazy re-encryption on next write) + stop sending
  updates; the storage-side grant (EPIC-005) is removed at the same time. Accept that a
  revoked recipient keeps what they already downloaded — same as every E2EE system.
- Device enrollment (EPIC-011) extends this: an identity's own devices receive folder keys
  through the same grant mechanism.

## 2. Which roots can ever be relay-blind

| Root | Relay-blind possible? | Why |
|------|----------------------|-----|
| `poweur-sys/public` | **Never** | Backs `/.well-known/poweur/` and anonymous discovery; must be plaintext. |
| `poweur-sys/relay` | **Never** | The relay *executes* this config (inbox policy, shares, app-password hashes, device registry). Blind config is unenforceable config. |
| `poweur-sys/private` | **Already blind by contract** | v1 rule: relay stores but must not read; sensitive contents (e.g. `storage-credentials.json`) are client-encrypted today. This is E2EE v0 in practice. |
| `/private` | **Yes — first candidate** | Owner-only; no relay-mediated feature needs the plaintext. Names could optionally be encrypted too (Cryptomator-style) since nobody but the owner lists it. |
| `/apps/<app-id>/` | **Yes** (per app) | Same audience as `/private` by default. Apps opt in per their own data sensitivity. |
| `/shared` | **Partially** | Content can be blind (recipients get keys via key-grants); but the relay still enforces *who may fetch which blobs* from `poweur-sys/relay` grants, and names should stay visible for share UX. Loses server-side search/preview. |
| `/public` | **Not usefully** | The audience is "any valid Poweur ID" — a key everyone can get protects nothing; `/pub` web serving needs plaintext. |

Consequence: **relay-blind is an opt-in property of `/private`, `/apps`, and (later, with
visible names) `/shared` — never of `poweur-sys/public`, `poweur-sys/relay`, or `/public`.**
The v1 trust split in the tree already anticipates this boundary.

## 3. v1 hooks that must not be broken

These are the concrete constraints on v1 code (all already satisfied — listed so future
changes don't regress them):

1. **ETags are content hashes of stored bytes, not of plaintext.** The index
   (`apps/api/internal/files`) hashes whatever the provider writes. Encrypted blobs get
   valid, stable ETags for free; sync (`change_id`) works unchanged. Do not add
   plaintext-derived metadata (e.g. server-side text extraction) to the index contract.
2. **Opaque names must stay legal.** Path rules (storage-model.md) allow base64url-safe
   name characters and impose only length/depth caps — an encrypted-name scheme fits within
   existing segment rules. Do not add server-side name semantics (extension-based behavior)
   outside `/public` + `/pub`.
3. **`StorageProvider` is byte-oriented.** No provider method assumes it can interpret
   content. Keep it that way; an E2EE layer is purely client-side and the provider never
   learns about it.
4. **Locks and quota act on ciphertext.** Quota counts stored bytes (ciphertext is ~same
   size + small header); WebDAV locks are path-based. No change needed.
5. **Marker/reserved names:** `.poweur-*` prefix stays reserved so a future
   `.poweur-e2ee` folder marker (mode flag + wrapped-key pointer) cannot collide with user
   data.

## 4. Recommended phased path

- **Phase 0 (shipped, v1):** `poweur-sys/private` contract — relay stores, must not read;
  clients encrypt sensitive items (storage credentials) to the identity enc key. No format
  standardization beyond "encrypted JSON blob".
- **Phase 1 — owner-blind `/private`:** client-side age-style envelope (X25519 recipients =
  identity + enrolled devices), per-folder opt-in via `.poweur-e2ee` marker; names visible,
  content opaque. CLI/web encrypt/decrypt transparently; DAV third-party mounting of such
  folders shows ciphertext (documented limitation). Requires EPIC-011 device keys.
- **Phase 2 — blind `/shared`:** key-grants over Poweur messaging (`kind: key-grant`),
  grant-holder reads via normal relay-mediated DAV but decrypts locally; revocation =
  grant removal + lazy folder-key rotation. Requires EPIC-005 grants to be stable.
- **Phase 3 (optional) — name encryption for `/private`:** Cryptomator-style encrypted
  names/flattened dirs for owner-only roots, only if threat model demands hiding structure
  from the relay operator. Costs sync/browse ergonomics; decide with real user demand.

Non-goals at every phase: blind `poweur-sys/relay` (unenforceable), blind `/public`
(pointless), server-side search over encrypted content (accept the loss or do client-side
indexing).
