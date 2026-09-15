# INT-000 — Integrations program: Poweur ID as first-party identity for open source

- **Status:** proposed
- **Theme:** adoption & ecosystem growth
- **Depends on (Poweur side):** EPIC-021 (generic OAuth/OIDC + IndieAuth bridge), EPIC-003/004
  (WebDAV + sync), EPIC-005 (sharing), EPIC-009 (typed messages), EPIC-010 (agent SDK, MCP)

## Why integrations are the growth engine

Poweur doesn't win by asking users to switch tools. It wins when the tools people already run
— Keycloak realms, Nextcloud servers, Discourse forums, n8n workflows, Open WebUI instances —
gain a login button, a share target, or a message channel that happens to be a Poweur ID.
Every merged upstream integration converts an existing community into Poweur-addressable users,
and gives that project something its competitors lack: **verified decentralized identity,
spam-free messaging, and user-owned file storage without running any of it themselves.**

The pitch to maintainers is always some subset of:

1. **Kill password/social-login dependency.** Sign-In with Poweur is stateless for the
   verifier — resolve `alice.poweur.net`, check a signature. No OAuth app registration with
   Google/Facebook, no client secrets, no account-recovery email infrastructure. Self-hosted
   projects especially hate depending on big-tech IdPs; this is the self-sovereign alternative
   that still has Google-login UX.
2. **Spam dies at the identity layer.** Comments, signups, federated shares and DMs can require
   a valid (optionally contact-vouched) Poweur ID. Projects spend enormous effort on CAPTCHAs
   and moderation queues; verified-identity-by-default removes whole abuse classes.
3. **User-owned data = zero-cost sync/storage backend.** Any app that can speak WebDAV (or just
   write files) can store its per-user data in the *user's* Poweur home — portable, syncable,
   shareable — instead of building accounts + storage + sharing themselves.
4. **A federation layer that already solved identity.** Projects with federation ambitions
   (Matrix, Nextcloud, Mastodon, XMPP) all struggle with "who is this remote user really?";
   Poweur's resolver chain + key pinning answers it.

## Integration tiers

Tag every integration with the depth it needs — small tiers first, deep tiers once trust exists:

| Tier | Name | What it means | Typical effort |
|------|------|---------------|----------------|
| T1 | **Directory** | The project's IDs/handles are *published* in the user's Poweur identity (`payments.json`, `profile.json` links) — no upstream code change required, or a one-line docs/format addition | hours |
| T2 | **Sign-In** | "Log in with Poweur ID" via our OIDC bridge or a native provider plugin | days |
| T3 | **Storage** | App reads/writes user data in the Poweur home (WebDAV/scoped tokens) | days–weeks |
| T4 | **Sharing & messaging** | Poweur IDs as first-class share targets / notification & event channels | weeks |
| T5 | **Native federation** | The project treats Poweur identity as a peer identity model (key verification, contact graph) | per-project |

## Execution playbook (every integration task follows this)

1. **Scout**: confirm the project's extension mechanism (plugin/SPI/provider/connector) and
   contribution norms; find the issue where they discuss decentralized auth (there almost
   always is one).
2. **PoC in our repo first**: build against the demo relay; record a 2-minute demo.
3. **Upstream issue → PR**: lead with *their* benefit (use the paragraphs in these epics as the
   pitch), keep the PR minimal and plugin-shaped, offer to maintain it.
4. **Docs + listing**: tutorial on our docs site, entry in the integrations registry
   (`conventions/registry.json`), badge for their README.
5. **Fallback**: if upstream stalls, ship as an external plugin/package in our org and list it
   in their marketplace (most projects below have one) — merged upstream is the goal, published
   and usable is the baseline.

## Epic index

| ID | Category | File |
|----|----------|------|
| INT-001 | Identity & verification providers (incl. EU/eIDAS) | [INT-001-identity-verification.md](INT-001-identity-verification.md) |
| INT-002 | Payments — crypto & conventional | [INT-002-payments.md](INT-002-payments.md) |
| INT-003 | Open-source AI tools & agent frameworks | [INT-003-ai-agents.md](INT-003-ai-agents.md) |
| INT-004 | Collaboration, productivity & federation tools | [INT-004-collaboration-tools.md](INT-004-collaboration-tools.md) |
| INT-005 | Agent control planes (OpenClaw, Hermes) | [INT-005-agent-control-planes.md](INT-005-agent-control-planes.md) |

## Cross-cutting prerequisite tasks (Poweur side)

### INT-000-T1 — Bridge adoption track

Bridge implementation, security and packaging are owned by
[EPIC-021](../EPIC-021-oauth-oidc-indieauth-bridge.md). This integration task begins once its
OIDC surface is usable and turns that generic service into upstream adoption:

- [ ] Maintain tested Keycloak, Authentik, Dex and oauth2-proxy configurations against the
      hosted issuer and an independently self-hosted bridge
- [ ] Reduce each integration to normal issuer/client/redirect configuration wherever possible;
      open an upstream change only when a documented product limitation requires it
- [ ] Feed failures in standards compatibility, claims or self-hosting back to EPIC-021 rather
      than adding project-specific behavior to the bridge

### INT-000-T2 — Integration starter kit
- [ ] "Integrate Poweur" landing page on the docs site: tiers, SDK links, demo relay
      credentials, brand assets ("Sign in with Poweur" button kit)
- [ ] Issue/PR templates for upstream pitches; the persuasive paragraph for each project in
      INT-001..004 is the opening of the upstream issue
- [ ] Public integrations registry + status board (scouted → PoC → PR open → merged)

### INT-000-T3 — Reference WebDAV/storage recipes
- [ ] Cookbook: "store your app's per-user data in the user's Poweur home" for the T3 tier —
      token acquisition, path conventions (`/apps/<reverse-dns>/`), conflict guidance, with
      runnable samples in Go/TS/Python
