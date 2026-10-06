# INT-006 — Quick-win apps & the supported-apps list

- **Status:** proposed
- **Theme:** adoption — a public, tested list of apps that work with Poweur today
- **Depends on (Poweur side):** EPIC-022 (OIDC/IndieAuth bridge), EPIC-020 (drive, shares, links,
  rclone backend E20-T13), EPIC-009 T7–T10 (intent types, typed routing, follow feeds), EPIC-014
  (anonymous messages), INT-000 (tiers, playbook); section E also EPIC-001 (identity documents)

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
4. **Identity-layer compatibility with a neighbour.** Solid (section E): a WebID profile and a
   Solid-OIDC provider mode, so a Poweur ID can sign in to Solid apps and reach Solid pods.

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
| Formbricks (`formbricks/formbricks`), OpnForm (`JhumanJ/OpnForm`) — forms | webhook → responses appended to a CSV in the owner's drive; "verified respondent" = signed Poweur ID, one response per ID | E26-T4's model inside a popular form builder |
| Cal.com (`calcom/cal.com`) — scheduling | app-store app: booking confirmations and changes as Poweur messages; attendee verified by ID | spam bookings die at the identity layer |
| Rallly (`lukevella/rallly`) — group polls | verified participants; results posted to a group | no duplicate or fake votes |
| Twenty (`twentyhq/twenty`) — CRM | Poweur ID as a contact field with pinned key; a messaging channel next to email | E26-T6's CRM model inside an existing CRM |
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

## E. Solid, the closest neighbour: a WebID and Solid-OIDC (T13, T14)

**Why.** [Solid](https://solidproject.org/) is the project closest to Poweur in purpose: people own
their identity and data, and apps ask for access instead of keeping a copy. EPIC-006 chose plain JSON
and files over Linked Data, and EPIC-020 chose end-to-end encryption, so the *data* layers differ on
purpose. The *identity* layers do not have to. If a Poweur ID can act as a Solid WebID and the bridge can
act as a Solid-OIDC provider, then people can sign in to Solid apps, and reach pods that grant their
WebID access, with the keys and passkeys they already have. No Solid project has to agree to anything.

**The real difference, and why it limits the scope.** A Solid pod server enforces access (WAC or ACP) on
data it can read. A Poweur relay holds ciphertext and metadata only, so it cannot enforce an ACL on
private content: access is whoever holds the key. That is the Poweur advantage for private data, and also
why a relay cannot *be* a Solid server for it. This epic covers identity only. (Data-layer options are
at the end.)

**What the Solid specifications require** (checked 5 Oct 2026 against `solidproject.org/TR/oidc`, `/wac`
and `/protocol`; re-read before building, they are still evolving):

| Piece | Requirement | Poweur today |
|-------|-------------|--------------|
| WebID | An HTTP URI that dereferences to an RDF profile (Turtle and JSON-LD) | identity document, `profile.json`, a `did:web` projection; no RDF profile |
| Issuer trust | The resource server reads the user's WebID profile for `solid:oidcIssuer`; this stops an issuer minting tokens for WebIDs it does not own | the bridge has an issuer (`https://oauth.poweur.org`); nothing links an ID to it |
| ID token | a `webid` claim; `aud` includes the client ID and `"solid"`; `azp`; `cnf` bound to a DPoP key | `sub`, `poweur_id`, no DPoP |
| Provider metadata | `webid` in `scopes_supported` | `openid`, `poweur_id`, `profile` |
| Client identity | dereferenceable client ID documents served as `application/ld+json` | URL client IDs exist for IndieAuth (EPIC-022); other format |
| Tokens | DPoP proofs on token and resource requests (RFC 9449) | none; no refresh tokens are issued |
| Access control | WAC (`acl:Read`, `Write`, `Append`, `Control`) or ACP, set by the pod server | share roles `read`, `write`, `append`, `create`, `admin` (EPIC-020) |
| Encryption | not specified; server-enforced access | end-to-end for private data |

### T13 — A WebID profile for every Poweur ID

- **Where.** `GET https://<id>/.well-known/poweur/webid`, content-negotiated to `text/turtle` and
  `application/ld+json`, served by the relay beside `profile.json` (same route family, CORS `*`, the
  resolver's hardening applies to anyone fetching it). The WebID is `https://<id>/.well-known/poweur/webid#me`.
  It is derived from public data only (identity document, `profile.json`), so it reveals nothing new.
- **Content.** `foaf:Agent` (an ID can be a person, a bot or an agent) with `foaf:name` and `foaf:img` from the
  profile, `solid:oidcIssuer` set to the relay's advertised bridge (`OAUTH_BRIDGE_URL`; a self-hosted relay
  names its own bridge), `owl:sameAs` the `did:web` identifier, and `schema:identifier` the Poweur ID. No
  `pim:storage`: a Poweur ID does not claim to be an LDP storage.
- **Opt-out.** A `capabilities.json` entry `webid` (default on) so an operator or owner can switch it off.
- **Tests.** Golden files for both serialisations; an RDF round-trip in CI (`n3` as a dev-only
  dependency) proving Turtle and JSON-LD say the same thing; a custom-domain ID served by its own relay.
- **Docs.** `apps/docs/docs/protocol/interoperability.md`: "WebID", next to the `did:web` section.

### T14 — The bridge as a Solid-OIDC provider

Behind a flag (`OAUTH_SOLID=on`), so a deployment that does not want it is unchanged.

- **Metadata.** Add `webid` to `scopes_supported`, `dpop_signing_alg_values_supported` (ES256, EdDSA) and the
  Solid-OIDC discovery marker.
- **ID token.** Add the `webid` claim (the ID's WebID from T13) when the `webid` scope is granted, put the
  client ID and `"solid"` in `aud`, set `azp`, and bind the token to the client's DPoP key (`cnf.jkt`).
  The existing `poweur_id` claim is unchanged.
- **DPoP (RFC 9449).** Validate the proof on the token request: signature, `htm`, `htu`, `iat` window, a
  `jti` replay cache (bounded, in the database like the other replay state) and an optional server nonce.
  Resource servers validate the proofs they receive, not the bridge.
- **Client identifiers.** Fetch and validate Solid client ID documents (JSON-LD with the Solid OIDC context:
  `client_id`, `redirect_uris`, `client_name`, grant types). Reuse the URL-client fetcher and its SSRF
  limits (no redirects, size cap, no private addresses); redirect URIs must match exactly, as for IndieAuth.
- **Consent.** One more line: "Share your WebID (the address of your Poweur ID's profile)". Nothing else
  changes in what an application can see: no messages, no files.
- **Refresh tokens: an open question that also unlocks other work.** The bridge issues none today. Solid
  client libraries keep sessions alive with refresh tokens, so without them a session ends when the first
  token expires. Find out what the libraries actually require; if they need refresh tokens, add them
  (rotation, reuse revokes the grant, DPoP-bound), which also turns on the indieauth.rocks tests that are
  skipped today (S501 to S505, S603).
- **Acceptance test (CI, nightly tier).** Run a Community Solid Server container with a pod whose ACL grants a
  Poweur WebID read access; a headless Solid client signs in through the bridge with a Poweur ID and reads a
  resource from that pod; a second ID without a grant gets a 403; a token for a WebID whose profile names
  another issuer is refused by the pod. Plus unit tests for DPoP (replay, wrong `htu`, stale `iat`).
- **Risks.** DPoP is easy to get subtly wrong (review it as security code, add it to the threat model);
  Solid-OIDC and ACP are still moving; some pod providers may restrict which issuers they accept even though
  the specification says to trust the profile; people may expect Poweur to *host* pods, so say plainly that it
  does not.

**Out of scope here, possible later (T15, only if someone asks):**

- A read-only LDP view of **public** folders, which are plaintext already.
- A **client-side** Solid facade (run by a person's own device or agent) that exposes their decrypted drive as an
  LDP storage for Solid apps. Poweur share roles map onto WAC like this: `read` is Read; `write` is Write
  and Append; `append` is Append; `admin` is Control; `create` has no direct equivalent (Append or Write on the
  parent container). A server-side version cannot work for private data.
- JSON-LD context files for the app-data conventions, so that PCP documents are also readable as Linked Data
  without making RDF a requirement (EPIC-006 stays plain JSON).

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
- [ ] **INT-006-T12** Static-site deploy recipes (Atom/RSS output for feeds moved to EPIC-027 E27-T1)
- [ ] **INT-006-T13** Solid WebID: serve a WebID profile for every Poweur ID (Turtle and JSON-LD, with
      `solid:oidcIssuer`), golden and round-trip tests, docs (section E)
- [ ] **INT-006-T14** Solid-OIDC provider mode in the bridge (`OAUTH_SOLID=on`): `webid` scope and claim, DPoP,
      Solid client ID documents, refresh-token decision; nightly test against a Community Solid Server pod
      (section E)

**Acceptance:** the supported-apps page lists at least 15 apps with green compatibility tests,
at least one upstream PR is merged, and each listed app has a recipe a new user can follow in
under ten minutes. For Solid (section E): a Poweur ID signs in to a Solid app and reads a resource from a pod
that granted its WebID, in a nightly CI test.
