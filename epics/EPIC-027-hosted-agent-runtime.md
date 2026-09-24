# EPIC-027 — Hosted automation & agent runtime

- **Status:** proposed; validate EPIC-010's local runner before implementation
- **Priority:** P2
- **Depends on:** EPIC-010 (agent identities, SDK, rules and local runner), EPIC-020 (`append`,
  versioned storage), EPIC-026 (entitlements and metering)
- **Interacts with:** INT-003, INT-005, EPIC-024 (agents in Spaces), EPIC-013 (observability),
  EPIC-025-T7 / EPIC-029 (neutral authorities for multiplayer apps and games)
- **Unlocks:** paid scheduled automations, always-on agents and team-controlled execution

## Progress

| Task | Status | Notes |
|------|--------|-------|
| E27-T1 Execution/trust model | open | what runs, whose identity acts, approval and isolation |
| E27-T2 Sandboxed runner & durable scheduler | open | limits, queues, retries and cancellation |
| E27-T3 Secrets, grants & human approval | open | scoped credentials, no platform-held identity master keys |
| E27-T4 Audit, metering & billing | open | per-run record, compute/network/storage usage |
| E27-T5 Packages, publishers & customer runners | open | signed packages first; payments deferred |
| E27-T6 Operations, abuse & portability | open | egress policy, incident isolation, export/self-host |
| E27-T7 Room-bound app authorities | open | neutral game host / validator attached to an E25-T7 room |

## Goal

Offer the reliable operation users cannot get from a laptop-only automation runner: schedules,
durable event consumption, retries, isolated execution, secrets and auditable approvals. Preserve
EPIC-010's rule files and SDK so every hosted workflow can move to a customer-run runner without
rewriting or surrendering its identity.

The hosted runtime acts through short-lived, narrowly scoped delegation. It never stores an
identity master private key and never receives broader access merely because it bills the owner.

## Tasks

### E27-T1 — Execution and trust model

- [ ] Specify workflow/agent package, immutable version, requested resources, event triggers,
      scopes, egress domains and operator/publisher identity.
- [ ] Define principal attribution: user, agent identity, workflow version, runner instance and
      approving human appear distinctly in consequential actions.
- [ ] Threat model untrusted code, dependency confusion, covert exfiltration, fork bombs, crypto
      mining, event loops, cross-tenant leakage and malicious output.
- [ ] Choose initial sandbox boundary and document why it meets the threat model.
- [ ] Portable runner protocol shared by poweur.net and customer-supplied runners.

**Acceptance:** a security review can trace which principal authorizes every file/message/network
action, and the same workflow runs against the local EPIC-010 runner with declared degradation.

### E27-T2 — Sandboxed runner, scheduler & event delivery

- [ ] Durable schedules and subscriptions for typed messages, file changes and Space events.
- [ ] At-least-once delivery with event IDs, idempotency keys, bounded retries, dead-letter state
      and user-visible replay.
- [ ] CPU, wall time, memory, disk, process, network and concurrency limits enforced outside the
      guest runtime.
- [ ] Cancellation, timeout and deploy-new-version behavior; an update never silently mutates an
      already running execution.
- [ ] Warm/cold execution measurements and quotas suitable for a small public deployment.
- [ ] Integration harness runs adversarial and normal workflows across multiple tenants.

**Acceptance:** restarts do not lose triggers; duplicate delivery is identifiable; a malicious
workflow cannot exceed limits or observe another tenant's data.

### E27-T3 — Secrets, delegated grants & human approval

- [ ] Encrypted secrets store with per-workflow references, versioning, rotation and redacted logs;
      plaintext is released only into an authorized sandbox execution.
- [ ] Short-lived, audience-bound file/message capabilities minted from explicit user grants; no
      identity master key in the service.
- [ ] Human approval action that pauses durably, sends a signed request and binds approval to the
      exact proposed action/hash, expiry and approver.
- [ ] Revoke workflow, device, grant or secret and stop subsequent queued/retried actions.
- [ ] Bring-your-own model keys: LLM API keys are ordinary sealed secrets; the runtime does not
      resell inference, so there is no model margin to defend and no lock-in to one provider.
- [ ] UI shows requested scopes, egress and secrets before enablement and on every material update.

**Acceptance:** replacing a workflow with one requesting broader scope requires new approval;
revocation stops its next action; logs and support tooling never display secret plaintext.

### E27-T4 — Audit, usage metering & billing integration

- [ ] Append-only per-run record: trigger, package version, actor, approvals, inputs/outputs by
      reference, actions, resource use, status and retry lineage.
- [ ] Encrypt user-visible logs while retaining minimal operator metrics for abuse/capacity.
- [ ] Idempotent meters for CPU duration, memory tier, network egress and retained execution data;
      integrate through EPIC-026 entitlements rather than checking plan names.
- [ ] A small free allowance (order of an hour a month) so personal automations work on the free
      tier; exhausting it pauses the workflow and messages its operator rather than failing silently.
- [ ] Budget and rate controls per identity/organization/workflow; hard budget cannot strand an
      already authorized destructive action half-complete without a surfaced recovery state.
- [ ] Cost/usage views and alerts; export in an open format consumable by a self-hosted runner.

**Acceptance:** a retried run is billed according to documented policy exactly once; a user can
explain every consequential action and move the workflow plus history to their own runner.

### E27-T5 — Packages, publishers & customer-supplied runners

- [ ] Signed package manifest with source URL, reproducible/artifact hash, publisher Poweur ID,
      requested scopes, resources, egress and compatible runtime.
- [ ] Opt-in directory extending E10-T5; verified publisher means verified control/history, not a
      guarantee that code is safe.
- [ ] Install/update/rollback with scope-diff review and immutable version pinning.
- [ ] Register customer-supplied runners, attest their key, assign workloads and revoke them.
- [ ] Defer marketplace payments/revenue share until installation, retention and support demand
      are demonstrated; leave a provider-neutral receipt hook rather than a store monopoly.

**Acceptance:** a user installs a signed package, reviews scopes, pins a version, moves it to a
customer runner and rolls back without changing the workflow format or identity.

### E27-T6 — Operations, abuse, incident isolation & portability

- [ ] Egress proxy policy, DNS rebinding/private-network protection and per-destination limits.
- [ ] Tenant/workflow kill switches distinct from suspending its Poweur identity.
- [ ] Capacity, queue-lag, failure-rate and sandbox-violation telemetry with privacy-safe labels.
- [ ] Incident runbooks for leaked secret, malicious package, runaway event loop and regional
      runner loss.
- [ ] Retention/deletion policy for run inputs, outputs, logs and dead letters.
- [ ] One-command export and self-hosted import; hosted-only integrations identify their portable
      fallback explicitly.

**Acceptance:** an abuse drill disables one package across hosted runners without stopping other
workloads or confiscating publisher identities; affected owners can export and continue locally.

### E27-T7 — Room-bound app authorities

Most multiplayer apps can make the host player authoritative (EPIC-029 game kit). Some — ranked
games, auctions, anything where no participant should be trusted — need a neutral party.

- [ ] Attach a signed package to an E25-T7 room as its authority: it receives room events,
      validates moves, writes authoritative state to the room's durable document and is
      identified as `operated_by` the app publisher (E10-T1).
- [ ] Lifecycle bound to the room: start on first join, idle-stop, resume from durable state.
- [ ] Metered like any other run (T4); the room owner or the app publisher pays, declared in the
      app manifest.

**Acceptance:** an EPIC-029 demo game runs with a hosted authority; a client sending an illegal
move is rejected; killing the authority mid-game resumes from durable state.

## Non-goals

- Storing user identity master keys.
- An unrestricted general-purpose cloud-compute service.
- Making agents trusted merely because they are listed or paid.
- Marketplace commissions before the package ecosystem proves demand.

