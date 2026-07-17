# Poweur — Roadmap Epics

This folder contains the epics that take Poweur from a DNS-identity messaging MVP to a
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
| [EPIC-002](EPIC-002-relay-registration-and-persistence.md) | Relay-only registration, wildcard identities & durable relay storage | Identity / Infra | complete (PoW gate deferred) | E01 |
| [EPIC-003](EPIC-003-file-storage-webdav.md) | Per-identity file storage & WebDAV access | Files | complete (S3 provider deferred) | E02 |
| [EPIC-004](EPIC-004-file-sync.md) | File sync protocol & sync clients | Files | core complete (daemon, T5/T6 deferred) | E03 |
| [EPIC-005](EPIC-005-sharing-acl.md) | Sharing, ACLs, groups & public-to-any-valid-ID | Files / Trust | proposed | E03 |
| [EPIC-006](EPIC-006-poweur-sys-conventions.md) | `/poweur-sys` layout & application data conventions | Files / Ecosystem | proposed | E03 |
| [EPIC-007](EPIC-007-contacts-trust-antispam.md) | Contacts, trust & anti-spam | Trust / Messaging | proposed | E03, E06 |
| [EPIC-008](EPIC-008-sign-in.md) | Sign in with Poweur ID (third-party auth) | Identity / Ecosystem | proposed | E01 |
| [EPIC-009](EPIC-009-messaging-upgrades.md) | Messaging upgrades: persistence, push, attachments, groups | Messaging | proposed | E02, E03 |
| [EPIC-010](EPIC-010-agents-automation.md) | Agents, app ecosystem & no-code automations | Ecosystem | proposed | E04, E05, E09 |
| [EPIC-011](EPIC-011-key-management-recovery.md) | Key management, multi-passkey enrollment & recovery | Identity / Security | proposed | E01, E02 |
| [EPIC-012](EPIC-012-identity-websites.md) | Identity websites (active HTML, contact forms, hosting shape) | Files / Web | proposed (design notes) | E03, E06, E07, E09 |

## Integration epics (`integrations/`)

Adoption-focused epics targeting **upstream open-source projects** — work that mostly lands in
*their* repositories (plugins, providers, connectors) rather than this one. See
[INT-000-overview.md](integrations/INT-000-overview.md) for the tier model and execution
playbook.

| ID | Title | Depends on |
|----|-------|------------|
| [INT-000](integrations/INT-000-overview.md) | Integrations program overview, tiers & OIDC bridge | E01, E08 |
| [INT-001](integrations/INT-001-identity-verification.md) | Identity & verification providers (Keycloak, Authentik, Dex, EUDI/eIDAS, walt.id, …) | INT-000 |
| [INT-002](integrations/INT-002-payments.md) | Payments — crypto & conventional (Lightning, BTCPay, Open Payments, Revolut/Wise handles, …) | E01, E06 |
| [INT-003](integrations/INT-003-ai-agents.md) | AI tools & agent frameworks (MCP, Open WebUI, LangChain, n8n, OpenHands, …) | E04, E05, E09, E10 |
| [INT-004](integrations/INT-004-collaboration-tools.md) | Collaboration & federation tools (Nextcloud, Matrix, Discourse, Joplin, Forgejo, …) | INT-000, E03, E05 |

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
                   └─ feeds EPIC-008 (Poweur as recovery anchor for other services)

EPIC-003 + EPIC-007/009 ──► EPIC-012 (identity websites; design notes)
```

## Architecture deltas at a glance

Today (MVP):

- Identity keys & routing live **only in DNS TXT/A records**; registration requires a DNS
  provider API token and the relay performs zone writes (`apps/api/internal/dns/`).
- The relay is **stateless/in-memory** (`apps/api/internal/storage/`): inboxes, sessions and
  identities vanish on restart; DNS is the only durable store.
- Messaging is E2E-encrypted, signed (identity or session key + `SessionProof`), relayed
  relay-to-relay over HTTPS by resolving the recipient's A record.
- No file storage or sync exists yet.

Target (after these epics):

- Identity resolves via a **resolver chain**: DNS TXT *or* `https://<id>/.well-known/poweur/…`,
  enabling millions of IDs under a wildcard domain with zero DNS writes per user.
- The relay gains a **durable per-identity filesystem** (the user's "home"), which simultaneously
  backs `.well-known` identity documents, `/poweur-sys` configs, the public/shared/private file
  trees, message persistence and app data.
- A **sync protocol** (WebDAV + changes feed) keeps the home in sync with user devices; a
  **sharing model** grants access by Poweur ID and group with verifiable, signed grants.
- **Contacts and inbox policies** make spam-free communication the default.
- **Sign in with Poweur ID** plus SDKs/bridges (did:web, OIDC) let third-party apps adopt the
  identity, and store their data in the user's home under agreed conventions — making
  messaging + files the interop substrate for agentic automations.

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
