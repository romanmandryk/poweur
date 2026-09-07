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
Ed25519 signing key and an X25519 encryption key; a passkey (or a PIN) only supplies the secret
that **wraps** those keys at rest.

Three consequences follow, and they explain most of the design below:

- **Nothing binds an identity to a domain.** A client with its own secure storage — a phone's
  Keychain or Keystore — needs no WebAuthn at all.
- **The relay can never recover you.** It holds public keys and ciphertext; it has never seen a
  private key or a wrapping secret. Recovery is always something *you* hold.
- **Losing the wrapped copy loses nothing if you hold the seed**, and losing the seed loses
  nothing if a wrapped copy survives. They are independent paths to the same key material.

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

### Encoding a seed for humans

The seed is 32 bytes; base64url is its storage form. A BIP39 24-word mnemonic is an alternative
**encoding of those same bytes** — not a different secret. It is worth using only where a human
transcribes the seed by hand, because it adds a checksum that catches typos and avoids the
`l/I/1` and `O/0` confusions of base64url. It is an encoding choice, not an artifact format: a
printed card, a PDF and a text file are all equally valid carriers.

For CLI and automated use, the base64url seed is the right form — pipe it straight into a
password manager or secrets store.

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
- [CLI Reference](/clients/cli-reference) — `identity create`, `key derive`, `key recover`, `key rotate`
