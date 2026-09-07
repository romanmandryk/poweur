# EPIC-015 — Web app UX: the whole product, surfaced

- **Status:** proposed
- **Priority:** P1 (the backend of EPICs 003–007/014 has almost no web surface; this is where the product becomes usable)
- **Depends on:** EPIC-003 (files/DAV), EPIC-004 (sync/changes), EPIC-005 (sharing), EPIC-006 (profiles/capabilities), EPIC-007 (contacts/policy), EPIC-014 (anon/PoW); consumes [EPIC-017](EPIC-017-typescript-client-sdk.md) (`@poweur/client`) via E15-T6
- **Unlocks:** real user testing, EPIC-012 (identity websites reuse these components), adoption

## Goal

Turn the web app from a **messaging + identity** client into the full front door for
everything the relay now does: a freshly registered user should be able to manage their
inbox policy (contacts / anonymous / proof-of-work), see and grow their contacts (adding
people from a received message in one tap, or by typing/searching a name), browse and
manage their files, and share a file or folder with someone by contact or by typed name —
without ever touching the CLI. This epic also **re-thinks the information architecture**
now that there are six feature areas, and lays reusable components (identity picker,
audience picker, policy controls) that EPIC-012's contact-form/website work builds on.

## Background (current web app — what exists, what's missing)

- SPA in `apps/web/` (vanilla JS, no framework — keep it). `js/app.js` (~1500 lines) is the
  controller: three top-level pages **main | launcher | settings** and full-screen
  sub-pages **add-id | new-id | unlock | compose**, a bottom nav, a slide-up panel system
  (`#panel-root`/`#panel-backdrop`), and hand-rolled SVG icons. Tested with Vitest
  (unit + live-relay) and Playwright (e2e).
- **Shipped modules with UI:** identity create/import/unlock (passkey + PIN,
  `js/passkey.js`/`js/crypto.js`), messaging compose + inbox (`js/messaging.js`), a file
  browser (`js/files.js` — list/upload/download/mkdir/rename/delete against DAV) reachable
  but not yet a first-class destination.
- **Shipped on the relay, NOT surfaced in the web app:**
  - Inbox policy modes + the anonymous/PoW block (EPIC-007/014) — no settings screen.
  - Contacts + requests + key pinning (EPIC-007) — `contacts.json` is written/read only by
    the CLI; the web app has no contacts concept at all.
  - Sharing grants + groups (EPIC-005) — no share dialog, no received-shares view.
  - Profile / capabilities (EPIC-006) — no profile editor or contact profile cards.
  - Sync changes feed (EPIC-004) — file browser doesn't auto-refresh.
  - Anonymous send + PoW solver (`js/pow.js` exists, unused in the UI).
- The web app already speaks the relay's auth (DAV tokens, challenge-signed GETs) via
  `js/api.js`/`js/messaging.js`, so most of this is new screens over existing calls, not
  new protocol.

## Design direction

- **Five primary destinations** (bottom nav / sidebar), replacing the current
  main/launcher/settings triad:

  | Destination | Contents |
  |-------------|----------|
  | **Messages** | conversations, compose, inbox + **requests** + **anonymous** as distinct trays |
  | **Contacts** | contact list, requests inbox, profile cards, add-by-search/type |
  | **Files** | file explorer (the promoted `js/files.js`), per-item share, received shares |
  | **Launcher** | apps/automations (unchanged; forward-looking to EPIC-010) |
  | **Settings** | identity, inbox policy + anonymous/PoW, profile editor, devices, security |

- **Reusable components** (build once, use everywhere) — these are the real deliverable:
  - **IdentityInput**: type-or-search a Poweur ID; validates + resolves (profile card
    preview via EPIC-006 `/.well-known/poweur/profile.json`); autocompletes from contacts.
    Used by compose, add-contact, and the share audience picker.
  - **AudiencePicker**: multi-select of contacts + groups + free-typed IDs — the share
    dialog and (later) group editor consume it.
  - **PolicyControls**: the inbox-policy + anonymous/PoW editor as a self-contained panel.
  - **ProfileCard**: avatar/name/bio/capabilities resolved from an ID's home, with the
    right primary action in context (message / add contact / accept request).
- **Progressive disclosure for a fresh user:** first run after registration shows a short
  **"set up your inbox"** flow (pick a policy, optionally set a profile) instead of dropping
  them into an empty inbox — but every step is skippable.
- **Everything is files underneath.** Contacts, policy, shares, profile are reads/writes of
  `poweur-sys/...` over DAV (validated server-side, EPIC-006), so the web app writes the
  same documents the CLI does and they sync for free (EPIC-004). No web-only state.
- Keep the stack: vanilla JS modules, the existing panel/nav system, Vitest + Playwright.
  No framework, no build step beyond what exists.

## Tasks

### E15-T1 — Information architecture & shared components

- [ ] Refactor `app.js` navigation to the five destinations; migrate existing
      main/compose/settings content without regressing messaging or identity flows
- [ ] Build `js/components/identity-input.js` (type/search/validate/resolve, contacts
      autocomplete), `audience-picker.js`, `profile-card.js` as framework-free modules with
      unit tests (Vitest, DOM via jsdom/happy-dom as the suite already uses)
- [ ] Resolver helper in `js/api.js`: fetch + cache `profile.json` / `capabilities.json`
      for an ID (EPIC-006 well-known), used by ProfileCard and IdentityInput

**Acceptance:** the five destinations render and route; components have unit tests; no
regression in the existing messaging/identity Playwright e2e.

### E15-T2 — Contacts & requests

- [ ] Contacts destination: list from `poweur-sys/relay/contacts.json` (via DAV) with
      profile cards, states (accepted / requested / blocked), petnames, search/filter
- [ ] **Add a contact** two ways: (a) IdentityInput (type or search a name) → sends
      `sys.contact.request`; (b) one-tap **"Add contact"** on any received message / request
- [ ] Requests tray: pending incoming (`GET /requests/{id}`) with **Accept / Block**;
      pending outgoing shown as `requested`
- [ ] Key-pinning UX (EPIC-007 E07-T4): on a pinned-key mismatch, a **blocking dialog**
      showing both key fingerprints and requiring explicit "trust new key" (mirrors the
      CLI's `--accept-new-key`); this closes the E07-T4 "web blocking dialog" open item
- [ ] Writes go through the file API so contacts sync across devices

**Acceptance:** Playwright flow — user A requests user B by typed name, B sees the request,
accepts from the requests tray, both can then message; a simulated key swap raises the
blocking dialog.

### E15-T3 — Inbox policy, anonymous & PoW settings

- [ ] PolicyControls panel in Settings: choose mode (open / contacts_only /
      contacts_and_requests) with plain-language descriptions of each
- [ ] Anonymous block editor: allow toggle, challenge (none / pow), a **difficulty slider
      in human terms** ("~1 s on a laptop, ~10 s on a phone" from the E14 measured table),
      max-bytes / max-per-day; writes the `anonymous` object into `inbox-policy.json`
- [ ] Distinguish **verified / payment** as visible-but-disabled ("coming soon") so the
      vocabulary is discoverable
- [ ] Anonymous tray in Messages: reads `GET /anon/{id}`, renders each message visibly
      **unauthenticated** (no sender, no reply affordance), decrypts client-side
- [ ] Wire `js/pow.js` into an anonymous **send** path (used by E15-T5 / EPIC-012 too):
      solve with a progress indicator, warn above ~20 bits

**Acceptance:** a user enables anonymous+PoW from Settings; an anonymous message solved in
the browser appears in the anonymous tray and nowhere else; the policy file round-trips.

### E15-T4 — Files explorer & sharing

- [ ] Promote `js/files.js` to a first-class Files destination: breadcrumb navigation, the
      layout roots (public / shared / private / apps) with audience badges (reuse E03-T5
      styling), upload (chunked via the E14/E04 upload endpoint for large files),
      download, mkdir, rename, delete, quota display
- [ ] **Share dialog** on any file/folder: AudiencePicker (contacts + groups + typed IDs) +
      read/read-write toggle + optional expiry → writes a signed grant to
      `poweur-sys/relay/shares/` (EPIC-005). Client signs with the identity key (reuse
      `js/crypto.js` sign), matching `poweur share add`
- [ ] "Shared with" indicator on shared items; a **share management** view to list/revoke
      grants (`poweur-sys/relay/shares/` listing via the sync manifest, like `share ls`)
- [ ] Received-shares view: shares granted **to** the user (v1: access an owner's tree
      directly with a visitor DAV token, as the CLI does; a mounted `/shared/<owner>/` view
      arrives with EPIC-005's offer/accept flow)
- [ ] Changes-feed auto-refresh (EPIC-004): poll `GET /sync/{id}/changes` and live-update
      the current folder — closes the E04-T5 "web auto-refresh" open item

**Acceptance:** Playwright flow — user shares a folder with a contact by picking them from
the audience picker; the grantee (second browser) reads and writes it; owner revokes and
access stops.

### E15-T5 — Profile, first-run onboarding & polish

- [ ] Profile editor in Settings: display name, avatar (upload into `/public`, referenced as
      a tree path per EPIC-006), bio, links → writes `poweur-sys/public/profile.json`;
      ProfileCard renders it everywhere a contact appears
- [ ] Capabilities are shown read-only on the profile (what this identity speaks)
- [ ] **First-run onboarding** after a fresh registration: a 2–3 step, fully skippable flow
      — set inbox policy, optionally set display name/avatar, a one-line "you're set" — so a
      new user lands in a configured app, not an empty inbox
- [ ] Empty states across all destinations (no contacts / no files / no messages) with the
      obvious next action
- [ ] Accessibility + responsive pass (keyboard nav, focus traps in dialogs, mobile
      layout), dark/light parity, and a docs update (`apps/docs/docs/web/` walkthrough)

**Acceptance:** a brand-new identity, created in the web app, completes onboarding and can
message, add a contact, set a policy, upload and share a file — all without the CLI;
Playwright covers the fresh-user happy path end to end.

### E15-T6 — Import `@poweur/client` instead of owning the protocol

*Paired with [E17-T6](EPIC-017-typescript-client-sdk.md) — same work, tracked from both sides.
Sequence it **first** if EPIC-017 has landed (every screen below is then written against the
package), otherwise last, as a refactor behind the existing test suites.*

- [ ] Add `@poweur/client` as a `workspace:*` dependency of `@poweur/web`; vendor the built ESM
      into the served static tree and add the import-map entry in `index.html`
      (`"@poweur/client": "/app/vendor/poweur-client/index.js"`) — no bundler, per the constraint above
- [ ] Drop the `esm.sh` import-map entry for `@noble/ciphers`: the dependency arrives vendored with
      the client, removing a CDN fetch from first paint and from offline/self-hosted installs
- [ ] Delete the duplicated protocol modules (`js/crypto.js`, the protocol half of `js/api.js`,
      `js/messaging.js`, `js/files.js`, `js/pow.js`); `js/app.js` keeps only UI
- [ ] Keep `js/passkey.js` + `js/storage.js` and expose them as the browser `Signer`/`KeyStore`
      implementation the package expects — passkey/PIN gating stays a web-app concern and raw keys
      never cross the package boundary
- [ ] Existing Vitest + Playwright suites pass **unchanged** — they are the regression net for this
      refactor; do not rewrite them in the same PR

**Acceptance:** `apps/web` contains no canonical strings, signing or crypto of its own; `pnpm
test:all` is green; the relay serves the SPA exactly as before.

## Forward-looking (prepare for, don't build)

- **EPIC-012 (identity websites / contact forms):** IdentityInput, ProfileCard and the anon
  PoW send path are the exact pieces a public contact form needs — keep them free of
  app-shell dependencies so they can be embedded standalone.
- **EPIC-010 (agents/apps):** the Launcher destination stays the seam for installed apps;
  the `/apps/<app-id>` file views (E15-T4) are where app data becomes visible.
- **EPIC-009 (messaging v2 — push/attachments):** build the Messages trays so a WebSocket
  push channel and file-reference attachments slot in without a rewrite (render from a
  message store, not directly from a poll response).
- **EPIC-013 (observability):** the web app is a metrics source too — keep user actions
  routed through a thin action layer so client-side counters can be added later without
  scattering instrumentation.

## Non-goals

- No framework migration (React/Vue/etc.) — the vanilla-JS module approach stays.
- No new relay endpoints; this epic is UI over the existing API. If a screen needs data the
  relay can't serve yet, note it and defer to the owning epic rather than adding a web-only
  backend.
- Group **identities** (addressable groups) remain EPIC-005 T5; the AudiencePicker uses
  owner-local groups only.
