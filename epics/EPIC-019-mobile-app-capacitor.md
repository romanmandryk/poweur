# EPIC-019 — Mobile app: Capacitor shell over the web client

- **Status:** proposed
- **Priority:** P2 (after EPIC-015 makes the web app worth wrapping; the decision itself is P1 because it constrains E15)
- **Depends on:** [EPIC-015](EPIC-015-web-app-ux.md) (the UI being wrapped), [EPIC-017](EPIC-017-typescript-client-sdk.md) (`@poweur/client`), [EPIC-018](EPIC-018-identity-onboarding-naming.md) (onboarding + credential scope), [EPIC-011](EPIC-011-key-management-recovery.md) (T8 key transfer)
- **Unlocks:** EPIC-008 (the app is the consent surface for sign-in), EPIC-009 (push), `requirements.md`'s mobile-app requirements

## Progress

| Task | Status | Notes |
|------|--------|-------|
| E19-T1 Shell + single-origin client | open | blocked on E15-T1 |
| E19-T2 Native key custody (`kdf:"native"`) | open | the piece that frees self-hosters |
| E19-T3 Optional passkey enrollment | open | hosted identities only |
| E19-T4 Push & background sync | open | needs E09; hooks only until then |
| E19-T5 Packaging, CI & release | open | |
| E19-T6 Retire the native stubs | open | `apps/android`, `apps/ios` |
| E19-T7 Multi-identity across relays | open | **the reason to install the app** |
| E19-T8 Bringing an existing identity over | open | device-to-device; the relay cannot approve |

## Goal

Ship one iOS and one Android app that are the **existing web client** running in a native
shell — full web-app functionality plus the things a browser cannot do: hardware-backed key
custody without WebAuthn, push, background sync, the system share sheet — and, above all,
**many identities across many relays in one app**.

The last one is the reason a user installs anything at all. The web client is structurally
single-identity: `localStorage` is per-origin, so `alice.r1.com/app/` cannot see an identity
stored by `alice.r2.com`, and no amount of UI work changes that. The shell's single origin
is what makes aggregation possible, so E19-T7 is a **headline requirement**, not a nicety.

## Background & the decision

`apps/android/` and `apps/ios/` exist but are stubs — a `package.json` each, wrapping
`gradle assembleRelease` / `xcodebuild` against source that was never written.

Three options were weighed:

| Option | Cost | Verdict |
|--------|------|---------|
| **Native (Swift + Kotlin)** | Two full UI implementations; no Go mobile bindings exist; every EPIC-015 screen built three times forever | rejected |
| **React Native** | Rewrite every screen; `@poweur/client` needs WebCrypto polyfills RN lacks natively; pay the port cost *and* keep paying it | rejected |
| **Capacitor** | Reuse the shipped UI and `@poweur/client` verbatim; native plugins for the gaps | **chosen** |

Capacitor wins on more than packaging convenience. A Capacitor app runs on **one fixed
origin** and talks to relays cross-origin — which the relay already permits
(`Access-Control-Allow-Origin: *`, [`apps/api/internal/relay/webstatic.go:16`](../apps/api/internal/relay/webstatic.go)).
That is precisely the multi-relay property a per-identity-origin browser app cannot have,
so the shell is architecturally *better* placed than the web app for switching identities
across relays.

### Why a self-hoster does not need to publish their own app

This was the open question that motivated the epic, and the answer falls out of how keys are
already stored. The passkey is **not** the identity key: PRF output derives an AES key that
wraps the Ed25519/X25519 keys (`wrapKeysAES`,
[`apps/web/js/vault.js`](../apps/web/js/vault.js)), and the stored record already
carries a `kdf` discriminator (`"prf" | "pbkdf2"`). The passkey is a **lock, not the key** —
so nothing binds an identity to a domain. Two independent consequences:

- **Web:** the relay serves the SPA itself (`WEB_STATIC_DIR` → `/app/`), so someone running
  the released relay image gets the identical app at `bob.example.org/app/`, with `rp.id`
  scoped to their own domain. They publish nothing.
- **Native:** add `kdf: "native"` — Keychain / Android Keystore, biometric-gated, supplies
  the 32-byte wrapping secret. No WebAuthn, therefore **no associated-domains constraint**,
  therefore the store build works against any relay on any domain. Associated domains
  (E19-T3) become an *optional* nicety for `poweur.net` identities, not a gate.

This is why E19-T2 is the load-bearing task, not E19-T3.

## Design direction

- **No fork of the UI.** The shell loads the same static tree `apps/web` produces. If a
  screen needs a native capability it goes through a capability interface with a web
  fallback, never a forked screen.
- **Relay URL is configuration, not `window.location.origin`.** Today `doCreateIdentity`
  reads `const relayUrl = window.location.origin`
  ([`apps/web/js/app.js`](../apps/web/js/app.js)) — correct for a relay-served SPA, fatal for
  a shell on `capacitor://localhost`. E15-T1 must make the base URL come from the identity
  record / config. **This is the one thing E15 has to get right for this epic to be cheap**,
  and it is cheap to do now.
- **Key custody is an interface with three implementations** — `prf` (browser passkey),
  `pbkdf2` (PIN, works everywhere), `native` (Keychain/Keystore). The stored record's `kdf`
  field already discriminates them; nothing else in the app cares which is in use.
- **Mobile-first layout is EPIC-015's job, not this epic's.** A wrapped web app feels native
  or doesn't based on the layout it wraps. E19 must not become the place where responsive
  bugs are fixed.
- **Multiple identities across multiple relays** is a first-class case in the shell (the
  active-identity selector from `requirements.md`), unlocked by the single-origin property.

## Tasks

### E19-T1 — Shell scaffolding & single-origin client

- [ ] Capacitor project in `apps/mobile/` (replacing the `apps/android` / `apps/ios` stubs,
      T6) with `ios/` and `android/` platform folders; web assets built from `apps/web`
- [ ] Verify the app works with **no `window.location.origin` assumptions**: relay base URL
      resolved from the active identity record; add a regression test in `apps/web` that
      fails if a module reads `location.origin` for a relay call
- [ ] Configure allowed navigation / CSP so the shell may reach arbitrary relay hosts (the
      multi-relay property) without becoming an open web view
- [ ] Deep links (`poweur://` + universal/app links) for sign-in approvals (EPIC-008) and
      share targets
- [ ] Splash/icon/theme parity with the web app's dark/light handling

**Acceptance:** the shell runs on an iOS simulator and an Android emulator, connects to a
local dev relay, and completes an existing identity's unlock + inbox load.

### E19-T2 — Native key custody (`kdf: "native"`)

- [ ] Capacitor plugin exposing biometric-gated secure storage (iOS Keychain with
      `SecAccessControl` + LocalAuthentication; Android Keystore + `BiometricPrompt`)
- [ ] Register it as a third `kdf` alongside `prf` / `pbkdf2` in the wrapped-key record;
      `wrapKeysAES` / `unwrapKeysAES` are reused unchanged — only the 32-byte secret's
      source differs
- [ ] Identity creation in the shell defaults to `native`, with `pbkdf2` PIN as the
      documented fallback where biometrics are unavailable or the user declines
- [ ] **Tiered custody**, because background receive (T7) cannot prompt for biometrics:

      | Operation | Credential | Gate |
      |---|---|---|
      | Poll inbox, count, badge | session key (`SessionProof`) | device unlock, no prompt |
      | Decrypt for a notification preview | identity X25519 enc key | device unlock, **opt-in** |
      | Send, share, manage, rotate | identity signing key | biometric, always |

      Default is **badge-only**: counts without previews, so the encryption key stays
      biometric-gated. Previews are an explicit per-identity opt-in that moves the enc key to
      a device-unlock-gated store — the tradeoff must be stated in the UI, not buried
- [ ] Session keys are minted while the identity is unlocked in the foreground and renewed
      before `ExpiresAt`; an expired session degrades to "unlock to refresh", never to a
      silent stop
- [ ] Explicit failure handling: biometric enrollment changed, key invalidated by the OS,
      device restored from backup — each needs a defined recovery path (re-enroll from
      another device / seed per EPIC-011), not a crash
- [ ] **Self-hosted acceptance path:** the same store build registers and uses an identity on
      a relay serving a domain unrelated to `poweur.net`

**Acceptance:** an identity created in the app on a self-hosted relay
(`*.example.org`) unlocks by biometrics on relaunch; no WebAuthn is invoked at any point;
the wrapped-key record round-trips through `unwrapKeysAES`.

### E19-T3 — Optional passkey enrollment (hosted identities)

- [ ] Associated domains (`webcredentials:`) / Digital Asset Links for the operator's hosted
      domain only, using the E18-T4 `rp.id`
- [ ] Offer passkey enrollment as an **additional** credential on hosted identities (synced
      across the user's devices), never as a requirement
- [ ] Verify the platform's wildcard behaviour for `webcredentials` on the target OS versions
      before relying on it; if a bare registrable domain does not cover subdomain origins as
      expected, fall back to T2 custody and say so in the docs rather than shipping a flow
      that silently fails on one platform
- [ ] Self-hosted identities skip this task's UI entirely — it must not appear as a broken
      or disabled affordance

**Acceptance:** a hosted identity enrolled with a passkey in the app unlocks on a second
device; a self-hosted identity shows no passkey UI and is fully usable.

### E19-T4 — Push & background sync

- [ ] APNs / FCM registration; token published to the relay as a device record
- [ ] **Relay-side push requires EPIC-009.** Until it lands, ship the client hooks and a
      foreground polling path; do not add relay endpoints here (E15's non-goal applies)
- [ ] Background sync of the changes feed (EPIC-004) within each platform's background
      execution limits
- [ ] Notification payloads carry **no message content** — E2E encryption means the payload
      is a wake-up, and the app decrypts locally

**Acceptance:** the app receives a wake-up and surfaces a decrypted-locally notification for
a new message; with push unavailable, foreground polling still delivers it.

### E19-T5 — Packaging, CI & release

- [ ] `apps/mobile` build wired into the workspace; CI builds both platforms on PRs touching
      `apps/web` or `apps/mobile` (a broken shell must not be discovered at release time)
- [ ] Signing/provisioning documented; store metadata, privacy nutrition labels and data-safety
      declarations drafted (a client that holds keys locally and talks to a user-chosen server
      needs these answered precisely)
- [ ] Versioning policy: the shell version tracks the web app it embeds
- [ ] Over-the-air web-asset updates evaluated **and explicitly decided** — they interact with
      store policy and with the integrity expectations of a key-holding client; the decision
      and its rationale go in the docs either way

**Acceptance:** a signed build of each platform is produced by CI from a clean checkout.

### E19-T6 — Retire the native stubs

- [ ] Delete `apps/android/` and `apps/ios/` (each is a lone `package.json` invoking a
      non-existent Gradle/Xcode project)
- [ ] Update workspace config and any references
- [ ] Record the native-vs-RN-vs-Capacitor decision in `apps/docs/docs/` so the question is
      not re-litigated from scratch

**Acceptance:** `pnpm install` is clean with no stub packages; the decision is documented.

### E19-T7 — Multi-identity across multiple relays

The headline feature. Assume two independent relays, `r1.com` and `r2.com`, each hosting its
own subdomains. One install must create, hold and operate identities on both.

- [ ] **Create an identity on any relay:** the domain is user-supplied, not a constant. The
      shell discovers that relay's rules by calling **its** `GET /hosted/availability`
      (E18-T2) and rendering the `policy` object it returns — min/max length, charset and
      reserved verdicts are per-relay and must never be hardcoded to the operator's own
- [ ] **Identity store keyed by identity, carrying its own relay base URL**, custody `kdf`,
      credential scope and session state. Nothing global: two identities on two relays share
      no configuration (this is E15-T1's no-`location.origin` constraint paying off)
- [ ] **Active identity governs sending and managing only** — compose, share, contacts,
      settings and policy edits all bind to the active identity, and switching must not
      strand an in-flight operation
- [ ] **Receiving is all-identity, always.** The poll/sync loop iterates every stored
      identity against its own relay regardless of which is active, using the session-key
      tier from T2 so no biometric prompt is possible
- [ ] **Badges** — per-identity unread/action counts (messages, contact requests, share
      offers) shown in the identity switcher, aggregated on the app icon. A count must be
      derivable **without** decrypting, so the badge works under default custody
- [ ] Per-relay push registration (T4): one device holds N registrations across N relays;
      the payload names the identity and carries **no content**. Relays without push
      configured degrade to fetch-on-open — the UI must not imply real-time delivery it
      cannot provide
- [ ] Per-identity failure isolation: an unreachable, throttled or 5xx-ing relay degrades
      **only** its own identity's row. One dead relay must never stall the other's polling or
      block app start
- [ ] Identity switcher UX: avatar/handle/relay, unread counts, and the relay's host visible
      — two identities may share a handle (`alice.r1.com` and `alice.r2.com`), so the handle
      alone is not a label

**Tests (required, not optional):**

- [ ] Integration: two relay instances on distinct hosted domains; register an identity on
      each from one client; assert both are independently usable
- [ ] Receive-while-inactive: send to the **non-active** identity, assert its badge count
      increments with no unlock and no decryption
- [ ] Isolation: kill relay B; assert identity A polls, sends and renders normally, and B's
      row shows a degraded state rather than an app-level error
- [ ] Switching: assert send/manage operations bind to the newly active identity and that
      no credential, session or DAV token leaks across identities
- [ ] Policy: assert the create flow renders relay B's `policy` when targeting B, including
      a min-length that differs from relay A's

**Acceptance:** one install holds `alice.r1.com` and `alice.r2.com`; both receive while
inactive and badge correctly; switching changes only the send/manage context; taking one
relay offline leaves the other fully functional.

### E19-T8 — Bringing an existing identity to the device

- [ ] **Reuse [E11-T3](EPIC-011-key-management-recovery.md)'s enrollment ceremony — do not
      invent a second one.** That task owns all three transports (short code + PAKE, QR, and
      the synced-passkey shortcut); this task's job is the shell's side of them: the phone
      generates the ephemeral keypair, drives the chosen transport, then re-wraps under its
      native secure-store secret and discards the ephemeral key
- [ ] The shell should offer **the typed code as prominently as QR**, since a phone scanning a
      laptop screen and a laptop unable to scan anything are equally common
- [ ] The relay may carry the ciphertext as a **blind transport** only. Document explicitly
      that it cannot approve, inspect or recover a transfer: it holds no identity key and no
      wrapping secret — approval is the **existing device's**, never the relay's. This
      corrects a natural but wrong mental model and belongs in the user-facing docs
- [ ] Rendezvous blobs are single-use and short-TTL; a completed or expired transfer is
      unrecoverable by design
- [ ] Show the receiving device's fingerprint on the sending device before the user confirms,
      so the QR channel is authenticated rather than trusted blindly
- [ ] **Protocol constraint to record:** senders encrypt to the identity's
      `encryption_public_key` (`packages/client-ts/src/messages.ts`) and
      `SessionCreateRequest` delegates **signing only** — it carries no encryption key. So
      the X25519 key must be *copied* to every device. EPIC-011's multi-key enrollment can
      give each device a distinct **signing** key, but per-device **encryption** keys need a
      protocol change (senders would encrypt N times). Transfer is therefore structural, not
      a v1 shortcut — flag it to EPIC-011 rather than solving it here
- [ ] Passkey reality check for the docs: `alice.r1.com` and `alice.r2.com` have different
      registrable domains, hence different WebAuthn RPs, hence **necessarily different
      passkeys** — and the shell cannot use either without that relay's domain in its
      associated-domains file. `kdf:"native"` (T2) is the answer; passkeys are not

**Acceptance:** an identity created in a browser on `r1.com` is transferred to the app by QR
and unlocks there by biometrics; the relay observes only opaque ciphertext; a replayed or
expired rendezvous token fails closed.

## Non-goals

- **No UI fork and no framework migration.** EPIC-015's vanilla-JS modules are the app; if
  the shell needs something the web app cannot express, that is an EPIC-015 change.
- **No desktop shell** (Electron/Tauri) in this epic — the same reasoning would apply, but
  the demand is not established.
- **No new relay endpoints.** Push needs EPIC-009; device records need EPIC-011.
- **No offline-first rearchitecture.** Background sync is best-effort within platform limits;
  a full offline store is EPIC-004/009 territory.
- **No per-device encryption keys.** T8 records why this needs a protocol change; EPIC-011
  owns per-device *signing* keys. Do not invent a partial scheme here.
- **No cross-relay identity linking.** `alice.r1.com` and `alice.r2.com` are unrelated
  identities that happen to share a device and a handle. The app must never imply otherwise —
  no merged inbox, no unified profile, no shared contacts.
