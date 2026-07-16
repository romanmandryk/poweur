# EPIC-011 — Key management, multi-passkey enrollment & recovery

- **Status:** proposed
- **Priority:** P0 (a primary identity that can be permanently lost is not a primary identity)
- **Depends on:** EPIC-001 (rotation statements, E01-T5), EPIC-002 (durable storage); interacts with EPIC-004 (device registry), EPIC-007 (contacts — for social recovery), EPIC-008 (Poweur as recovery anchor for other services)
- **Unlocks:** trustworthy "Poweur ID as your primary identity" positioning

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
| Identity signing key | Ed25519 | CLI: `keys_dir/<id>.key` (`apps/cli/internal/identity/keys.go`); Web: JWK in localStorage | CLI: **plaintext base64, file mode 0600**; Web: AES-wrapped | permanent |
| Encryption key | X25519 | CLI: `keys_dir/<id>.enc`; Web: JWK in localStorage | same as above | permanent |
| Passkey (WebAuthn) | platform/roaming authenticator | browser/OS keystore | biometric/UV | n/a — **wraps** the two keys above via the PRF extension (`apps/web/js/passkey.js`); **exactly one credential supported**; PIN/PBKDF2 fallback when PRF unavailable |
| Session keys | Ed25519 | client memory/disk + relay session store | identity-signed `SessionProof`, ≤ 24 h TTL (`apps/api/internal/relay/sessions.go`) | hours |
| Relay challenges | nonce | relay memory | single-use, short expiry | minutes |
| (planned) DAV tokens / app passwords | bearer / argon2id hash | relay + `poweur-sys/private/` | scoped, revocable (E03-T3) | hours–long |
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

## Design direction

**One master seed, many wrappings, explicit roles.**

- Derive both long-lived keys from a single 32-byte master seed via HKDF with distinct info
  strings (`poweur/v1/sign`, `poweur/v1/enc`). One seed = one recovery artifact. (Migration:
  existing identities keep their independent keys; the seed model applies at next rotation via
  E01-T5 — write the migration path explicitly.)
- A **keystore** of wrapped seed copies, one per enrolled authenticator/device, stored in
  `poweur-sys/private/keystore/` so it syncs through the relay (EPIC-002 durable storage,
  EPIC-004 sync): `{enrollment_id, kind: passkey|hardware-key|cli-passphrase|recovery-kit,
  wrap: prf|pin|passphrase, wrapped_seed, created_at, role}`. The relay only ever sees
  ciphertext — wrapping keys never leave authenticators/devices.
- A signed **enrollment policy** file (`poweur-sys/private/keystore/policy.json`, signed by the
  identity key) records each enrollment's role: `device` (default) or `recovery-master`.
  Clients enforce: enrollments with role `device` may add devices but not remove others;
  `recovery-master` (your YubiKey/Titan) may **remove any enrollment, rotate the seed, and
  revoke all sessions** — the "I lost my phone, kill it" path. Hardware-backed roaming
  authenticators are recommended (and flagged in UI) for this role because they're offline,
  phishing-resistant and PRF-capable.
- **Recovery kit** = the seed as a BIP39 mnemonic (24 words) + identity name + relay, rendered
  as a printable PDF. Standard, offline, vendor-free. The kit is itself just another
  "enrollment" (kind `recovery-kit`) recorded in the policy so the UI can nag if none exists.
- **Social recovery (phase 2)** = Shamir shares (SLIP-0039) of the seed, each encrypted to a
  recovery contact's X25519 key and stored in *their* home; reassembly requires a public,
  time-delayed ceremony that every enrolled device and contact is notified of and the owner
  can veto — collusion among contacts is made *detectable and slow* rather than silently
  possible (honest threat model below).

## Tasks

### E11-T1 — Key management v1: seed, multi-enrollment, recovery kit (the "good enough" core)

The recommended starting point — comprehensive enough to be trustworthy, simple enough to ship
as one coherent unit. No protocol changes, no relay-side crypto: it's client logic plus the
synced keystore convention.

- [ ] Spec `apps/docs/docs/security/key-management.md`: master-seed derivation (HKDF),
      keystore + policy file formats (PCP, EPIC-006 registry), enrollment/removal flows,
      migration for existing two-key identities
- [ ] Web client: enroll **multiple passkeys** for one identity (re-wrap the seed under each
      authenticator's PRF output; fix the single-`credentialId` assumption in
      `apps/web/js/passkey.js` and the localStorage schema in `app.js`)
- [ ] Move wrapped keystore to `poweur-sys/private/keystore/` via the relay (localStorage
      becomes a cache, not the only copy — survives cleared site data)
- [ ] Recovery kit: generate BIP39 mnemonic at registration (or on demand), printable PDF +
      "verify your kit" re-entry check; restore flow: mnemonic → seed → keys → new device
      enrolled, all sessions optionally revoked
- [ ] Key inventory UI ("Keys & devices" panel in web app + `poweur keys ls` in CLI): every
      enrollment, kind, role, created/last-used, with remove buttons (policy-gated) —
      merges the session list and (later) device registry view from E04-T6
- [ ] Enrollment removal: re-encrypt nothing (seed unchanged) but delete the wrapped copy,
      revoke the enrollment's sessions/tokens, and append a signed policy update — document
      that removal without rotation does NOT protect against an attacker who already extracted
      the seed (that's what rotation is for)

**Acceptance:** register on laptop → enroll phone passkey + YubiKey → clear laptop site data →
recover via any of: phone, YubiKey, or paper mnemonic; inventory shows all enrollments;
integration test covers mnemonic round-trip.

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

Enrolling device B shouldn't require the paper kit if device A is at hand.

- [ ] Cross-device flow: B shows QR (ephemeral X25519 pubkey + challenge), A scans, user
      approves on A (passkey prompt), A sends the seed wrapped to B's ephemeral key through
      the relay (`sys.enroll.offer` typed message to self), B unwraps, creates its own
      passkey wrapping, registers enrollment
- [ ] Abuse hardening: short expiry, code confirmation displayed on both screens (SAS),
      enrollment notification to all existing devices
- [ ] CLI variant: `poweur keys enroll` prints the offer URI for headless boxes

**Acceptance:** phone-to-laptop enrollment in under a minute in the demo; MITM test (wrong SAS)
fails closed.

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
- [ ] Codify the recovery scenarios as permanent integration fixtures: lost-one-device,
      lost-all-devices-have-kit, lost-everything-social, stolen-device-kill, compromised-seed
      rotation — these are the product's most load-bearing promises, they get CI coverage
- [ ] User-facing docs: "How not to lose your Poweur ID" one-pager, surfaced during onboarding

**Acceptance:** all five drills green in CI; review findings triaged into issues.
