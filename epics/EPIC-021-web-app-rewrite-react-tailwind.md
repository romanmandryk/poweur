# EPIC-021 — Web app rewrite: React + Tailwind, side by side at `/newapp/`

- **Status:** in progress — T1, T3–T7 done; T2 done pending first production deploy; T8–T11 (contacts, files, launcher, settings) next
- **Priority:** P1 (the string-templated shell is the source of the flicker, focus and
  event-rebinding bugs, and every EPIC-015 screen added makes it worse)
- **Depends on:** [EPIC-015](EPIC-015-web-app-ux.md) (the screens being rewritten),
  [EPIC-017](EPIC-017-typescript-client-sdk.md) (`@poweur/client`, consumed unchanged),
  [EPIC-019](EPIC-019-mobile-app-capacitor.md) (the Capacitor shell must wrap the new build)
- **Unlocks:** EPIC-015 T7–T12 and E19-T3/T4 built on components instead of string
  templates; EPIC-012 reusing real components

## Progress

| Task | Status | Notes |
|------|--------|-------|
| E21-T1 Scaffold `apps/web-next` (Vite, React 19, TS, Tailwind v4) | **done** | Vite 8, React 19.3 + Compiler (Babel preset), Tailwind 4.3, 69 KB gz placeholder bundle; ESLint replaced by source-guard tests |
| E21-T2 Relay serves `/newapp/` beside `/app/` + prod wiring | **done** (deploy unverified) | relay 0.1.7; image builds locally with `/web-next` baked in; confirm on prod after merge |
| E21-T3 Non-UI modules carried over verbatim | **done** | 12 modules + 15 test files (176 tests green); legacy↔next storage compat + byte-identical guard in `test/lib/legacy-compat.test.js` |
| E21-T4 State store, routing & app shell | **done** | Zustand `route` / `session` / `data` / `ui` stores; shell, boot, overlays, back nav; every unported screen is a labelled `NotPorted` stand-in linking to `/app/` |
| E21-T5 Design system: tokens + primitives | **done** | tokens from `style.css` in `index.css`; `src/ui/*`; IdentityInput, ProfileCard, AudiencePicker, PolicyControls in React; 257 tests green. Bundle now 144 KB gz (React + Radix + `@poweur/client` crypto) — above the 70–90 KB estimate, recheck in T13 |
| E21-T6 Front doors & gates | **done** (unit-verified; e2e in T12) | landing + claim card, identity door, relay prompt, add-id, unlock, claim + DNS claim, onboarding, sign-in approval, join device; `actions/` hold the ported identity lifecycle. 304 tests green. Bundle 195 KB gz (argon2 / bip39 / keystore now reachable) |
| E21-T7 Messages destination & conversation | **done** (unit-verified; e2e in T12) | trays, conversation rows, new chat, thread view, push stream, archive, read marks, signed / group / attachment send, offline outbox, key-change dialog; `actions/messages.ts` + `actions/contacts.ts`. 327 tests green. Bundle 210 KB gz |
| E21-T8 Contacts destination | open | |
| E21-T9 Files destination & sharing | open | |
| E21-T10 Launcher & claim | open | |
| E21-T11 Settings & every panel | open | |
| E21-T12 E2E parity: one Playwright suite, both apps | open | the cutover gate |
| E21-T13 Capacitor shell on the new build | open | |
| E21-T14 Cutover: `/app/` serves the new app, legacy deleted | open | |

**Resuming after an interruption:** check the table above and the checkboxes below; the first
unchecked box in the lowest-numbered open task is the next thing to do. T6–T11 are independent
of each other once T1–T5 are done. The per-screen parity checklist in T12 is the ground truth
for "is this screen finished" — a screen is done when its specs pass against `/newapp/`.

## Goal

Replace the vanilla-JS SPA in `apps/web` with a React + Tailwind app of **identical
behavior**, built in one pass rather than migrated screen-by-screen inside the old shell.
Host it at `<host>/newapp/` on the same relays, next to the current `<host>/app/`, so both can
be compared on real identities. When parity is reached, `/app/` switches to the new build and
the legacy app is deleted in one change.

This epic is a **rewrite for parity, not a redesign**. New product behavior (EPIC-015
T7–T12 open items, E19-T3/T4) lands after cutover, on the new app. During the rewrite the
legacy app is feature-frozen: bug fixes only, and each one is mirrored in `apps/web-next`.

## Background

### Why the current app flickers

[`apps/web/js/app.js`](../apps/web/js/app.js) (~5,700 lines) holds all UI state in two
mutable globals, `S` (data) and `R` (in-memory route: `page`, `sub`, `params`). Every state
change calls `render()`, which does `app.innerHTML = …` for the **whole app** and then
`attachEvents()` re-binds ~126 listeners (55 call sites trigger it). Every button and input is
destroyed and recreated on each change, so:

- hover/active states and CSS transitions restart → the visible flicker
- focus is lost and put back by hand (`focusSelector()`), scroll and input state are fragile
- panels (`showPanel()` and ~20 `show*Panel()` callers) live outside `#app` in `#panel-root`
  with their own ad-hoc lifecycle

[`apps/web/css/style.css`](../apps/web/css/style.css) is ~2,100 hand-written lines.
`js/components/dom.js` already builds DOM safely via `el()` for the three shared components —
the right idea, without a reconciler.

### What must not change

| Constraint | Where it comes from |
|------------|--------------------|
| **Same origin as `/app/`** → the new app shares `localStorage` (`poweur:active`, `poweur:config`, `poweur:identity:*`, `poweur:session:*`, `poweur:root`, `poweur:theme`), `sessionStorage` and passkeys (`rp.id` is the host). Formats must stay byte-compatible so one identity works in both apps. | `js/storage.js`, `js/passkey.js`, `js/vault.js` |
| **Relative asset paths**: the same build must work at `/app/`, `/newapp/`, a static `/` and `capacitor://localhost/` | `apps/mobile/scripts/stage-web.mjs`, `test/vendor.test.js` |
| **Self-contained, no CDN**: offline and self-hosted installs | EPIC-015 E15-T6 |
| **Host-aware modes** (`launcher` / `identity` / `shell` / unknown) | `js/mode.js`, E15-T7 |
| **Deep links**: `#claim=…`, `?auth=…` | `app.js` (`#claim=` handling, `signin.js`) |
| **No HTML built from strings** containing user or remote data | `js/components/dom.js` rationale |
| **Tests against a real Go relay** | AGENTS.md → Web client tests |

### Hosting today

Prod mounts the checkout's `apps/web` read-only into the relay container
(`docker-compose.prod.yml`: `./apps/web:/web:ro`, `WEB_STATIC_DIR=/web`); the relay serves it
under `/app/` ([`apps/api/internal/relay/webstatic.go`](../apps/api/internal/relay/webstatic.go)),
with SPA fallback to `index.html`. Caddy forwards `poweur.net` and `*.poweur.net` wholesale to
the relay, so a new path on the relay needs **no Caddy change**. The deploy does
`git reset --hard` + `docker compose up` — there is no Node build on the VM, which is why the
new app's build has to be produced in CI (T2).

## Design direction

| Concern | Choice | Why |
|---------|--------|-----|
| Language / build | **TypeScript + Vite**, `base: "./"` | typed end to end with `@poweur/client`; relative base keeps one build valid at every mount point |
| UI | **React 19** with the React Compiler | reconciler removes the full-page re-render; largest ecosystem; what agents write most reliably |
| Styling | **Tailwind CSS v4** (`@tailwindcss/vite`), **no hand-written CSS files** beyond one `index.css` holding `@import "tailwindcss"`, `@theme` tokens and the `dark` variant | consistency through tokens, no dead CSS |
| Primitives | **Radix UI** (dialog, dropdown, tabs, toast, switch) wrapped as local components in `src/ui/` (shadcn-style: owned source, not a component-library dependency) | accessible focus-trap/escape/aria for free; panels become real dialogs/sheets |
| State | **Zustand** stores (`session`, `route`, `messages`, `contacts`, `files`, `ui`) | maps 1:1 onto today's `S`/`R`, so porting is mechanical; selectors re-render only what reads the slice |
| Routing | **In-memory route store** mirroring `R` (`page`, `sub`, `params`) — no router library, no URL paths | parity: the current app has no URL routing; deep links stay hash/query based |
| Async / relay | plain async actions in stores calling the existing `js/client.js` seam | the protocol stays in `@poweur/client`; no query-cache library until a real need appears |
| Class merging | `clsx` + `tailwind-merge` via one `cn()` helper | variants without string soup |
| Icons | `lucide-react` (tree-shaken) | replaces emoji icons consistently |
| Tests | Vitest + React Testing Library (unit), the **existing Playwright suite** parameterized by app path (e2e) | parity is proven by the same specs, not new ones |

Dependencies are pinned in the lockfile; nothing is loaded from a CDN at runtime.
`dangerouslySetInnerHTML` is banned (lint rule).

---

## Tasks

### E21-T1 — Scaffold `apps/web-next`

- [x] `apps/web-next/` as pnpm workspace package `@poweur/web-next`, version `0.1.0`
- [x] Vite + React 19 + TypeScript (strict) + React Compiler (`reactCompilerPreset` via
      `@rolldown/plugin-babel`; `@babel/core` pinned to 7 because `apps/docs` Docusaurus needs it)
- [x] Tailwind v4 via `@tailwindcss/vite`; `src/index.css` with `@theme` tokens and
      `@custom-variant dark` keyed on `[data-theme="dark"]` (what `poweur:theme` sets today)
- [x] `vite.config.ts`: `base: "./"`, output `dist/`, no external runtime URLs
- [x] `@poweur/client` as a `workspace:*` dependency (bundled — no vendor copy, no import map)
- [x] ~~ESLint~~ → source-guard tests instead (fewer deps): `test/scaffold.test.tsx` bans
      `dangerouslySetInnerHTML` / `innerHTML =` / `insertAdjacentHTML` and any `.css` besides
      `index.css`; `test/lib/origin.test.js` now scans all of `src/` for `location.origin`
- [x] `src/build-info.ts` with `APP_VERSION` / `APP_BUILD_TIME` (test asserts it matches package.json)
- [x] Root scripts: `web-next:dev`, `web-next:build`, `web-next:typecheck`, `web-next:test`
      (`web-next:test:e2e` arrives with T12)
- [x] Viewport meta, `viewport-fit=cover`, `pt-safe` / `pb-safe` utilities; theme applied before
      first paint from `poweur:theme`

**Acceptance:** `pnpm web-next:build` emits `dist/index.html` whose asset references are all
relative; `pnpm web-next:test` runs green on an empty smoke test.

### E21-T2 — Relay serves `/newapp/` beside `/app/`; prod wiring

- [x] Generalize `mountWebStatic(mux, dir)` → `mountWebStatic(mux, prefix, dir)`; mount
      `/app/` from `WEB_STATIC_DIR` (unchanged) and `/newapp/` from new
      `WEB_NEXT_STATIC_DIR` (unset → not mounted)
- [x] Go unit tests (`internal/relay/webstatic_test.go`): `/newapp` → 301 `/newapp/`, SPA
      fallback, path traversal rejected, unset dir mounts nothing, `/app/` behavior unchanged
- [x] `GET /` document lists `web_ui_next`
- [x] Bump relay patch version (`apps/api/internal/buildinfo.Version` 0.1.6 → 0.1.7)
- [x] **Prod build:** `web` Node stage in `apps/api/Dockerfile` (pnpm 10, frozen lockfile)
      builds `@poweur/client` + web-next and copies `dist/` to `/web-next`; `.dockerignore`
      admits the workspace manifests, `packages/client-ts`, `apps/web-next`;
      `WEB_NEXT_STATIC_DIR: /web-next` in `docker-compose.prod.yml`. Legacy
      `./apps/web:/web:ro` mount stays until T14. CI gains a `web-next` job.
- [x] Web test helper `startRelay()` also sets `WEB_NEXT_STATIC_DIR` to `apps/web-next/dist`
- [x] `deploy/OPS.md`: note that `/newapp/` is the preview build
- [x] `.claude/launch.json` → `relay-web-next`: local relay on :8088 serving both apps
- [ ] Verify on production after the first deploy: `https://poweur.net/newapp/` and
      `https://<handle>.poweur.net/newapp/` load

**Acceptance:** after a deploy, `https://poweur.net/newapp/` and
`https://<handle>.poweur.net/newapp/` serve the new build while `/app/` is untouched; an
identity unlocked in `/app/` is visible (and unlockable) in `/newapp/` in the same browser.

### E21-T3 — Non-UI modules carried over verbatim

These already contain no DOM code and are covered by Vitest. Move, don't rewrite.

- [x] Copy to `apps/web-next/src/lib/` **byte-identical** (no `.ts` conversion yet — the guard
      below would flag it): `storage`, `vault`, `passkey`, `native`, `keystore`, `client`,
      `mode`, `profiles`, `threads`, `outbox`, `signin`, `enroll-wait`
- [x] Copy their unit tests to `test/lib/` and keep them green; helpers (`relay`, `identity`,
      `browser-globals`) copied to `test/helpers/`. `fingerprint` imports the bundled
      `@poweur/client` instead of `vendor/`. `devices.test.js` is a component test → T5
- [x] **Compatibility test** (`test/lib/legacy-compat.test.js`): identity / active / config
      written by either app reads back in the other, same bytes; removal is shared
- [x] **Freeze guard:** same file asserts each carried module is byte-identical to
      `apps/web/js/` — a legacy fix not mirrored here fails CI
- [x] ~~Hardcoded `/app/` links~~ → done in **T6** (`lib/claim.ts`): the identity door's "Claim a
      different name" and the `#claim=` hand-off URL both use the current mount path

**Acceptance:** all copied unit tests pass under `apps/web-next`; cross-app storage
compatibility test passes.

### E21-T4 — State store, routing & app shell

- [x] Zustand stores replacing `S` field-for-field: `state/data.ts` (per-identity slices +
      `resetForIdentity`), `state/session.ts` (`identity`, `config`, `unlocked` mirror of the
      key store, `mode`), `state/ui.ts` (theme, toasts, loading, panel). `dropdownOpen` is gone —
      Radix owns the menu's open state
- [x] Route store replacing `R` (`state/route.ts`): `go`, `push`, `pop` (pop of `thread` clears
      the open conversation); `DETAIL_SUBS` render in `#detail-pane` beside the list at ≥1024px,
      full-screen below — same breakpoints as the legacy CSS
- [x] `shell/App.tsx` picks: gate sub-page → front door → header + page + detail pane + nav, and
      the Welcome / Locked gating per destination
- [x] Header (wordmark, theme toggle, `#id-pill` + Radix dropdown with `[data-switch]` /
      `#dd-add-id`, `#btn-add-id-header`); bottom nav → rail (768px) → sidebar (1024px) with
      badges from `state/badges.ts` (`incomingRequests`, `unreadAnonymous`, `navBadges`)
- [x] Overlays (`shell/Overlays.tsx`): `toast()` with same-message dedupe, `setLoading()`,
      `openPanel(title, render, onClose)` on a Radix dialog keeping `#panel-root` /
      `#panel-title` / `#panel-close-btn`; bottom sheet on a phone, centred dialog from 768px
- [x] Boot parity (`shell/boot.ts`, runs before first render): `?auth=` → settings/auth,
      `#claim=` hand-off adopted and stripped → unlock, no keys and no valid session → unlock,
      `resolveMode()` corrects the door, title and meta description
- [x] ~~SSE/poll loops started once, outbox retry~~ → done in **T7** (`shell/useMessaging.ts`)
- [x] Back: Escape pops a *detail* sub-page (gates have nowhere to go back to), Radix handles
      Escape in panels; Capacitor `backButton` closes the panel, then pops
- [x] Unported destinations / sub-pages / doors render `screens/NotPorted.tsx` — keeps
      `.dest-title` and `#btn-back`, links to the same screen in `/app/`

**Acceptance:** ✅ unit-level (`test/shell/app.test.tsx`): switching all five destinations keeps
the same `.app-header`, `.bottom-nav` and tab nodes; a focused control survives a data update.
The Playwright version (element handles across a live message) moves to **T12**, since it needs
T6's registration screens to reach a signed-in shell.

### E21-T5 — Design system: tokens + primitives

- [x] Tokens from `css/style.css` into `@theme` (`src/index.css`): surfaces, text (`fg` /
      `muted` / `faint`), `sep`, `accent` (#5856D6) + soft, status colours, radii (`card` 16,
      `button` 14, `control` 10), shadows, animations; dark set swapped under `[data-theme=dark]`;
      `h-header` / `h-nav` / `pt-safe` / `pb-safe` utilities. `cn()` teaches tailwind-merge the
      token names
- [x] `src/ui/`: `Button` (primary / passkey / ghost / secondary / danger / link; `sm`) +
      `IconButton`; `Field` (`Input`, `Textarea`, `Label`, `FormGroup`, `Note`, `CheckRow`);
      `Avatar`; `Display` (`Chip`, `CountBadge`, `SectionLabel`, `EmptyState`, `Spinner`,
      `Skeleton`, `Notice`); `Layout` (`DestHeader`, `SubPage`, `SettingsGroup`, `SettingsRow`);
      `Sheet`. Legacy class names stay on elements as test hooks only (`btn-primary`,
      `.idin-status`, `.nav-tab`…) — no CSS targets them
- [ ] ~~`Select`, `Switch`, `Tabs`, `Toast` primitives~~ → built when a screen needs one (T7 trays,
      T11 settings); toasts are a store + `Toaster`, not Radix Toast (same-message dedupe)
- [x] Port shared components to React: `IdentityInput` (uncontrolled input so `lookup()` stays
      synchronous; debounce/token races preserved), `ProfileCard`, `AudiencePicker`,
      `PolicyControls`; imperative handles via `ref` match the legacy return objects. Pure
      helpers split out: `lib/identity.ts`, `lib/policy.ts`, `lib/custody.ts`; `devices.js`
      carried byte-identical (added to the freeze guard)
- [x] Component tests (RTL) mirroring `test/components.test.js` — every case ported, plus
      default-domain completion, Enter-submits, disabled action on error, cached paint,
      anonymous toggle; `credentialRpId` cases moved to `test/lib/passkey-rpid.test.js`
- [x] No `.css` files except `src/index.css` (guard test)
- [x] Found and fixed on the way: the panel's `aria-labelledby` pointed at Radix's generated
      id while the title kept `#panel-title` — the dialog had no accessible name

**Acceptance:** screens in T6–T11 compose `src/ui/` + Tailwind. `style=` appears only for the
per-identity avatar colour (a runtime value), so that part of the grep criterion is relaxed.

### E21-T6 — Front doors & gates

- [x] Front door by mode (`screens/registry.tsx`): `launcher` / `shell` → `doors/Landing.tsx`
      (hero, claim card, how-it-works, `#opt-have-id`, `#opt-own-domain`); `identity` →
      `doors/IdentityDoor.tsx` (checking / claimed / claimable / unavailable / offline, probe in
      `actions/door.ts`); `unknown` → Welcome; Locked from T4
- [x] Claim card (`doors/ClaimCard.tsx`): skeleton until probed, suffix as fixed text / picker /
      typed input by hosted-domain count, pasted-FQDN normalization, debounced availability with
      the relay's own wording, PRF / native-custody note (`#claim-prf-required`); `DnsClaimCard`
      for the self-hosted path. Fields stay uncontrolled so a relay refusal can put the attempt back
- [x] Relay prompt (`doors/RelayPrompt.tsx`): production / local / typed URL, `/health` probe
      before saving, re-resolves the mode; shown in the shell or when no relay is known
- [x] Sub-pages: `AddId` (options from `addIdOptions`), `Unlock` (PRF passkey or native
      keystore, `returnTo`), `Claim` (moved here from T10 — the landing is where it starts),
      `Onboarding` (policy → profile → done; Continue saves, a failed save stays put; Skip
      writes nothing) with a React `ProfileEditor`, `SignInApproval` (paste → verify → unlock
      or approve → copy / continue); boot starts approval for `?auth=`
- [x] Join-device panel (`screens/JoinDevice.tsx`): known subject skips the form, rendezvous
      code + SAS, poller from `enroll-wait.js`, closing the panel cancels the rendezvous,
      approval adopts the seed
- [x] Actions ported from `app.js` into `src/actions/` (`identity`, `door`, `signin`,
      `account`); `afterUnlock()` / `onUnlocked()` is the hook T7 hangs inbox loading on
- [x] Links built from the mount path (`lib/claim.ts`: `identityAppUrl`, `launcherAppUrl`) — the
      hand-off and "Claim a different name" stay in `/newapp/` when started there
- [x] Unit tests: `test/screens/{doors,gates,join-device}.test.tsx`, `test/lib/claim.test.ts`

**Acceptance:** unit-level ✅. The Playwright specs move to **T12** with `APP_PATH`; known
dependencies there: `onboarding.spec` goes on to contacts / messages / files (T7–T9) and its
skip test imports `./js/client.js` (needs a web-next equivalent); `enrollment.spec`'s approving
side is the Keys & devices panel (T11); `native-custody.spec`'s last two cases need Settings (T11).

### E21-T7 — Messages destination & conversation

- [x] Trays (`screens/messages/Messages.tsx`): inbox / requests / anonymous with `.tray-badge`
      counts (requests waiting, anonymous unread); FAB `#btn-compose` on a phone, `#btn-compose-top`
      from 768px; each tray keeps its empty state
- [x] Conversation rows from `buildConversationRows`: petname, group chip, `#thread` label,
      preview (expiry countdown included), unread badge from read marks, attachment "Open",
      one-tap Add for strangers; rows are `role=button` and answer Enter / Space
- [x] Requests tray: incoming (ProfileCard, intro, Accept / Block) and outgoing (Requested,
      Cancel); answering drops the request immediately. Anonymous tray: no sender, no avatar,
      no reply; looking at it marks it read; "turned off" links to Settings
- [x] New chat (`NewChat.tsx`): IdentityInput with the identity's domain as default; a group is
      detected by its roster answering
- [x] Thread view (`Thread.tsx`): newest 10 + "Load more (n earlier)", day separators, ticks
      from acks (sent / delivered / read / failed, none for groups), group sender names, expiry
      countdown, attachment send (file picker, typed text as caption) and open, Cmd/Ctrl+Enter
      sends. Draft, page size and scroll are component state, so a live message changes nothing
      the reader is doing; keyed per conversation so they do not leak between threads
- [x] Actions (`actions/messages.ts`, `actions/contacts.ts`, `actions/relay.ts`): inbox drain +
      archive with single-flight and forced re-drain, group-label verification against the
      signed roster, history restore (anonymous split out), anonymous drain, push stream
      (`ready` / `request` / `anon` / message), read marks + read receipts per policy, signed /
      group / attachment send keeping our own copy, pin check with the key-changed dialog
      (`components/KeyMismatchDialog.tsx`), contact accept / block / request / remove,
      auto-promotion when our request is accepted, all challenge reads serialized
- [x] Outbox: a retryable failure queues the sealed message (`outbox.js`); retried on unlock and
      on the stream's `ready`
- [x] Lifecycle (`shell/useMessaging.ts`): unlocking pulls everything and holds the stream on any
      destination; locking / switching stops it; returning to the tab re-drains. Replaces the
      legacy per-render loads
- [x] Unit tests: `test/actions/messaging.test.tsx`, `test/screens/messages.test.tsx`
      (`test/helpers/fake-client.ts` is a PoweurClient-shaped fake)

**Acceptance:** unit-level ✅, including "a message arriving while open keeps the draft, the
input node and focus". Playwright moves to **T12**. Needs there: `conversation`, `nav-badges`
and `durability` send and read through `page.evaluate(import("./js/client.js"))`, which a bundled
app does not serve — T12 needs a test-only seam (e.g. `window.__poweurTest`); `messaging.spec`
drives modules directly and checks the vendored import map, so it stays a legacy-only spec.

### E21-T8 — Contacts destination

- [ ] List with filter, contact rows, contact panel, add-contact panel (preset from a message)
- [ ] Requests handling, key pin

**Acceptance:** `contacts` spec passes against `/newapp/`.

### E21-T9 — Files destination & sharing

- [ ] Explorer (owner picker, folders, chunked upload with progress, download, delete)
- [ ] Share panel (AudiencePicker, grants, expiry), shares panel, received shares
- [ ] Changes-feed refresh without resetting the view

**Acceptance:** `files`, `sharing` specs pass against `/newapp/`.

### E21-T10 — Launcher & claim

- [ ] Launcher destination (`renderLauncher`)
- [x] ~~Claim flow and DNS claim screens reached from the launcher~~ → built in **T6**

**Acceptance:** `launcher`, `journeys` specs pass against `/newapp/`.

### E21-T11 — Settings & every panel

- [ ] Settings page sections, About (versions: app, SDK, relay)
- [ ] Panels: policy, profile, connected apps, keys & devices, approve device, recovery kit,
      identity keys, lookup, relay, session, DNS, analytics
- [ ] Theme toggle persisted to `poweur:theme`

**Acceptance:** `policy`, `analytics`, `destinations` specs pass against `/newapp/`; every
`show*Panel` in legacy `app.js` has a checked counterpart below.

### E21-T12 — E2E parity: one Playwright suite, both apps

- [ ] `APP_PATH` env (default `/app/`) used by `test/helpers/app-ui.mjs` and every spec that
      builds a URL (`messaging`, `modes`, `launcher`, `enrollment`, …)
- [ ] Selectors: prefer role/label; where specs use ids / `data-*`, the new app keeps them
- [ ] `pnpm web:test:e2e:next` runs the suite with `APP_PATH=/newapp/`
- [ ] CI runs both until T14
- [ ] New specs only for things legacy could not do: DOM-identity (no flicker) check, focus
      retention during live updates

**Parity checklist** (a spec passing against `/newapp/` checks its box):

- [ ] analytics · [ ] attachments · [ ] contacts · [ ] conversation · [ ] destinations
- [ ] durability · [ ] enrollment · [ ] files · [ ] hosted · [ ] journeys · [ ] launcher
- [ ] messaging · [ ] modes · [ ] multi-relay · [ ] native-custody · [ ] nav-badges
- [ ] onboarding · [ ] policy · [ ] sharing

**Acceptance:** the full Playwright suite is green against both `/app/` and `/newapp/`.

### E21-T13 — Capacitor shell on the new build

- [ ] `apps/mobile/scripts/stage-web.mjs` stages `apps/web-next/dist` behind a flag
      (`WEB_SOURCE=next`), keeping the relative-path check
- [ ] Verify on iOS simulator: relay prompt → create (native keystore) → unlock → send/receive;
      Android `:app:assembleDebug` green
- [ ] Safe areas, keyboard overlap in thread view, hardware back (Android) closes sheet → pops

**Acceptance:** the E19-T1 simulator walkthrough passes on the new build.

### E21-T14 — Cutover: `/app/` serves the new app, legacy deleted

Only after T12 is fully green and a manual side-by-side pass on production.

- [ ] Delete legacy `apps/web` contents; move `apps/web-next` → `apps/web`
      (package name back to `@poweur/web`, version above the last legacy version)
- [ ] Relay: `/app/` serves the new build; `/newapp/` → 301 `/app/` (keep for one release,
      then remove `WEB_NEXT_STATIC_DIR`); bump relay patch
- [ ] Prod: drop the `./apps/web:/web:ro` mount; image carries the built app at `/web`
- [ ] Mobile: stage from the built app by default, remove `WEB_SOURCE` flag; bump mobile patch
- [ ] Remove `web:vendor` scripts and `apps/web/vendor/` (the bundler consumes
      `@poweur/client` directly); update AGENTS.md ("no bundler" section, vendor rule), README,
      CLAUDE.md, EPIC-019's staging notes
- [ ] E2E: drop `APP_PATH` dual run
- [ ] Mark this epic complete; re-point EPIC-015 T7–T12 at the new code

**Acceptance:** production `/app/` runs the React app; `apps/web` contains no legacy JS/CSS;
`pnpm web:test:all`, `apps/integration` and `pnpm client:test` are green.

## Risks

| Risk | Mitigation |
|------|------------|
| New app corrupts an identity record shared with `/app/` | T3 moves storage modules verbatim + cross-app compatibility test; do not change record formats in this epic |
| "One-shot" misses behavior buried in 5,700 lines | parity is defined by the existing e2e suite (T12), plus the `show*Panel` / `render*` checklists in T6–T11 |
| Bundle size in the shell | React + Radix + Zustand ≈ 70–90 KB gz; acceptable for a bundled app; check in T13 |
| Legacy keeps changing during the rewrite | feature freeze on `apps/web`; bug fixes mirrored in `apps/web-next` in the same change set |
