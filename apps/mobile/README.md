# `@poweur/mobile` — the Capacitor shell (EPIC-019)

The iOS and Android apps are the **web client in a native container**. There is no mobile
build of the UI and no forked screen: `pnpm stage` copies `apps/web` into `www/`, and
`cap sync` carries it into the platform projects.

```bash
pnpm --filter @poweur/mobile sync     # stage apps/web, then cap sync
pnpm --filter @poweur/mobile ios      # …and open Xcode
pnpm --filter @poweur/mobile test     # staging invariants (no native toolchain needed)
```

## Why a shell rather than native or React Native

A Capacitor app runs on **one fixed origin** and talks to relays cross-origin, which the
relay already permits. That is the property a browser app cannot have: `localStorage` is
per-origin, so `alice.r1.com/app/` structurally cannot see an identity stored by
`alice.r2.com`. Many identities across many relays in one app is the reason to install
anything at all, and it falls out of the shell's origin rather than being built.

## What the shell adds, and what it must not

It adds capabilities a browser lacks — hardware-backed key custody, push, background sync,
the share sheet — through the seam in [`apps/web/js/native.js`](../web/js/native.js), which
answers "is this available?" and otherwise leaves the app unchanged. Everything there has a
web fallback and runs untouched in a plain browser.

It does **not** fix layout. Mobile-first is EPIC-015's job, and a responsive bug found on a
phone belongs in `apps/web`.

## Native key custody

`kdf: "native"` is the load-bearing piece. A browser wraps identity keys with a passkey's
PRF output, and a passkey brings WebAuthn's origin model with it — associated domains,
`rp.id`, a relying party. A platform keystore has none of that, which is what lets **one
store build work against any relay on any domain**, self-hosted included. Associated
domains (E19-T3) are then an optional nicety for hosted identities rather than a gate.

The JavaScript half is complete and tested; the plugin contract it expects — four methods
over Keychain / Android Keystore — is documented at the bottom of `native.js`.

## State of the platform projects

`ios/` is generated (`cap add ios`, Capacitor 8, SwiftPM — no CocoaPods). **It has not been
compiled here:** this machine's Xcode 26.6 has no iOS platform installed, only an iOS 18.3
simulator runtime, so every `xcodebuild` destination is ineligible. Install it with
`xcodebuild -downloadPlatform iOS` (several GB) before expecting a build.

`android/` is not generated yet — it needs an Android SDK, which this machine does not have.
