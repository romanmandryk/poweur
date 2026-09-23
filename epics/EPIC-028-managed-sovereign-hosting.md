# EPIC-028 — Managed & sovereign hosting

- **Status:** proposed; begin after organizational demand, not before the shared hosted service
- **Priority:** P3 / demand-led
- **Depends on:** EPIC-013 (deployment/observability), EPIC-020 (service topology), EPIC-026
  (organizations and commercial lifecycle)
- **Interacts with:** EPIC-011 (recovery), EPIC-022/023 (bridges), EPIC-027 (runners)
- **Unlocks:** dedicated, regional, customer-cloud and air-gapped commercial deployments

## Progress

| Task | Status | Notes |
|------|--------|-------|
| E28-T1 Service catalog, isolation & responsibility | open | shared/dedicated/customer-cloud/air-gapped |
| E28-T2 Reproducible provisioning & upgrade channels | open | versioned bundles; no snowflake installs |
| E28-T3 Backup, restore, DR & migration | open | encrypted backups, tested RPO/RTO, operator portability |
| E28-T4 SLO/SLA, support access & audit | open | consented access, evidence, incident communication |
| E28-T5 Residency, compliance & sovereign dependencies | open | data map, regions, bridge/provider limitations |
| E28-T6 Customer lifecycle & exit | open | trial, production, renewal, suspension, export, teardown |

## Goal

Sell operation and assurance rather than protocol exclusivity: dedicated Poweur deployments with
documented isolation, regional placement, backups, upgrades, support and service objectives. Every
deployment runs the same open components and publishes standard identities/endpoints. Customers
can take over or move to another operator without converting their data into a proprietary format.

## Tasks

### E28-T1 — Service catalog, isolation and responsibility model

- [ ] Define supported shapes: shared poweur.net, dedicated logical tenant, dedicated cluster,
      customer cloud and air-gapped/offline.
- [ ] For each shape document compute, storage, keys, network, databases, observability, backups,
      email/OAuth bridges and runner isolation.
- [ ] Shared-responsibility matrix: operator, customer admin, end user and external provider.
- [ ] Threat model noisy neighbor, operator compromise, cross-tenant routing, metadata exposure,
      support access and customer-cloud credential loss.
- [ ] Size/performance envelopes and transparent exclusions; sales promises must map to tested
      deployment profiles.

**Acceptance:** every sold topology maps to a versioned, testable profile with an explicit owner
for each key, backup, update and incident action.

### E28-T2 — Reproducible provisioning and upgrade channels

- [ ] Turn current Ansible/compose deployment into versioned profiles for combined and split
      control/files services, bridges and optional runners.
- [ ] Automated domain/certificate/service-binding bootstrap without exporting identity private
      keys to the operator.
- [ ] Stable, preview and security-only update channels with compatibility gates and rollback.
- [ ] Customer-cloud bootstrap with least-privilege credentials and drift detection.
- [ ] Offline bundle, dependency inventory and signed artifact verification for air-gapped sites.
- [ ] Provisioning conformance test verifies health, federation, backup and declared capabilities.

**Acceptance:** a clean environment reaches a tested production profile without manual mutation;
the same release upgrades and rolls back in shared, dedicated and customer-cloud fixtures.

### E28-T3 — Backup, restore, disaster recovery & migration

- [ ] Backup ownership and encryption model for control data, ciphertext file chunks, databases,
      bridge state and configuration; operator backup keys cannot decrypt user content.
- [ ] Incremental schedule, retention, immutable/off-site copy and integrity verification.
- [ ] Automated restore drills with measured RPO/RTO for each service profile.
- [ ] Region/cluster failover preserving identity routing, service bindings and replay protection.
- [ ] Import/export and cutover between poweur.net, dedicated deployment and customer-operated
      deployment with bounded dual-write/read-only windows.
- [ ] Disaster communication and customer evidence package.

**Acceptance:** a scheduled destructive-environment drill restores a representative organization
within its target and verifies messages, shares, device revocation and encrypted files end to end.

### E28-T4 — SLO/SLA, support access and audit evidence

- [ ] Define measurable SLOs for identity resolution, message acceptance/delivery, file service,
      auth/email bridges and recovery; exclude recipient/off-network dependencies explicitly.
- [ ] SLA tiers, maintenance notice, service-credit calculation and public/customer status views.
- [ ] Support-access workflow: customer approval, purpose, least privilege, expiry, recording and
      immutable audit; emergency access has notification and retrospective review.
- [ ] Tenant-scoped logs/metrics/traces and export without cross-tenant identifiers.
- [ ] Incident severity, notification deadlines, postmortem and vulnerability response process.

**Acceptance:** an SLO breach is computed from the same telemetry shown to the customer; a support
session expires automatically and produces a complete customer-visible audit record.

### E28-T5 — Residency, compliance and sovereign dependency map

- [ ] Data-flow inventory by service and deployment shape: content ciphertext, identity/control
      metadata, billing, telemetry, backups, email plaintext boundary and third-party processors.
- [ ] Regional placement and transfer controls for primary, replica, backup and operational logs.
- [ ] Document dependencies that cannot be sovereign in each configuration (payment provider,
      outbound email, push notification, DNS/CDN) and supported self-operated replacements.
- [ ] Retention/legal-hold/export capabilities without claiming relay access to E2EE plaintext.
- [ ] Evidence automation: configuration, version/SBOM, restore results, access audit and SLOs.
- [ ] Treat certifications as later business decisions; do not claim compliance from architecture
      alone.

**Acceptance:** a customer can trace every class of its data and external processor, select a
supported regional profile and receive evidence matching what the system actually enforces.

### E28-T6 — Customer lifecycle and credible exit

- [ ] Quote/order-to-provision handoff through EPIC-026 organizations and entitlements without
      hard-coding commercial contracts into deployment configuration.
- [ ] Trial, acceptance test, production cutover, capacity change, renewal and end-of-service
      runbooks.
- [ ] Billing failure and abuse suspension preserve export and distinguish infrastructure service
      from the customer's independent Poweur identities.
- [ ] Customer takeover: deliver deployment config, encrypted data, bindings, keys the customer
      owns and a verified recovery procedure.
- [ ] Migration to another provider with endpoint/key rotation and signed discovery updates.
- [ ] Teardown proof covering active data, replicas, backups and retained statutory records.

**Acceptance:** an exit drill moves a dedicated customer to a customer-operated deployment while
preserving IDs and Space/share references; the former environment is verifiably retired according
to retention policy.

## Non-goals

- Proprietary protocol extensions available only to managed-hosting customers.
- Promising access to or recovery of end-to-end encryption keys the operator does not possess.
- Certifications before customer demand and operating evidence justify them.
- Bespoke snowflake deployments that cannot follow the supported upgrade path.

