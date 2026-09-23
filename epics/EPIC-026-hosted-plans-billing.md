# EPIC-026 — Hosted accounts, organizations, plans & billing

- **Status:** proposed
- **Priority:** P1 before charging for poweur.net; not a prerequisite for self-hosted protocol work
- **Depends on:** EPIC-002 (hosted identities), EPIC-003 (quota), EPIC-013 (operations),
  EPIC-018 (onboarding)
- **Interacts with:** EPIC-020 (unique-chunk accounting), EPIC-022 (hosted auth), EPIC-023
  (email allowances), EPIC-027 (compute metering), EPIC-028 (managed deployments)
- **Unlocks:** Free, Personal, Pro and Team services without coupling open protocol semantics to
  poweur.net's commercial policy

## Progress

| Task | Status | Notes |
|------|--------|-------|
| E26-T1 Commercial boundary & entitlement model | open | billing account is not a Poweur identity |
| E26-T2 Customer accounts & organization ownership | open | payer, members, roles, identity assignments |
| E26-T3 Plans, checkout, invoices & lifecycle | open | provider adapter; upgrades, failure, grace, cancellation |
| E26-T4 Usage metering & quota enforcement | open | storage, public transfer, email, auth and later compute |
| E26-T5 Hosted-service UI & support tools | open | plan, usage, seats, invoices, export/delete; audited support |
| E26-T6 Privacy, abuse, portability & financial operations | open | tax, refunds, suspension separation, reconciliation |

## Goal

Add the commercial control plane needed to operate poweur.net sustainably while keeping the
software, protocols and self-hosted experience open.

The central invariant is:

> A Poweur identity is not a billing account, and a hosted plan is not a cryptographic
> capability.

A customer account may pay for several identities, a domain, an organization or an agent. An
identity may move to another operator without changing its protocol data. Relays and services
consume generic entitlements such as `storage_bytes` or `email_send_monthly`; plan names and
payment-provider objects never enter signed identity documents or federated wire formats.

## Design direction

### Entitlements, not plan checks

Services receive a small, cached entitlement snapshot bound to the hosted subject:

```json
{
  "subject": "identity:alice.poweur.net",
  "storage_bytes": 107374182400,
  "public_transfer_bytes_monthly": 107374182400,
  "email_send_monthly": 1000,
  "aliases": 10,
  "custom_domains": 1,
  "valid_until": "...",
  "revision": 42
}
```

The billing service decides how a named plan produces this snapshot. Protocol services enforce
numbers/capabilities, cache briefly and fail according to documented availability rules. A
self-hoster supplies entitlements locally or runs without the hosted billing component.

### Organization is commercial ownership, not identity authority

An organization owns subscriptions, pooled allocations and administrative assignments. Poweur
group identities and Space roles remain signed collaboration authority. An organization admin may
allocate service resources or suspend hosting, but does not automatically receive a user's private
keys or decrypt their data.

## Tasks

### E26-T1 — Commercial boundary and entitlement contract

- [ ] Decision record separating identity, customer account, organization, subscription,
      entitlement subject, service usage and signed protocol authority.
- [ ] Versioned entitlement schema for identity-, organization-, service-identity- and
      deployment-scoped resources; no vendor plan names in consumers.
- [ ] Authenticated internal read/watch contract with revision, expiry, caching and revocation.
- [ ] Service behavior when billing is unavailable: active paid users receive a bounded grace
      cache; creation/upgrade waits; absence must not widen a quota or delete data.
- [ ] Map first resources: stored unique bytes, public-link transfer, outbound email, aliases,
      hosted OAuth clients and later automation compute.
- [ ] Self-hosted implementation/configuration that has no dependency on poweur.net.

**Acceptance:** relay, files and one bridge can consume a fake entitlement provider; switching it
off neither grants extra resources nor makes existing protocol data unreadable during the stated
grace window.

### E26-T2 — Customer accounts, organizations & ownership

- [ ] Customer account authentication and recovery distinct from hosted identity keys; prefer
      authenticating with a Poweur ID while supporting the operational recovery needed for billing.
- [ ] Bind/unbind hosted identities to a payer through an explicit signed consent flow; payment
      alone never proves control of an identity.
- [ ] Organization model: billing owner, billing admin, service admin, auditor and member.
- [ ] Seats, invitations, removal and ownership transfer; define what happens to individually
      controlled identities when a person leaves an organization.
- [ ] Allocate pooled storage/allowances without giving administrators content access.
- [ ] Managed-domain verification and assignment hooks; DNS control does not reveal identity keys.
- [ ] Audit every ownership, role and allocation change.

**Acceptance:** an organization pays for three independently controlled identities, reallocates
pooled storage without decrypting them, removes a member without stealing their identity, and
transfers billing ownership safely.

### E26-T3 — Plans, checkout, invoices & subscription lifecycle

- [ ] Provider-neutral billing interface; implement one initial provider (for example Stripe)
      behind it rather than leaking provider object IDs into product services.
- [ ] Product/price catalog with Free, Personal, Pro and Team as configuration, not code branches;
      monthly/annual prices and storage add-ons.
- [ ] Checkout, customer portal, tax address, invoices/receipts and webhook signature/idempotency.
- [ ] Upgrade immediately with prorated entitlement; downgrade at period end with an explicit
      over-quota outcome before confirmation.
- [ ] Payment failure state machine: retry, notification, grace, read-only restriction and eventual
      retention/deletion policy. Never immediately delete encrypted user data.
- [ ] Cancellation, refund/chargeback and reactivation semantics.

**Acceptance:** provider test mode covers signup, upgrade, downgrade while over quota, failed
renewal, grace, cancellation and replayed/out-of-order webhooks without duplicate charges or
incorrect entitlements.

### E26-T4 — Usage metering, limits & enforcement

- [ ] Durable usage ledger with idempotent events and reconciliation against each service's source
      of truth; counters are never derived solely from lossy metrics.
- [ ] Storage meters unique referenced chunks plus manifests/metadata; define treatment of shared
      content, versions, trash, incomplete uploads and garbage-collection delay.
- [ ] Public transfer, link downloads, email sends/aliases, OAuth activity and later automation
      compute meters, each with a documented billing unit and reset boundary.
- [ ] Soft warnings and hard limits; messages between established contacts remain fair-use rather
      than a tiny product counter.
- [ ] Reservation/commit/release protocol where an operation could overshoot a hard allowance.
- [ ] Usage API and alerts at configurable thresholds; owner and organization totals reconcile.
- [ ] Cost telemetry by backend operation so protocol chunking/traffic patterns cannot silently
      make a plan loss-making.

**Acceptance:** forced retries and service restarts do not double-count; authoritative usage can
rebuild the ledger; a hard quota stops new writes but preserves reads, export and deletion.

### E26-T5 — Hosted-service UI and support operations

- [ ] Account area for plan, renewal, payment method, invoices, usage, add-ons and cancellation.
- [ ] Organization area for invitations, roles, seats, allocations and managed domains.
- [ ] In-product limit warnings that explain the affected hosted resource without implying the
      Poweur identity or open protocol has expired.
- [ ] Export/move-to-another-relay journey available on every plan, including during grace and
      read-only restriction.
- [ ] Support console with least-privilege roles, reason/ticket capture, time-bounded elevation and
      immutable audit; no content/key access.
- [ ] Accessible responsive flows and Playwright coverage for customer and administrator journeys.

**Acceptance:** a customer can subscribe, attach an identity, see reconciled usage, download an
invoice, cancel and export without staff intervention; every staff mutation is attributable.

### E26-T6 — Privacy, abuse, portability & financial operations

- [ ] Data inventory and retention for customer, invoice, usage and support records; keep billing
      metadata out of public identity documents.
- [ ] Distinguish billing restriction, abuse suspension and protocol-level blocking in data model,
      notices and appeals. Paying does not bypass abuse controls.
- [ ] Account/organization deletion with statutory invoice retention and documented anonymization.
- [ ] Tax/VAT handling, invoice numbering, currencies, refunds and monthly provider reconciliation.
- [ ] Fraud controls for free trials, payment instruments, outbound email and public transfer,
      biased toward resource restriction rather than identity confiscation.
- [ ] Portability contract: export formats, redirect/migration period and removal of hosted bindings.
- [ ] Runbook and alerts for webhook backlog, entitlement divergence, meter lag and provider outage.

**Acceptance:** a reconciliation drill explains every entitlement from subscription and usage
records; deletion removes non-required personal data; an abuse-suspended paid user can export data
but cannot buy their way around the suspension.

## Non-goals

- Charging self-hosters to use the protocol or interoperable clients.
- Putting plan names, payment status or provider IDs in identity documents.
- Selling access to message/file contents or advertising profiles.
- Pay-to-send settlement, which remains EPIC-016.
- Dedicated managed deployments and SLAs, which are EPIC-028.

