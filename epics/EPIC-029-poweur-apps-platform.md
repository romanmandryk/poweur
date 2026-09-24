# EPIC-029 — Poweur Apps: publish, open and share local-first apps (games included)

- **Status:** proposed; start after the EPIC-025 reference editor proves the session layer
- **Priority:** P2 (the developer-adoption engine)
- **Depends on:** EPIC-012 (separate sandboxed origin for active content — decide both together),
  EPIC-008 (consented, path-scoped app grants), EPIC-006 (app namespaces, conventions),
  EPIC-025 (durable documents + T7 rooms API), EPIC-017 (`@poweur/client`)
- **Interacts with:** EPIC-024 (Apps slot in a Space), EPIC-010-T5 (directory/trust surface),
  EPIC-027-T7 (neutral hosted authorities), EPIC-030 (paid apps), EPIC-026 (egress entitlements)
- **Unlocks:** third-party apps with no backend; multiplayer games; whiteboard/design/co-editing
  tools by people other than us; the "everything app" feel from many small apps
- **Monetizes (hosted):** egress of popular apps lands on the *publisher's* entitlement; paid apps
  and in-app purchases through EPIC-030. Directory ranking is never sold.

## Progress

| Task | Status | Notes |
|------|--------|-------|
| E29-T1 App manifest & signed bundles | open | `app.json`: entry, permissions, conventions, publisher signature |
| E29-T2 Publish from a home | open | `poweur app publish` → immutable versioned bundle |
| E29-T3 App origin & sandbox | open | per-app origin on the E12 sister host; no relay cookies/tokens |
| E29-T4 Runtime bridge (`@poweur/app`) | open | identity, files, documents, rooms, messages via `postMessage` |
| E29-T5 Open, install, revoke | open | consent screen, scoped grant to `/apps/<app-id>/`, launcher |
| E29-T6 Sharing & open-with | open | share a document → recipient opens it in a compatible app |
| E29-T7 Game kit | open | lobby/invites, turn-based state, realtime input, host migration |
| E29-T8 App directory | open | opt-in index, signed ratings, abuse reporting, no pay-for-rank |
| E29-T9 `create-poweur-app` & tutorial | open | templates: notes, multiplayer game, file tool |

## Goal

**An app is a folder.** A developer publishes a static bundle (HTML/JS/WASM) from their own home.
Anyone opens it on a sandboxed app origin, signs in with their Poweur ID, and the app keeps its
data in *the user's* home under a scoped grant and collaborates over EPIC-025 documents and rooms.
The developer runs no servers and pays nothing on the free tier until the app is popular.

Games are the showcase that makes this legible: a board or party game is a durable document
(state) + a room (moves, chat) + invites over ordinary messages. Friends on different relays play
without a game server; if a neutral referee is needed, EPIC-027-T7 provides one.

EPIC-025 deliberately proves the layer with one reference editor and excludes a generic app/game
platform. This epic is that platform, built only once the editor shows the layer holds.

## Background

- E12 concluded active HTML must not share the identity/API origin and sketched a sister host
  (`*.sites.poweur.net`). Apps need the same isolation, so the decision should cover both.
- E08 already grants consented, path-scoped access to an app's own namespace; `apps/tasks` and
  `apps/guestbook` are working precedents of "app data lives in the user's home".
- E10-T5 designs an opt-in agent directory and a "what can touch my home" panel; apps reuse both.

## Design direction

- **Signed, immutable bundles.** Manifest + content hashes signed by the publisher ID; the loader
  verifies before running, so a compromised CDN or relay cannot swap code. Users may pin versions.
- **Bridge, not tokens-in-JS.** The app talks to the user's relay only through a small host frame
  that enforces the manifest's permissions; the app never holds the session token.
- **Authority for multiplayer:** default "host player is authoritative" with host migration;
  neutral authorities are an EPIC-027-T7 option, declared in the manifest with who pays.
- **Open by default:** sideloading an app by URL always works; the directory is a convenience.

## Tasks

### E29-T1 — Manifest & signed bundles
- [ ] `app.json` PCP + schema: id, version, entry, permissions (paths, room bindings, message
      types), conventions consumed/produced, optional authority, pricing hook (EPIC-030), signature.
- [ ] Go/TS vectors; CLI validation.

**Acceptance:** a tampered file in a published bundle fails verification before any code runs.

### E29-T2 — Publish
- [ ] `poweur app publish ./dist` → chunked upload, signed manifest, version history; previous
      versions remain loadable by hash.

**Acceptance:** publishing v2 leaves v1 pinned users unaffected.

### E29-T3 — Origin & sandbox
- [ ] Per-app origin on the sister host; strict CSP; service-worker scope rules; no relay cookies.
- [ ] Security suite: a hostile app cannot read another app's data, the relay session or the
      identity origin.

**Acceptance:** the hostile-app suite passes in CI.

### E29-T4 — Runtime bridge
- [ ] `@poweur/app`: `identity()`, `files`, `docs` (EPIC-025), `rooms` (E25-T7), `send()`,
      `purchase()` (EPIC-030); every call checked against the manifest.

**Acceptance:** the EPIC-025 Markdown editor runs as an app purely through the bridge.

### E29-T5 — Open, install, revoke
- [ ] Consent screen with publisher ID, permissions and conventions; grant written; launcher tile;
      pin into a Space's Apps slot (EPIC-024).

**Acceptance:** install → use → revoke is fully visible in the "what can touch my home" panel.

### E29-T6 — Sharing & open-with
- [ ] Share offers carry an `opened_with` hint; a recipient without the app gets an install
      prompt; any app declaring the convention can open the document.

**Acceptance:** Alice shares a whiteboard; Bob on another relay opens it in one click.

### E29-T7 — Game kit
- [ ] Lobby and invite convention (invite-bound rooms, inbox-policy gated), turn-based engine on a
      durable document, realtime input channel on a room, host migration, spectators.
- [ ] Two demo games: a turn-based one (chess) and a realtime party/drawing game.

**Acceptance:** four players on two relays finish a game; the host leaves mid-game and play
continues; a reload resumes from durable state.

### E29-T8 — Directory
- [ ] Opt-in index of published apps (the index is itself an identity publishing files);
      one signed rating per ID; abuse reporting to the publisher's relay; no paid ranking.

**Acceptance:** the directory lists templates and demo apps and installs work from it.

### E29-T9 — Templates & tutorial
- [ ] `create-poweur-app` (Vite/React) with notes, multiplayer-game and file-tool templates.

**Acceptance:** "zero to a deployed multiplayer app in ten minutes" tutorial runs in CI.

## Non-goals

- Native binary distribution or an exclusive store; sideloading always works.
- Server-side app backends beyond EPIC-027's sandboxed runs.
- A Figma-compatible or office-file engine — those are apps someone builds on this.
