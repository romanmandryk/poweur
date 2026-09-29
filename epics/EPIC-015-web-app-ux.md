# EPIC-015 — Web app UX: the whole product, surfaced

- **Status:** in progress — T1–T6 done (the app for someone who *has* an ID);
  **T7–T12 open** (the front door for someone who does not, and the big screen);
  **T14 partial** (logo, favicon and install icons shipped; the violet palette remap is open)
- **Code:** since [EPIC-021](EPIC-021-web-app-rewrite-react-tailwind.md) E21-T14 the app is the React +
  Tailwind client in `apps/web/src/`; `js/…` paths below name the legacy files a screen was
  ported from, and T7–T12 continue on the React code
- **Loading & refresh (web 0.1.15):** screens no longer print an inline "Loading…" line that shoves
  the list when it goes. `shell/ActivityIndicator` floats a spinner under the header while the screen
  on show waits for data it does not have (after 250 ms, so a fast read never blinks it; background
  drains stay silent). Messages and Contacts refresh on pull-down (`ui/PullToRefresh`): the content
  follows the finger and a spinner sits in the gap until the reads settle
- **Settings split (web 0.1.17):** identity-scoped rows (profile, keys, inbox policy, session) require unlock; device-scoped rows (add/switch identity, relay URL, lookup, About) stay usable while locked. Creating a new identity locks the previous one first, so onboarding cannot inherit its name, bio or inbox.
- **Profile photos everywhere (web 0.1.16):** the profile editor (onboarding and Settings) crops a
  picked photo to a 256 px square and previews it in the avatar circle; saving uploads it to
  `public/avatars/avatar-<hash>.<ext>` behind a `.poweur-web-public` marker (without one `/pub/`
  never served the old `public/avatar.png` to anyone) and deletes the replaced file. `ui/Avatar`
  reads `state/avatars`: our own photo is kept on the device (shown on unlock and in the switcher
  before keys open, read back over DAV on a new device), other people's come from their public
  profiles, resolved by Messages, Thread, Contacts, the Files owner picker and the identity door
- **Priority:** P1 (the backend of EPICs 003–007/014 has almost no web surface; this is where the product becomes usable)
- **Depends on:** EPIC-003 (files/DAV), EPIC-004 (sync/changes), EPIC-005 (sharing), EPIC-006 (profiles/capabilities), EPIC-007 (contacts/policy), EPIC-014 (anon/PoW); consumes [EPIC-017](EPIC-017-typescript-client-sdk.md) (`@poweur/client`) via E15-T6; the second wave consumes [EPIC-018](EPIC-018-identity-onboarding-naming.md)'s name policy, `GET /hosted/availability` and credential scope
- **Unlocks:** real user testing, EPIC-012 (identity websites reuse these components),
  [EPIC-019](EPIC-019-mobile-app-capacitor.md) (the Capacitor shell wraps this UI), adoption
- **Related:** [EPIC-018](EPIC-018-identity-onboarding-naming.md) owns the *mechanism* of
  identity claiming — name policy, the availability endpoint, the launcher host, credential
  scope. It shipped all of it. **T7–T12 own the screens over that mechanism**: which front
  door a visitor gets, and what it asks them. T1–T6 assumed the user already had an ID

## Progress

| Task | Status | Notes |
|------|--------|-------|
| E15-T1 Information architecture & shared components | **done** | five destinations, IdentityInput / AudiencePicker / ProfileCard, relay URL off `location.origin` |
| Keys, devices & recovery (E11's web surface) | **done** | seed-derived identities, keystore inventory, recovery kit, the E11-T3 ceremony |
| E15-T2 Contacts & requests | **done** | contacts destination, requests tray merging queue + inbox, key-pin dialog. **Friction fixed since manual testing:** a bare handle is completed with your own domain (a hosted relay puts everyone under one, so typing `alice` failed validation with "that does not look like a Poweur ID"); the add-contact buttons resolve on press instead of staying disabled behind a debounced lookup, which left someone who typed a name and pressed the button they were looking at with nothing at all; a queued request's intro is decrypted and shown |
| E15-T3 Inbox policy, anonymous & PoW | **done** | `policy-controls.js`, anonymous tray, in-page PoW send (the send *toggle* left the signed-in UI with E15-T13; the SDK path stays) |
| E15-T4 Files explorer & sharing | **done** | chunked upload, share dialog, received shares, changes-feed refresh |
| E15-T5 Profile, first-run onboarding & polish | **done** | profile editor, three skippable steps, a11y pass, walkthrough docs |
| E15-T6 Import `@poweur/client` | **done** | protocol modules deleted; `js/client.js` is the only construction site |
| Durable messages & honest badges ([EPIC-009](EPIC-009-messaging-upgrades.md) E09-T1's web surface) | **done** | the message store was memory-only and the relay drains on pickup, so a refresh lost messages *permanently*; the app now redraws from the archive at `poweur-sys/private/messages/`, keeps its own sent copies, and counts unread from read marks rather than from how much it happens to hold. `test/e2e/durability.spec.js` asserts each of these after a reload |
| **E15-T7 App modes: one SPA, three front doors** | **open** | `js/mode.js`; boot routes on host, not on storage. A launcher host always shows the claim landing — an identity left in this origin's storage by the claim hand-off is not offered there |
| **E15-T8 The parent-domain landing** | **open** | how-it-works + `[handle].poweur.net` claim field. **Web 0.1.36:** poweur.org's look (glass P on its glow, night ground, the site's "Claim your name on the open internet" box as the claim); "I already have an ID" checks the name and redirects to its door; the DNS "Use my own domain" form is replaced by a link to the self-hosting guide |
| **E15-T9 The identity host: sign in, or claim this name** | **open** | claimed door + join skips the name; passkey is web-only (shell uses join / create) |
| **E15-T10 Stop asking what the relay already knows** | **open** | the hosted checkbox, the domain field, the DNS rows |
| **E15-T11 Desktop & tablet layout** | **open** | the 768px breakpoint currently only moves the nav |
| **E15-T12 Onboarding failure states & polish** | **open** | policy-driven validation, taken-on-submit, offline, titles |
| **E15-T14 Brand rollout: glass P logo & violet palette** | **partial** | brand defined in [`design/brand/`](../design/brand/README.md). **Shipped (web 0.1.31):** logo in the header, front doors and welcome gate; favicon, touch icon, manifest and share image. **Web 0.1.36:** the header wordmark is poweur.org's horizontal lockup, and the app-icon tile on the front doors is replaced by the glass P on its glow. **Open:** the token remap to violet, avatar contrast, the contrast test, and docs / bridge alignment |
| **E15-T13 Conversation view & paged history** | **partial** | conversation view shipped: newest 10 bubbles, contextual "Load more", ticks, attachments, expiry, inline reply, live updates (`test/e2e/conversation.spec.js`). "Load more" pages what the archive already loaded at unlock; download paging waits for [EPIC-020](EPIC-020-storage-protocol-v2.md) E20-T11 history v2 |

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
- **Shipped modules with UI:** identity create/import/unlock (passkey PRF; PIN is a non-goal,
  `js/passkey.js`/`js/vault.js`), messaging compose + inbox (`js/messaging.js`), a file
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
- Profile presentation degrades to the identity document **in dev only**.
  `/.well-known/poweur/profile.json` is Host-routed, which is exactly right in production:
  a card fetches `https://bob.poweur.net/.well-known/…`, the browser sets `Host` for it,
  and the relay serves Bob's tree with `Access-Control-Allow-Origin: *`. A local relay is
  the odd case — many identities behind one IP that DNS does not know — and there the
  browser cannot say which tree it wants. Identity documents survive it because they also
  have a path-addressed route (`GET /identities/{id}`); `profile.json` has no twin. Not a
  product gap, so nothing is asked of EPIC-006. Capabilities surface either way, from the
  identity document.

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
      `test/e2e/enrollment.spec.js` drives two browser contexts and asserts the digits match.
      The identity-host door (`alice.poweur.net`) does not re-ask the name; the offer is posted
      to the identity's home relay; request-code paste strips wrapping noise so a phone keyboard
      cannot turn a live offer into "rendezvous not found or expired".
      Join polling is one-in-flight (not `setInterval`), claims immediately, retries
      network blips, wakes on `visibilitychange` / focus, and exposes **Check now** —
      a backgrounded phone used to dump a stack of 404 toasts when it resumed.

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

**The requests tray is the handshake, independent of who may chat.** The relay parks a
`sys.contact.request` in the requests queue in every inbox mode except `contacts_only`.
The tray still merges leftover inbox copies from older relays, and it keeps showing a
request even when the sender is already an accepted contact — that is how a one-sided
handshake (they asked; you already listed them) gets answered. Blocked senders stay hidden.

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

### E15-T5 — Profile, first-run onboarding & polish — **done**

- [x] Profile editor in Settings: display name, avatar, bio and a link →
      `poweur-sys/public/profile.json`. The avatar is uploaded into `/public` first and
      referenced as a **tree path**, which is the schema's rule and the reason a profile
      can never point at a third party's server. Saving primes the resolver cache so the
      new name appears on cards at once — which is also what makes the editor usable
      against a local dev relay, where the well-known fetch cannot resolve (see below)
- [x] Capabilities shown read-only on the profile panel — what this identity speaks, not a
      preference
- [x] **First-run onboarding**: three skippable steps (policy → profile → done) after a
      fresh registration. Skipping writes **nothing** — no policy document, no empty
      profile — so "skipped" and "chose the default" stay distinguishable, which matters
      because the relay treats an absent policy as `open`
- [x] Empty states on every destination and tray, each naming the next action
- [x] Accessibility pass: the slide-up panel is a real dialog (focus moves in, Tab is
      trapped, Escape closes, focus returns to the control that opened it — by id, since
      the shell re-renders from strings and a node reference would be detached); settings
      rows are focusable and answer Enter/Space; every `role="button"` row does too
- [x] Docs: [`apps/docs/docs/web/walkthrough.md`](../apps/docs/docs/web/walkthrough.md),
      wired into the sidebar along with the Trust and Sharing pages, which existed but were
      unreachable from it

**Landed in `@poweur/client`:** `profile.ts` (`validateProfile`, `readProfile`,
`writeProfile`, `PoweurClient.profile()/setProfile()`), mirroring `identity/profile.go`.
Go emits new `profiles` vectors (`packages/identity/vectors_test.go`) and the TS
conformance suite asserts it accepts and rejects exactly the same documents — the avatar
rule especially, since the browser is the first thing to write one of these.

**Found by the acceptance test: the contact handshake never completed.** Alice requests,
Bob accepts — and Alice's own `contacts.json` still said `requested`, so her policy
refused Bob's first message and neither side could see why. Under `contacts_and_requests`
the relay routes `sys.contact.accept` into the *requests queue*, so the app now drains
that queue on the Messages destination (not only on the Requests tray) and promotes a
contact we ourselves asked for — but only while the key we pinned at request time is
still theirs. This is EPIC-007's "auto-pin-on-accept lands with the web UX pass".

**Two more bugs the pass turned up:** a `render()` in a loader's `.then()` re-ran
`attachEvents`, which called the loader, which resolved immediately — a render loop that
starved the page (loaders now repaint themselves and mark themselves loaded even on
failure). And the requests queue was guarded by a `loaded` flag, which is right for a
document read and wrong for a *drain*: an acceptance fetched once and never again is a
handshake that never finishes. It is throttled instead.

**Acceptance:** met — `test/e2e/onboarding.spec.js`. A brand-new identity completes
onboarding, then, entirely through the UI: adds a contact by typed name, has it accepted
on the other side, exchanges a message, uploads a file and shares it, and the grantee sees
exactly that file. Two further cases cover skipping (nothing written) and keyboard use of
the dialogs.

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
      implementation the package expects — passkey PRF gating stays a web-app concern and raw keys
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

## Second wave — the front door, and the big screen (T7–T12)

T1–T6 built the app for a user who already has an identity. Everything before that moment —
what a visitor sees when they arrive, and what the app asks them — was left as it was, and
it does not distinguish between the three places the same static tree gets served from.
T11 is separate: the layout was built mobile-first on purpose, and the desktop half of that
promise was never written.

### Background (what the code does today)

- **`boot()` looks at storage, never at the host.** [`apps/web/js/app.js:3922`](../apps/web/js/app.js)
  branches on whether an identity is stored and whether it is unlocked. A visitor with none
  gets `renderWelcome()` ([`app.js:316`](../apps/web/js/app.js)) — the same "Welcome to Poweur ID
  / Get started" card on `id.poweur.net`, on `poweur.net` and on `bob.poweur.net`. The relay
  already publishes the difference: `GET /` returns `launcher_host` and `hosted_domains`
  ([`apps/api/internal/relay/server.go:277`](../apps/api/internal/relay/server.go)), and E18-T3
  reads it in exactly one place — `handOffToIdentityOrigin()`
  ([`app.js:1951`](../apps/web/js/app.js)) — to decide where to *send* a new identity, never to
  decide what to *render*.
- **The claim form asks for the parent domain as free text**
  ([`app.js:682`](../apps/web/js/app.js)). So a visitor on `bob.poweur.net` can type `alice` /
  `poweur.net` and claim a name with no relationship to the host they are on. The result is
  not broken — the hand-off moves them to `alice.poweur.net` — but the question should never
  have been asked, and asking it is how a per-identity origin stops meaning anything.
- **"Hosted on this relay (no DNS token)"** ([`app.js:687`](../apps/web/js/app.js), and again on
  step 2 at [`app.js:639`](../apps/web/js/app.js)) asks the user to choose a registration mode
  that is not theirs to choose: a registration is hosted **iff the domain is in
  `HOSTED_DOMAINS`**, which `GET /` already publishes. The same is true of the "DNS provider"
  settings row ([`app.js:816`](../apps/web/js/app.js)), shown to hosted users who will never hold
  a token.
- **Signing in asks for the identity as free text** (`renderAddId()`,
  [`app.js:868`](../apps/web/js/app.js)) — including on the one host where it is knowable from
  the URL bar.
- **The apex serves a JSON banner.** Caddy already routes `http://poweur.net` *and*
  `http://*.poweur.net` to the relay
  ([`deploy/infra/caddy/Caddyfile:15`](../deploy/infra/caddy/Caddyfile)), but `handleRoot`
  redirects `/` → `/app/` for the single `LauncherHost` only
  ([`server.go:262`](../apps/api/internal/relay/server.go)), so a human typing the product's own
  domain gets `{"service":"poweur-relay",…}`.
- **The desktop layout is one breakpoint.** `#app` is a 480px column
  ([`css/style.css:77`](../apps/web/css/style.css)); the only wide-screen rule
  ([`style.css:89`](../apps/web/css/style.css)) sets `max-width:100%` and turns the bottom nav
  into an 80px icon rail ([`style.css:265`](../apps/web/css/style.css)). Nothing else responds.
  At 1440px a message row, a settings row and a contact row are each ~1360px wide with a 40px
  avatar pinned left and a chevron pinned right; `.sub-page` (`height:100dvh`) covers the whole
  display to compose one message; the slide-up panel is still a bottom sheet; and the FAB sits
  in the far corner of a 27" screen. E15-T5's accessibility pass covered keyboard and focus but
  not geometry, and E15-T1's "mobile-first" constraint was honoured literally: mobile first,
  and then nothing.

### Design direction

#### Mode is a property of the host, resolved once at boot

A new `js/mode.js` exports `resolveMode()` → `{ mode, host, relayUrl, subject, hostedDomains,
launcherHosts }`, computed from `location.hostname` plus the relay root document, cached per
origin in `sessionStorage` and revalidated in the background. It is **not** a build flag and
not a stored preference: the same static tree is served on every host, and the host is the
only thing that differs.

| Mode | When | Front door | May ask for |
|------|------|-----------|-------------|
| `launcher` | host is a launcher host or a bare hosted domain (the apex) | how-it-works + claim a name | a **handle** — never a domain |
| `identity` | host is `<label>.<hosted domain>`, or `GET /identities/{host}` returns a document | sign in (claimed) / claim this name (unclaimed) | **nothing**; handle and domain are the host |
| `shell` | `capacitor://`, `file://`, or no meaningful host (EPIC-019) | relay picker, then `launcher` behaviour | relay URL, then a handle |

Anything else — a dev server on `localhost:5173`, a self-hoster's `relay.example.org` — keeps
today's generic welcome. The rule for `identity` mode covers self-hosted identities too: a
host under `hosted_domains` is an identity host whether or not the name is taken, and a host
that is *not* under `hosted_domains` is one when the relay can produce an identity document
for it.

**Fail open, but not into the wrong door.** If the root document cannot be fetched, fall back
to the generic welcome rather than to a guessed mode. A landing page offering to claim a name
on a relay we cannot reach is worse than one that says it cannot reach the relay.

#### Claimed-ness is a fetch — but "absent" is not "claimable"

The obvious probe is the identity's own published documents: registration serves
`/.well-known/poweur/id.json` (and `/pubkey`, `/enckey`) Host-routed off the identity's own
origin ([`wellknown.go:17`](../apps/api/internal/relay/wellknown.go)), so a 404 there looks like
"unclaimed" for free. Three reasons the front doors use `GET /hosted/availability` (E18-T2)
instead, recorded here because it is a reasonable thing to want to simplify away:

1. **It is not extra state.** `Exists()` and `DocumentJSON()` are two reads of the *same*
   `IdentityStore` map ([`identity_store.go:189`](../apps/api/internal/storage/identity_store.go)) —
   the registration record the relay keeps in order to verify signatures, route DAV and serve
   `id.json` at all. Availability adds no rows and no writes; it is a different query over
   state that has to exist either way.
2. **Absent ≠ claimable, and that gap is a spent passkey.** `admin.poweur.net`,
   `www.poweur.net` and `pay.poweur.net` all 404 and none of them can be claimed — they are
   reserved (E18-T1). A document probe would render "claim this name" on those hosts, take the
   user through a WebAuthn ceremony, and have `POST /identities` refuse: exactly the ordering
   failure E18-T2 was built to remove. Availability answers *taken / reserved / blocked /
   too_short / charset* and returns the policy block E15-T12 validates against.
3. **Host routing does not work in the harness.** The well-known route needs the browser to
   send a `Host` the relay can map to a tree; the test relay is one listener on `127.0.0.1`
   with no DNS — the same limitation this epic already recorded for `profile.json` under
   E15-T1. Availability is query-addressed, and `GET /identities/{host}` is the path-addressed
   twin, so both are drivable in e2e.

**Do not probe `profile.json` for this.** It is optional by design — E15-T5's onboarding is
skippable and skipping writes nothing — so its absence means "no profile", not "unclaimed", and
it additionally requires DAV to be enabled. Only `id.json` / `pubkey` track registration.

For a host **outside** `hosted_domains` availability answers `domain_not_hosted`, which is the
honest answer: claimability there is not this relay's to decide. That case falls back to
`GET /identities/{host}`, whose 200/404 is claimed-ness asked the only other way, with no
claim offered on a 404.

*Noted while checking this:* neither `/.well-known/poweur/id.json` nor `GET /identities/{id}`
is rate-limited, so the availability endpoint's cost-weighted bucket (E18-T2) caps the
*expensive* probe while a cheaper one sits beside it. Not a vulnerability — a public registry
is enumerable by design and E18-T2 says so — but the cost argument reads stronger than it is.
Worth a look in EPIC-013's abuse pass rather than here.

#### One relay-side change, and it is configuration

`LAUNCHER_HOST` becomes a set: `LAUNCHER_HOSTS`, defaulting to `id.<d>` **and** `<d>` for every
entry in `HOSTED_DOMAINS`, with the old single-value var still accepted. `handleRoot` redirects
for any of them and the root document advertises `launcher_hosts` alongside today's
`launcher_host`. This is E18-T3's mechanism rather than this epic's, and it is noted there —
it lives here because it is one config change and every screen that depends on it is in this
epic. *Operator note:* a `*.poweur.net` wildcard certificate does not cover the apex. This
deployment terminates TLS at Cloudflare so there is nothing to do; a self-hoster pointing an
apex at their relay needs a certificate that names it.

#### The desktop layout is three layouts, one DOM

Not a second app and not a framework: the same markup with real breakpoints behind it. Phone
(<768px) is unchanged — it is the layout the Capacitor shell wraps and the one that already
works. Tablet (768–1023px) keeps the icon rail and caps the content column at a readable
measure. Desktop (≥1024px) gets a labelled sidebar and a **two-pane** content area for the
three list/detail destinations, where today's full-screen sub-page becomes a detail pane.

## Tasks (second wave)

### E15-T7 — App modes: one SPA, three front doors

- [ ] `js/mode.js` with `resolveMode()` as described above: `location.hostname` + the cached
      root document, a synchronous best guess so the first paint is content rather than a
      spinner, and one async correction if the guess was wrong
- [ ] `boot()` ([`app.js:3922`](../apps/web/js/app.js)) routes on mode **before** it consults
      storage. A stored identity still wins on its own origin — the unlock screen is unchanged
- [ ] A stored identity for a *different* host on this origin (hand-off leftovers, EPIC-019's
      multi-identity shell) stays reachable through the identity switcher but does not get to
      choose the front door. The host does
- [ ] Relay: `LAUNCHER_HOSTS` (CSV, defaulting to `id.<d>` and `<d>` per hosted domain), honoured
      by `handleRoot` and advertised in the root document; `LAUNCHER_HOST` still accepted.
      Cross-referenced from E18-T3
- [ ] Unit tests over a host table: launcher host, apex, identity host, unknown host,
      `capacitor://localhost`, `localhost:5173`, a trailing dot, mixed case, and a host with a
      port. Host parsing is where this goes wrong quietly
- [ ] e2e: one relay, three `Host` values, three first screens. The Playwright relay is a single
      listener on `127.0.0.1` with no DNS, so drive it with
      `--host-resolver-rules=MAP *.poweur.net 127.0.0.1` rather than trying to make the names
      resolve — the same constraint E18-T3 hit and worked around

**Acceptance:** a visitor with no identity gets three different first screens on
`id.poweur.net`, `poweur.net` and `bob.poweur.net`, and the JSON service banner never reaches a
human on a hosted domain.

### E15-T8 — The parent-domain landing

- [ ] Replace `renderWelcome()` in `launcher` mode with a landing page: **what this is, in three
      one-sentence steps** (a name you own → messages and files that live under it → sign in to
      other apps with it), above the fold at 375px and centred at a readable width on desktop
- [ ] The dominant control is **one field with the domain as a fixed suffix inside it** —
      `[ alice ].poweur.net` — not a second input. The suffix comes from the resolved mode, not
      from `S.config.parentDomain`
- [ ] Live availability under the field (reuse E18-T3's 350 ms debounce and last-write-wins
      token), the relay's own message rendered inline, and **Create ID** enabled only on
      `available`
- [ ] **Create ID goes straight to the passkey ceremony.** Today's step 2 exists only to collect
      DNS credentials, which a hosted claim never needs (T10) — so for the common path there is
      no step 2
- [ ] When `hosted_domains` holds more than one entry the suffix becomes a `<select>` inside the
      same control. Still not free text
- [x] Demoted, below the fold: **"I already have an ID"** and self-hosting. *Changed (web
      0.1.36):* on the launcher "I already have an ID" is a field, not a passkey prompt — the
      keys of a hosted name live on its own origin, so the launcher checks the name
      (`GET /hosted/availability`) and sends the visitor to `<name>.<domain>/app/`, or says it
      doesn't exist and can be claimed above. The shell keeps the add-identity screen.
      **"Use my own domain" is gone**: the DNS form is removed, and the landing links to the
      [self-hosting guide](../apps/docs/docs/relay/self-hosting.md) instead
- [ ] The existing hand-off (E18-T3) carries the new record to the identity origin unchanged

**Acceptance:** e2e on the launcher host — a visitor types a name, is told it is reserved and
then that it is taken *before any passkey exists*, picks a free one, creates it, and arrives on
the identity's own origin with the fragment cleared.

### E15-T9 — The identity host: sign in, or claim this name

- [ ] Resolve claimed-ness once on entering `identity` mode, by the rule above —
      availability for a host under `hosted_domains`, `GET /identities/{host}` otherwise,
      and **no claim offered on a `reserved` or `blocked` verdict**, only on `available`
- [x] **Claimed** → a sign-in screen for exactly this identity: avatar, handle and domain from
      its published profile (E15-T5's resolver), one **Sign in with passkey**, one **Add this
      device** (E11-T3's ceremony), and **no control that takes a typed identity**. "Add new ID"
      does not belong on this door — creating an identity is what the launcher is for. Join
      starts the ceremony immediately (no "your Poweur ID" field, no extra tap)
- [ ] **Unclaimed** → the claim flow with handle and domain fixed to the host and rendered as
      *text, not inputs*, with the subject stated plainly ("You're claiming **bob.poweur.net**").
      Straight to the passkey, and no hand-off: we are already on the identity's origin
- [ ] A stored identity for this host short-circuits both into today's unlock screen
- [x] Passkey sign-in is a **browser** authenticator: it is offered on web doors, never in the
      native shell (the shell joins an existing ID or creates one under the OS keystore;
      optional passkey enrollment is E19-T3). The free-text passkey field survives only on
      the launcher ("I already have an ID") and on an unknown host — never on the identity
      host, which already knows the name

**Acceptance:** e2e — a claim completes on `bob.poweur.net` with no name field on screen; a
second browser context on that host is offered sign-in only and can reach no create form; and
`alice.poweur.net` on the same relay still offers the claim, proving the branch is per-host and
not per-relay.

### E15-T10 — Stop asking what the relay already knows

The question that prompted this: *is "Hosted on this relay (no DNS token)" the user's decision,
or the relay's?* It is the relay's. Hosted means the domain is in `HOSTED_DOMAINS`, which the
root document publishes; the checkbox turns a fact into a prompt, and gets it wrong the moment
the user unticks it on a relay that hosts nothing else.

- [ ] Delete the hosted checkbox from both renders ([`app.js:687`](../apps/web/js/app.js) and
      [`app.js:639`](../apps/web/js/app.js)). Hosted is derived; the DNS path is reached by
      choosing **"Use my own domain"**, which is a different intent rather than a modifier on
      this one
- [ ] Delete the free-text parent-domain input ([`app.js:682`](../apps/web/js/app.js)). The
      domain is the host's in `identity` mode, the mode's suffix in `launcher` mode, and a select
      only when there is genuinely more than one
- [ ] Show the DNS provider settings row ([`app.js:816`](../apps/web/js/app.js)) only for an
      identity registered through the DNS path. A hosted user has no token and never will
- [ ] Keep `renderRelayPrompt()` for `shell` and unknown hosts only — a relay-served SPA already
      knows which relay it is on
- [ ] **Nothing here removes self-hosting.** The DNS path keeps every field it has; it moves
      behind an explicit choice that reveals them. *Superseded (web 0.1.36):* the web DNS form
      is removed — self-hosting means running a relay for your domain, and the landing links to
      its guide. `createIdentity` still accepts a DNS token, and the CLI keeps the DNS path
- [ ] The CLI keeps `--hosted`, and that divergence is correct: a CLI has no host to infer from

**Acceptance:** unit tests over the derivation (`domain ∈ hosted_domains`), e2e asserting neither
front door renders a checkbox or a domain input, and the existing DNS-mode registration path
still green end-to-end.

### E15-T11 — Desktop & tablet layout

- [ ] **Tablet (768–1023px):** icon rail as today, content column capped at a readable measure
      and centred, slide-up panel becomes a centred modal
- [ ] **Desktop (≥1024px):** labelled sidebar (~240px) and a **two-pane** content area for the
      list/detail destinations — Messages (conversations │ thread), Contacts (list │ profile),
      Files (tree │ folder)
- [ ] `.sub-page` stops being a `100dvh` overlay above 768px: compose, unlock and add-id render
      into the detail pane or a modal, and the back button becomes a close. One class, so the
      change is structural rather than per-screen
- [ ] `#panel-root` becomes a centred dialog ≥768px — **the same focus trap, Escape handling and
      ARIA from E15-T5**, different geometry only. Not a second dialog implementation
- [ ] The FAB ([`style.css:555`](../apps/web/css/style.css)) is a thumb-reach idiom; above 768px
      the primary action moves into the destination header, where the eye already is
- [ ] Readable measures everywhere: list rows, settings rows and form cards stop spanning the
      viewport. This is the single biggest visible fix
- [ ] Hover and focus-visible states, which a touch-first sheet never needed; branch on
      `pointer: fine` rather than width where the distinction is about input and not size
- [ ] Keyboard: the sidebar is a tab-stop set, `/` focuses search where a destination has one,
      Escape closes the detail pane
- [ ] The awkward cases, tested rather than assumed: landscape phone, iPad portrait at exactly
      768px, and iPad split-view at ~507px — the width that breaks a naive two-column rule

**Acceptance:** Playwright at 375 / 768 / 1024 / 1440 asserting no horizontal scroll at any of
them, no row wider than the content measure, the panel centred above 768px, and list *and*
detail visible simultaneously at 1024px. Assertions on geometry, **not** pixel snapshots —
screenshot diffing across machines is a flake source this suite does not need.

### E15-T12 — Onboarding failure states & polish

- [ ] **Validate against the relay's policy, not a hardcoded 3.** `doNextIdentityStep()` refuses
      `handle.length < 3` ([`app.js:1922`](../apps/web/js/app.js)) while `docker-compose.prod.yml`
      sets `NAME_MIN_LEN=6`. The availability verdict already carries
      `policy {min_len, max_len, charset}` (E18-T2) and the client throws it away — render the
      rule *before* the user types and validate against it, so a constraint is visible rather
      than discovered
- [ ] **Taken between the check and the submit.** Availability is advisory and fails open, so
      `POST /identities` can still refuse — after a passkey ceremony. Return the user to the name
      field with the relay's reason rather than a toast, and say what became of the credential
      they just created
- [ ] **Relay unreachable** is a first-class state on the landing, not a `console.warn`
      ([`app.js:1898`](../apps/web/js/app.js)): the button says why it cannot check instead of
      quietly enabling itself
- [ ] **Passkey support is checked before the name, not after.** E11 already declines to enroll
      when the browser hides `getPublicKey()`; the landing should say so up front and refuse
      authenticators without PRF, rather than after a name has been chosen
- [ ] Paste tolerance in the handle field: `alice.poweur.net`, `@alice`, `Alice`, and trailing
      whitespace all normalise to `alice`. A field with a visible suffix invites pasting the
      whole thing
- [ ] Per-mode `<title>` and `<meta name="description">` — `poweur.net` is a landing page a
      search engine will index, `bob.poweur.net` is not — and the wordmark links to the launcher
      from an identity host, which is otherwise the only navigation between the two doors
- [ ] Skeletons instead of empty flashes where a screen fetches before first paint; the mode
      probe in particular must never produce a blank frame
- [ ] Docs: [`web/claim-your-id.md`](../apps/docs/docs/web/claim-your-id.md) is written around the
      two-step launcher form and needs rewriting for the three doors; the
      [walkthrough](../apps/docs/docs/web/walkthrough.md) gains a desktop section

**Acceptance:** e2e covering a policy-rejected name shown before typing finishes, a
taken-on-submit recovery that returns to the field, and an offline landing that refuses to
pretend it checked.

### E15-T13 — Conversation view & paged history

**Background.** The Messages tray renders one row per (contact, thread) with only the latest
message's preview ([`renderTray`](../apps/web/js/app.js)). Tapping a row marks it read and
pushes the *compose* sub-page with `to`/`thread` preset — so nothing in the web app or the
Capacitor shell ever shows an earlier message. The data is all there: `S.messages` holds both
sides (the archive keeps sent copies, E09-T1), per-message acks including read ticks
(E09-T6), attachments and `expires_at`.

The archive itself does not scale as it is read today. Layout is
`poweur-sys/private/messages/<YYYY-MM>/<sortkey>-<id>.json`, one sealed file per message
([`packages/identity/history.go`](../packages/identity/history.go)). `MessageHistory.load()`
in `@poweur/client` lists every shard and then issues **one GET + one unseal per message**,
and the web app calls it on every unlock; the Go CLI's `Load` does the same over the sync
manifest, and `poweur history [peer]` prints the entire result with no limit. At 10k messages
that is 10k requests before the tray can draw. Filenames carry time but not the peer, so
filtering a conversation still means opening everything.

- [x] **Conversation screen** (`R.push("thread", { peer, thread, group })`): bubbles
      oldest→newest, own messages right-aligned, day separators, sender name per bubble in
      groups. Tapping a tray row opens this, not compose; "New message" stays compose
- [ ] **Per-message state in the bubble:** sent / delivered / read ticks from `S.acks`,
      outbox pending/failed with retry, expiry countdown, "Open 📎" on attachment bubbles,
      the generic "app message (&lt;type&gt;)" line for unknown types (reuse `describeMessage`)
- [x] **Inline reply composer** pinned to the bottom (safe-area aware in the shell), keeping
      the thread id; attachment picker behind an icon. Sending appends optimistically
- [ ] **Live:** push events already trigger `loadInbox()` → `render()`; the open thread must
      re-render too (today it only repaints when `R.page === "messages" && !R.sub`), stick to
      the bottom when already there, and show a "new messages ↓" pill when scrolled up
- [ ] Read marks advance as the thread is viewed (to the newest visible message), not on the
      row tap
- [ ] **Paging comes from history v2, not the v1 layout** —
      [EPIC-020](EPIC-020-storage-protocol-v2.md) E20-T11 (one append-only log per conversation
      on chunked storage, `tail` / `before` APIs in both SDKs, CLI `--limit/--before`). The
      conversation screen above can ship first on today's full load; the two items below wait
      for E20-T11
- [x] Web: unlock loads the newest 20 per conversation (one request each) for the tray; the
      thread's "Load more" reads the previous page from the archive (one request) with the
      scroll position preserved; the last-seen history is kept in the encrypted on-device
      snapshot (EPIC-020 E20-T11, `durability.spec.js`)
- [ ] Desktop (with E15-T11): the thread renders into the detail pane of the two-pane layout

**Shipped so far (2026-09-14, web 0.1.12):**
- The conversation screen shows the newest 10 messages first. "Load more (N earlier)" appears only while older messages exist, and adds 10 at a time.
- Direct messages you sent show sent / delivered / read ticks, or "!" when the relay's `sys.delivery.failed` notice arrives. Group messages show no ticks.
- Attachment bubbles have "Open 📎", expiring messages show a countdown, and unknown message types show the generic line.
- The reply box is text only (Cmd/Ctrl+Enter sends); attachments still go through compose. Its sending code is shared with compose (`sendSigned`).
- Push events repaint the open conversation. It stays pinned to the bottom while you're there, and stays in place after "Load more".
- Open work from the list above:
  - the "new messages ↓" pill;
  - outbox pending/failed bubbles;
  - read marks per visible message (still marked when the conversation opens);
  - download paging (E20-T11).


**Chat-style composer and nav badges (web 0.1.13):**
- **The compose screen is gone.**
  - "New message" is a To picker that opens the conversation; a group identity is recognised by its roster.
  - Contact rows, the contact sheet's "Message" button and add-contact "Message" all open the conversation.
  - Attachments send from the 📎 button beside the reply box, using any typed text as the caption.
- **"Send anonymously" is no longer offered in the signed-in app.** The SDK path (`sendAnonymous`, PoW solved in-page) is unchanged and `policy.spec.js` drives it directly. A signed-out anonymous send surface belongs with EPIC-012's contact page.
- **Nav badges:**
  - Messages counts unread messages (signed, from read marks, plus unread anonymous).
  - Contacts counts incoming requests waiting for an answer.
  - History and requests load on every destination so the numbers are right wherever you are. A background load repaints another destination only when a badge number changed.
- **Unlocking pulls everything immediately.** Passkey or native unlock (including web passkey sign-in), keystore recovery, restore on a new device and registration all call `pullAfterUnlock()`. It fetches history, inbox, requests (forced), contacts, policy and then the anonymous queue, and retries the outbox. It no longer depends on having returned to Messages or on the push stream's `ready` event. `nav-badges.spec.js` blocks the event stream, unlocks from Contacts and checks both badges.
- **Found on the way: a push landing mid-drain was dropped.** `loadInbox` was single-flight, so an event arriving while a drain was in flight joined it and missed any message posted after its GET left. On Messages every render drained again, which hid the gap; anywhere else the message stayed on the relay until something unrelated happened. Push events and unlock now call `loadInbox({ force: true })`, which queues one more drain, the same pattern `loadRequests` already used. Renders never force, so a drain cannot schedule itself in a loop.

**Acceptance:** e2e — A and B exchange messages; B opens the conversation and sees both sides
in order with ticks; A sends while B sits in the thread and it appears without interaction;
after a reload the thread is intact. Once E20-T11 lands: a relay test seeds a 2k-message
archive and asserts the tray paints having fetched at most one tail chunk per conversation,
and that scrolling up fetches the previous one.

### E15-T14 — Brand rollout: the glass P logo and the violet palette

**Background.** The app has no logo and no favicon. `apps/web/index.html` links no icon, and the
welcome gate ([`screens/gates.tsx`](../apps/web/src/screens/gates.tsx)) shows a generic
`MessageCircle` glyph on an accent tile. Its colours are iOS system colours: accent `#5856D6`, a
`#8B5CF6` gradient partner, and system success, danger and warning. The docs site uses Tailwind blue
and the OAuth bridge a third blue. The brand is now defined in
[`design/brand/README.md`](../design/brand/README.md):
- **Logo:** a P built from three pieces and a dot (ID, messaging, file sharing; the dot is the separator
  in a domain name). It comes in black glass, flat black and flat white.
- **Palette:** OKLCH scales with Poweur Violet `#724CCF` as the primary, indigo night `#0E1344` as the
  secondary, amber and teal accents, and violet-tinted ink neutrals.
- **Tokens:** every value is in [`design/brand/tokens.css`](../design/brand/tokens.css).

This is a re-skin: **token values change, token names do not.** Components already write `bg-accent`,
`text-muted` and so on (E21-T5), so most of the change is `index.css`.

- [ ] **Tokens:** remap the semantic tokens in `apps/web/src/index.css`, light and dark, per the
      "Applying it to the web app" table in the brand README.
      - `accent` becomes `violet-600`, and `violet-400` in dark mode.
      - `accent-2` becomes `indigo-700`, so the gradient is `violet-600 → indigo-700`.
      - Neutrals become ink.
      - Success, danger and warning get the checked semantic values.
      - `--shadow-accent` gets a violet tint.
      - Import the brand scales as `--color-violet-*`, `--color-indigo-*`, `--color-amber-*`,
        `--color-teal-*` and `--color-ink-*`, so a component can reach a step when a semantic
        token does not fit.
- [ ] **Hard-coded colours:**
      - The welcome tile's `shadow-[0_12px_40px_rgb(88_86_214/.35)]` becomes the token.
      - `AVATAR_PALETTE` in [`lib/identity.ts`](../apps/web/src/lib/identity.ts) becomes the eight
        equal-lightness brand avatar hues. The current palette gives white initials only 2.1–2.2:1 on
        three of its eight colours. The avatar colour of an identity changes once, which is acceptable.
      - A source-guard test forbids hex literals outside `index.css`.
- [x] **Logo assets in the app** (web 0.1.31). The SVGs live in `src/assets/brand/` rather than
      `public/brand/`: Vite bundles them, so they resolve relatively at `/app/`, `/` and
      `capacitor://`, and the small flat ones are inlined as `data:` URIs.
      - `ui/Logo` takes `variant: "glass" | "flat"` and a height.
      - Flat swaps black for white in dark mode with CSS alone (`dark:`), so there is no flash
        while the theme loads. No `tone` prop was needed.
      - Glass falls back to flat below 48 px (`GLASS_MIN_HEIGHT`).
      - `Wordmark` is the P plus "Poweur ID". `BrandTile` is the glass P on `bg-brand-glow`, a new
        utility in `index.css`.
      - The brand scales are added to `@theme` as `--color-{violet,indigo,amber,teal,ink}-*`. This is
        additive: the semantic tokens keep their values until the remap subtask above.
- [x] **Favicon and install icons** in `index.html`, generated by
      [`design/brand/scripts/icons.py`](../design/brand/scripts/icons.py) into `public/`:
      - `favicon.svg`: a violet-600 tile with the flat white P. It reads on light and dark tabs
        alike, so it needs no `prefers-color-scheme` swap.
      - `favicon.ico` at 16, 32 and 48 px, in place of a single `favicon-32.png`.
      - `apple-touch-icon.png` at 180 px: the glass P on the glow.
      - `manifest.webmanifest`: `icon-192`, `icon-512`, a maskable 512 and the SVG.
      - `og-image.png` at 1200 × 630.
      - The links are root paths (`/favicon.svg`) that Vite rewrites against `base: "./"`.
        `test/brand-assets.test.ts` checks that the files exist, their PNG sizes and the manifest.
        The mobile staging test checks that the built HTML stays relative.
      - The relay also serves `favicon.ico`, `favicon.svg` and `apple-touch-icon.png` at the
        host root (relay 0.1.14). A browser on `alice.poweur.net` asks that host for
        `/favicon.ico`; `GET /` would otherwise answer with the JSON service banner, so the
        tab stayed blank on every identity. `/app/favicon.ico` is the same file.
      - `theme-color` is unchanged.
- [x] **Welcome gate:** the `MessageCircle` tile became `BrandTile` (96 px, 28 px radius, pop-in).
      The heading and copy stay.
- [x] **Header:** `Wordmark` in the app header and in `DoorPage`, so the front doors show it too.
      The launcher landing shows `BrandTile` at 80 px above its headline (hidden in short
      landscape).
- [ ] **Primary button:** the gradient stays; its values come from the tokens. Check the focus ring
      (`accent`) against both themes.
- [ ] **Contrast pass:** every text and background token pairing in both themes meets WCAG AA,
      asserted in a unit test from the token values. The pairings and ratios are listed in the brand
      README.
- [ ] **Aligned surfaces:** the same change on the other surfaces that show the brand.
      - Docs ([`custom.css`](../apps/docs/src/css/custom.css)): Infima primary moves to the violet
        scale, and the logo becomes `flat/p-black.svg`.
      - OAuth bridge ([`bridge.css`](../apps/oauth/bridge/static/bridge.css)): accent becomes violet,
        greys become ink.
      - These bump their own packages.
- [x] **Mobile hand-off:** the same script writes the shell's icons and splash screens (mobile 0.1.6),
      tracked in [EPIC-019](EPIC-019-mobile-app-capacitor.md) E19-T1.

**Acceptance:**
- The unit contrast test is green in both themes.
- The source-guard test finds no hex literal outside `index.css`.
- e2e covers four things:
  - `link[rel=icon]` and the manifest resolve at `/app/`.
  - `GET /favicon.ico` on an identity host and on a launcher host returns the icon, not the service banner.
  - The welcome gate renders the logo `<img>` with a non-empty `alt`.
  - Dark mode swaps the flat logo to white.
  - The E21-T12 suite stays green.
- The web package gets a patch version bump.

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
- **The *mechanism* of identity claiming is EPIC-018** — name policy, `GET /hosted/availability`,
  the launcher host and credential scope all shipped there. T1–T6 assumed an identity already
  existed; **T7–T12 build the screens over E018's mechanism** and add no protocol of their own.
  The one relay-side change in the second wave (`LAUNCHER_HOSTS` accepting the apex, E15-T7) is
  configuration and a redirect, not an endpoint, so the "no new relay endpoints" rule holds.
- **The mobile shell is EPIC-019.** This epic only owes it the two constraints above — no
  Capacitor plugins, no native code, no shell-specific screens here.
