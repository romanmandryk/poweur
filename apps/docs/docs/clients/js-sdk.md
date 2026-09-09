---
id: js-sdk
sidebar_position: 3
title: JavaScript / TypeScript SDK
---

# `@poweur/client`

The Poweur protocol as a typed, runtime-agnostic package: identity, messaging,
files, sync, shares, contacts, policy and proof-of-work. It runs in the
browser, in Node ≥18, and in Bun and Deno, and it ships a `poweur` CLI that is
command-for-command with the Go one.

```bash
npm install @poweur/client
```

**The Go implementation stays canonical.** `packages/identity` and the relay
define the protocol; this package conforms to them through shared conformance
vectors, so the two implementations cannot silently diverge (see
[Conformance](#conformance)).

## The `Signer` model

Nothing above the crypto layer ever sees a private key. Callers hand the SDK a
`Signer`, and every signing path goes through it:

```ts
interface Signer {
  readonly identity: string;    // "alice.poweur.net"
  readonly publicKey: string;   // "ed25519:…"
  sign(canonical: string, encoding?: "base64url" | "base64std"): Promise<string>;
}
```

That single seam is what lets a passkey-gated browser key, an OS keychain and
an unattended agent's key file drive identical code. A `KeyStore` is the
matching abstraction for *where* keys live:

| Runtime | `KeyStore` | Notes |
|---------|-----------|-------|
| Node / Bun / Deno | `FileKeyStore` | `~/.poweur/keys` — the Go CLI's own directory |
| Browser | your own, over a passkey PRF | `LocalStorageKeyStore` exists for demos only |
| Tests, ephemeral agents | `MemoryKeyStore` | nothing touches disk |

`LocalSigner` and `LocalDecryptor` wrap raw key bytes when you already hold
them; implement `Signer` yourself when the key lives somewhere you cannot read.

## Quickstart: a messageable agent

```ts
import { PoweurClient, createIdentity, IdentityApi, RelayClient, signerFor } from "@poweur/client";
import { FileKeyStore, FileSessionStore, nodeResolveOptions } from "@poweur/client/node";

const relayUrl = "https://poweur.net";
const store = new FileKeyStore();

// Create the identity once; afterwards `store.load(...)` is enough.
const created = await createIdentity(new IdentityApi(new RelayClient(relayUrl)),
  "myagent.poweur.net", { hosted: true });
await store.save(created.keys);

const { signer, decryptor } = signerFor(created.keys);
const client = new PoweurClient({
  relayUrl, signer, decryptor,
  sessionStore: new FileSessionStore(),
  resolve: nodeResolveOptions(),
});

// Poll, reply, and acknowledge what we actually read.
for (;;) {
  const { messages } = await client.inboxAndAck();
  for (const message of messages) {
    if (message.plaintext) await client.send(message.sender, `echo: ${message.plaintext}`);
  }
  await new Promise((r) => setTimeout(r, 5000));
}
```

`inboxAndAck()` emits the tick-2 receipt only for messages that actually
decrypted — proof the message reached a client, not merely a relay. Use
`inbox()` when you want to decide that yourself.

## Modules

Every area is a subpath import, so a bot that only sends messages does not pull
in WebDAV.

| Import | Covers |
|--------|--------|
| `@poweur/client` | everything, plus the `PoweurClient` facade |
| `@poweur/client/crypto` | Ed25519, X25519 + HKDF + ChaCha20-Poly1305, `Signer`/`KeyStore` |
| `@poweur/client/canonical` | every canonical signing string |
| `@poweur/client/resolve` | web-first resolution with the SSRF guards |
| `@poweur/client/identity` | register, publish/rotate encryption keys, export, rotate |
| `@poweur/client/messages` | send (session or identity), inbox, acks, anonymous send |
| `@poweur/client/files` | WebDAV: token minting, list/read/write/mkdir/move/delete, quota |
| `@poweur/client/sync` | changes feed, manifest, resumable chunked upload |
| `@poweur/client/shares` | signed grants and groups |
| `@poweur/client/contacts` | contacts.json, key pinning, request queue |
| `@poweur/client/policy` | inbox-policy.json |
| `@poweur/client/pow` | proof-of-work solve and check |
| `@poweur/client/node` | `~/.poweur` stores, DNS, the local sync engine |
| `@poweur/client/browser` | DNS-over-HTTPS resolver, `localStorage` key store |

## Sending

```ts
// Default: signed with a short-lived session key, sent straight to the
// recipient's relay. Your home relay sees no outbound traffic at all.
await client.send("alice.poweur.net", "hello");

// Headless agents with no session cache can sign with the identity key —
// the `--sign-with=identity` equivalent.
await client.send("alice.poweur.net", "hello", { signWith: "identity" });

// Route through your own relay instead: hides your IP from the recipient's
// relay, at the cost of showing your own relay every message you send.
await client.send("alice.poweur.net", "hello", { viaHomeRelay: true });
```

Messages are always end-to-end encrypted. An identity with no published
encryption key is a hard failure, never a plaintext downgrade.

### Anonymous send

No identity required — the recipient must have opted in, and may demand
proof-of-work:

```ts
import { sendAnonymous } from "@poweur/client";

const cancel = new AbortController();

await sendAnonymous("alice.poweur.net", "hello stranger", {
  onChallenge: ({ bits }) => console.log(`solving ${bits} bits…`),
  onSolveProgress: (attempts) => console.log(`${attempts} hashes…`),
  signal: cancel.signal,
});
```

The solver yields to the event loop between chunks, so a browser tab stays
responsive. Expect roughly 2^bits hashes: JavaScript runs 5–10× slower than the
Go solver, so warn users above ~20 bits. At those difficulties a UI needs both
of the last two options — `onSolveProgress` to show that work is happening, and
`signal` so the user can stop paying.

## Browser vs Node

The core touches no DOM API and no Node builtin — `fetch` and `crypto` come
from `globalThis`. Three things differ by runtime:

- **DNS.** Browsers cannot do DNS, so the browser build resolves TXT records
  over DNS-over-HTTPS (`dohTxtResolver`). That is a real trust difference: the
  DoH provider sees which identities you look up and answers instead of your
  system resolver. The web path (`/.well-known/poweur/id.json`) needs none of
  this and is always tried first.
- **Local sync.** `poweur sync`'s reconciliation engine needs a filesystem and
  is Node-only. Browsers get the remote half — changes feed, manifest, chunked
  upload — via `SyncClient`.
- **Key custody.** `FileKeyStore` is Node-only. In a browser, wrap keys with a
  passkey PRF and implement `KeyStore` over that. A PIN is a non-goal.

## CLI

```bash
npx @poweur/client identity create myagent.poweur.net --hosted --relay https://poweur.net
npx @poweur/client send alice.poweur.net "hello"
npx @poweur/client inbox
npx @poweur/client share add shared/project-x --with alice.poweur.net --perm rw
```

It reads and writes the **same `~/.poweur` tree as the Go CLI**:

```
~/.poweur/config.toml           active identity, relay, keys dir
~/.poweur/keys/<id>.key         Ed25519 private key
~/.poweur/keys/<id>.enc         X25519 private key
~/.poweur/sessions/<id>.toml    short-lived session record
~/.poweur/pending/<id>.jsonl    delivery journal (the ✓ / ✓✓ ticks)
```

Either CLI can create an identity the other then uses; sessions registered by
one are honoured by the other. `POWEUR_HOME` relocates the tree (tests use it).
The full command list is in the [CLI reference](/clients/cli-reference) — the
two are the same surface.

## Errors

Every failure is a `PoweurError` with a `code` you can branch on, rather than a
message you have to match:

`invalid_argument`, `invalid_signature`, `invalid_document`, `key_mismatch`,
`not_found`, `resolve_failed`, `relay_error`, `session_expired`,
`challenge_required`, `decrypt_failed`, `policy_rejected`, `unsupported`.

`RelayError` adds `status` and the relay's own `error` code;
`ChallengeRequiredError` carries the challenge envelope from a 428.

## Fail-closed behaviour

The resolver refuses rather than guesses, in every one of these cases:

- web and DNS publish different signing keys → `key_mismatch`
- the document's signature does not verify, or names a different identity
- the well-known fetch redirects, exceeds 16KB, or targets a private/loopback
  address (unless `allowPrivate` is set, which is for tests and dev only)
- a pinned contact's key changed with no rotation statement covering it → the
  CLI refuses to send until you pass `--accept-new-key`

## Conformance

`packages/identity/testdata/vectors/` holds fixtures generated by Go — canonical
strings and signatures, identity documents, share grants and groups, PoW
solutions, name-policy cases, `poweur-sys` documents, and sealed payloads. The
TypeScript suite re-derives each one and compares.

Regenerate them whenever a protocol change lands in Go:

```bash
pnpm vectors
```

CI regenerates and then runs the TypeScript suite, so a canonical string
changed in Go without a matching TypeScript change turns the build red.

## Versioning

Semver on the package, plus an exported `PROTOCOL_VERSION`. The
[EPIC-009](https://github.com/poweur/poweur) reserved envelope fields (`type`,
`thread_id`, `expires_at`, `metadata`) are already typed, so adopting them is a
minor version.
