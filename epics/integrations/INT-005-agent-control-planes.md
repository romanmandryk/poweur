# INT-005 — Agent control planes: OpenClaw, Hermes & the gateway class

- **Status:** proposed
- **Poweur prerequisites:** [EPIC-017](../EPIC-017-typescript-client-sdk.md) (`@poweur/client` — hard
  blocker for the plugin), EPIC-009 (typed messages + attachments), EPIC-010-T1 (agent identity +
  `operated_by`), EPIC-008 / INT-000-T1 (Sign-In, OIDC bridge); EPIC-005 shares are shipped
- **Goal:** become the **identity, messaging and file layer for personal agent gateways** — the
  fast-growing class of self-hosted control planes (OpenClaw, Hermes, and the wave following them)
  that run an agent behind Telegram/Discord/Slack/WhatsApp/Signal. They have solved orchestration
  and have *not* solved identity: they know a Telegram user id and an allowlist, and nothing more.
  Poweur supplies verified who-is-this, a channel their users control, and — the part none of them
  can build alone — **artifact handoff between two different gateways run by two different people**.

> Sibling of [INT-003](INT-003-ai-agents.md), which covers frameworks and chat UIs. Control planes
> are split out because the integration shape is different: they are *messenger-shaped* hosts, so
> the deliverable is a **channel plugin**, not a toolkit or a login button.

---

## What these projects use for identity today

**OpenClaw** recognizes three identity sources, and only one is trusted:

- **Gateway profiles** — durable local accounts. Sign-in is delegated to infrastructure:
  GitHub-backed sign-in *through Cloudflare Access or Tailscale Serve* verifies the person's GitHub
  account. Gateway auth itself is a shared bearer token, a password env var, or trusted-proxy
  identity headers (`gateway.trustedProxies`).
- **Channel senders** — supplied by channel plugins (Telegram/Slack ids). Documented as
  **display-only, never matched against profile identities**; the gateway "does not guess a profile
  from a sender ID."
- **Agent identities** — configured system actors. A personality/config file, not a keypair.

Sessions are keyed by origin, with `session.dmScope` ∈ `main | per-peer | per-channel-peer |
per-account-channel-peer`. Multiple humans sharing one session is handled by
`session.identityLinks`, which maps several channel identities onto one canonical peer id —
**hand-maintained config, no proof**. Sessions record a write-once `createdActor` only when the
creation path can prove who caused it. The security model is explicit and deliberate: one trust
boundary per gateway, ownership filters are *not* an isolation mechanism, and mutually adversarial
users are told to run separate gateways or hosts. Open upstream requests: multi-tenant profiles
(`openclaw/openclaw#61123`) and per-user context files (`#18565`).

**Hermes** (Nous Research, MIT) is structurally the same with less surface: one gateway fronting
Telegram/Discord/Slack/WhatsApp/Signal/email/CLI, DM pairing plus per-platform allowlists, one
session plane shared across CLI, messaging and cron, and agent identity as a `SOUL.md`-style file.
Single-operator self-hosted by design; **no formal channel-plugin SDK** — a platform is a gateway
adapter under `gateway/`.

### The gap, mapped to what we already ship

| Concern | Control planes today | Poweur |
|---|---|---|
| Who is this human | platform handle asserted by the messenger + local allowlist | signed identity document, resolver chain (well-known → DNS TXT), fail-closed key pinning |
| Cross-channel linking | `identityLinks` config, manual, unverifiable | one ID *is* the identity on every channel |
| Who is this agent | a personality file | agent = Poweur ID with its own keypair; `operated_by` accountability chain (E10-T1) |
| Portability | per-gateway config; nothing a third party can check | any verifier resolves and checks a signature, with no account anywhere |
| Files between people | messenger attachments, per-platform limits, no shared namespace | per-identity home, WebDAV + changes feed, **signed share grants**, immediate revocation |
| Spam control | pairing codes, allowlists | contacts policy + PoW/anonymous ladder (E07/E14) |

### The wedge: cross-gateway artifact handoff

Two agent operators today can exchange **text through a messenger** and nothing else. There is no
way for my OpenClaw to hand your Hermes a 40 MB artifact, a patch series, or a working folder —
and no way for either agent to keep working in a shared space afterwards. A Poweur grant is exactly
that primitive, and it works **across vendors**: my OpenClaw ↔ your Hermes, or either ↔ a plain CLI
user. Lead every upstream conversation with this, not with "an alternative to Telegram" — the
messenger's value is its existing users, and we don't win that argument in a PR.

Honest scoping for the same conversations: Poweur identity buys these projects **verified
attribution, not isolation**. OpenClaw's single-trust-boundary stance is a deliberate design
choice and our IDs do not change it. The isolation story only materializes alongside their own
multi-tenant work (`#61123`), where path-scoped DAV tokens are the natural primitive to offer.

---

## Tasks

### INT-005-T1 — Scout & external PoC (no upstream code)

- [ ] Confirm current plugin contracts and contribution norms; find/participate in the existing
      upstream threads on decentralized identity and multi-tenancy (`#61123`, `#18565`)
- [ ] Build a standalone bridge on `@poweur/client`: a Poweur ID whose inbox is forwarded into a
      running gateway and whose replies are sent back out — works today, no upstream change
- [ ] Record the 2-minute demo per the [INT-000](INT-000-overview.md) playbook

**Acceptance:** a colleague messages `claw.alice.poweur.net` from the CLI or web app and the agent
answers; the loop is demoed end to end.

### INT-005-T2 — OpenClaw channel plugin (`poweur`) — the flagship deliverable

Their SDK maps onto our protocol cleanly; build to the documented contract:

- [ ] `openclaw.plugin.json` with `channels: ["poweur"]` + `channelConfigs` schema; register via
      `defineChannelPluginEntry()`
- [ ] `config.listAccountIds` / `resolveAccount` / `inspectAccount` — one account per Poweur ID the
      gateway operates (relay URL, identity, key reference; secrets never in workspace `.env`)
- [ ] Inbound: normalize relay messages into the canonical envelope (sender = Poweur ID, route =
      conversation, `thread_id` → thread), attachments via `toInboundMediaFacts()`
- [ ] Outbound: `defineChannelMessageAdapter()` with an honest capability matrix (no
      `nativeStreaming`; declare only what we prove) and the required capability-proof tests
- [ ] `security.dm.resolvePolicy` / `resolveAllowFrom` ← our contacts + inbox policy. **Pairing
      codes become largely redundant**: the sender is cryptographically verified, so the allowlist
      is a contact list rather than a guess — keep a pairing hook mapped onto contact requests /
      the PoW ladder for stranger ingress
- [ ] `messaging.resolveSessionConversation()` mapping `poweur-id (+ thread_id)` → conversation/thread
- [ ] Durable ingress: `createChannelIngressMonitor()` keyed on our message ids; our two-tick acks
      give the at-least-once + dedupe semantics they expect

**Acceptance:** plugin installs into a stock gateway, passes the core capability-proof tests, and
survives a restart mid-delivery without duplicating or dropping a message.

### INT-005-T3 — Attachments as shares (the differentiator)

- [ ] Outbound "attachment" = write into the operator's home + signed grant to the recipient ID +
      a typed `sys.file.shared` message — not a blob push (needs EPIC-009 typed messages)
- [ ] Inbound media = DAV fetch with a scoped token, surfaced through the channel's normal media
      path so the agent sees a file, not a URL
- [ ] Folder handoff: grant a working directory both agents can read/write, with the changes feed
      as the "your turn" signal
- [ ] Demo: delegate a task to a colleague's agent, get a patch **and** the artifacts back

**Acceptance:** a file larger than any messenger's attachment cap moves between two gateways run by
two people on different relays; revoking the grant cuts access on the next request.

### INT-005-T4 — Verified participants in shared sessions

- [ ] Poweur ID as the canonical peer id, so `identityLinks` becomes *derived and verifiable*
      instead of hand-written config
- [ ] Participant records and `createdActor` carry a verified actor (signed), separating proven
      identity from display labels in the transcript
- [ ] Upstream proposal framed as attribution + auditability; offer path-scoped DAV tokens as the
      per-user resource primitive if/when their multi-tenant work lands

**Acceptance:** in a session shared by three people, every turn is attributable to a verified ID
without any per-user config in the gateway.

### INT-005-T5 — Gateway profile sign-in with Poweur ID

- [ ] Zero-upstream-code path first: our OIDC bridge (INT-000-T1) behind their trusted-proxy /
      identity-header mode — a self-hosted alternative to requiring Cloudflare Access or Tailscale
- [ ] Document the deployment (proxy config, `trustedProxies` hardening, header mapping)
- [ ] Native provider PR only after the plugin has landed and the relationship exists

**Acceptance:** an operator signs in to the control UI as `alice.poweur.net` with no third-party IdP.

### INT-005-T6 — Hermes gateway adapter

- [ ] Decide the client story for Python: thin port vs. subprocessing the Go CLI (**open
      question** — the CLI is the cheaper start and keeps one canonical implementation)
- [ ] Platform adapter alongside the existing ones (`gateway/`): inbound poll/push, outbound send,
      allowlist ← contacts, DM pairing ← contact requests, slash commands unchanged
- [ ] Attachments via INT-005-T3 shares once the message type exists

**Acceptance:** Hermes answers a Poweur message and can hand back a file; adapter runs from their
config system with no core changes.

### INT-005-T7 — Key custody & threat model (write before the first PR)

- [ ] Document the rule: **the gateway holds the agent's identity, never the operator's.** Agent =
      own Poweur ID with `operated_by: alice.poweur.net` (signed both ways); the operator grants
      the agent scoped access to their home rather than handing over key material
- [ ] Enumerate what a compromised gateway can and cannot do under that model, and how revocation
      behaves (grant revoked → next request fails; agent key rotated → E11 flow)
- [ ] Guidance on E2EE expectations: an agent that reads your inbox is a *participant*, not a
      transport — say so plainly rather than implying zero-knowledge

**Acceptance:** the doc exists on our site and is linkable from the upstream PR description; it is
the first question a security-minded maintainer asks.

### INT-005-T8 — Upstream, docs & listing

- [ ] Ship as an external plugin/package first (their marketplaces allow it), then upstream PR led
      with *their* benefit, per the INT-000 playbook — merged is the goal, published is the baseline
- [ ] Tutorial on our docs site; entry in `conventions/registry.json`; badge for their README

## Risks & open questions

- **Philosophical fit.** OpenClaw's "one trust boundary per gateway" is a considered position;
  a PR that implies otherwise gets rejected. Pitch attribution + cross-gateway files, never
  "this makes multi-tenancy safe."
- **Maintainer bandwidth & scope.** Both projects move fast and carry large plugin surfaces. Offer
  to maintain the plugin; keep the diff plugin-shaped.
- **Relay dependency.** Integration implies a relay (ours or self-hosted). Self-host instructions
  must be in the plugin README, not a footnote — this audience self-hosts by disposition.
- **Network bootstrap.** Our channel starts with no users. Every demo must therefore show *files
  and artifacts*, where the incumbent messengers are weakest, rather than chat volume.
- **Moving targets.** Both projects' contracts change between releases; pin the SDK version and
  keep the capability-proof tests as the early-warning system.
