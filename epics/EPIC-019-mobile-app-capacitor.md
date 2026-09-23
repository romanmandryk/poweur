# EPIC-019 — Mobile app: Capacitor shell over the web client

- **Status:** in progress — T1 and T2 done and **verified on an iOS simulator**, T6 and T7 done; the Android project is generated and builds
- **Priority:** P2 (after EPIC-015 makes the web app worth wrapping; the decision itself is P1 because it constrains E15)
- **Depends on:** [EPIC-015](EPIC-015-web-app-ux.md) (the UI being wrapped), [EPIC-017](EPIC-017-typescript-client-sdk.md) (`@poweur/client`), [EPIC-018](EPIC-018-identity-onboarding-naming.md) (onboarding + credential scope), [EPIC-011](EPIC-011-key-management-recovery.md) (T8 key transfer)
- **Unlocks:** EPIC-008 (the app is the consent surface for sign-in), EPIC-009 (push), `requirements.md`'s mobile-app requirements

## Progress

| Task | Status | Notes |
|------|--------|-------|
| E19-T1 Shell + single-origin client | **done** | iOS: builds, installs and runs on an iOS 26.5 simulator: claim → unlock → push-driven request, against a local dev relay. Android: `cap add android` done, `:app:assembleDebug` green, not yet run on an emulator |
| E19-T2 Native key custody (`kdf:"native"`) | **done, minus tiered custody** | Swift and Java plugins written and wired; identity creation in the shell takes the keystore over a passkey, and unlock comes back from it. Verified on the iOS simulator. Background-receive tiering stays open and belongs with T4 |
| E19-T3 Optional passkey enrollment | open | hosted identities only |
| E19-T4 Push & background sync | open | needs E09; hooks only until then |
| E19-T5 Packaging, CI & release | open | |
| E19-T6 Retire the native stubs | **done** | `apps/android` and `apps/ios` deleted; root scripts now point at `apps/mobile` |
| E19-T7 Multi-identity across relays | **done** (in the code the shell wraps) | two relays, two identities, one origin — `apps/web/test/e2e/multi-relay.spec.js` |
| E19-T8 Bringing an existing identity over | open | device-to-device; the relay cannot approve |

**The shell's front door is specified in [E15-T7](EPIC-015-web-app-ux.md).** The web app is
getting host-aware modes — `launcher` on the parent domain, `identity` on `bob.poweur.net` — and
the shell is the third: `capacitor://localhost` is not a meaningful host, so it resolves to
`shell` mode. The shell seeds from `https://poweur.net` (not from its own origin) and keeps
`renderRelayPrompt()` as a three-way picker — production, local emulator, or a typed URL —
which E15-T10 keeps for exactly this case. It then behaves like the launcher. The shell needs
no screen of its own for it, which is the point: E15-T9's removal of free-text identity entry
is scoped to the two browser doors, because in the shell the identity genuinely is not
knowable from the URL. E15-T11's desktop layout leaves the phone breakpoint untouched, so it
changes nothing the shell wraps.

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
[`apps/web/src/lib/vault.js`](../apps/web/src/lib/vault.js)), and the stored record already
carries a `kdf` discriminator (`"prf" | "native"`). The passkey is a **lock, not the key** —
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
  (the legacy `apps/web/js/app.js`) — correct for a relay-served SPA, fatal for
  a shell on `capacitor://localhost`. E15-T1 must make the base URL come from the identity
  record / config. **This is the one thing E15 has to get right for this epic to be cheap**,
  and it is cheap to do now.
- **Key custody is an interface with two web/shell implementations** — `prf` (browser
  passkey; authenticators without PRF are refused — PIN is a non-goal) and `native`
  (Keychain/Keystore). The stored record's `kdf` field already discriminates them.
- **Mobile-first layout is EPIC-015's job, not this epic's.** A wrapped web app feels native
  or doesn't based on the layout it wraps. E19 must not become the place where responsive
  bugs are fixed.
- **Multiple identities across multiple relays** is a first-class case in the shell (the
  active-identity selector from `requirements.md`), unlocked by the single-origin property.

## Tasks

### E19-T1 — Shell scaffolding & single-origin client — **partial**

- [x] Capacitor project in `apps/mobile/` with an `ios/` platform folder (Capacitor 8 uses
      SwiftPM, so there is no CocoaPods step). `pnpm run stage` builds `apps/web` and copies `dist/` into `www/`
      — a copy rather than a symlink, because `cap sync` follows it into the native project
      and a symlink there builds an app that works on one developer's machine
- [x] No `location.origin` assumptions — `test/origin.test.js` has enforced that since
      E15-T1, and `test/e2e/multi-relay.spec.js` now proves the positive case
- [x] Deep links: the `poweur://` scheme is registered in `Info.plist` and in the Android
      `VIEW`/`BROWSABLE` intent filter (`custom_url_scheme`). `@capacitor/app` delivers the
      URL, and `poweur://auth?request=…` opens Settings → Approve sign-in with that request
      code filled in. Universal links are
      **deliberately not** here — they bind the app to the operator's domain, and E19-T3
      owns that as an optional extra for hosted identities
- [x] **Android platform generated** (`cap add android`) and building: `:app:assembleDebug`
      is green against SDK 36 / JDK 21. A `network_security_config.xml` mirrors the iOS ATS
      exceptions so a dev relay on the host is reachable — including `10.0.2.2`, which is
      how an emulated device addresses its host. Not yet run on an emulator
- [x] **Icons and splash** (mobile 0.1.6), generated by
      [`design/brand/scripts/icons.py`](../design/brand/scripts/icons.py):
      - iOS: a 1024 px `AppIcon` with no alpha, plus dark and tinted (iOS 18) appearances. Any and
        dark are the favicon mark: flat white P on `violet-600`. Tinted stays a white P on black
        so iOS can apply its own tint. The 2732 px splash is the glass P on the brand glow.
      - Android: legacy `ic_launcher` and `ic_launcher_round` at every density, the same violet
        tile and white P. The adaptive icon uses a solid violet background, a white-P foreground
        and a white-P `monochrome` layer (Android 13 themed icons). Portrait and landscape
        splashes at every density stay the glass P on the glow.
      - `test/icons.test.mjs` pins the sizes, the missing alpha on iOS and the adaptive layers.
      - Not yet seen on a device or simulator.
- [ ] Navigation allow-list / CSP review

**Acceptance:** met on iOS. The shell runs on an iOS 26.5 simulator against a local dev
relay: it claims a handle (live availability answering from the relay), creates an
identity, unlocks it after a relaunch, and receives a contact request **by push with
nothing touched on the device**. Android builds but has not been run.

### What running it on a device found

Four defects, none of which any browser test could have caught, because each needs either
a real safe area, a non-`http(s)` origin, or an authenticator that does not exist:

1. **The header sat under the Dynamic Island.** It padded for `safe-area-inset-top` but
   kept a fixed height, so the padding squeezed its contents up instead of moving them
   down. Invisible at 375px in a browser, where the inset is zero.
2. **A fresh install could reach no relay.** `defaultRelayUrl()` fell back to
   `location.origin`, which in a shell is `capacitor://localhost` — so registration
   addressed the app itself, and the only screen that sets a relay lives behind an
   identity. A non-web origin now seeds `https://poweur.net`, and the landing picker
   can switch to a local emulator or a typed URL.
3. **Identity creation dead-ended.** WebAuthn needs a secure `http(s)` origin, so a shell
   has no authenticator *by construction* — native keystore custody is the answer
   (E19-T2). A PIN fallback is a non-goal. The same rule applies to the device-enrollment path,
   which would otherwise let an identity be joined on a browser but not on a phone.
4. **Every authenticated call was blocked before it left the browser.** The relay's CORS
   allow-list was missing `X-Poweur-Challenge`, so the preflight failed for every
   challenge-signed read. Nothing same-origin ever noticed — the web app is served *by*
   the relay and sends no preflight — which is why a suite of 29 browser tests was green
   while the shell could not read its own inbox. Pinned now by a preflight test that
   asserts every protocol header, from a `capacitor://localhost` origin.

Two smaller ones: iOS auto-capitalised handles (`autocapitalize="none"` on every identity
field), and a queued contact request produced no push at all — the relay notified only on
the inbox path, so a request waited silently until the app was opened.

### E19-T2 — Native key custody (`kdf: "native"`) — **done, minus tiered custody**

- [x] **The plugin is written on both platforms**, against the contract at the bottom of
      `apps/web/src/lib/native.js`: `ios/App/App/PoweurKeystorePlugin.swift` (Keychain +
      `SecAccessControl(.biometryCurrentSet)` + LocalAuthentication) and
      `android/app/src/main/java/net/poweur/app/PoweurKeystorePlugin.java` (a per-identity
      AES key in the Android Keystore, unlocked by `BiometricPrompt`, opening a ciphertext
      in preferences). Registration differs by platform and both are wired:
      `MainActivity.onCreate` on Android, and a `CAPBridgeViewController` subclass on iOS
      because `SceneDelegate` builds the root controller itself
- [x] Registered as a third `kdf` alongside `prf`: `wrapKeysAES` /
      `unwrapKeysAES` are reused unchanged, and only where the 32 bytes come from differs.
      Six unit tests cover the JS half against a fake plugin, including that the identity
      keys are never handed to it and that one secret is kept per identity
- [x] Identity creation in the shell defaults to `native`. Where biometrics are
      unavailable the app tries a PRF passkey rather than a PIN (PIN wrapping is a
      non-goal). `chooseCustody()` ranks keystore over passkey, and the fallback names
      the actual obstacle ("this device has no passcode") rather than reporting a
      missing passkey. Covered by
      `apps/web/test/e2e/native-custody.spec.js` against a contract-shaped fake plugin:
      that the keystore wins over a *working* passkey, that unlock never asks for a PIN,
      that a keystore which cannot gate on biometrics falls back to PRF rather than a PIN, and that removing an
      identity forgets its hardware secret
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
- [x] Explicit failure handling for the case the OS actually produces: an invalidated key
      reads back as *absent*, and the app says "add this device again, or use your recovery
      kit" rather than "unlock failed". Tested. The remaining paths (enrolment changed vs
      restored backup) are indistinguishable from JS and land in the same message
- [ ] Tiered custody for background receive, and the biometric/`device` gate split beyond
      the flag this seam already carries
- [ ] **Self-hosted acceptance path:** the same store build registers and uses an identity on
      a relay serving a domain unrelated to `poweur.net`

**Acceptance:** an identity created in the app on a self-hosted relay
(`*.example.org`) unlocks by biometrics on relaunch; no WebAuthn is invoked at any point;
the wrapped-key record round-trips through `unwrapKeysAES`.

**Where it stands against that:** met on an iOS 26.5 simulator against a local dev relay —
claim with no PIN, `kdf: "native"`, relaunch, unlock from the Keychain, Settings reporting
"Device keystore". Two gaps remain, both honest rather than incidental: the simulator has
no device passcode, so `.biometryCurrentSet` items are stored and read back without ever
drawing the Face ID sheet — the *gate* is only proven on hardware — and the Android plugin
compiles and is registered but has not been run on an emulator.

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
