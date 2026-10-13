# EPIC-028 — One-tap approvals: one link, no codes where they buy nothing

- **Status:** implemented 2026-10-13 (relay 0.2.17, CLI 0.2.11, SDK 0.2.14, web 0.2.26, bridge 0.2.4, mobile 0.2.1); open: real-device checks (T6, T15) and production `APP_LINKS_*` values
- **Priority:** P1 (every sign-in and every new device goes through these screens)
- **Depends on:** EPIC-008 (sign-in completion binding, requests by reference), EPIC-011 E11-T8
  (pairing v2), EPIC-009 E09-T11/T12 (background Web Push, installable per-identity PWA),
  EPIC-019 (deep links; E19-T4 for native push), EPIC-022 (bridge, push)
- **Touches:** `packages/identity`, `packages/client-ts`, `apps/web`, `apps/oauth`,
  `apps/demoapps/guestbook`, `apps/cli`, `apps/api` (relay), `apps/mobile`, `apps/site`, deploy

## Progress

| Task | Status | Notes |
|------|--------|-------|
| E28-T1 Spec + threat-model update | **done** | spec in `auth/sign-in.md` (approve page, codes by channel), `key-management.md`, bridge and relay docs |
| E28-T2 Shared helpers + vectors (Go/TS) | **done** | `ApprovePath`/`ApproveURL`/`ApprovePageForSigner`/`ApproveLink`, `Delivery.Finish`, `NewMatchChoices`, context `match_choices`/`push_uri`; TS twins; `signin-reference.json` regenerated |
| E28-T3 Bridge: no QR code, push pick-one-of-three, remembered ID, QR → approve link | **done** | bridge 0.2.4; pause in the nonces table; `POST /r/{code}/push` |
| E28-T4 Guestbook: same rules as the reference RP | **done** | no code; remembered ID (`guestbook_last_id`) gives the approve link — no extra typing; `match_failed` metric removed |
| E28-T5 Web signer `/id/approve`: decide where to approve without asking | **done** | web 0.2.26; `lib/approve.ts`; boot reads `#auth=`, `noapp` after an Android fallback |
| E28-T6 Relay: app-association files for hosted IDs | **partial** | relay 0.2.17; `APP_LINKS_*`; served on every host. **Open:** wildcard fetch rules on real devices |
| E28-T7 Mobile: universal links / App Links for hosted IDs | **done** | mobile 0.2.1; iOS and Android builds green; untested on a device |
| E28-T8 Add device: one QR, no "Approve with" chooser | **done** | terminal command behind *Use a terminal* |
| E28-T9 Segmented short-code input | **done** | `ui/CodeInput.tsx`; caught in e2e: a `maxLength` cut a padded code short — removed |
| E28-T10 "Sign in with Poweur" screen: remove the Settings paste entry | **done** | |
| E28-T11 CLI: no `--code` for links; pick for pushed prompts | **done** | CLI 0.2.11 |
| E28-T12 Web Push for sign-in requests | **done** | separate topic, 5-min TTL, no badge change; `?open=signin` opens the newest prompt |
| E28-T13 Move the web app from `/app/` to `/id/` | **done** | `/app/sw.js` retires itself and sends windows to `/id/?moved=1`, which tells the user to reinstall |
| E28-T14 Identity root: *Sign in as alice.poweur.net* | **done** | Playwright maps `*.poweur.net` to the test relay |
| E28-T15 Integration + Playwright journeys | **partial** | Go, integration (`OAUTH_02/03`, `WEBPUSH_01`, guestbook), web unit, full Playwright 77/0. **Open:** device checklist |

**Order:** T13 → T1/T2 → T3–T5, T8–T12, T14 → T6/T7 (need real devices) → T15 throughout.

## Goal

Make every approval look the same to the user: **a link (or QR) opens the place that holds your
keys, and you tap Approve.** No choosing between app, browser and terminal first, no hand-off pages
in between, no typed numbers, and a number to *pick* only where it is the one thing standing between
an attacker and the account: unsolicited push.

## Where we are (audit, 2026-10-12)

Taps and typing for a first-time sign-in, keys in the phone app, starting on a desktop:

| Step | Bridge today | Guestbook today | Add device today (scan) |
|------|--------------|-----------------|-------------------------|
| Start | click *Sign in with Poweur* | click *Sign in* | open `/app/`, *Add this device* |
| Choose where to approve | — | — | **pick app / browser / terminal** |
| Type ID | yes | in the signer | — |
| Scan QR | yes | yes | yes |
| Hand-off page | **tap *Open the Poweur app*** (+ iOS "Open in Poweur?") | redirect into web signer | — |
| Unlock | biometric | biometric / passkey | biometric |
| Code | **type 2 digits** | **type 2 digits** | none (typed-code path: compare 6) |
| Approve | tap | tap | tap |
| Consent | tap (first time per app) | — | — |

**Same browser, keys here** (e.g. alice is signed in to her web app and signs in to the guestbook
or a bridge app for the first time): no code today, and none after this epic. The approval
finishes at a single-use `resume_uri` that works only in the browser holding the RP's binding
cookie, so a forwarded link cannot finish elsewhere. The web signer's optional *Started on another
screen? Enter the code* field makes this look otherwise; E28-T5 removes it.

**Since E09-T11/T12 (web 0.2.24):** each hosted ID installs as its own PWA (scope `/app/`) with
background Web Push. Two platform facts from that work shape this epic. On iOS a scanned QR or a
tapped link opens **Safari, not the installed app**, and the installed app's storage is isolated
from Safari. So on an iPhone, keys usually live in the installed app, where no link can reach them,
and **push is how an approval gets there**. Android Chrome, by contrast, opens in-scope links in an
installed web app.

## Why the codes exist, and what each decision costs

### Sign-in: the 2-digit match code

Not cryptography: a **"look at the screen that started this"** check (`auth/sign-in.md` → *Who may
complete a sign-in*). The attacks it addresses:

1. **Forwarded QR / link (QRLJacking).** The attacker starts a sign-in as you and gets you to
   scan or open it. The request is honestly for the real site, so the signer honestly shows the
   real site's name, and one tap finishes the sign-in **in the attacker's browser**.
   The attacker needs a channel to you: a message, or a phishing page. **The code does little
   here.** The most effective form, a phishing page showing the bridge's live QR, can show the
   live code beside it, and a message can carry the code too.
2. **Push fatigue.** Anyone who knows your ID can trigger *Send to my Poweur app*. No channel to you
   is needed; the prompt arrives unprompted, and one reflexive tap is the account. Here a number is
   the real defence. Microsoft added number matching to Authenticator for exactly this.

### Add device: the 6 digits

Cryptographic (E11-T8): they detect a relay that swapped the new device's key. They appear **only on
the typed-code path** (no camera), where they are compared, never typed; the scan path has none.
Removing them would let a malicious relay add its own device and receive the seed. **Kept.**

## Decisions

### D1 — Codes by channel — **accepted**

| Channel | Code | Why |
|---------|------|-----|
| Same browser (redirect, keys here) | none (as today) | binding cookie + single-use `resume_uri` |
| QR scanned, link opened, CLI given a link | **none** (was: type 2 digits) | the attacker needs a channel to you, and on that channel the code can be shown too |
| Push (Web Push to the PWA, the native app, or the in-app list) | **pick one of three** (was: type 2 digits) | unsolicited; the only defence against push fatigue |

**Accepted risk:** a forwarded QR or link becomes a one-tap takeover. That is the same posture as
WhatsApp, Discord and Steam QR logins. The signer still shows the site, the app name and the
initiator context (`context_uri`: browser, how long ago) with a plain *Only approve a sign-in you
just started* line. Revisit if abuse shows up: the code can come back per channel without a
protocol change.

**Pick one of three, in detail.** Only a transaction that has been pushed to at least once
requires it; the RP decides, not the channel the signer claims:

- On the first push the RP draws the correct two-digit number and two distinct decoys; the starting
  screen shows *Tap **42** on your phone*.
- `context_uri` returns `match_choices` (shuffled, never which is right) for that transaction, and
  the signer shows three buttons instead of *Approve*. Scanning the QR of a pushed transaction
  asks the same.
- **One pick.** A wrong pick ends the transaction, **pauses pushes for that ID for 15 minutes**
  at this RP, and the app says *Someone may be trying to sign in as you*. The pick is the signal,
  so a wrong one costs the attacker the channel instead of giving another try.
- A blind guess wins 1 in 3 (typing was 1 in 100). With the pause and the existing cap of three
  sends per sign-in, an attacker gets one guess per 15 minutes.

### D2 — Where does a QR or link go? — **accepted** (path updated by D5)

Today the QR goes to the RP's `/r/CODE` page, which asks the phone to choose. Instead, when the
RP knows the ID (the bridge always does; the guestbook will ask for it first), the QR is

```text
https://alice.poweur.net/id/approve#auth=https%3A%2F%2Foauth.poweur.org%2Fr%2FK7QM4XP2   (sign-in)
https://alice.poweur.net/id/approve#pair=K7QM4XP2.<commitment>       (add device)
```

The fragment keeps the request out of server logs, as pairing links already do. Opened by a phone
camera:

1. **App installed and the host is hosted** (`*.poweur.net`): the OS opens the app straight on the
   approval screen through universal links / App Links. Android Chrome does the same for an
   **installed PWA**, because the link is inside its scope (D5). No page and no tap before
   *Approve*.
2. **Otherwise the identity's web app loads `/id/approve` and decides by what it holds,** not by
   asking:
   - this browser holds the ID's keys → approve here;
   - phone, no keys:
     - Android: `intent://…;scheme=poweur;package=net.poweur.app;S.browser_fallback_url=…` opens
       the store app, or falls back silently;
     - iOS: *Open in Poweur app* (iOS shows an error alert when a custom scheme has no app, so
       nothing fires automatically) and **Send to my home-screen app**, which asks the RP to push
       (E28-T12). Safari cannot open an installed PWA, and the PWA usually holds the keys on an
       iPhone. A push makes the transaction a pick-one-of-three one, and the number is on the
       desktop screen in front of you;
     - *Use this browser instead* below them;
   - desktop, no keys → *Approve on your phone* with this same link as a QR.
3. **The CLI** takes the same link: `poweur auth approve <link>` and `poweur key approve <link>`.

When the RP does not know the ID, `/r/CODE` stays, applying step 2 without the keys check, with a
*Which ID?* field. RPs are told to ask for the ID first or use a remembered one.

**Accepted cost:** universal links bind the store app to `poweur.net` (E19-T1 deliberately avoided
this), scoped to hosted IDs only. Self-hosted IDs keep `poweur://` and get step 2, which is one tap
more, never a dependency on our domain.

### D3 — Smaller cuts that need no protocol change — **accepted**

- **Remember the last ID** on the bridge (a plain cookie, not a secret): *Continue as
  alice.poweur.net* replaces typing it. `login_hint` still wins. When the remembered ID is the
  one signing in, the push goes out automatically as the page opens (the pick and the pause
  still apply). With E28-T12 that reaches a closed home-screen app.
- **The signer stops asking "Started on another screen?"**; the pick appears only when
  `context_uri` returns `match_choices`.
- **The bridge session** stays 12 h by default; the operator may raise it.

### D4 — Settings → "Approve sign-in request" — **accepted** (remove the entry, keep the screen)

The screen serves **both** native Poweur sign-in and the OAuth bridge (the bridge is a native
sign-in RP underneath). Its approval view, where links, QR, `poweur://`, `/id/approve` and pushed
prompts all land, stays and is titled **Sign in with Poweur**. The paste box behind the Settings
row (*Paste the sign-in code shown by the app*) is removed: its name collides with the 2-digit
code, the CLI has `poweur auth approve`, and every other route opens the screen directly.

What remains of copy and paste is the rare **response** for an RP without a `response_uri`. It gets
a title and explainer: *This app can't receive approvals directly. Copy this approval and paste it
into the app you're signing in to. Never paste it anywhere else — anyone holding it can sign in as
you there for the next few minutes.*

### D5 — The web app moves from `/app/` to `/id/`; approvals at `/id/approve` — **accepted**

`alice.poweur.net/id/` names what it is (your ID's app) and frees `/app/` from sounding like an app
store link. **Approvals live inside it, at `/id/approve`, not at a top-level `/approve`.** That is
better, not the same:

| | `/id/approve` | `/approve` |
|---|---|---|
| Installed PWA on Android opens it directly | **yes**: inside the manifest scope `/id/` | no: out of scope, opens in the browser |
| Service worker, push click-through, badge | same scope as everything else | a second scope, or none |
| Relay | nothing new: it already serves the SPA under its base | a second SPA entry with its own base path |
| Universal links / App Links | path `/id/approve` | path `/approve` (no difference) |
| Same origin as the keys | yes | yes |

Neither changes where keys are: `localStorage` and IndexedDB are per **origin**, not per path, so an
identity unlocked at `/app/` is still there at `/id/`.

**What the move costs:**

- **Installed PWAs** have `id`/`start_url`/`scope` `/app/`. A changed manifest `id` is a different
  app, so installs made since web 0.2.24 must be reinstalled. On iOS the old home-screen app's
  isolated storage goes with it, so the ID is unlocked once more in the new one. This is why the
  move goes first: those installs are hours old today.
- **The old service worker** at `/app/` keeps receiving pushes. Ship `/app/sw.js` as a kill switch
  that unregisters itself. Its push subscription dies with it, the relay gets `410` and drops the
  registration (E09-T11 already does this); the new worker at `/id/` subscribes again on unlock.
- **Links in the wild** (pairing QRs on screens, bookmarks, identity pages cached by others,
  `web_signer` advertised in capabilities, third-party bridges): `/app/*` answers `308` to
  `/id/*`. Browsers carry the fragment across a redirect, so `#pair=` links survive. Parsers keep
  accepting `/app/` forms.

### D6 — Identity root: *Sign in as alice.poweur.net* — **accepted**

`alice.poweur.net/` is the public identity page (EPIC-012). Today it turns *Message* into *Open your
inbox* when the `poweur_ids` cookie (written by the web app on the parent domain) says this browser
already uses alice. A browser that has **never** used alice sees only the visitor view, so alice on
a new laptop has no way in from her own page.

- Not recognized: the visitor view stays as it is (*Message alice*, *Get your own ID*), plus a
  quiet secondary **Sign in as alice.poweur.net** → `/id/`. There the app shows unlock if this
  origin has her keys, otherwise *Add this device* (QR for her phone), restore from recovery kit,
  or the passkey sign-in.
- Recognized: unchanged (*Open your inbox* → `/id/`).
- Another ID recognized (bob): *Message as bob* as today, and *Sign in as alice.poweur.net* still
  offered underneath.
- The page stays static and script-light: one link added server-side and hidden by `page.js` when
  the owner is recognized, so it works with scripts off.

## Result, if D1–D6 ship

| Case | Steps |
|------|-------|
| Same browser holds the keys (any RP, first time or not) | click *Sign in* → unlock → **Approve** (→ consent on the bridge, first time per app) |
| Bridge, keys in the phone's store app or Android PWA | click → *Continue as alice* / type ID → scan → app opens → unlock → **Approve** → consent |
| Bridge, keys in an iPhone home-screen app | click → *Continue as alice* → notification on phone → unlock → **tap 42** |
| Add device, scan | *Add this device* → scan → app opens → unlock → **Approve** |
| Add device, no camera (browser to browser) | type `K7QM-4XP2` into eight boxes → compare 6 digits → **They match** |
| Alice on a new laptop, from her own page | `alice.poweur.net` → **Sign in as alice.poweur.net** → *Add this device* → scan with phone → Approve |

## Tasks

### E28-T1 — Spec and threat model

- [x] `auth/sign-in.md` → *Who may complete a sign-in*: a cross-device delivery may omit `match`
      and carry `"finish": "poll"`; a transaction that has been pushed to requires `match`
      (one of `match_choices`, one attempt); a wrong match pauses pushes for that identity at
      that RP for 15 minutes; same-device unchanged
- [x] RP context gains `match_choices` (present only for pushed transactions; never marks the
      right one) and `push_uri` (the RP's *send to my app*, keyed by request code; see E28-T12)
- [x] Threat table rewritten: forwarded QR/link accepted as residual risk with the reasoning above;
      push fatigue → pick + pause; phishing page with live QR (no channel-level fix; context and
      short TTL); `push_uri` abuse (it only ever produces a pick-one-of-three prompt, rate-limited)
- [x] `/id/approve` link form, its fragment keys (`auth`, `pair`, `id`), and the same-origin checks
      (`r` must still match the request's `audience`)
- [x] Remove the paste-request transport from the spec's transport table; the paste-**response**
      transport stays for RPs without `response_uri`
- [x] `security/key-management.md`: pairing QR is the `/id/approve` link; the typed path's compare
      digits are unchanged and why

### E28-T2 — Shared helpers and vectors

- [x] Go `identity`: `ApproveLink(host, ref)`, `ParseApproveLink` (accepts the `/app/?auth=`,
      `/app/#pair=`, `/id/…`, `poweur://auth` and `poweur://pair` forms); TS twins in
      `@poweur/client`; vectors `approve-link.json`; `pairing.json` links move to `/id/`
- [x] `signin`: `ParseDelivery` / TS `finish`; `NewMatchChoices()` (correct + two distinct decoys,
      shuffled); `SignInContext.MatchChoices`, `SignInContext.PushURI`
- [x] `SIGNIN_MATCH_CODE_DIGITS` stays 2; `normalizeMatchCode` stays for CLI input

### E28-T3 — Bridge

- [x] Callback accepts cross-device approvals without `match` unless the transaction was pushed
- [x] Push: choices drawn on first send; context serves them; wrong pick → transaction failed +
      15-minute push pause for the identity + audit `native.match_failed`
- [x] `push_uri` by request code (for the phone's *Send to my home-screen app*), same limits as the
      desktop button
- [x] QR and copy carry `ApproveLink`; `/r/CODE` stays for JSON and the unknown-ID fallback;
      signer discovery falls back to `/id/` instead of `/app/`
- [x] Remembered last ID on the identify page; auto-send push when it matches
- [x] UI: no code beside the QR; after a push, *Tap **42** on your phone*
- [x] Bridge patch bump

### E28-T4 — Guestbook (reference RP)

- [x] Same rules (no code for QR, pick for push if it ever pushes; today it does not)
- [x] Uses the remembered ID (set at sign-in) so its link can be the `/id/approve` link, with no
      extra typing; `/auth/r/{code}` handoff
      mirrors the bridge's fallback

### E28-T5 — Web signer `/id/approve`

- [x] Route `/id/approve` inside the SPA; reads the fragment, survives unlock
- [x] Decision: keys here → approve; phone without keys → Android intent with fallback / iOS
      *Open in Poweur app* + *Send to my home-screen app*; desktop without keys → QR of the same link
- [x] Remove *Started on another screen?* and the code field; render three pick buttons in place
      of *Approve* when `match_choices` is present; wrong pick shows the warning
- [x] Web patch bump

### E28-T6 — Relay: app association for hosted identities

- [x] Serve `/.well-known/apple-app-site-association` (path `/id/approve` only) and
      `/.well-known/assetlinks.json` on hosted hosts and the apex, from config (team ID, package,
      signing-cert fingerprint); absent config → 404
- [ ] Verify Apple's and Android's wildcard rules (`*.poweur.net`: which host each OS fetches
      from) on real devices before relying on them; check which wins on Android when both the
      store app (verified App Link) and a PWA (WebAPK scope) claim the link
- [x] Relay patch bump

### E28-T7 — Mobile: universal links / App Links

- [x] iOS `applinks:*.poweur.net` associated domains; Android `autoVerify` intent filter for
      `https://*.poweur.net/id/approve`
- [x] Shell routes `/id/approve#auth=…` to sign-in approval and `#pair=…` to the pairing approver,
      at launch and while running (the `poweur://` routing already does both)
- [x] Self-hosted identities unchanged (`poweur://`); E19-T1 note updated
- [x] Mobile patch bump

### E28-T8 — Add device: one QR

- [x] New device shows one QR (the `/id/approve#pair=` link) and the short code; remove the
      *Approve with: app · browser · terminal* tabs
- [x] Terminal: a *Use a terminal* disclosure with `poweur key approve <link>`
- [x] Typed path unchanged in protocol (8-char code, compare 6 digits)

### E28-T9 — Segmented short-code input

The approver types the 8-character pairing code (`K7QM-4XP2`) into one plain text field today.
Replace it with the common one-time-code control:

- [x] `ui/CodeInput.tsx`: **eight cells with a dash drawn between the fourth and fifth** (the dash
      is not a cell and is never typed)
- [x] Typing a character fills the cell and moves to the next; Backspace on an empty cell moves
      back and clears it; arrow keys and clicking move between cells; typing over a filled cell
      replaces it
- [x] **Paste anywhere** fills from the first cell: accepts `K7QM-4XP2`, `k7qm4xp2`, with spaces,
      and a whole pairing link (then switches to the link path: no digits to compare)
- [x] Crockford normalization as you type: lowercase → upper, `O` → `0`, `I`/`L` → `1`;
      characters outside the alphabet are refused with a shake, not inserted
- [x] Submits on the eighth character; the error state clears on the next edit
- [x] Built as **one real `<input>` with eight painted cells**, not eight inputs: paste,
      iOS/Android one-time-code autofill, IME and screen readers behave on a single field
      (`autocomplete="one-time-code"`, `autocapitalize="characters"`, `spellcheck=false`,
      `aria-label="Pairing code, 8 characters"`)
- [x] Unit tests (paste forms, normalization, backspace, refusal); Playwright on the typed
      pairing path in both themes and at phone width

### E28-T10 — "Sign in with Poweur" screen

- [x] Remove Settings → *Approve sign-in request* and the paste box; the route stays for links,
      deep links, `/id/approve` and pushed prompts (Messages → Sign-in requests)
- [x] Title **Sign in with Poweur** throughout (loading, request, result)
- [x] Response-copy result for RPs without `response_uri`: title, explainer and warning as in D4;
      the word "code" not used for the long value
- [x] Web patch bump (with E28-T5)

### E28-T11 — CLI

- [x] `poweur auth approve <link>` needs no `--code`; `--code` remains for pushed prompts and
      prints the three choices when omitted
- [x] CLI patch bump

### E28-T12 — Web Push for sign-in requests

E09-T11 left this kind open. A `sys.auth.request` admitted under `trusted_auth_services` today
reaches only an open app (SSE); with this it reaches a closed home-screen app.

- [x] Relay: `auth` in `pushKinds`, `Urgency: high`, short `TTL` (the request's remaining life),
      its own `Topic` so it does not collapse into the message count; never skipped by the
      message coalescer
- [x] Payload stays content-free (`{v, identity, kind: "auth", ts}`): notification *Sign-in
      request waiting · alice.poweur.net*, no app or person named (E09-T12's rule)
- [x] `sw.js`: `notificationclick` for `auth` opens `/id/?open=signin`, which lands on the newest
      pending request after unlock; the pick-one-of-three screen follows
- [x] Store app: same kind through E19-T4's APNs/FCM transports when they land
- [x] Relay + web patch bumps

### E28-T13 — Move the web app from `/app/` to `/id/` (first)

- [x] Web: Vite `base` `/id/`; manifest `id`/`start_url`/`scope` `/id/`; `sw.js` at `/id/sw.js`;
      every in-app link and `?open=` target
- [x] `/app/sw.js` kill switch: unregisters itself (the relay drops the dead subscription on `410`)
- [x] Relay: serve the SPA and per-host manifest (`relay/pwa.go`) at `/id/`; `/app/*` → `308`
      `/id/*` with the query; `web_signer` capability and identity-page links point at `/id/`;
      `Link`/IndieAuth and launcher links updated
- [x] Identity package and SDK: `PairingLink`, `SignInReferenceDeepLink` web forms, identity app
      URLs; parsers accept both paths; vectors updated
- [x] Bridge (`discoverSigners` fallback), guestbook, CLI output, mobile shell routing, `apps/site`,
      Caddy and compose files, docs
- [x] Reinstall notice: the retired `/app/` worker sends open windows to `/id/?moved=1`, and the
      app says to add it to the home screen again (a toast, rather than detecting the old
      `start_url`, which a page cannot read)
- [x] Tests: redirect keeps query and fragment (Playwright), old pairing and `?auth=` links still
      work, kill-switch worker unregisters, manifest at `/id/`
- [x] Patch bumps: relay, web, SDK, CLI, bridge, mobile

### E28-T14 — Identity root: *Sign in as alice.poweur.net*

- [x] `page.html`: secondary **Sign in as {{.Identity}}** link to `/id/` (server-rendered, works
      without scripts)
- [x] `page.js`: hidden when the owner is recognized (*Open your inbox* already shows); kept under
      *Message as bob* when another ID is recognized
- [x] `/id/` without this origin's keys leads with *Add this device* (QR for the phone), then
      recovery kit and passkey sign-in, so the link never lands on a claim form for a taken name
- [x] Tests: `identity_page_test.go` (link present, escaped), Playwright for the three cookie states
- [x] Relay patch bump (assets are embedded in the relay)

### E28-T15 — Tests

- [x] Go unit: cross-device without `match` accepted; pushed transaction requires `match`; wrong
      pick fails and pauses pushes; decoys distinct and never revealed; `push_uri` limits
- [x] Integration (`apps/integration`): `TestINT_OAUTH_02` with the CLI as the phone and no code;
      `TestINT_OAUTH_03` with a pick (right → signed in; wrong → failed + paused);
      `TestINT_WEBPUSH_01` extended with an `auth` push
- [x] Playwright: camera path to `/id/approve` with and without keys in the browser; add device
      with no chooser and with the segmented input; no Settings paste row; `/app/` redirects;
      identity root sign-in link; link-builder unit tests for the Android intent (not drivable in
      Playwright)
- [ ] Device checklist (with E09-T12's): iPhone home-screen app approves a bridge sign-in from a
      push; Android QR opens the installed PWA at `/id/approve`

## Non-goals

- Removing the add-device compare digits on the typed path (they are the relay-tamper check)
- Universal links for self-hosted domains (the store app cannot list them)
- Proximity proof (BLE, as in FIDO hybrid): desktop browsers cannot advertise it
- Opening an installed iOS PWA from Safari: no platform API exists; push is the route
