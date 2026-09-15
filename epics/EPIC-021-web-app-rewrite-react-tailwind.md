# EPIC-021 — Web app rewrite: React + Tailwind, side by side at `/newapp/`

- **Status:** proposed
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
| E21-T1 Scaffold `apps/web-next` (Vite, React 19, TS, Tailwind v4) | open | |
| E21-T2 Relay serves `/newapp/` beside `/app/` + prod wiring | open | relay patch bump |
| E21-T3 Non-UI modules carried over verbatim | open | storage format must stay byte-compatible |
| E21-T4 State store, routing & app shell | open | replaces `S`, `R`, `render()`, `attachEvents()` |
| E21-T5 Design system: tokens + primitives | open | replaces `css/style.css` |
| E21-T6 Front doors & gates | open | |
| E21-T7 Messages destination & conversation | open | |
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

- [ ] `apps/web-next/` as pnpm workspace package `@poweur/web-next`, version `0.1.0`
- [ ] Vite + React 19 + TypeScript (strict) + React Compiler (babel plugin)
- [ ] Tailwind v4 via `@tailwindcss/vite`; `src/index.css` with `@theme` tokens and
      `@custom-variant dark` keyed on `[data-theme="dark"]` (what `poweur:theme` sets today)
- [ ] `vite.config.ts`: `base: "./"`, output `dist/`, no external runtime URLs
- [ ] `@poweur/client` as a `workspace:*` dependency (bundled — no vendor copy, no import map)
- [ ] ESLint: react-hooks, react-compiler, ban `dangerouslySetInnerHTML`
- [ ] `src/build-info.ts` with `APP_VERSION` / `APP_BUILD_TIME` (shown in Settings → About)
- [ ] Root scripts: `web-next:dev`, `web-next:build`, `web-next:test`, `web-next:test:e2e`
- [ ] Viewport meta, `viewport-fit=cover`, safe-area insets as Tailwind utilities

**Acceptance:** `pnpm web-next:build` emits `dist/index.html` whose asset references are all
relative; `pnpm web-next:test` runs green on an empty smoke test.

### E21-T2 — Relay serves `/newapp/` beside `/app/`; prod wiring

- [ ] Generalize `mountWebStatic(mux, dir)` → `mountWebStatic(mux, prefix, dir)`; mount
      `/app/` from `WEB_STATIC_DIR` (unchanged) and `/newapp/` from new
      `WEB_NEXT_STATIC_DIR` (unset → not mounted)
- [ ] Go unit tests: `/newapp` → 301 `/newapp/`, SPA fallback, path traversal rejected,
      unset dir mounts nothing, `/app/` behavior unchanged
- [ ] `GET /` capabilities list mentions `web_ui_next` when mounted
- [ ] Bump relay patch version (`apps/api/internal/buildinfo.Version`)
- [ ] **Prod build:** CI builds `apps/web-next` and bakes `dist/` into the relay image
      (new Node stage in `apps/api/Dockerfile`, copied to `/web-next`), set
      `WEB_NEXT_STATIC_DIR: /web-next` in `docker-compose.prod.yml`. (Baking avoids committing
      build output; the legacy `./apps/web:/web:ro` mount stays until T14.)
- [ ] Web test helper `startRelay()` also sets `WEB_NEXT_STATIC_DIR` to `apps/web-next/dist`
- [ ] `deploy/OPS.md`: note that `/newapp/` is the preview build

**Acceptance:** after a deploy, `https://poweur.net/newapp/` and
`https://<handle>.poweur.net/newapp/` serve the new build while `/app/` is untouched; an
identity unlocked in `/app/` is visible (and unlockable) in `/newapp/` in the same browser.

### E21-T3 — Non-UI modules carried over verbatim

These already contain no DOM code and are covered by Vitest. Move, don't rewrite.

- [ ] Copy to `apps/web-next/src/lib/` with minimal changes (types via JSDoc → `.ts` only
      where free): `storage`, `vault`, `passkey`, `native`, `keystore`, `client`, `mode`,
      `profiles`, `threads`, `outbox`, `signin`, `enroll-wait`
- [ ] Copy their unit tests (`storage`, `vault`, `passkey`, `native`, `keystore`,
      `keystore-relay`, `client-relay`, `mode`, `profiles`, `threads`, `outbox`, `signin`,
      `enroll-wait`, `fingerprint`, `origin`, `devices`) and keep them green
- [ ] **Compatibility test:** a record written by legacy `apps/web/js/storage.js` is read by
      the new module and vice versa (identity, session, config, active, theme)
- [ ] Hardcoded `/app/` links (`app.js` "Claim a different name", the `#claim=` join URL) are
      built from the **current mount path**, so links opened from `/newapp/` stay in `/newapp/`

**Acceptance:** all copied unit tests pass under `apps/web-next`; cross-app storage
compatibility test passes.

### E21-T4 — State store, routing & app shell

- [ ] Zustand stores replacing `S` field-for-field (`config`, `identity`, `messages`, `acks`,
      `tray`, `contacts`, `history`, `thread`, files state, `dropdownOpen` → local UI state)
- [ ] Route store replacing `R`: `go(page)`, `push(sub, params)`, `pop()`; `DETAIL_SUBS`
      (`new-chat`, `thread`) render beside the list at ≥768px, full-screen below
- [ ] `<App>` picks: sub-page gate → front door (`isFrontDoor`) → header + page + detail pane +
      bottom nav — the same decision tree as `render()`
- [ ] Header (wordmark, theme toggle, identity pill + switcher dropdown), bottom nav with
      badges (`unreadTotal`, requests)
- [ ] Global overlays as React: toasts (`#toast-root`), loading overlay (`#loading-root`),
      sheet/dialog host replacing `showPanel()`
- [ ] Boot sequence parity: mode resolution, `#claim=` / `?auth=` handling, session restore,
      SSE/poll loops started once (not per render), outbox retry
- [ ] Back button / Escape closes the top sheet, then pops the sub-page (Capacitor back too)

**Acceptance:** navigating all five destinations never recreates the header or nav DOM nodes
(Playwright asserts element handle identity across a state change); no focus loss while
typing in any input during live message arrival.

### E21-T5 — Design system: tokens + primitives

- [ ] Extract colors, radii, spacing, font sizes, shadows from `css/style.css` into `@theme`
      tokens, light + dark
- [ ] `src/ui/`: `Button` (variants: primary / secondary / ghost / danger; sizes; loading),
      `Input`, `Textarea`, `Select`, `Switch`, `Tabs`, `Sheet` (bottom sheet on phone, side
      dialog on wide), `Dialog`, `DropdownMenu`, `Toast`, `Badge`, `Avatar` (palette from
      `components/dom.js`), `ListRow`, `EmptyState`, `Spinner`, `PageHeader` with back button
- [ ] Port shared components: `IdentityInput`, `AudiencePicker`, `ProfileCard`,
      `PolicyControls`, `Devices` (from `js/components/`)
- [ ] Component tests (RTL) mirroring `test/components.test.js`
- [ ] No `.css` files except `src/index.css`

**Acceptance:** every screen in T6–T11 is composed from `src/ui/` + Tailwind utilities; a
grep for `style=` and new `.css` files in `src/` finds nothing.

### E21-T6 — Front doors & gates

- [ ] Front door by mode: landing (`renderLanding`, claim card + note), identity door
      (`renderIdentityDoor` + footer), generic welcome, locked
- [ ] Relay prompt (three-way: production / local emulator / typed URL — shell mode)
- [ ] Sub-pages: `add-id`, `unlock` (passkey, native keystore, seed), `onboarding` (three
      skippable steps), `claim` incl. DNS claim, `auth` (sign-in approval)
- [ ] Join-device panel, enroll-wait flow

**Acceptance:** `modes`, `launcher`, `hosted`, `onboarding`, `enrollment`, `native-custody`
specs pass against `/newapp/`.

### E21-T7 — Messages destination & conversation

- [ ] Trays: inbox / requests / anonymous with counts
- [ ] Conversation list rows (`buildConversationRows`, `threadLabel`, delivery state, expiry)
- [ ] New chat (IdentityInput, key-mismatch dialog)
- [ ] Thread view: newest page + "Load more", ticks, read receipts, attachments
      (upload/download), expiry countdown, inline reply, draft preserved across live updates
- [ ] Outbox: queued/retry states

**Acceptance:** `messaging`, `conversation`, `attachments`, `durability`, `nav-badges` specs
pass against `/newapp/`; live message arrival does not reset scroll or the draft.

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
- [ ] Claim flow and DNS claim screens reached from the launcher

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
