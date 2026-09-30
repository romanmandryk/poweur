# INT-007 — Open-source app suite: data-handling labels, connect flow & candidate apps

- **Status:** proposed
- **Theme:** adoption — a coherent "works with Poweur" suite (mail, docs, whiteboard, calendar,
  publishing, media) built mostly from existing open-source apps
- **Depends on (Poweur side):** EPIC-022 (OIDC/IndieAuth bridge), EPIC-020 (E2EE drive, scoped
  drive handles, shares, service identities), EPIC-023 (email bridge), EPIC-025 (Yjs collaboration),
  EPIC-009 (typed messages), INT-000 (tiers, playbook), INT-006 (supported-apps list)
- **Out of scope here:** billing, plans, hosted provisioning, the admin console and the bundled
  commercial offering.

## Why this epic

INT-004 and INT-006 list apps one at a time. This epic answers a different question: *what is the
smallest coherent suite a person could use instead of Google Workspace or Proton, and how honest
can we be about where each app keeps its data?*

Two facts shape it:

1. **Sign-in and messaging are cheap; storage is the expensive part.** Any app with generic OIDC
   needs configuration, not a fork. Swapping an app's persistence layer for the drive is easy only
   for apps with a storage adapter or a client-first design, and impractical for apps whose whole
   model lives in Postgres.
2. **Drive data is end-to-end encrypted.** A server-side app cannot read it unless the user
   grants it a share as a service identity (EPIC-020). Anything needing plaintext on the server
   (full-text search, previews, OCR, AI, office conversion) is therefore a different class of app
   and must say so.

## Data-handling labels

Every app on the supported list (INT-006-T1) carries exactly one label. It appears in the
registry, on the docs page, and on the consent screen when a user connects the app.

| Label | Meaning | Who can read the content | Typical apps |
|-------|---------|--------------------------|--------------|
| **E2EE** | Data lives in the user's drive, encrypted client-side; the app is a static client using a scoped drive handle | Only the user and identities they share with | Excalidraw, Yjs editors, local-first tools |
| **Shared with app** | A server-side app reads a folder the user granted to a service identity | The user, plus the app's operator while the share exists; revocable | Indexers, converters, server-side editors |
| **Hosted data** | The app keeps its own database (Postgres, etc.) on its operator's infrastructure; Poweur provides identity and messaging only | The app's operator | Docs servers, photo libraries, CMS databases |

Rules:

- A label describes the **strongest** access the operator has, not the marketing tier.
- An app may offer more than one mode (for example a client-only mode and a hosted mode); each
  mode is listed separately.
- Moving an app to a weaker label is a breaking change in the registry and re-prompts consent.
- The label is never implied by "uses Poweur sign-in".

## Connect flow for storageless apps

A static client hosted anywhere can work with no backend: the user signs in, grants a scoped
handle, and the app reads and writes its own folder. This is **not** OIDC. OIDC proves identity;
it does not hand over drive keys and must not.

EPIC-020 already defines the model for first-party apps (a sandboxed origin; the host frame holds
keys and gives the app a scoped drive handle, plus picker grants like `drive.file`). The open
design question for this epic is the **third-party-origin case**: an app served from its own origin
asking the user's Poweur client for a handle.

Requirements for the spec (INT-007-T1):

- The app receives a handle to **its own folder** plus nodes the user explicitly opens; never the
  whole drive, contacts or system files.
- Keys stay in the user's Poweur client (host frame, popup or native shell); the app origin gets
  a capability, not the private key.
- The consent screen states the label, the app origin and what the handle can reach.
- Revocation is one action in Settings and is visible to the app as a failed handle.
- A compromised or malicious app origin can at worst read and alter its own folder. Document this
  limit plainly: a web app holding decrypted data must be trusted as code.
- Sign-in (OIDC) and connect (scoped handle) remain separate steps that can be combined in one
  consent screen.

## Candidate suite

Ratings are effort for Poweur sign-in plus the listed storage work, 1 (configuration) to 10
(rewrite). Licences and versions must be re-verified at scouting time.

| Area | Base | Licence | Sign-in | Storage work | Target label |
|------|------|---------|---------|--------------|--------------|
| Docs / wiki | La Suite Docs (`suitenumerique/docs`; Yjs, BlockNote, Django) | MIT | 2 | 6–8 (native) or export/sync to drive (4) | Hosted data; E2EE variant via EPIC-025 |
| Whiteboard | Excalidraw | MIT | 2 | 4 (scene files in app folder; rooms via EPIC-025) | E2EE |
| Video | La Suite Meet (`suitenumerique/meet`, LiveKit), Jitsi | MIT / Apache-2.0 | 2 | none | Hosted data (no stored content) |
| Files / groupware | Nextcloud | AGPL-3.0 | 2 | 7–9; prefer coexistence | Hosted data |
| Mail server | Stalwart (IMAP, JMAP, CalDAV, CardDAV) | AGPL-3.0 + enterprise licence | 3 | none | Hosted data; EPIC-023 is the Poweur-native path |
| Webmail | Bulwark, SnappyMail, Roundcube (JMAP/IMAP clients) | varies | 3 | none | Hosted data |
| Calendar / contacts | Stalwart or Radicale back end; UI to be built | AGPL / GPL | 3 | 4–5 for a new UI | Hosted data; E2EE calendar is a later build |
| Scheduling | Cal.com | AGPL + commercial directories | 3 | none | Hosted data |
| Publishing / sites | Payload CMS | MIT | 3 (custom auth strategy) | 3 media adapter; DB adapter 9 (avoid) | Hosted data; public folders (`/pub`) for the E2EE/static path |
| Photos | Immich | AGPL-3.0 | 2 | 6 | Hosted data |
| Passwords | Vaultwarden | AGPL-3.0 | 2 | none | Hosted data (its own vault) |
| Notes | Memos, Joplin | MIT | 2 | 3–5 (Joplin via `rclone serve`) | Hosted data / E2EE via rclone |
| Forms | OpnForm, Formbricks | AGPL / AGPL + commercial | 2 | 3 (CSV in drive) | Hosted data |
| Projects | Vikunja, Plane | AGPL / AGPL | 2 | 8 (sign-in only) | Hosted data |

Gaps with no good open-source base (build, or adopt an emerging project):

- **Spreadsheets.** Collabora and OnlyOffice work but run server-side on plaintext. Univer
  (Apache-2.0 core, TypeScript) is the most promising client-side engine; scout which features sit
  behind a paid tier.
- **Slides.** Only weak options today.
- **Calendar UI** that is pleasant and E2EE-aware (servers exist, clients do not).
- **Glue:** unified launcher/shell, cross-app search, org admin console, shared notifications,
  mobile shells. These are the product; most are private work (see below).
- **Email operations.** Code is small; deliverability, reputation and abuse handling are not.
- **Aliases** (SimpleLogin / addy.io are open source; this is EPIC-023 T9).

## Repository boundary

| Stays public (`INT`, this repo) | Stays private (`COM`) |
|---------------------------------|------------------------|
| Sign-in recipes, compatibility CI, registry entries and labels | Billing, plans, entitlements, invoicing |
| Plugins, adapters and connectors for upstream projects | Hosted provisioning, tenant lifecycle, infrastructure and secrets |
| **Any modified AGPL code that is run as a service** | Admin console, unified shell, branding, support tooling |
| Connect-flow spec and SDK changes | Go-to-market, pricing, SLAs |

Constraints:

- **AGPL applies to forks served to users.** If we modify an AGPL app and run it as a service,
  the modified source must be offered to its users. Such forks live in public repositories; only
  the operational wrapper stays private. MIT/Apache bases may live in either.
- Nothing in this repo contains hosted infrastructure details, secrets or pricing.
- Entitlements are enforced outside the relay (EPIC-023 plans note). The public protocol exposes
  what the relay already needs; plan names never appear in the protocol.
- This repo is not public yet, but INT files are written so they can be published unchanged.

## Free and paid layers (public summary)

The commercial packaging is decided privately. What this epic fixes publicly:

- **Storageless apps** (label E2EE) need no operator-side database, so they are listed and usable
  against any Poweur ID, hosted or self-hosted.
- **Apps with their own database** (label Hosted data) cost someone to run. Whoever runs them
  (Poweur, a third party or the user) decides who pays. The label tells the user which case it is.
- Entitlements attach to the identity, and identities remain portable to a self-hosted relay.

## Tasks

- [ ] **INT-007-T1** Connect-flow spec for third-party-origin static apps: scoped handle request,
      consent screen, revocation, threat model (extends EPIC-020 apps model); decision record on
      whether it lives in the SDK or the OIDC bridge (recommendation: SDK, separate from OIDC)
- [ ] **INT-007-T2** Data-handling labels: add `data_class` (`e2ee` | `shared-with-app` |
      `hosted`) to `conventions/registry.json`, the supported-apps page and the consent screen;
      update INT-006-T1 entry schema
- [ ] **INT-007-T3** Suite scouting: confirm licence, current OIDC support, storage hook and
      maintenance health for each base in the candidate table; record versions tested
- [ ] **INT-007-T4** Excalidraw storage adapter plus EPIC-025 room gating (first E2EE reference
      app; shares work with INT-004-T7)
- [ ] **INT-007-T5** La Suite Docs: OIDC recipe, then export/sync of documents into a drive
      folder; decide whether an EPIC-025 (Yjs) client replaces its persistence
- [ ] **INT-007-T6** Payload CMS: auth strategy plugin and media storage adapter; recipe for
      publishing from a public drive folder as the no-database alternative
- [ ] **INT-007-T7** Mail path decision record: Stalwart + JMAP webmail versus EPIC-023 bridge,
      and how each maps to the labels
- [ ] **INT-007-T8** Calendar/contacts: CalDAV/CardDAV back end choice and a minimal UI
      (FullCalendar + a CalDAV client library); E2EE calendar as a design note
- [ ] **INT-007-T9** Spreadsheet evaluation: Univer (client-side) versus Collabora/OnlyOffice
      (server-side); prototype one with the connect flow
- [ ] **INT-007-T10** Licence and fork register: per-app licence, any commercial-licence
      directories, and whether modified code is published (AGPL rule)
- [ ] **INT-007-T11** Public-repo hygiene check before publication: no hosted infrastructure,
      secrets or pricing in `epics/`, recipes or CI config

**Acceptance:** the supported-apps page shows every suite app with a data-handling label; one
E2EE app (Excalidraw) and one Hosted-data app (La Suite Docs) work end to end against a real relay
through the recipes; the connect-flow spec is reviewed and its threat model names what a malicious
app origin can and cannot reach.
