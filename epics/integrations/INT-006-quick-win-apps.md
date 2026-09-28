# INT-006 — Quick-win apps & the supported-apps list

- **Status:** proposed
- **Theme:** adoption — a public, tested list of apps that work with Poweur today
- **Depends on (Poweur side):** EPIC-022 (OIDC/IndieAuth bridge), EPIC-020 (drive, shares, links,
  rclone backend E20-T13), EPIC-009 T7–T10 (intent types, typed routing, follow feeds), EPIC-014
  (anonymous messages), INT-000 (tiers, playbook)

## Why this epic

INT-001…005 are deep, per-project integrations. This epic is the opposite: **many small wins**,
each ending in a line on a public "Works with Poweur" list backed by an automated test. Three
kinds of win need no or almost no upstream code:

1. **Configuration only.** The app already speaks generic OIDC; the EPIC-022 bridge makes
   "Log in with Poweur ID" a tested config recipe.
2. **One PR, many apps.** Notification and handle libraries are shared by hundreds of apps;
   adding Poweur once reaches all of them.
3. **Protocol fit.** Apps whose design already matches Poweur's (key-in-fragment links,
   verified-identity anti-spam, "store it in the user's space") need a small plugin, a fork or
   a documented recipe.

**Fork policy.** When upstream is slow, ship a maintained fork or external plugin under the
Poweur org, rebased on each upstream release, listed as "Poweur fork" on the supported list, and
retired as soon as upstream merges. Every upstream PR follows the INT-000 playbook.

**Storage note.** With EPIC-020, private and shared files are end-to-end encrypted and the relay
has no WebDAV. Apps that need plaintext files reach them on the user's device (sync daemon,
`rclone serve`), or through the SDK; server-side apps that must read a user's files get a share
like any other identity (a service identity), which the user sees and can revoke.

## A. Configuration-only sign-in (T2) — the fastest list growth

Each entry = a tested recipe (docker compose + config + screenshots) against the hosted bridge
and a self-hosted bridge, plus a CI login test.

| App | What Poweur adds beyond login |
|-----|-------------------------------|
| Immich (`immich-app/immich`) — photos | later: share an album to a Poweur ID instead of a public link |
| HedgeDoc (`hedgedoc/hedgedoc`) — collaborative Markdown | verified co-authors; export notes into a Poweur drive via the SDK |
| Memos (`usememos/memos`) — quick notes | publish memos to a Poweur feed folder (E09-T10) |
| Miniflux (`miniflux/v2`) — RSS reader | follow Poweur feed folders through their Atom export (see D) |
| Karakeep (`karakeep-app/karakeep`) / Linkwarden (`linkwarden/linkwarden`) — bookmarks | share a collection with a Poweur ID |
| Wekan (`wekan/wekan`) — kanban | invite collaborators by Poweur ID |
| Paperless-ngx (`paperless-ngx/paperless-ngx`) — documents | ingest from a shared Poweur folder via the sync daemon |
| Actual Budget (`actualbudget/actual`) — budgeting | household budget shared between two IDs |
| Mealie (`mealie-recipes/mealie`) — recipes | share a recipe book with family IDs |
| Vaultwarden (`dani-garcia/vaultwarden`) — passwords (OIDC SSO since 1.35) | login only; the vault keeps its own master password |
| WriteFreely (`writefreely/writefreely`) — blogging | generic OAuth login; later, `list.subscribe` from readers |
| Outline, BookStack, Forgejo/Gitea, Grafana, Penpot | already in INT-003/004 — their recipes live in this list too |

To scout (login support unconfirmed): Lemmy, PieFed, Etherpad (`ep_openid_connect` plugin),
Docmost, Formbricks self-hosted SSO.

## B. One PR, many apps — notification and handle libraries (T1/T4)

### Apprise — `caronc/apprise`
The notification library behind many self-hosted tools (Uptime Kuma, changedetection.io and
others can deliver through it). A `poweur://` target sending a signed, encrypted message from a
bot identity makes Poweur a notification channel for every Apprise-enabled app at once, with
the recipient's inbox policy deciding what gets through.

### Shoutrrr — `containrrr/shoutrrr`
The Go notification library used by Watchtower and other Go tools. Same `poweur://` target on
the Go SDK.

### Uptime Kuma — `louislam/uptime-kuma`
Has its own provider list besides Apprise; a native provider gives a first-class "Poweur"
option in the UI — a visible listing for self-hosters.

### ntfy — `binwiederhier/ntfy` / Gotify — `gotify/server`
Push-notification servers. A bridge in either direction (topic → Poweur ID, Poweur typed
messages → phone push) is a small daemon on the SDK.

### Handles and proofs: Bluesky, Nostr, the Fediverse, Keyoxide
Zero upstream code — the relay (or the user's `.poweur/public`) serves the discovery files:

- **Bluesky:** an ID that links its DID gets `/.well-known/atproto-did`, so `alice.poweur.net`
  is a valid Bluesky handle.
- **Nostr:** `/.well-known/nostr.json` (NIP-05) for IDs that publish a Nostr key, so the ID is a
  verified Nostr identifier.
- **Fediverse:** WebFinger aliases so `@alice@poweur.net` resolves to the user's Mastodon account.
- **Keyoxide** (`keyoxide`): add Poweur as a claim/proof type so identity proofs link both ways.

Each is opt-in per ID (profile setting), written as a PCP so self-hosted relays serve the same files.

## C. Protocol-fit plugins and forks (T3/T4)

| App | Integration | Why it fits |
|-----|-------------|-------------|
| Send fork (`timvisee/send`) and PrivateBin (`PrivateBin/PrivateBin`) | sign in with Poweur; "send this link to a Poweur ID" as an encrypted message | both already keep the key in the URL fragment, like E20-T7 links |
| listmonk (`knadh/listmonk`) — newsletters | messenger plugin delivering campaigns as Poweur messages; import `sys.list.subscribe` requests as subscribers | a self-hosted newsletter that reaches Poweur IDs without email |
| Formbricks (`formbricks/formbricks`), OpnForm (`JhumanJ/OpnForm`) — forms | webhook → responses appended to a CSV in the owner's drive; "verified respondent" = signed Poweur ID, one response per ID | E31-T4's model inside a popular form builder |
| Cal.com (`calcom/cal.com`) — scheduling | app-store app: booking confirmations and changes as Poweur messages; attendee verified by ID | spam bookings die at the identity layer |
| Rallly (`lukevella/rallly`) — group polls | verified participants; results posted to a group | no duplicate or fake votes |
| Twenty (`twentyhq/twenty`) — CRM | Poweur ID as a contact field with pinned key; a messaging channel next to email | E31-T6's CRM model inside an existing CRM |
| Monica (`monicahq/monica`) — personal CRM | contacts as Poweur IDs; reminders as messages | the address book already is the social graph |
| Remark42 (`umputun/remark42`) — comments | Poweur as a login provider (Go) | comments without CAPTCHAs or big-tech logins |
| Excalidraw, CryptPad | already INT-004-T7 | — |

## D. Make our own formats readable by existing apps

- **Follow feeds (E09-T10) also publish Atom/RSS** next to `feed.json`, so every feed reader
  (Miniflux, FreshRSS, NetNewsWire) can follow a public Poweur feed with no integration at all.
- **Public folders served as a static site** (E03-T6 carried into EPIC-020): recipes for
  Hugo, Astro, Eleventy and Jekyll to deploy with `poweur drive put` or `rclone`.
- **Local plaintext for desktop tools:** `rclone serve webdav` (E20-T13) recipes for Joplin,
  Obsidian remotely-save, Logseq, Zotero WebDAV sync — replacing INT-004's direct-WebDAV recipes.

## Tasks

- [ ] **INT-006-T1** Supported-apps list: `apps/docs/docs/integrations/supported-apps.md` +
      `conventions/registry.json` entries (app, tier, path: config / plugin / upstream PR / fork,
      tested version, recipe link, status); "Works with Poweur" badge
- [ ] **INT-006-T2** Compatibility CI: nightly docker-compose job per listed app runs a headless
      login (and, where applicable, notification or share) test against the bridge and a real
      relay; an app is listed as "supported" only while its test is green
- [ ] **INT-006-T3** Section A recipes, in batches of five, starting with Immich, HedgeDoc,
      Memos, Miniflux and Vaultwarden
- [ ] **INT-006-T4** Apprise `poweur://` target (upstream PR) and Shoutrrr target; Uptime Kuma
      native provider
- [ ] **INT-006-T5** Handles: atproto-did, NIP-05 and WebFinger alias serving for opted-in IDs
      (PCP + relay + profile setting); Keyoxide proof type PR
- [ ] **INT-006-T6** Send/PrivateBin fork with Poweur sign-in and send-to-ID
- [ ] **INT-006-T7** listmonk messenger + subscription import
- [ ] **INT-006-T8** Formbricks or OpnForm → drive CSV + verified respondents
- [ ] **INT-006-T9** Cal.com app and Rallly verified participants
- [ ] **INT-006-T10** Twenty and Monica: Poweur ID contacts + messaging channel
- [ ] **INT-006-T11** Remark42 provider PR
- [ ] **INT-006-T12** Static-site deploy recipes (Atom/RSS output for feeds moved to EPIC-032 E32-T1)

**Acceptance:** the supported-apps page lists at least 15 apps with green compatibility tests,
at least one upstream PR is merged, and each listed app has a recipe a new user can follow in
under ten minutes.
