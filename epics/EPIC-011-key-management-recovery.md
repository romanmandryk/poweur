# EPIC-011 — Key management, multi-passkey enrollment & recovery

- **Status:** proposed
- **Priority:** P0 (a primary identity that can be permanently lost is not a primary identity)
- **Depends on:** EPIC-001 (rotation statements, E01-T5), EPIC-002 (durable storage); interacts with EPIC-004 (device registry), EPIC-007 (contacts — for social recovery), EPIC-008 (Poweur as recovery anchor for other services), [EPIC-018](EPIC-018-identity-onboarding-naming.md) (credential scope / rpId, E18-T4)
- **Unlocks (also):** [EPIC-019](EPIC-019-mobile-app-capacitor.md) E19-T8 — the mobile shell's "bring an existing identity over" reuses E11-T3's ceremony rather than inventing its own
- **Unlocks:** trustworthy "Poweur ID as your primary identity" positioning

> **Inbound from EPIC-001:** E01-T5 is **done** — rotation uses `previous_keys` +
> `identity-rotation` canonical string (`POST /identities/{id}/rotate`, `poweur key rotate`).
> Wire E11-T2 seed rotation to that same format — do not invent a second protocol.

> **Inbound from [EPIC-019](EPIC-019-mobile-app-capacitor.md) (E19-T8):** the X25519
> **encryption key must be copied to every device** — it cannot be per-device. Senders encrypt
> to the identity's `encryption_public_key` (`resolveRecipientEncKey`,
> `packages/client-ts/src/messages.ts`), and `SessionCreateRequest` delegates **signing only** — it
> carries no encryption key. So per-device *signing* keys are achievable here, but per-device
> *encryption* keys would require senders to encrypt N times: a protocol change, out of scope
> for this epic. **This is why the seed model is the right shape** — one seed, N wrappings,
> copied whole to each device (E11-T3) rather than split per device. Record the constraint in
> E11-T1's spec so it is not rediscovered.

## Progress

> **Inbound from [EPIC-015](EPIC-015-web-app-ux.md) (E15-T1):** the web app's
> **"Keys & devices"** and **"Recovery kit"** surfaces exist now, backed by
> `apps/web/js/keystore-mock.js`. This browser's own enrollment is real (kind, wrap and
> creation time read from the identity record); every other row is flagged `mock` in the UI
> and every write throws `KeystoreUnavailable` naming E11-T1 rather than faking success.
> The mock's shapes are the ones specified below, so landing T1 means replacing function
> bodies with the relay calls — `listEnrollments`, `enrollAuthenticator`,
> `removeEnrollment`, `generateRecoveryKit`, `keystoreAvailable` — and deleting the file.
> `apps/web/test/keystore-mock.test.js` pins the contract in the meantime.
>
> Note also that identities created by the web app are `legacy-keypair`: it generates two
> independent WebCrypto keys, so the migration path below is the *normal* case for web
> users, not an edge case. `apps/web/js/vault.js` already converts both directions between
> WebCrypto JWKs and the SDK's raw key bytes, so `identityKeysFromSeed` plugs straight in.

| Task | Status | Ships in | Notes |
|------|--------|----------|-------|
| E11-T1 Seed, multi-enrollment, recovery kit | **partial** | **v1** | seed derivation + CLI/SDK recovery **done**; relay endpoints, multi-passkey, kit, inventory open |
| E11-T2 Recovery-master role & elevated ops | open | v1 | policy enforcement + kill-lost-device |
| E11-T3 Enrollment ceremony (code+PAKE / QR) | open | v1 | synced-passkey shortcut deferrable |
| E11-T4 CLI/bot key storage hardening | open | v1 | independent of T1–T3, can run in parallel |
| E11-T5 Social recovery | open | later | design doc gates implementation |
| E11-T6 Poweur ID as recovery anchor | open | later | needs EPIC-008 |
| E11-T7 Security review & drills | open | v1 (review) | review **before** T3 ships, drills ongoing |

**Implementation order:** T1 → (T2 ‖ T3) → T7 review → T4 any time. T5/T6 are separate
phases and must not block v1. Within T1, build in this order: seed derivation and schemas →
relay keystore endpoints → multi-passkey enrollment in the web client → recovery kit →
inventory UI.

## Goal

Make losing a device a non-event and losing *all* devices a recoverable event — without email,
phone numbers, or commercial third parties. Concretely: multiple passkeys/devices per identity,
a designated **recovery-master authenticator** (e.g. YubiKey/Titan) with elevated rights to
remove other enrollments, an offline recovery kit, and — in later phases — **social recovery**
through trusted contacts who can help restore access but cannot collude to steal the identity.
Finally, flip the dependency: Poweur ID becomes the thing that *recovers your other accounts*,
instead of an email address being the thing that recovers your Poweur ID.

## Background: full key inventory (current code)

| Key / credential | Type | Where it lives today | Protection | Lifetime |
|---|---|---|---|---|
| Identity signing key | Ed25519 | CLI: `keys_dir/<id>.key` (`apps/cli/internal/identity/keys.go`); Web: JWK in localStorage, signed inside WebCrypto (`apps/web/js/vault.js`) | CLI: **plaintext base64, file mode 0600**; Web: AES-wrapped | permanent |
| Encryption key | X25519 | CLI: `keys_dir/<id>.enc`; Web: JWK in localStorage | same as above | permanent |
| Passkey (WebAuthn) | platform/roaming authenticator | browser/OS keystore | biometric/UV | n/a — **wraps** the two keys above via the PRF extension (`apps/web/js/passkey.js`); **exactly one credential supported**; PIN/PBKDF2 fallback when PRF unavailable |
| Session keys | Ed25519 | client memory/disk + relay session store | identity-signed `SessionProof`, ≤ 24 h TTL (`apps/api/internal/relay/sessions.go`) | hours |
| Relay challenges | nonce | relay memory | single-use, short expiry | minutes |
| (planned) DAV tokens / app passwords | bearer / argon2id hash | relay + `poweur-sys/relay/` | scoped, revocable (E03-T3) | hours–long |
| (planned) agent tokens | bearer | relay | path/scope-bound (E08-T4, E10) | configurable |

Key observations driving the design:

1. **The passkey is a wrapper, not the identity.** This is actually the right architecture —
   it means multi-passkey support is "N wrapped copies of the same key material", not a
   protocol change. But today N=1, the wrapped blob lives only in one browser's localStorage,
   and clearing site data destroys the identity.
2. **Everything already chains to one root.** Sessions, tokens and (per EPIC-001) rotation
   statements are all signed by the identity key. Recovery therefore has exactly one job:
   *never lose the ability to produce one Ed25519 signature* — everything else re-derives.
3. **The CLI stores root keys in plaintext.** Acceptable for bots on hardened hosts, not for
   humans; needs at-rest encryption regardless of recovery work.
4. **The synced keystore is circular unless it has its own bootstrap read.** Reading anything
   under `poweur-sys/` means minting a DAV token, which means signing the canonical
   `dav-token` string **with the identity key** (`packages/client-ts/src/files.ts`). The keystore exists
   to *recover* that key. So a synced keystore is a fine **backup and multi-device sync**
   mechanism for a client that is already unlocked, but it is **not** by itself an answer to
   "I cleared site data" — that needs a read path authenticated by something other than the
   identity key. See E11-T1 below; do not describe the synced copy as cleared-site-data
   recovery without it.

**Status of `poweur-sys/private/keystore/`:** design-only — it appears in this epic and nowhere
else (not in `apps/api`, not in `apps/docs`, not in the EPIC-006 path registry). Reviewed and
**split**, because one path was carrying two things with incompatible access models:

| What | Where it goes | Auth to read | Why |
|---|---|---|---|
| **Wrapped seed copies** (ciphertext, one per enrollment) | relay-managed store behind `/identities/{id}/keystore` — **not** a DAV path | **WebAuthn assertion** | must be readable *before* you hold the identity key, or it cannot answer "I cleared site data"; must not be deletable from the E15-T4 file explorer |
| **Enrollment policy / metadata** (ids, kinds, roles, labels, timestamps — no ciphertext) | `poweur-sys/private/keystore/policy.json` | DAV token (identity key) | no circularity — you are already unlocked when managing devices; syncs via EPIC-004 for free |

Keeping the wrapped seeds in the DAV tree was rejected on three counts: it needs a read path
the identity key cannot provide, E15-T4 gives users a delete button over that tree, and two
auth models on one path is precisely the confusion that prompted this review.

**Already satisfied by shipped code:** `apps/web/js/passkey.js` sets `residentKey: "required"`,
so the discoverable-credential requirement of the bootstrap read needs no client change.

## Design direction

**One master seed, many wrappings, explicit roles.**

- Derive both long-lived keys from a single 32-byte master seed via HKDF with distinct info
  strings (`poweur/v1/sign`, `poweur/v1/enc`). One seed = one recovery artifact. (Migration:
  existing identities keep their independent keys; the seed model applies at next rotation via
  E01-T5 — write the migration path explicitly.)
- A **keystore** of wrapped seed copies, one per enrolled authenticator/device, held by the
  relay as opaque ciphertext (see the split table above) and addressed by the endpoints in
  E11-T1. The relay only ever sees ciphertext — wrapping keys never leave authenticators.
- A signed **enrollment policy** (`poweur-sys/private/keystore/policy.json`, metadata only)
  records each enrollment's role: `device` (default) or `recovery-master`. Clients enforce:
  `device` enrollments may add devices but not remove others; `recovery-master` (your
  YubiKey/Titan) may **remove any enrollment, rotate the seed, and revoke all sessions** — the
  "I lost my phone, kill it" path. Hardware-backed roaming authenticators are recommended (and
  flagged in UI) for this role because they're offline, phishing-resistant and PRF-capable.

### Concrete formats (normative — implement these, don't re-derive them)

**Seed derivation.** 32 random bytes, HKDF-SHA256, empty salt, distinct info strings:

```
seed          := 32 random bytes
signing key   := HKDF-SHA256(ikm=seed, salt="", info="poweur/v1/sign",  L=32) → Ed25519 seed
encryption key:= HKDF-SHA256(ikm=seed, salt="", info="poweur/v1/enc",   L=32) → X25519 scalar, clamped per RFC 7748
vault key     := HKDF-SHA256(ikm=seed, salt="", info="poweur/v1/vault", L=32)   (E11-T6)
```

**Wrapping is unchanged from shipped code** — `wrapKeysAES`/`unwrapKeysAES`
(now `apps/web/js/vault.js`, unchanged in format): HKDF-SHA256 over the wrapping secret with salt
`poweur-key-wrapping-v1`, then AES-256-GCM. The PRF salt stays `poweur-prf-v1`; each
authenticator yields a different PRF output for the same salt, which is exactly what
multi-enrollment needs — **no change to the salt for multi-passkey support.**

**Keystore entry** (relay-held, ciphertext):

```json
{
  "version": 1,
  "enrollment_id": "<base64url, 16 random bytes>",
  "kind": "passkey | hardware-key | cli-passphrase | recovery-kit | native",
  "wrap": "prf | pin | passphrase | native",
  "payload": "seed | legacy-keypair",
  "credential_id": "<base64url>",
  "credential_public_key": "<COSE key, base64url>",
  "wrapped": { "iv": "<b64url>", "ciphertext": "<b64url>", "salt": "<b64url, pin/passphrase only>" },
  "label": "MacBook Pro",
  "created_at": "<RFC3339>",
  "last_used_at": "<RFC3339>"
}
```

> **New relay requirement:** `credential_public_key` must be stored at enrollment. The relay
> does not persist WebAuthn credentials today (the passkey is purely client-side), but it
> cannot verify a bootstrap assertion without them. This is the one genuinely new thing the
> relay learns; note it in the privacy section of the spec.

**`policy.json`** (DAV tree, metadata only, signed by the identity key over canonical JSON —
reuse the EPIC-001 canonicalization, do not invent a second one):

```json
{
  "version": 1,
  "identity": "alice.poweur.net",
  "enrollments": [
    { "enrollment_id": "…", "kind": "passkey", "role": "device",
      "label": "MacBook Pro", "created_at": "…" }
  ],
  "updated_at": "<RFC3339>",
  "signature": "<base64url ed25519>"
}
```

**Endpoints** (following the shipped `/identities/{identity}/rotate` and `/auth/challenge`
patterns):

| Method & path | Auth | Purpose |
|---|---|---|
| `PUT /identities/{identity}/keystore` | identity-key signature (client is unlocked) | add/replace an enrollment's wrapped copy + credential public key |
| `POST /identities/{identity}/keystore/fetch` | **WebAuthn assertion** over a `GET /auth/challenge` nonce | bootstrap read — returns ciphertext entries only |
| `DELETE /identities/{identity}/keystore/{enrollment_id}` | identity-key signature, `recovery-master` role enforced client-side and re-checked against `policy.json` | enrollment removal |

Writes are identity-key-authenticated because you are unlocked when enrolling; reads are
assertion-authenticated because bootstrap is the whole point. `fetch` must not reveal
credential IDs to an unverified caller (discoverable credentials — already satisfied client-side).

- **Recovery kit** = the seed, encoded for whoever has to carry it. BIP39 (24 words) is an
  *encoding of the same 32 bytes*, not a second secret; it earns its keep only where a human
  transcribes by hand, because it adds a typo-catching checksum and avoids base64url's `l/I/1`
  and `O/0` confusions. **The carrier is not part of the spec** — a printed card, a PDF, a text
  file and a password-manager entry are equally valid, and the CLI simply prints the base64url
  seed (`--from-seed`). Do not build a PDF generator as though it were the deliverable. The kit
  is itself just another "enrollment" (kind `recovery-kit`) recorded in the policy so the UI can
  nag if none exists.
- **Social recovery (phase 2)** = Shamir shares (SLIP-0039) of the seed, each encrypted to a
  recovery contact's X25519 key and stored in *their* home; reassembly requires a public,
  time-delayed ceremony that every enrolled device and contact is notified of and the owner
  can veto — collusion among contacts is made *detectable and slow* rather than silently
  possible (honest threat model below).

## Tasks

### E11-T1 — Key management v1: seed, multi-enrollment, recovery kit (the "good enough" core)

The starting point — comprehensive enough to be trustworthy, simple enough to ship as one
coherent unit. All formats are fixed above; this task is implementation, not design.

**Build order:** schemas + seed derivation → relay endpoints → multi-passkey web client →
recovery kit → inventory UI.

- [x] Spec [`apps/docs/docs/security/key-management.md`](../apps/docs/docs/security/key-management.md):
      seed derivation (normative), the passkey-is-a-lock framing, seed recovery, kit encoding,
      and the legacy-identity path. Also brought
      [`clients/cli-reference.md`](../apps/docs/docs/clients/cli-reference.md) up to date — it
      documented 18 of 33 commands; `key`, `contacts`, `requests`, `policy`, `anon`, `share`,
      `sync` and `auth` were entirely missing
- [ ] Extend that spec with the keystore entry, `policy.json`, the three endpoints, and the
      privacy note that the relay now stores WebAuthn credential public keys. Register
      `poweur-sys/private/keystore/policy.json` in the EPIC-006 path registry
- [ ] Record the **encryption-key-is-shared** constraint from the E19-T8 inbound note above:
      per-device *signing* keys are in scope, per-device *encryption* keys are not, and the
      reason (senders encrypt to one `encryption_public_key`) belongs in the spec so the
      question is settled once
- [x] Seed derivation — canonical Go in [`packages/identity/seed.go`](../packages/identity/seed.go)
      (`DeriveSigningKey` / `DeriveEncryptionKey` / `DeriveVaultKey`, `ParseSeed`/`EncodeSeed`),
      conforming TS in [`packages/client-ts/src/crypto/seed.ts`](../packages/client-ts/src/crypto/seed.ts),
      pinned by `testdata/vectors/seed-derivation.json` + `test/seed.test.ts` (22 tests). Vectors
      sign a fixed message so derivation is checked end-to-end, not just byte equality
- [x] `identityKeysFromSeed()` in the SDK and CLI seed support: `identity create --seed/--from-seed`,
      `key recover <id> --seed`, `key derive --seed`. Live-relay coverage in
      `packages/client-ts/test/seed-relay.test.ts` (6) and `apps/integration/seed_test.go`
      (`TestINT_SEED_01`–`04`), including the full recovery drill and the wrong-seed negative
- [ ] Relay: `PUT /identities/{id}/keystore`, `POST …/keystore/fetch`,
      `DELETE …/keystore/{enrollment_id}` with the auth model in the table above; persist
      `credential_public_key`; verify assertions against it; rate-limit `fetch` per identity
- [ ] Web client: enroll **multiple passkeys** per identity — re-wrap the seed under each
      authenticator's PRF output. Fix the single-`credentialId` assumption in
      `apps/web/js/passkey.js` and the `encryptedKeys`/`credentialId` shape in
      `apps/web/js/storage.js` (today's record holds exactly one of each); localStorage becomes
      a cache of the enrollment that unlocked this browser
- [ ] Recovery kit: BIP39 encode/decode of the seed (`@scure/bip39` in TS, a reviewed Go
      wordlist package) exposed as an **encoding option**, not a document format — the CLI keeps
      printing base64url, the web app renders words for hand-copying. Plus a "verify your kit"
      re-entry check. The seed plumbing is done (`--from-seed` emits the raw seed); this is the
      human-readable encoding on top, and adds a wordlist dependency on both sides Restore flow: mnemonic → seed → keys → new enrollment
      registered, sessions optionally revoked
- [ ] Key inventory UI ("Keys & devices" in the web app + `poweur keys ls`): every enrollment,
      kind, role, label, created/last-used, with policy-gated remove buttons — merges the
      session list and (later) the E04-T6 device registry view
- [ ] Enrollment removal: delete the wrapped copy and the credential public key, revoke that
      enrollment's sessions/tokens, append a signed `policy.json` update. **Document that
      removal without rotation does not protect against an attacker who already extracted the
      seed** — that is what E11-T2 rotation is for

#### Migration for existing identities (must not strand anyone)

Identities registered today have two **independent** keys that are not seed-derived, so they
cannot produce a 24-word kit. Do not force a rotation to fix this.

- [ ] Support `payload: "legacy-keypair"` keystore entries — the wrapped blob holds both JWKs
      instead of a seed. Every flow except the recovery kit works unchanged for these:
      multi-enrollment, bootstrap fetch, removal, inventory
- [ ] Offer (never force) **rotate-to-seed**: generate a seed, derive new keys, publish via the
      **shipped** E01-T5 rotation (`POST /identities/{id}/rotate`, `previous_keys` +
      `identity-rotation` canonical string) and `POST /identities/{id}/encryption-key` — do not
      invent a second rotation protocol
- [ ] The recovery kit is offered only for seed-based identities; legacy identities see a
      "rotate to enable a recovery kit" prompt explaining the tradeoff (contacts re-pin keys,
      EPIC-007 T4)
- [ ] Integration coverage in `apps/integration/`: a legacy identity enrolls a second
      authenticator, does a bootstrap fetch, then rotates to seed and produces a valid kit

**Acceptance:** register on laptop → enroll phone passkey + YubiKey → clear laptop site data →
recover via any of: phone, YubiKey, or paper mnemonic; inventory shows all enrollments;
integration test covers mnemonic round-trip. Specifically assert the circularity is broken:
after clearing site data the laptop fetches the keystore with **only** a WebAuthn assertion
(no identity-key signature available), and a caller without an enrolled authenticator gets
nothing — including no credential IDs. A legacy two-key identity passes every case except the
kit, and passes that too after opting into rotation.

### E11-T2 — Recovery-master role & elevated operations

- [ ] Policy enforcement: only `recovery-master` enrollments can remove other enrollments,
      trigger seed rotation, or bulk-revoke sessions; UI flow to designate exactly the
      hardware-key class of authenticators (warn on platform passkeys — they sync via
      iCloud/Google, which is a different trust profile than a Titan key in a drawer)
- [ ] "Kill my lost device" flow end-to-end: recovery-master removes enrollment → relay
      revokes its sessions (`DELETE /sessions/:id` exists) + DAV tokens + app passwords
- [ ] Seed rotation ceremony (compromise response): new seed, re-wrap under all surviving
      enrollments, publish rotation statement (E01-T5), re-encrypt `poweur-sys` private files
      readable only via old enc key if any, notify contacts (`sys.key.rotated` message so
      pinned keys update with continuity proof, EPIC-007 T4)
- [ ] CLI parity: `poweur keys remove/rotate --sign-with=recovery-master`

**Acceptance:** stolen-phone drill in integration tests: phone enrollment removed by hardware
key, phone's session can no longer read inbox, contacts' clients accept the rotation without
key-change warnings.

### E11-T3 — New-device enrollment ceremony (no seed typing)

Enrolling device B shouldn't require the paper kit if device A is at hand. **QR is not
sufficient on its own:** the common phone-browser → laptop-browser case has no usable camera
(denied permission, no camera, or a desktop that cannot scan), and email is explicitly off the
table. Ship **two transports plus one optimisation**, sharing a single enrollment state machine.

#### Transport 1 — short code + PAKE (default; no camera, any direction)

- [ ] The **new** device displays a 6–9 digit code; the user types it on the **trusted**
      device (which already holds the identity and is where the biometric prompt belongs)
- [ ] Establish the channel with a **PAKE (SPAKE2 or CPace)** using the code as the password,
      over a relay rendezvous. **Not** a KDF over the code: the relay holds the blob and could
      brute-force a short code offline. A PAKE makes guessing online-only — one attempt per
      session, fail closed — which is what makes 6 digits safe
- [ ] One guess per rendezvous, short TTL, single-use; a wrong code destroys the rendezvous
      rather than allowing a retry
- [ ] Relay is a **blind rendezvous**: it sees PAKE messages and ciphertext, never the code,
      the seed, or a brute-forceable transcript
- [ ] **Library risk is real** — JS PAKE implementations are less mature than the primitives
      around them. Pin one reviewed implementation, record the choice and version in the spec,
      and make it explicit scope for the E11-T7 security review. Do **not** hand-roll SPAKE2,
      and do not fall back to a non-PAKE scheme if the library proves unusable — reduce to
      Transport 2 and say so

#### Transport 2 — QR (retained; fastest where a camera exists)

- [ ] New device shows a QR (ephemeral X25519 pubkey + challenge); the trusted device scans,
      the user approves with a passkey prompt, and the seed travels wrapped to the ephemeral
      key via `sys.enroll.offer` (typed message to self)
- [ ] Offer QR and code side by side, always — never make the code a hidden fallback behind a
      camera-permission failure, since the user may have declined the prompt already

#### Optimisation — synced passkey, zero ceremony

- [ ] Where the passkey provider syncs (iCloud Keychain, Google Password Manager, 1Password)
      **and** the rpId matches (E18-T4), no ceremony is needed: the new browser does a WebAuthn
      assertion for the already-enrolled credential, gets the same PRF output, and unwraps the
      entry it fetches from the relay keystore
- [ ] Uses the **bootstrap read endpoint from E11-T1** (`POST /identities/{id}/keystore/fetch`,
      assertion-gated, discoverable credentials) — specified there, not here. Note this is the
      relay-held store, **not** `poweur-sys/private/keystore/`, which holds metadata only
- [ ] **This path is an optimisation and may be deferred.** Recovery does not depend on it:
      Transport 1 carries the seed from the other device, and the recovery kit covers the
      no-other-device case. Ship it when the zero-ceremony UX is worth the endpoint's cost, and
      cut it without touching the rest of the epic if it isn't
- [ ] Detect and offer this path first, but **never depend on it** — it fails across passkey
      ecosystems (iPhone → Windows/Chrome) and across relays (`alice.r1.com` vs `alice.r2.com`
      are different RPs, hence different credentials). Falling back to Transport 1 must be one
      click, not a dead end

#### Shared hardening (all transports)

- [ ] Short expiry; **number matching** shown on both screens as the confirmation step
      (replaces the SAS wording — same function, the interaction users already know)
- [ ] Number matching is a **confirmation, never a transport.** A standalone "approve on your
      other device" prompt is an MFA-fatigue surface: the identity name is public, so anyone
      could trigger prompts. Approval is only ever offered inside a ceremony the user started
- [ ] Enrollment notification to **all** existing devices, including the device fingerprint
      and transport used, so an unexpected enrollment is visible after the fact
- [ ] CLI variant: `poweur keys enroll` prints the offer URI **and** the numeric code for
      headless boxes

**Acceptance:** phone-to-laptop enrollment completes in under a minute **with the camera
denied**, using the typed code; the QR path still works where a camera exists; the synced-passkey
path enrolls a second browser in the same ecosystem with no ceremony; MITM test (wrong code /
wrong number match) fails closed and burns the rendezvous; all pre-existing devices are notified.

### E11-T4 — CLI/bot key storage hardening

- [ ] Encrypt key files at rest: passphrase (age/scrypt-style) or OS keychain (macOS Keychain,
      Linux secret-service) chosen at `poweur init`; `POWEUR_KEY_PASSPHRASE` env for daemons
- [ ] Migration command for existing plaintext `keys_dir` files; loud warning when loading
      legacy plaintext keys
- [ ] Optional FIDO2 hardware-key support in CLI (libfido2/`go-libfido2`) so the
      recovery-master story works for terminal-first users too

**Acceptance:** fresh `poweur init` never writes plaintext private keys; legacy files migrate;
integration suite runs with passphrase-protected keys.

### E11-T5 — Social recovery via recovery contacts (ambitious phase)

The no-email, no-vendor answer to "house fire: all devices and the paper kit are gone."

- [ ] Design doc first (`apps/docs/docs/security/social-recovery.md`) with an **honest threat
      model**: k-of-n Shamir (SLIP-0039) shares of the seed, encrypted to each contact's
      X25519 key, stored in their homes (EPIC-005 share mechanics). Mitigate contact collusion
      by *protocol*, not wishful crypto: shares are released only into a **public recovery
      ceremony** — a signed recovery request (new candidate key) that relays broadcast to all
      enrolled devices and all recovery contacts, followed by a mandatory delay (configurable,
      default 7 days) during which any existing enrollment or quorum of contacts can veto.
      Colluding contacts must therefore announce the theft to the victim's devices and wait a
      week. Document residual risks (relay suppression of notifications → mitigate via
      multi-channel: every contact's client independently shows the ceremony)
- [ ] Implement ceremony state machine as typed messages (`sys.recovery.*`, EPIC-009) with the
      relay as coordinator but never a shareholder
- [ ] Contact UX: "Alice asked you to be a recovery contact" (accept stores the encrypted
      share), periodic share health-checks (contact still reachable? share still decryptable?),
      share rotation when the contact set changes
- [ ] Owner UX: choose contacts (recommend 3-of-5), simulate-recovery drill mode
- [ ] Integration test: full recovery with k contacts + delay elapsed; veto test; collusion
      test asserts devices/contacts were notified before any share release

**Acceptance:** an identity with no surviving devices and no kit recovers via 3 of 5 contacts
after the delay window; every veto path works; threat-model doc reviewed.

### E11-T6 — Poweur ID as the recovery anchor for everything else

The strategic inversion: other accounts recover *via* Poweur.

- [ ] Encrypted vault convention `poweur-sys/private/vault/` for third-party recovery codes,
      TOTP seeds and backup credentials — wrapped with a vault key derived from the master
      seed; CLI/web UI to store/reveal entries (passkey prompt per reveal)
- [ ] Relying-party recovery pattern for EPIC-008: an RP records the user's Poweur ID at
      signup and offers "recover access with your Poweur ID" — a Sign-In assertion (E08-T1)
      replaces email reset links; document as part of the verifier SDK + add to the reference
      RP (E08-T2)
- [ ] Import/export bridges: Bitwarden/Vaultwarden-compatible export format so users can bring
      existing recovery material in (and leave — no lock-in, it's their files)

**Acceptance:** reference RP demonstrates account recovery via Poweur Sign-In with email
entirely absent; vault entries survive device loss via E11-T1 recovery.

### E11-T7 — Security review & recovery drills as fixtures

- [ ] Commission/perform a focused review of the seed-derivation, wrapping and ceremony
      designs before E11-T5 ships (external eyes on crypto choices: HKDF info strings, PRF
      salt versioning — `poweur-prf-v1` exists, SLIP-0039 parameters)
- [ ] **Explicitly in scope: E11-T3's PAKE choice, its library and version**, the rendezvous
      fail-closed behaviour, and the assertion-gated keystore endpoint. These are the newest
      and least battle-tested pieces in the epic — review them before the ceremony ships, not
      alongside E11-T5
- [ ] Codify the recovery scenarios as permanent integration fixtures: lost-one-device,
      lost-all-devices-have-kit, lost-everything-social, stolen-device-kill, compromised-seed
      rotation — these are the product's most load-bearing promises, they get CI coverage
- [ ] User-facing docs: "How not to lose your Poweur ID" one-pager, surfaced during onboarding

**Acceptance:** all five drills green in CI; review findings triaged into issues.

## Non-goals

- **No per-device encryption keys** — see the E19-T8 inbound note; senders encrypt to one
  `encryption_public_key`, so changing this is a protocol change owned elsewhere.
- **No relay-side plaintext, ever.** The relay stores wrapped blobs and credential public keys.
  If a design step needs the relay to unwrap something, the step is wrong.
- **No email or phone recovery channel**, at any phase. That is the point of the epic.
- **No custodial or vendor escrow** fallback.
- **No new rotation protocol** — E01-T5 shipped; seed rotation reuses it.

## Open questions (resolve during T1, don't block on them)

- **PAKE library choice** for E11-T3 Transport 1 — pin one reviewed implementation and version;
  explicit scope for E11-T7.
- **`fetch` rate-limit shape** — per identity, per IP, or both. It is an availability tradeoff:
  too tight and a user with a flaky connection cannot recover.
- **Kit format versioning** — BIP39 encodes the seed but not the identity or relay; decide
  whether the printed kit carries them as text only (simplest) or in a versioned envelope.
