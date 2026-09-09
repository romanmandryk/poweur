---
id: key-management
sidebar_position: 3
title: Key Management & Recovery
---

# Key Management & Recovery

An identity you can permanently lose is not an identity. This page specifies how a Poweur ID's
long-lived keys are derived, stored and recovered.

## The passkey is a lock, not the key

A common misreading is that the passkey *is* the identity. It is not. The identity is an
Ed25519 signing key and an X25519 encryption key; a passkey's PRF output (or a
phone's native keystore) supplies the secret that **wraps** those keys at rest.
A user PIN is a non-goal on the web client — authenticators without PRF are refused.

Three consequences follow, and they explain most of the design below:

- **Nothing binds an identity to a domain.** A client with its own secure storage — a phone's
  Keychain or Keystore — needs no WebAuthn at all.
- **The relay can never recover you.** It holds public keys and ciphertext; it has never seen a
  private key or a wrapping secret. Recovery is always something *you* hold.
- **Losing the wrapped copy loses nothing if you hold the seed**, and losing the seed loses
  nothing if a wrapped copy survives. They are independent paths to the same key material.

### Credential scope (`rp.id`)

A passkey is bound to a WebAuthn *relying party*, and the browser will only mint or assert one
whose `rp.id` is the page's host or a registrable suffix of it. Since EPIC-018 a hosted
credential is scoped to the **registrable domain of the identity's home** — `alice.poweur.net`
gets `rp.id = poweur.net` — so a credential created on the launcher host (`id.poweur.net`)
opens on the identity's own origin with no second enrollment. Self-hosted identities are
unchanged in spirit: `bob.example.org` gets `example.org`, and a public suffix is never
claimed (`bob.co.uk` stays `bob.co.uk`).

The tradeoff, stated so it is a decision rather than an accident: hosted passkeys are scoped
per **domain**, not per identity, so any `*.poweur.net` origin can request an assertion for any
hosted credential. Every one of those origins is the same relay under the same operator, so
this adds no trust boundary that did not already exist, and the WebAuthn user handle keeps
identities distinct in the authenticator's picker. It is not a claim that an identity is bound
to a domain — the first consequence above still holds.

The scope is recorded on the identity record at creation and reused for every later assertion.
Records written before this existed carry no scope and keep being asserted against the host
they were minted on, so **existing passkeys go on working**; a client that "upgraded" them to
the registrable domain would simply stop finding the credential.

## Master seed

Both long-lived keys derive from a single 32-byte master seed, so there is exactly one artifact
to protect.

```
seed           := 32 random bytes
signing key    := HKDF-SHA256(ikm=seed, salt="", info="poweur/v1/sign",  L=32) → Ed25519 seed
encryption key := HKDF-SHA256(ikm=seed, salt="", info="poweur/v1/enc",   L=32) → X25519 scalar
vault key      := HKDF-SHA256(ikm=seed, salt="", info="poweur/v1/vault", L=32)
```

Normative details:

- **Salt is empty**, which RFC 5869 defines as `HashLen` zero bytes. Go passes a nil salt and
  TypeScript omits the argument; both produce the same bytes, and the conformance vectors pin it.
- **Info strings are protocol constants.** They are versioned (`poweur/v1/...`) and must never be
  edited in place — a new scheme gets a new string.
- **The X25519 scalar is stored unclamped.** Both `curve25519.X25519` (Go) and `@noble/curves`
  clamp internally, matching how randomly generated encryption keys are already stored.
- **Wire form** is unpadded base64url, used by `--seed`, keystore entries and recovery kits.

The implementations are [`packages/identity/seed.go`](https://github.com/poweur/poweur/blob/master/packages/identity/seed.go)
(canonical) and `packages/client-ts/src/crypto/seed.ts`, pinned to each other by
`testdata/vectors/seed-derivation.json`. The vectors sign a fixed message with every derived
key, so a key that matches byte-for-byte but cannot actually sign still fails.

### Recovering from a seed

The seed alone reconstitutes the identity — offline, with no relay call:

```bash
poweur key derive  --seed "$(cat alice.seed)"            # check before trusting
poweur key recover alice.poweur.net --seed "$(cat alice.seed)" --relay https://relay.poweur.net
poweur inbox
```

Because recovery is pure key derivation it always *succeeds* locally: a wrong seed yields valid
keys that simply are not this identity's. `poweur key derive` exists so you can compare against
the published document before relying on it.

### Encoding a seed for humans (the recovery kit)

The seed is 32 bytes; base64url is its storage form. A BIP39 24-word mnemonic is an alternative
**encoding of those same bytes** — not a different secret. It is worth using only where a human
transcribes the seed by hand, because it adds a checksum that catches typos and avoids the
`l/I/1` and `O/0` confusions of base64url. It is an encoding choice, not an artifact format: a
printed card, a PDF and a text file are all equally valid carriers.

For CLI and automated use, the base64url seed is the right form — pipe it straight into a
password manager or secrets store.

## The keystore

A seed you must transcribe is a recovery path of last resort. The everyday path is a **keystore**:
one wrapped copy of the seed per enrolled authenticator, held by the relay as opaque ciphertext.

```
enrollment := { enrollment_id, kind, wrap, payload,
                credential_id, credential_public_key, credential_alg,
                wrapped: { iv, ciphertext, salt? },
                label, role, created_at, last_used_at }
```

Wrapping is unchanged from the shipped code: HKDF-SHA256 over the wrapping secret with salt
`poweur-key-wrapping-v1`, then AES-256-GCM. The secret comes from a passkey's PRF output
(`poweur-prf-v1`), a CLI passphrase, or a device's secure storage — `wrap` says which.
Adding an authenticator is re-wrapping the same seed, never re-keying the identity.

### Why writes and reads authenticate differently

| Operation | Authenticated by | Because |
|---|---|---|
| Enroll, remove | identity key | you are unlocked when managing devices |
| **Bootstrap read** | **WebAuthn assertion** | its purpose is recovering an identity whose key you no longer hold |

Requiring the identity key for the read would be circular — that circularity is exactly what the
endpoint exists to break. The gate is instead possession of an enrolled authenticator, which
survives cleared site data.

For the same reason the wrapped blobs live **outside** the DAV tree, in relay-managed storage:
reading `poweur-sys/` needs a DAV token signed by the identity key, and a blob inside the user's
file tree would be one misplaced delete away from destroying their recovery. Only enrollment
*metadata* belongs in the tree.

### What the relay learns, and what it cannot do

Storing credential public keys is new — the passkey was previously client-side only — and the
relay needs them to verify assertions. It gains the ability to *check* a signature, never to
produce one, and never to unwrap a blob.

Three properties the endpoints enforce:

- **Single-use challenges.** Consumed on every attempt, so a captured assertion cannot be replayed.
- **User verification required**, not mere presence: the read releases key material.
- **No credential enumeration.** An unenrolled credential id and a bad signature return identical
  responses. Because the web client already requests discoverable credentials
  (`residentKey: "required"`), the relay never has to reveal credential ids to an unverified caller.

Wire details are in the [Relay API reference](/relay/api-reference).

### Removing an enrollment is not revocation

Deleting a wrapped copy denies that authenticator the bootstrap read. It does **not** help
against an attacker who already extracted the seed — for that, rotate. Removal and rotation
answer different questions, and conflating them is the mistake this section exists to prevent.

## Enrolling a new device

The kit is the path of last resort. The everyday path is a ceremony between two devices, and it
needs an **authentic** channel rather than a secret one:

1. The new device generates an ephemeral X25519 keypair and opens a rendezvous.
2. It shows a six-digit code derived from its own public key.
3. The user types the rendezvous id on a device that already holds the seed, and compares the
   code shown on both screens.
4. That device seals the seed to the ephemeral key; the new device opens it and re-wraps it
   under its own custody.

**Comparing the code is the authentication step.** Skip it and the seed goes to whoever opened
the rendezvous.

### Why there is no PAKE

An earlier draft of this design had the code protect the payload. That makes it a six-digit
password: anyone holding the ciphertext — the relay included — could brute-force it offline,
which is exactly the problem SPAKE2 and friends exist to solve. It would have forced a PAKE into
the trusted path of every client, and no reviewed browser implementation of one exists.

Having the *new* device generate the keypair dissolves the requirement. The code then
authenticates a public key and encrypts nothing, so there is no offline target at all: forging
it means finding a colliding six-digit code on the first and only try, against a single-use
rendezvous. This is the numeric-comparison model used by Bluetooth pairing and Signal safety
numbers.

The lesson generalises: **a short code can authenticate a public key safely, but cannot protect
a secret.** Which of the two you are doing decides whether you need a PAKE.

### What the ceremony guarantees

- The relay sees an ephemeral public key and a sealed blob, and can open neither.
- Only the identity owner can approve: delivery is signed by the identity key, bound to that
  specific rendezvous, so an approval cannot be redirected to another offer.
- A rendezvous is single-use and expires in ten minutes; a claimed one is gone.
- Offers are capped per identity, because the offer endpoint is necessarily unauthenticated and
  the identity name is public.

QR remains a valid transport for the same ceremony — it carries the ephemeral key instead of the
user typing a code — but it is an optimisation, not the baseline. The typed code is what works
when there is no camera, which is the common case for phone-to-laptop.

## Key files at rest (CLI)

The CLI wrote private keys as plaintext base64 with mode `0600`: defensible for a bot on a
hardened host, thin for a laptop. `poweur key protect` wraps them with scrypt (N=32768) +
AES-256-GCM, keyed by a passphrase from `POWEUR_KEY_PASSPHRASE`.

Both formats stay readable — an encrypted file is JSON and begins with `{`, plaintext is bare
base64 — so detection is by shape and migration needs no rename, no flag and no config change.
Existing installs keep working and can opt in when they choose.

The passphrase protects the key *file*, not the identity: losing it is equivalent to losing the
device, and the answer is the same — recover from the seed or another enrolled device.

## Identities created before the seed model

Identities registered before this design have **two independent keys** that are not derived from
any seed, so they cannot produce a recovery kit. They are not stranded and are not forced to
change:

- Every flow except the recovery kit works unchanged.
- `poweur key rotate` can move an identity onto a seed, publishing a rotation statement so
  contacts follow the new key instead of warning about it.

`poweur identity create` without `--seed` or `--from-seed` still generates two independent
random keys, so nothing changes for existing scripts.

## Related

- [Security Model](/security/model) — overall threat model
- [Web identity](/protocol/web-identity) — identity documents and key rotation
- [CLI Reference](/clients/cli-reference) — `identity create`, `key derive`, `key recover`,
  `key rotate`, `key kit`, `key ls`, `key enroll`/`approve`/`claim`, `key protect`
- [Relay API reference](/relay/api-reference) — keystore and enrollment endpoints
