# EPIC-015 — Web app UX: the whole product, surfaced

- **Status:** in progress — T6, T1, T2, T3 and T4 done; T5 open
- **Priority:** P1 (the backend of EPICs 003–007/014 has almost no web surface; this is where the product becomes usable)
- **Depends on:** EPIC-003 (files/DAV), EPIC-004 (sync/changes), EPIC-005 (sharing), EPIC-006 (profiles/capabilities), EPIC-007 (contacts/policy), EPIC-014 (anon/PoW); consumes [EPIC-017](EPIC-017-typescript-client-sdk.md) (`@poweur/client`) via E15-T6
- **Unlocks:** real user testing, EPIC-012 (identity websites reuse these components),
  [EPIC-019](EPIC-019-mobile-app-capacitor.md) (the Capacitor shell wraps this UI), adoption
- **Related:** [EPIC-018](EPIC-018-identity-onboarding-naming.md) owns identity claiming,
  the launcher host and name policy — this epic assumes the user already has an ID

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

- SPA in `apps/web/` (vanilla JS, no framework — keep it). `js/app.js` is the controller.
  *As of E15-T1* it routes five destinations (**messages | contacts | files | launcher |
  settings**) plus full-screen sub-pages **add-id | unlock | compose**, over the existing
  bottom nav and slide-up panel system (`#panel-root`/`#panel-backdrop`). The protocol lives
  in `@poweur/client` (E15-T6); `js/vault.js` holds key custody and `js/client.js` builds the
  one client every screen uses. Tested with Vitest (unit + live-relay) and Playwright (e2e).
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

### Two constraints that come from EPIC-019 — apply them from T1

These cost almost nothing now and are a rewrite later. Both exist because the Capacitor
shell ([EPIC-019](EPIC-019-mobile-app-capacitor.md)) wraps *this* UI verbatim.

1. **Mobile-first, not a responsive pass at the end.** A wrapped web app feels native or
   doesn't based on the layout it wraps. Treat touch targets, safe areas, one-handed reach
   and the bottom nav as inputs to T1's information architecture, not as T5 polish. The
   accessibility/responsive bullet stays in T5, but it must be a *check*, not the first time
   a small screen is considered.
2. **No `window.location.origin` for relay calls.** `doCreateIdentity` currently does
   `const relayUrl = window.location.origin` — correct for a relay-served SPA, fatal for a
   shell running on `capacitor://localhost`. The relay base URL must come from the active
   identity record / config. This also lets one client hold identities on **several relays**,
   which the relay's permissive CORS already allows.

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

### E15-T1 — Information architecture & shared components — **done**

- [x] Refactor `app.js` navigation to the five destinations; migrate existing
      main/compose/settings content without regressing messaging or identity flows.
      Files is promoted from a sub-page to a destination; Messages gains the
      inbox/requests/anonymous tray shell (inbox live, the other two empty states for
      T2/T3); Contacts reads `contacts.json` through the SDK read-only, with requests,
      accept/block and key-pinning left to T2
- [x] Build `js/components/identity-input.js` (type/search/validate/resolve, contacts
      autocomplete), `audience-picker.js`, `profile-card.js` as framework-free modules with
      unit tests (21 tests, happy-dom). They build **DOM, not HTML strings** — they take
      user-typed identities and remote profile text, and a template literal is how an
      injection lands; `js/components/dom.js` sets text through the DOM API so escaping is
      not something a caller can forget. No app-shell imports either, so EPIC-012 can embed
      them in a public contact form
- [x] Resolver helper for `profile.json` / `capabilities.json` (EPIC-006 well-known),
      cached and request-coalesced, used by ProfileCard and IdentityInput. It lives in the
      new `js/profiles.js` rather than `js/api.js` — E15-T6 deleted that file
- [x] **Relay base URL from the identity record, not `location.origin`** — landed with
      E15-T6. `test/origin.test.js` fails if any module but `storage.js` reads the origin,
      and pins it to the single documented `defaultRelayUrl()` seam
- [x] **Mobile-first layout** for the destination shell and panel system: five tabs fit
      375px, every nav tab and row clears the 44px touch floor, safe-area insets were
      already in place

**Also landed here — the import map is relative, not `/app/…`.** The path in E15-T6's
bullet was absolute, which works only when the relay serves the SPA at `/app/`. A static
dev server mounts it at `/` and EPIC-019's shell at `capacitor://localhost/`, so
`./vendor/poweur-client/index.js` is the portable form — the same coupling constraint 2
removes from relay URLs.

**Deferred with reasons (not dropped):**

- Requests and anonymous trays render empty states only — the data paths are E15-T2/T3.
- Contacts is read-only; `sys.contact.request`, accept/block and the pinned-key dialog are
  E15-T2.
- `AudiencePicker` is built and unit-tested but has no consumer until the share dialog in
  E15-T4, so the shell does not import it yet.
- Profile presentation degrades to the identity document. `/.well-known/poweur/profile.json`
  is **Host-routed** and a browser cannot set Host, so on a shared dev relay another
  identity's `poweur-sys/public` is unreachable; there is no non-Host path for it and adding
  one is EPIC-006's call, not this epic's. Capabilities still surface, because the identity
  document carries them.

**EPIC-011 dependency: mocked at first, now real.** T1 shipped `js/keystore-mock.js` while
E11 was in flight — this browser's enrollment real, everything else flagged `mock`, every
write refusing loudly rather than faking success. EPIC-011 landed on master and the mock is
**deleted**; `js/keystore.js` calls the relay. See the *Keys, devices and recovery* section
below for what that added.

**Acceptance:** met. The five destinations render and route (`test/e2e/destinations.spec.js`
at a 375px viewport, including a no-horizontal-scroll and a 44px touch-target check);
components have unit tests (`test/components.test.js`, `test/profiles.test.js`, and — since
the mock was replaced — `test/keystore.test.js` + `test/keystore-relay.test.js`);
`test/e2e/hosted.spec.js` is unchanged and green; the origin-independence test passes.

### Keys, devices and recovery (EPIC-011's web surface) — **done**

E11-T1–T4 landed on master; this is the UI over them, and it replaces the T1 mock.

- [x] **Seed-derived identities.** `generateSeedIdentityJwks()` starts from one 32-byte seed
      and derives both keys with EPIC-011's normative HKDF, so every identity registered from
      now on can produce a recovery kit. The seed travels inside the existing AES-GCM blob
      alongside the two JWKs — additive, so blobs written before it still open and simply have
      no kit
- [x] **Registration enrolls this browser** in the relay keystore. Without it an identity lives
      in exactly one `localStorage` and clearing site data destroys it, which is the failure
      EPIC-011 exists to remove. `createPasskey` captures `getPublicKey()` /
      `getPublicKeyAlgorithm()`; where a browser does not expose them the app **declines to
      enroll and says why**, rather than storing a copy no assertion could ever unlock
- [x] **Keys & devices**: the real inventory over `POST /keystore/list`, marking this device,
      showing role and wrap in plain language, with removal that revokes the device's sessions
      and escalates to a recovery-master assertion only when the relay asks for one
- [x] **Recovery kit**: 24 words from the master seed, with a type-it-back check verified
      against the seed just rendered — so a kit that looks right but decodes to something else
      fails now rather than in a year. Legacy identities are told plainly why they have none
- [x] **Bootstrap recovery**: signing in with an identity this browser holds nothing for
      fetches the wrapped seed with a WebAuthn assertion alone and re-wraps it under a fresh
      local passkey — a **new** enrollment, since reusing the fetched one would overwrite a
      copy another authenticator still needs
- [x] **The E11-T3 ceremony, both halves.** The new device shows a request code and six digits;
      the trusted device looks it up and shows the same six digits to compare before approving.
      `test/e2e/enrollment.spec.js` drives two browser contexts and asserts the digits match

**A note on what the user types.** The six digits are a *comparison*, not an address: the
rendezvous id is 16 random bytes, so that is what moves between devices, exactly as
`poweur key approve <rendezvous-id> --sas` does. The UI says "request code" for the id and
keeps the six digits purely as the confirmation step, which is what makes the ceremony safe
without a PAKE.

**Gaps closed in `@poweur/client` on the way** (Go was ahead of TypeScript):
`canonicalKeystoreList` + `KeystoreApi.list()` with a conformance vector — the relay verified
`CanonicalKeystoreList` and the Go CLI signed it, but no JS client could enumerate devices;
`KeystoreApi.challenge()`, which the bootstrap read documented needing but did not provide;
and `.keystore` / `.enroll` accessors on `PoweurClient`.

**Still open, and owned by EPIC-011:** rotate-to-seed for pre-EPIC-011 identities (offered,
never forced — contacts re-pin), designating a recovery-master from the web app (E11-T2 has
the enforcement; the UI to nominate one is not built), and QR as an alternative to typing the
request code (E11-T3 Transport 2, unchecked there).

### E15-T2 — Contacts & requests — **done**

- [x] Contacts destination: list from `poweur-sys/relay/contacts.json` (via DAV) with
      profile cards, states (accepted / requested / blocked), petnames, search/filter.
      The row's own tap messages them and an overflow panel holds petname, block/unblock
      and remove — at 375px a name, an ID, a state chip *and* a button do not fit
- [x] **Add a contact** two ways: (a) IdentityInput (type or search a name) → sends
      `sys.contact.request` (with an optional intro and petname); (b) one-tap **"Add
      contact"** on a message from someone we hold no entry for
- [x] Requests tray: pending incoming with **Accept / Block**; pending outgoing shown as
      `requested`, with a cancel
- [x] Key-pinning UX (EPIC-007 E07-T4): on a pinned-key mismatch, a **blocking dialog**
      showing both keys and requiring explicit "Trust new key" (the web twin of
      `--accept-new-key`); a signed rotation re-pins silently and says so instead. Closes
      the E07-T4 "web blocking dialog" open item
- [x] Writes go through the file API (`@poweur/client`'s `Contacts`), so contacts sync
      across devices and to the CLI

**The requests tray reads two sources, because a request arrives two ways.** Under
`contacts_and_requests` the relay parks a stranger's first `sys.contact.request` in the
requests queue; under the default `open` policy the identical envelope is delivered to the
inbox as a typed message. A tray that read only the queue would be empty for every
default-policy user, with their contact request buried among conversations — so the tray
merges the queue with inbox messages of that type, minus anyone already accepted or blocked.

**Found and fixed here: the inbox list was losing messages.** `GET /inbox` and
`GET /requests/{id}` both **drain** — the relay hands each entry over exactly once — and
`app.js` assigned each poll response over `S.messages`. Any second render therefore emptied
the list. Both now fold into a session-lived store keyed by message id, which is also the
"render from a message store, not a poll response" seam this epic promised EPIC-009.
Durability across reloads stays EPIC-009's (inbox persistence).

**Also: challenge-signed reads are serialized.** The relay keeps one outstanding challenge
per identity, so the inbox drain and the requests drain invalidated each other's signature
when both ran from one render. E15-T6 noted this constraint; `challengeSerial()` in
`app.js` is the general fix, replacing the inbox-only single-flight guard.

**Landed in `@poweur/client` (E17), not the app:** `PoweurClient.requestContact()`,
`acceptContact()` and `blockContact()`. The compose-two-things logic (write the contacts
document, then send the typed envelope, in that order so a failed send leaves a retryable
intent) existed only inside the TS CLI's command layer, where a browser cannot reach it.
Both CLIs now call the same helpers, which also fixes a Go↔TS divergence: `poweur contacts
request` in Go pinned the resolved key, the TS one did not. New tests:
`packages/client-ts/test/contacts-relay.test.ts` (4 live-relay cases covering both policy
routings, the re-request guard, and that a block sends nothing).

**Acceptance:** met. `test/e2e/contacts.spec.js` drives two browser contexts — A requests B
by typed name (the button unlocks only once the ID resolves), B sees it in the requests
tray with the intro, accepts, B's pin is asserted from the stored document, B replies and A
receives it — plus a simulated key swap that raises the blocking dialog, refuses the send,
and goes through only after "Trust new key".

### E15-T3 — Inbox policy, anonymous & PoW settings — **done**

- [x] `js/components/policy-controls.js`: the mode picker with plain-language descriptions,
      framework-free and app-shell-free like the other components. It edits **one**
      document — mode and the `anonymous` block are saved together, because "open" plus
      anonymous-denied and "open" plus anonymous-allowed-at-8-bits are different inboxes
- [x] Anonymous block editor: allow toggle, challenge, difficulty slider, max-bytes /
      max-per-day. Turning anonymous off writes **no** `anonymous` key at all rather than
      `allow: false` — an absent block already means deny, everywhere
- [x] **Difficulty in human terms.** The slider labels each stop with what the *sender's
      browser* pays ("22 bits — about ~15 s on a laptop, ~40 s on a phone") and warns past
      20 bits. The numbers are the E14 measured table with that document's own 5–10×
      browser penalty applied: quoting the native Go timings to someone dialling a cost for
      web senders understates it by an order of magnitude
- [x] **verified / payment** are shown disabled and labelled "soon" — designed policy slots
      the relay answers but does not enforce, so the vocabulary is discoverable without
      promising a gate that is not there
- [x] Anonymous tray in Messages: drains `GET /anon/{id}` and renders each message as a
      different kind of object — no avatar, no sender line, no reply and no add-contact.
      Decryption is client-side (the SDK's `anonQueue`)
- [x] Anonymous **send** from compose, behind a toggle that says plainly what it costs the
      reader (no sender, no reply). The PoW is solved in the page with attempt-count
      progress and a warning over 20 bits

**Landed in `@poweur/client`:** `sendAnonymous` gained `onSolveProgress` and `signal`. It
already reported the challenge, but not the mining, so a 24-bit challenge was a tab that
sat still for a minute with no way to stop it — the failure mode that teaches people to
distrust the feature. Covered in `test/messaging-relay.test.ts` (progress across chunks at
20 bits; abort at 26).

**Acceptance:** met — `test/e2e/policy.spec.js`. A user sets mode + anonymous + PoW from
Settings; the Settings rows and the stored document both reflect it; a second browser
sends anonymously, solving the proof-of-work *in the page*; the message appears in the
anonymous tray with no reply affordance and the signed inbox stays empty. A second case
asserts the default: an identity that never opted in refuses the same send, visibly.

### E15-T4 — Files explorer & sharing — **done**

- [x] Files destination with breadcrumbs, root badges, download, mkdir, rename, delete and
      quota — most of this landed with T1's promotion; T4 adds **chunked upload** above the
      SDK's 64 MB threshold, because a single PUT of a large file over a phone connection
      is one all-or-nothing request
- [x] **Share dialog** on any file/folder under a shareable root: AudiencePicker (its first
      consumer, as planned) + read / read-write + optional expiry → a grant signed **in the
      browser** with the identity key and PUT into our own tree. Only paths under `/shared`
      and `/apps` offer it, and never the roots themselves — the same rule
      `normalizeGrantPath` enforces, applied before the button appears rather than as an
      error after
- [x] "Shared" chip on granted rows; a **Shared by you** panel listing every grant with its
      audience, permissions and expiry, and a Revoke that deletes the document
- [x] Received shares: a **Shared with me** source that opens an owner's tree with a
      visitor token. The token is minted `dav:full` deliberately — the scope is not the
      permission, the owner's signed grant is, and a read-scoped token would refuse a write
      the owner *did* allow
- [x] Changes-feed auto-refresh: a 5 s poll of `GET /sync/{id}/changes` while Files is on
      screen, reloading only when a change touches the folder being looked at. Closes the
      E04-T5 "web auto-refresh" open item

**There is no "shares granted to me" listing, so the visitor names the owner.** Grants live
in the owner's `poweur-sys`, which only they can read; EPIC-005's `sys.share.offer` is what
will let a recipient discover shares. What makes naming the owner enough is the grant
engine: read covers the *ancestors* of a granted path and listings filter siblings, so an
owner who shared one folder shows exactly that folder to that visitor and nothing else.

**Found and fixed here:** `AudiencePicker` rendered a literal "000" above its input when it
had no contacts and no groups — `groups.length && node` is `0`, not `false`, and `0` is a
legitimate text child. It had never been rendered empty before, since T1 built it without a
consumer. Pinned by a unit test.

**Also:** the changes poll builds its `SyncClient` from the cached DAV token rather than
`client.sync()`. `clientFor()` returns a fresh `PoweurClient` per call, so its internal
token cache is always empty and the poll would have signed a new token every five seconds.

**Acceptance:** met — `test/e2e/sharing.spec.js`. The owner shares a folder by picking the
grantee in the audience picker; the grantee opens their tree, reads the file and writes a
new one; the owner revokes and the grantee's next request is refused. A second case asserts
the changes feed: a file written behind the UI's back appears with no click.

### E15-T5 — Profile, first-run onboarding & polish

- [ ] Profile editor in Settings: display name, avatar (upload into `/public`, referenced as
      a tree path per EPIC-006), bio, links → writes `poweur-sys/public/profile.json`;
      ProfileCard renders it everywhere a contact appears
- [ ] Capabilities are shown read-only on the profile (what this identity speaks)
- [ ] **First-run onboarding** after a fresh registration: a 2–3 step, fully skippable flow
      — set inbox policy, optionally set display name/avatar, a one-line "you're set" — so a
      new user lands in a configured app, not an empty inbox. Picks up *after*
      [EPIC-018](EPIC-018-identity-onboarding-naming.md)'s claim flow hands off to the
      identity's own origin; the two must join without a second credential prompt
- [ ] Empty states across all destinations (no contacts / no files / no messages) with the
      obvious next action
- [ ] Accessibility + responsive pass (keyboard nav, focus traps in dialogs, mobile
      layout), dark/light parity, and a docs update (`apps/docs/docs/web/` walkthrough)

**Acceptance:** a brand-new identity, created in the web app, completes onboarding and can
message, add a contact, set a policy, upload and share a file — all without the CLI;
Playwright covers the fresh-user happy path end to end.

### E15-T6 — Import `@poweur/client` instead of owning the protocol — **done**

*Paired with [E17-T6](EPIC-017-typescript-client-sdk.md) — same work, tracked from both sides.
Sequence it **first** if EPIC-017 has landed (every screen below is then written against the
package), otherwise last, as a refactor behind the existing test suites.*

- [x] Add `@poweur/client` as a `workspace:*` dependency of `@poweur/web`; vendor the built ESM
      into the served static tree and add the import-map entry in `index.html`
      (`"@poweur/client": "/app/vendor/poweur-client/index.js"`) — no bundler, per the constraint above.
      [`apps/web/scripts/vendor.mjs`](../apps/web/scripts/vendor.mjs) walks the ESM graph from the
      package's entry points, copies only what is reachable (48 files, 440 KB) and rewrites bare
      specifiers to relative paths, so the import map needs two entries rather than one per
      transitive `@noble` subpath. `pnpm vendor:check` fails when `vendor/` is stale; the tree is
      committed because prod mounts `apps/web` straight from the checkout
- [x] Drop the `esm.sh` import-map entry for `@noble/ciphers`: the dependency arrives vendored with
      the client, removing a CDN fetch from first paint and from offline/self-hosted installs.
      A Playwright assertion fails if the SPA requests anything off-origin
- [x] Delete the duplicated protocol modules (`js/crypto.js`, the protocol half of `js/api.js`,
      `js/messaging.js`, `js/files.js`, `js/pow.js`); `js/app.js` keeps only UI. `js/client.js` is
      the single place a `PoweurClient` is built, which is what makes the relay-URL rule structural
- [x] Keep `js/passkey.js` + `js/storage.js` and expose them as the browser `Signer`/`KeyStore`
      implementation the package expects — passkey/PIN gating stays a web-app concern and raw keys
      never cross the package boundary. `js/vault.js` holds the custody half of the old `crypto.js`:
      the wrapped-blob format is byte-identical (HKDF salt `poweur-key-wrapping-v1`, AES-256-GCM,
      `{signingJWK,encJWK}`) so shipped identities keep opening and EPIC-011 can re-wrap the same
      payload. `WebCryptoSigner` signs inside WebCrypto, so the Ed25519 key never enters package
      memory; the X25519 key does, because message decryption is ChaCha20-Poly1305 and WebCrypto
      has none
- [x] Existing Vitest + Playwright suites pass — `test/e2e/hosted.spec.js`, the UI regression net,
      is **unchanged**. The Vitest protocol suites (`crypto`, `pow`, `hosted-api`,
      `messaging-relay`, `files-relay`) were retired rather than kept: they tested modules that no
      longer exist and their coverage now lives in `packages/client-ts`. What replaced them tests
      what `apps/web` still owns — `vault.test.js` (JWK ↔ SDK key bytes, signer/decryptor parity,
      wrap formats), `client-relay.test.js` (the same live-relay flows driven through the app's own
      modules), `origin.test.js`, `vendor.test.js`

**Found during adoption (fixed here):**

- `@poweur/client` captured `globalThis.fetch` unbound in `RelayClient`, `resolveIdentity` and
  `dohTxtResolver`. Node tolerates that; a browser throws `Illegal invocation`, so registration
  failed on the first real page load and no Node-only test could have caught it. Fixed with
  `defaultFetch()` in `http.ts` and pinned by `packages/client-ts/test/fetch-binding.test.ts`,
  which models the browser's receiver rule.
- The relay keeps **one outstanding challenge per identity**
  (`apps/api/internal/storage/challenges.go`), so two overlapping authenticated GETs invalidate
  each other. `app.js` had a render-driven inbox fetch *and* an explicit one after unlock; the
  inbox fetch is now single-flight. Worth noting for EPIC-009: any concurrent authenticated read
  hits this, and a per-challenge (rather than per-identity) store would remove the constraint.

**Acceptance:** `apps/web` contains no canonical strings, signing or crypto of its own; `pnpm
test:all` is green; the relay serves the SPA exactly as before.

## Forward-looking (prepare for, don't build)

- **EPIC-019 (mobile shell):** beyond the two constraints, keep native-capable seams thin —
  key custody already discriminates on the stored `kdf` field, so a third (`native`)
  implementation should need no UI change. The shell also holds **many identities across many
  relays** (E19-T7), which the web app structurally cannot (`localStorage` is per-origin, so
  `alice.r1.com/app/` cannot see an identity stored by `alice.r2.com`). Keep identity-scoped
  state keyed by identity rather than global, so the same modules work under both.

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
- **Identity claiming, the launcher host, name policy and credential scope are EPIC-018.**
  This epic assumes an identity exists. The one new relay endpoint the flow needs
  (`GET /hosted/availability`) lives there, keeping the "no new relay endpoints" rule intact.
- **The mobile shell is EPIC-019.** This epic only owes it the two constraints above — no
  Capacitor plugins, no native code, no shell-specific screens here.
