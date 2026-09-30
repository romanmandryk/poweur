# EPIC-010 — Agents, app ecosystem & no-code automations

- **Status:** proposed
- **Priority:** P2 (the payoff layer — start once the substrate is usable)
- **Depends on:** EPIC-004 (sync/changes feed), EPIC-005 (sharing), EPIC-008 (scoped grants), EPIC-009 (typed messages, push)
- **Unlocks:** the end-state vision — agents and apps collaborating through identity, messaging and files without custom integrations
- **Hosted execution:** the E10-T3 "relay-hosted runner" go/no-go is out of scope for this repository. Validate the local runner first.

## Goal

Make Poweur the easiest place to run **trusted agents and automations**: an agent is just a
Poweur ID (bots already are first-class in the protocol) with scoped access to homes, contacts
to talk to, typed messages for events, and the shared filesystem as the universal pipe between
apps. The target user experience: *"when a file lands in this shared folder, notify the team
and run this transformation"* — assembled without code, interoperable across vendors because
the substrate (IDs + files + messages + conventions) is shared.

## Background

- Bots/agents are already in the identity model (the original MVP requirements: "bots and automated agents
  manage their own key pairs programmatically"; CLI supports `--sign-with=identity` headless
  signing). What's missing is the *platform* around them: scoped capabilities, event sources,
  lifecycle and discovery.
- Every primitive this epic composes ships in earlier epics: path-scoped tokens (E03-T3),
  changes feed + `sys.sync.changed` (E04), share grants (E05), app namespaces + registries
  (E06), inbox policy for bot addresses (E07), scoped sign-in grants (E08), typed messages +
  WebSocket push (E09). This epic is mostly composition + DX + a rules engine.

## Tasks

### E10-T1 — Agent identity & capability profile

Define what makes an identity an "agent" operationally (not a new identity type — a profile).

- [ ] Spec `apps/docs/docs/agents/overview.md`: agent = Poweur ID + `capabilities.json` flag +
      owner link (`operated_by: alice.poweur.net`, signed by both — accountability chain for
      abuse handling and trust UX "this bot is run by alice")
- [ ] Delegation pattern: human grants their *own* agent scoped tokens to their home
      (path-scoped, from E03-T3) vs agent-with-own-home for standalone services — document
      both with worked examples
- [ ] Inbox-policy defaults for agents (`open` with rate limits, or contacts-only worker) —
      presets in the spec
- [ ] CLI: `poweur agent create <name> --operated-by <id>` scaffolding keys, registration,
      capability profile in one command

**Acceptance:** spec merged; one command yields a messageable agent identity whose operator is
verifiable in clients.

### E10-T2 — Agent SDK (Go + TypeScript)

The "write an agent in 30 lines" kit, wrapping: register/session, resolver, send/receive
(typed, E2EE), WebSocket subscribe, DAV/sync client, share accept.

- [ ] Go SDK consolidating what `apps/cli/internal/` already implements (session
      management, encryption, journal) into a public, documented package
- [ ] TypeScript SDK (Node) with the same surface — shares test vectors with Go for wire
      compatibility
- [ ] Event-handler ergonomics: `on("chat.text", fn)`, `on("sys.share.offer", fn)`,
      `onFileChanged("/apps/x/**", fn)` (changes-feed subscription under the hood)
- [ ] Example gallery (runnable, CI-tested): echo bot, file-drop converter (image lands in
      shared folder → thumbnail written back), daily-digest bot (reads task convention from
      E06-T5, messages a summary)

**Acceptance:** all three examples run against the integration harness; SDK docs published on
the docs site.

### E10-T3 — Automation rules engine ("poweur flows")

No-code automations as **files**: rule documents in the user's home, executed by a runner —
so automations themselves sync, share and version like everything else.

- [ ] Rule format PCP: trigger (message type/sender filter, file change glob, schedule) →
      conditions → actions (send message, move/copy file, create share, call agent, webhook)
      — JSON, schema-validated, stored in `/apps/net.poweur.flows/rules/`
- [ ] Runner v1: a daemon (reuses agent SDK) the user runs on their device or server with
      *their* scoped token — explicitly not relay-executed in v1 (keeps relay simple and the
      trust story clean: your automations run with your keys where you choose)
- [ ] Design doc: relay-hosted execution option (multi-tenant runner, resource limits,
      billing hooks) — go/no-go for v2
- [ ] Web UI: visual rule builder (trigger/action pickers fed by the registries from E06-T4)
      writing the same rule files
- [ ] Worked demo: the vision sentence — file lands in shared folder → team group message +
      converted copy in another folder, configured entirely in the web UI

**Acceptance:** demo automation assembled in the UI runs via the runner against two identities;
rule files survive sync round-trips.

### E10-T4 — Cross-app data pipelines (conventions in anger)

Prove "share data between apps without coding" with real seams.

- [ ] Webhook bridge agent (inbound: HTTP→message/file; outbound: event→HTTP) — the escape
      hatch to every existing SaaS; config is a file, credentials in `poweur-sys/private`
- [ ] Import/export adapters as agents: ICS calendar feed → convention files; RSS → messages;
      CSV drop → task convention (E06-T5) — each is both a useful tool and a template
- [ ] Document the pipeline pattern: app A writes convention files → share grants give app
      B's agent read access → B reacts via changes feed; contrast with point-to-point API
      integrations (n² connectors vs shared substrate)

**Acceptance:** an external webhook posts → file appears in a shared folder → flow rule
notifies a group → second app's agent consumes it; all configured, none of it coded.

### E10-T5 — Agent directory & trust surface

Discovery without a central app store; trust without blind installs.

- [ ] Directory convention: agents publish `poweur-sys/public/agent.json` (what it does,
      scopes it requests, operator, source URL); a crawler/registry site indexes *opt-in*
      agents (the registry is itself just an identity publishing files — dogfood)
- [ ] Client UX: before granting scopes to an agent (E08-T4 flow), show operator chain,
      requested scopes, and directory metadata
- [ ] Revocation & audit: `connected-apps.json` + access logs (E08-T4, E03-T4) presented as a
      single "what can touch my home" panel in the web app
- [ ] Abuse path: misbehaving agent → block (E07) + abuse report to operator's relay (E07-T5)

**Acceptance:** directory site lists the example agents; granting and revoking an agent's
access is fully visible and reversible from the web app panel.
