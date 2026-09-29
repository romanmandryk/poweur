# Poweur — Roadmap Epics

This folder tracks Poweur's evolution from its original DNS-identity messaging MVP toward a
**collaborative internet for people, agents and bots**, where:

- everyone can trust the identity of who they work with,
- resources (files, folders) can be shared easily and granularly with other Poweur IDs and groups,
- you communicate with identities you trust (contacts) and stay free of anonymous spam,
- you sign in to third-party apps with your Poweur ID and use native messaging + a synced,
  shareable filesystem as the interoperability layer between apps, pipelines and agents —
  comparable to Google Drive/Dropbox + real-time messaging, but identity-native and open.

## Format

- One file per epic: `EPIC-0NN-<slug>.md`.
- Each epic has **Goal**, **Background** (grounded in the current codebase), **Design direction**,
  and a list of **Tasks**. Tasks are written as self-contained units (`E0N-T0M`) with subtask
  checklists and acceptance criteria, so each task can be copied 1:1 into a GitHub issue and
  delivered as one or a few PRs.
- Task IDs are stable; reference them in PR titles, e.g. `E03-T2: WebDAV auth via Poweur session`.
- **Track progress in the epic file itself:** use `[x]` / `[ ]` on subtasks and a **Progress**
  table at the top (`done` / `partial` / `open`). Agents and humans update these when work ships
  or is deferred — treat epics as the issue tracker until GitHub issues are filed.

## Epic index

| ID | Title | Theme | Status | Depends on |
|----|-------|-------|--------|------------|
| [EPIC-001](EPIC-001-web-identity.md) | Web-based identity resolution (`/.well-known/poweur/`) | Identity | complete | — |
| [EPIC-002](EPIC-002-relay-registration-and-persistence.md) | Relay-only registration, wildcard identities & durable relay storage | Identity / Infra | complete (E02-T1 SQLite/bbolt index open) | E01 |
| [EPIC-003](EPIC-003-file-storage-webdav.md) | Per-identity file storage & WebDAV access | Files | complete, **deprecated** → EPIC-020 | E02 |
| [EPIC-004](EPIC-004-file-sync.md) | File sync protocol & sync clients | Files | core complete (T1–T4, T6 device registry done; T5 desktop/mobile + fsnotify daemon deferred) | E03 |
| [EPIC-005](EPIC-005-sharing-acl.md) | Sharing, ACLs, groups & public-to-any-valid-ID | Files / Trust / Growth | core complete (T3 offer/accept/mount + T4 links + T5 groups done; **T6 file requests/conversion partial; T7 Send open**) | E03 |
| [EPIC-006](EPIC-006-poweur-sys-conventions.md) | `/poweur-sys` layout & application data conventions | Files / Ecosystem | **complete** (T1–T5; tasks dogfood = PCP-0007) | E03 |
| [EPIC-007](EPIC-007-contacts-trust-antispam.md) | Contacts, trust & anti-spam | Trust / Messaging | **complete** (T1–T5; stranger PoW gate moved to E14-T3) | E03, E06 |
| [EPIC-008](EPIC-008-sign-in.md) | Sign in with Poweur ID (third-party auth) | Identity / Ecosystem | **complete** (T1–T6; T6: short sign-in codes by reference) | E01 |
| [EPIC-009](EPIC-009-messaging-upgrades.md) | Messaging upgrades: persistence, push, attachments, groups | Messaging | T1–T6 **complete**; **T7–T10 open** (intent types, typed routing to apps, shared inboxes, follow feeds) | E02, E03 |
| [EPIC-010](EPIC-010-agents-automation.md) | Agents, app ecosystem & no-code automations | Ecosystem | proposed | E04, E05, E09 |
| [EPIC-011](EPIC-011-key-management-recovery.md) | Key management, multi-passkey enrollment & recovery | Identity / Security | v1 complete (T1–T4, T8 done; T7 external review partial; T5/T6 later phases) | E01, E02 |
| [EPIC-012](EPIC-012-identity-websites.md) | Generated identity pages (trusted HTML, profile, contact actions) | Identity / Web | **done** (T1–T5) | E01, E06, E14, E15 |
| [EPIC-013](EPIC-013-prod-deployment-observability.md) | Production deployment & observability (OTLP, logs/events, consent modes, Grafana, public growth) | Infra / Ops | implemented; rollout steps in deploy/OPS.md, T5 deferred | E02 |
| [EPIC-014](EPIC-014-anonymous-messaging-challenges.md) | Anonymous messaging & sender challenges (proof-of-work) | Trust / Messaging | core complete (web app shipped with E15-T3; public identity-page action shipped in E12; stranger gate remains in E14-T3) | E06, E07 |
| [EPIC-015](EPIC-015-web-app-ux.md) | Web app UX: settings, contacts, files & sharing for a fresh user | Web / UX | in progress (T1–T6 done; T7–T12 open — host-aware front doors, desktop/tablet layout; T14 partial — logo & icons shipped, palette remap open) | E03, E04, E05, E06, E07, E14, E18 |
| [EPIC-016](EPIC-016-pow-v2-and-pay-to-send.md) | Sender-challenge v2: pluggable memory-hard PoW & pay-to-send | Trust / Messaging / Payments | proposed | E14, E07, E06, INT-002 |
| [EPIC-017](EPIC-017-typescript-client-sdk.md) | `@poweur/client` TypeScript client SDK (web app + every JS integration) | Clients / Ecosystem | in progress (T1–T6 + T8 done; docs shipped; npm publish/release workflow open in T7) | E01, E03, E04, E05, E14 |
| [EPIC-018](EPIC-018-identity-onboarding-naming.md) | Hosted identity onboarding: launcher, name policy & credential scope | Identity / UX | **complete** (T1–T5) | E02, E01, E14 |
| [EPIC-019](EPIC-019-mobile-app-capacitor.md) | Mobile app: Capacitor shell over the web client | Clients / Mobile | in progress (iOS simulator verified; Android generated and builds; push/background sync, packaging and existing-ID transfer open) | E15, E17, E18, E11 |
| [EPIC-020](EPIC-020-storage-protocol-v2.md) | Storage v2: end-to-end encrypted drive, stateless relay, filesystem/S3 providers, replace/append commits | Files / Trust / Infra | proposed, **P0** (rewritten 2026-09-25) | E05, E06, E09, E11 |
| [EPIC-021](EPIC-021-web-app-rewrite-react-tailwind.md) | React + Tailwind web app rewrite and `/app/` cutover | Web / UX | **complete** (T1–T14; the React app is `apps/web`, served at `/app/`; legacy app removed) | E15, E17, E19 |
| [EPIC-022](EPIC-022-oauth-oidc-indieauth-bridge.md) | Generic OAuth 2.0 / OIDC bridge with IndieAuth compatibility | Auth / Ecosystem | in progress (T1–T7, T9–T10 done; T8 partial — conformance, additional live clients and remaining operations work) | E01, E08, E13 |
| [EPIC-023](EPIC-023-email-bridge.md) | Email bridge: `john@poweur.net` for opted-in IDs, Emails tray, pluggable outbound | Messaging / Growth | proposed | E01, E06, E07, E09, E13, E14 |
| [EPIC-024](EPIC-024-spaces-collaborative-workspaces.md) | Spaces: group identity + files + discussion + activity as one collaborative object | Collaboration / Product | proposed | E05-T3, E09, E17 |
| [EPIC-025](EPIC-025-realtime-collaboration.md) | Real-time collaboration protocol & collaborative Markdown reference editor | Collaboration / Protocol | proposed | E20, E24, E17 |
| [EPIC-026](EPIC-026-hosted-plans-billing.md) | Hosted accounts, organizations, plans, entitlements & billing | Commercial / Hosted service | proposed | E02, E03, E13, E18 |
| [EPIC-027](EPIC-027-hosted-agent-runtime.md) | Hosted automation & agent runtime | Agents / Commercial / Infra | proposed after local-runner validation | E10, E20, E26 |
| [EPIC-028](EPIC-028-managed-sovereign-hosting.md) | Managed, dedicated, customer-cloud & sovereign hosting (+ secondary inbox, backup & directory for self-hosters) | Enterprise / Infra | proposed; demand-led | E13, E20, E26 |
| [EPIC-029](EPIC-029-poweur-apps-platform.md) | Poweur Apps: publish, open and share local-first apps (game kit included) | Ecosystem / Developers | proposed; after the E25 reference editor | E08, E06, E25, E17 |
| [EPIC-030](EPIC-030-creator-commerce.md) | Creator commerce: paid shares, subscriptions, tips, channels & paid apps | Payments / Commercial | proposed; demand-led | E16, INT-002, E05, E26 |
| [EPIC-031](EPIC-031-reference-app-scenarios.md) | Headless reference apps: collaboration scenarios as the acceptance gate | Apps / Quality | in progress (T1 harness, T2 Markdown done), **P0** (gates E20 waves 2–3 and E09-T7–T10) | E20, E09, E14, E24 |
| [EPIC-032](EPIC-032-public-web-feeds-boards-indexers.md) | Public web: feeds, following, community boards & indexers | Social / Growth | proposed | E20, E09, E14, E24, E31 |

## Integration epics (`integrations/`)

Adoption-focused epics targeting **upstream open-source projects** — work that mostly lands in
*their* repositories (plugins, providers, connectors) rather than this one. See
[INT-000-overview.md](integrations/INT-000-overview.md) for the tier model and execution
playbook.

| ID | Title | Depends on |
|----|-------|------------|
| [INT-000](integrations/INT-000-overview.md) | Integrations program overview, tiers & bridge adoption | E22 |
| [INT-001](integrations/INT-001-identity-verification.md) | Identity & verification providers (Keycloak, Authentik, Dex, EUDI/eIDAS, walt.id, …) | INT-000 |
| [INT-002](integrations/INT-002-payments.md) | Payments — crypto & conventional (Lightning, BTCPay, Open Payments, Revolut/Wise handles, …) | E01, E06 |
| [INT-003](integrations/INT-003-ai-agents.md) | AI tools & agent frameworks (MCP, Open WebUI, LangChain, n8n, OpenHands, …) | E04, E05, E09, E10 |
| [INT-004](integrations/INT-004-collaboration-tools.md) | Collaboration & federation tools (Nextcloud, Matrix, Discourse, Joplin, Forgejo, …) | INT-000, E03, E05 |
| [INT-005](integrations/INT-005-agent-control-planes.md) | Agent control planes (OpenClaw, Hermes & the gateway class) | E17, E09, E10, INT-000 |
| [INT-006](integrations/INT-006-quick-win-apps.md) | Quick-win apps & the supported-apps list (OIDC recipes, Apprise/Shoutrrr, handles, Send/listmonk/forms/Cal.com/CRM plugins) | E22, E20, E09, INT-000 |

## Dependency graph

```
EPIC-001 (web identity) ──► EPIC-002 (relay registration + persistence)
                                  │
                                  ▼
                            EPIC-003 (file storage + WebDAV)
                             │        │         │
              ┌──────────────┤        │         └──────────────┐
              ▼              ▼        ▼                        ▼
        EPIC-004 (sync) EPIC-005 (sharing) EPIC-006 (conventions)
              │              │                        │
              │              └──────────┬─────────────┘
              │                         ▼
              │                  EPIC-007 (contacts/anti-spam)
              │
EPIC-001 ──► EPIC-008 (sign-in)         EPIC-002/003 ──► EPIC-009 (messaging v2)
              │                                              │
              └──────────────► EPIC-010 (agents & automations) ◄──

EPIC-001/002 ──► EPIC-011 (key management & recovery)
                   ├─ uses EPIC-005 shares + EPIC-007 contacts (social recovery)
                   ├─ feeds EPIC-008 (Poweur as recovery anchor for other services)
                   └─ E11-T3/T8's enrollment ceremony (commit-then-reveal, short code / QR)
                      is what EPIC-019's E19-T8 reuses to bring an ID to a phone

EPIC-001/006 + EPIC-014/015 ──► EPIC-012 (generated identity pages)

EPIC-002 ──► EPIC-013 (deployment & observability; feeds every epic's ops story)
EPIC-006/007 ──► EPIC-014 (anon messaging + PoW challenges)
                   ├─ PoW primitive closes EPIC-002's deferred registration gate
                   └─ anon ingress may feed EPIC-012's advertised public contact action

EPIC-001/003/004/005/014 ──► EPIC-017 (@poweur/client TS SDK)
                   ├─ EPIC-015 web app consumes it (E15-T6 / E17-T6)
                   └─ unblocks INT-005 (control planes), INT-003-T1 (MCP), INT-003-T4 (n8n)

EPIC-017 + EPIC-009/010 ──► INT-005 (OpenClaw / Hermes channel plugin + share-based handoff)

EPIC-014 + INT-002 ──► EPIC-016 (sender-challenge v2: memory-hard PoW + pay-to-send)
                   ├─ fixes the sha256 GPU-vs-mobile asymmetry; algo made pluggable
                   └─ turns the reserved `payment` slot into a recipient-priced fast-lane

EPIC-003/004/005/006/007/014 ──► EPIC-015 (web app UX: the whole product surfaced)
                   ├─ absorbs the deferred "web UX" items of E05/E07/E14
                   └─ its reusable components (IdentityInput, ProfileCard, anon PoW send)
                      feed EPIC-012 identity pages / contact actions

EPIC-002/001/014 ──► EPIC-018 (onboarding: launcher host, name policy, credential scope)
                   ├─ owns identity *claiming*; E015 assumes an ID already exists
                   └─ hands off to E015's first-run onboarding on the identity's origin

EPIC-015 + EPIC-017 + EPIC-018 ──► EPIC-019 (Capacitor mobile shell)
                   ├─ wraps E015's UI verbatim — hence E015's mobile-first +
                   │  no-`location.origin` constraints
                   └─ `kdf:"native"` key custody frees the store build from
                      associated-domains, so self-hosters need no fork

EPIC-005/006/009/011 ──► EPIC-020 (storage v2: E2EE drive, stateless relay)
                   ├─ replaces EPIC-003's model: no relay WebDAV, no fixed roots, no relay database
                   ├─ Proton-style key tree; shares on any node; key-in-fragment links
                   ├─ relay settings are `.poweur/` files the owner (or an agent) edits
                   ├─ filesystem or S3 provider; presigned client ↔ bucket transfers
                   ├─ replace + append commits; message history v2 → E15-T13 paging
                   └─ append files are the substrate EPIC-025 builds realtime on

EPIC-020 + EPIC-009 (T7–T10) ──► EPIC-031 (headless reference apps)
                   ├─ Markdown docs, site + contact + newsletter, form → CSV, board, CRM, whiteboard
                   ├─ every user action via SDK or CLI; same relay, cross relay, Go + TS actors
                   └─ the modules become EPIC-029 templates; gaps are fixed in the protocol, not the apps

EPIC-020 + EPIC-009 (T7–T10) + EPIC-024 ──► EPIC-032 (public web)
                   ├─ one → many: feed folders, Atom/microformats, following lighter than contacts
                   ├─ many → many in a community: boards hosted by group identities (classifieds)
                   ├─ scale: CDN-cached public nodes + relay subscription proxy (memory only)
                   └─ network-wide search/topics/location: replaceable indexers, never in the relay

EPIC-001 + EPIC-008 + EPIC-013 ──► EPIC-022 (generic OAuth/OIDC + IndieAuth bridge)
                   ├─ one issuer can authenticate IDs hosted on any public Poweur relay
                   ├─ same-browser signer first, cross-device QR second
                   ├─ optional message push is a separate delivery channel, not a contact
                   └─ unlocks INT-000/001/003/004/005 sign-in integrations

EPIC-009 + EPIC-007 + EPIC-014 + EPIC-013 ──► EPIC-023 (email bridge)
                   ├─ separate service like EPIC-022: its own origin + MX, no relay credential
                   ├─ inbound mail arrives as `email.message`, encrypted to the user's key
                   ├─ outbound `email.send` gated by quotas + the E14 PoW primitive
                   └─ growth loops: footer, invites, email → native E2E thread upgrade

EPIC-005-T3 + EPIC-009 + EPIC-017 ──► EPIC-024 (Spaces)
                   ├─ composes a group identity, shared root and thread; no second ACL system
                   ├─ owns invitations, membership roles, activity, comments and mentions
                   └─ gives files/messages/agents one user-facing collaborative object

EPIC-020 + EPIC-024 + EPIC-017 ──► EPIC-025 (real-time collaboration)
                   ├─ adopts an existing CRDT; storage remains an unopinionated versioned store
                   ├─ separates durable updates from ephemeral presence/cursors
                   ├─ proves the protocol with a collaborative Markdown editor
                   └─ T7 exposes the session transport as a general rooms API; T8 meters TURN

EPIC-002/003/013/018 ──► EPIC-026 (hosted plans & billing)
                   ├─ customer and organization accounts stay separate from Poweur identities
                   ├─ services enforce generic entitlements, never vendor plan names
                   └─ supplies storage/email/auth/compute meters without changing federation

EPIC-010 + EPIC-020 + EPIC-026 ──► EPIC-027 (hosted agent runtime)
                   ├─ follows validation of the portable local runner
                   ├─ adds sandboxing, schedules, secrets, approval, audit and compute metering
                   └─ every workflow remains movable to a customer-run runner

EPIC-013 + EPIC-020 + EPIC-026 ──► EPIC-028 (managed & sovereign hosting)
                   ├─ dedicated/customer-cloud/air-gapped profiles run the open components
                   ├─ sells operation, SLOs, backups, support and evidence—not protocol access
                   ├─ demand-led; credible customer exit is an acceptance criterion
                   └─ T7–T9: secondary inbox, backup target and relay directory for self-hosters

EPIC-025 + EPIC-008 ──► EPIC-029 (Poweur Apps)
                   ├─ "an app is a folder": signed static bundles on a separate app domain
                   ├─ app data lives in the user's home; realtime via E25-T7 rooms
                   └─ game kit; neutral authorities via E27-T7

EPIC-016 + INT-002 + EPIC-026 ──► EPIC-030 (creator commerce)
                   ├─ payment → signed grant; subscription = renewing grant
                   └─ reuses E16's gateway and cut accounting; no cut for bring-your-own gateway
```

## Product, adoption & monetization sequence

The numbered epics are a dependency map, not a command to build everything in numeric order. For
the hosted service, use the following sequence unless user evidence changes it:

### Now — close the viral sharing loop

0. **Storage v2 first (EPIC-020 waves 1–4, with EPIC-009 T7–T10).** Pre-launch is the cheapest
   moment to change the storage model, and the remaining sharing work (E05-T6 notifications,
   E05-T7 Send) should be built once, on node shares and encrypted links, not twice. It is done
   when the EPIC-031 headless reference apps pass on one relay and across relays.

1. Finish **E05-T3** share offer → accept → recipient mount. The primitives exist and this is the
   missing end-to-end journey.
2. Build **E05-T6** file requests and guest-to-ID handoff. A guest completes the job first; claiming
   an ID adds durable edit/sync/mount capabilities rather than unlocking artificially withheld
   content.
3. Finish the open EPIC-015 front-door/onboarding work that these links land on.
4. Ship **E05-T7 Send** — WeTransfer-grade transfers with a verified sender, receipts and
   send-to-email. Mostly composition of T4/T6; the highest virality per line of code.

**Evidence to advance:** repeat public-link use, completed guest actions, accepted shares and
organic ID claims. Do not collect filenames, contents or exported social graphs to obtain it.

### Next — create the repeat-use product

5. Build **EPIC-024 Spaces** as the coherent home for a group, files, discussion and activity.
6. Implement **EPIC-023 Email** in parallel only where operational capacity permits; it gives an ID
   immediate usefulness outside the Poweur network and a path back to native conversations.
7. Execute **INT-000** and the smallest high-leverage integrations before broadening the protocol:
   integration starter kit, WebDAV recipes, OIDC configurations and one visible upstream win.

**Evidence to advance:** Spaces retained over multiple weeks, multiple active members, repeated
file/message activity, email opt-in and successful third-party sign-ins/integrations.

### Then — differentiated collaboration

8. Land the EPIC-020 portion EPIC-025 actually needs on top of waves 1–4: append latency and
   batching (E20-T15).
9. Build **EPIC-025** around one collaborative Markdown application. Do not begin with a generic
   Figma/office/game platform; use the reference app to validate the open session and document
   formats.

**Evidence to broaden:** users co-edit across devices/relays, offline edits converge, exported
documents remain useful and applications other than the reference editor ask to reuse the layer.

10. Once that evidence exists, open the layer to third parties with **EPIC-029 Poweur Apps**
    (signed static apps on a sandboxed origin, the E25-T7 rooms API and a game kit). Games and
    whiteboards are the showcase; developers pay nothing until an app's egress is significant.

### Monetize hosted convenience without closing the network

11. Build **EPIC-026** before accepting recurring payment: account ownership, organizations,
   entitlements, metering, lifecycle, invoices, export and deletion are one product surface.
12. Initial hosted tiers may package Free, Personal, Pro and Team, but names/prices remain billing
    configuration. Protocol services see only resource entitlements.
13. Keep contact-to-contact messaging, identity verification, federation, standard formats and
    migration available to free and self-hosted users. Charge for durable resources, public
    delivery, bridges, administrative control, support and guarantees.

**Evidence to expand:** paid conversion, storage/public-transfer unit economics, support load,
organization invitations and retention—not theoretical feature differentiation.

The free/paid line and an illustrative Free / Personal / Pro / Team table live in EPIC-026's
design direction. Short version: never charge for the ID, recovery, E2E messaging, sign-in or
export; meter what costs money (bytes, egress, TURN, outbound email, compute); take a cut only on
money moved through the operator's gateway (E16, EPIC-030). Mobile uses web checkout. Parallel,
non-plan funding worth pursuing early: NLnet / NGI Zero grants, sponsorships and support
contracts with institutional self-hosters.

### Demand-led expansion

14. Complete EPIC-010's portable/local agent runner and validate real workflows before building
    **EPIC-027 Hosted Runtime**.
15. Pursue **EPIC-028 Managed & Sovereign Hosting** only with design partners prepared to pay for
    isolation, residency, SLOs, backups or support.
16. Add marketplace payments only after third-party package installation and retention demonstrate
    demand; discovery and sideloading must remain open. **EPIC-030** covers paid shares,
    subscriptions, tips, channels and paid apps on the E16 gateway.
17. Offer the small self-hoster services in **E28-T7–T9** (secondary inbox, encrypted backup
    target, relay directory) early if community relays appear — they grow federation.

This sequence intentionally lets the open protocol create adoption while poweur.net monetizes
scarce hosted resources and operational assurance. A subscription or suspension can limit what
poweur.net hosts; it must not invalidate a person's Poweur identity or make exported data
proprietary.

## Architecture at a glance

### Current implementation

- **Identity is web-first with DNS fallback.** Clients resolve
  `https://<id>/.well-known/poweur/id.json` first and fall back to `_poweur` DNS TXT records.
  Hosted identities under `HOSTED_DOMAINS` register without a DNS-provider token; the legacy
  DNS-provider path remains available for identities whose operator wants the relay to write DNS.
- **The relay has mixed durable and ephemeral state.** With `POWEUR_DATA`, identity documents,
  encrypted keystore material, undelivered messages, delivery acknowledgements and identity home
  files survive restart. Short-lived sessions, auth challenges, pairing rendezvous, pending contact
  requests and anonymous inboxes remain memory-only by design or pending further durability work.
- **Messaging is signed and end-to-end encrypted.** Identity or delegated session keys sign typed
  envelopes; relays forward relay-to-relay over HTTPS, durably spool normal undelivered messages,
  expose SSE push, receipts and expiry, and support attachments and group fan-out.
- **Each hosted identity has a whole-file home filesystem.** The `relay-fs` provider exposes
  private, shared, public, `/apps` and `/poweur-sys` trees through WebDAV. A changes journal,
  manifest API and resumable upload endpoint provide incremental discovery and large-file resume;
  `poweur sync` is a one-shot/cron reconciliation client rather than a continuous daemon.
- **Sharing and trust primitives are live.** Owner-signed grants, cross-identity read/write,
  public links, owner-local and group-identity audiences, contacts, key pinning, inbox policy,
  anonymous encrypted messages and proof-of-work challenges are implemented. Share offers,
  acceptance messages and recipient mount references remain open in E05-T3.
- **The main clients and auth bridge exist.** The React + Tailwind web app is served at `/app/`
  and consumes `@poweur/client`; the TypeScript package also ships a CLI but is not yet published to
  npm. The Capacitor shell has been exercised on iOS Simulator and Android builds, with store
  packaging and background operation still open. The OAuth/OIDC/IndieAuth bridge is deployed and
  its remaining interoperability/operations work is tracked in E22-T8.

### Important current limitations

- Storage is whole-file, plaintext on the relay (only `poweur-sys/private` items such as message
  history are client-encrypted; attachment bytes are not), `relay-fs` only, with no
  version-checked commits or S3 provider. EPIC-020 replaces it with an end-to-end encrypted drive.
- Recipient share mounts and the share offer/accept/revoke lifecycle are incomplete, so recipients
  currently need to know who shared with them. File-request and guest-conversion flows are E05-T6.
- Desktop continuous sync, native Files/Storage integration, mobile push/background sync and store
  releases are unfinished. The web file browser does poll the changes feed while open.
- The email bridge, general agent/automation
  runtime, Spaces, real-time collaborative documents, subscriptions, managed hosting, the apps
  platform and creator commerce are not implemented.
- The relay's default inbox policy remains open when no policy document exists for compatibility;
  clients can establish the recommended contacts-and-requests policy during onboarding.

### Planned architecture

- **EPIC-020** replaces EPIC-003's storage with an end-to-end encrypted drive (Proton-style key
  tree, node shares, key-in-fragment links), a relay whose only durable state is files in the
  drive (`.poweur/`), filesystem or S3 providers, and replace/append commits; WebDAV is dropped
  from the relay and reached on desktop through the sync daemon or rclone.
- **EPIC-023** adds an isolated email bridge that translates opted-in email traffic into encrypted
  Poweur messages without giving the bridge relay credentials.
- **EPIC-024 and EPIC-025** compose existing groups, grants, messages and files into portable
  Spaces, then add explicit CRDT document formats and ephemeral live sessions without making the
  storage service merge arbitrary files.
- **EPIC-029 and EPIC-030** open the platform to third-party local-first apps (games included) and
  let identities sell access to their content through signed grants.
- **EPIC-026** adds customer accounts, organizations, generic entitlements and billing outside the
  federated protocol; self-hosters do not depend on it and hosted identities retain export and
  migration paths.
- **EPIC-027 and EPIC-028** add, only after demand is demonstrated, portable hosted agent execution
  and managed/dedicated/sovereign deployment operations.

## How to work on a task

1. Pick a task (`E0N-T0M`) whose epic dependencies are met; open a GitHub issue using the task
   text if one doesn't exist yet.
2. Spec-impacting tasks should land the spec change in `apps/docs/docs/` in the same PR (the
   docs site is the protocol's source of truth, see `apps/docs/docs/protocol/`).
3. Keep wire-format and storage-layout decisions versioned: new endpoints under the relay get
   documented in `apps/docs/docs/relay/api-reference.md`; conventions go to the new
   `apps/docs/docs/conventions/` section (created in EPIC-006).
4. Every protocol behavior needs integration coverage in `apps/integration/` (the suite runs the
   CLI, relay and a fake DNS zone in-process — see `apps/integration/fakedns/zone.go`).
