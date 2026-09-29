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
| E26-T4 Usage metering & quota enforcement | open | storage, public transfer, email, auth and later compute. **Interim (relay 0.1.19):** per-identity storage overrides in `STORAGE_QUOTAS_FILE` (re-read on change) and `QUOTA_CONTACT` named in 507s and `/files/{id}/quota`; poweur.net runs a small, unadvertised free quota raised by support on request. Entitlements from a billing service should write the same file until T4 replaces it |
| E26-T5 Hosted-service UI & support tools | open | plan, usage, seats, invoices, export/delete; audited support |
| E26-T6 Privacy, abuse, portability & financial operations | open | tax, refunds, suspension separation, reconciliation |
| E26-T7 Organization identity features | open | org-issued IDs, membership attestations, org-scoped OIDC (E22), org recovery guardians |
| E26-T8 Operator kit | open | any relay operator runs the same billing service with their own plans |

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

### The free/paid line (what the entitlement resources are)

Charge for what costs the operator money or effort; never for the network itself.

| Never charged (trust anchor) | Metered resources (hosted cost) | Cut on value moved (E16, E30) |
|---|---|---|
| Having an ID, keys, recovery, rotation | Stored unique bytes and version-history days | Pay-to-send fast lane |
| E2E messaging between Poweur IDs (fair use) | Public egress: links, identity pages, apps (EPIC-012/029) | Paid shares, subscriptions, tips |
| Contacts, inbox policy, anti-spam | TURN bytes (E25-T8) | Paid apps / in-app purchases |
| Sign-in, OAuth bridge for individuals | Outbound email, aliases, custom mail domains (E23) | Registrar referral for custom domains |
| Export and migration to another relay | Compute seconds (E27), SLAs, support, dedicated ops (E28) | |

Messaging is deliberately not a product counter: a text message is a couple of KB of
ciphertext, and it is the network effect. Large attachments already travel as file references
(E09-T4), so they meter as storage — the honest unit.

Illustrative poweur.net packaging (configuration, not code — revisit with real unit costs; object
storage is roughly €5–6/TB-month, so a 2 GB free user costs about a cent a month in bytes and the
real free-tier risk is abuse, not honest use):

| | Free | Personal (~€3/mo) | Pro (~€8/mo) | Team (~€5/seat) |
|---|---|---|---|---|
| Storage | 2 GB | 200 GB | 1 TB | 1 TB/seat pooled |
| Public egress | 20 GB/mo | 500 GB/mo | 2 TB/mo | pooled |
| Send transfers (E05-T7) | 2 GB, 7 days | 50 GB, 90 days | 250 GB, branding | pooled |
| Version history | 30 days | 1 year | 1 year | policy |
| TURN | 2 h/mo | 50 h/mo | 200 h/mo | pooled |
| Email (E23) | receive + 20 sends/day | 500/day, 10 aliases, custom domain | unlimited aliases | org domain |
| Compute (E27) | 60 min/mo | 20 h/mo | 100 h/mo | pooled |
| Commerce cut (E30) | 5 % | 3 % | 1.5 % | 1.5 % |

**Mobile:** store builds show plan state and link out to web checkout (reader-app / DMA
link-out rules) instead of in-app purchase — the platform cut would erase a thin margin.

**Anti-abuse interplay:** an active paid entitlement may *lower* the registration / anonymous-send
proof-of-work difficulty (E16's `payment` slot is the protocol-level analogue), but never lifts an
abuse suspension (T6).

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

### E26-T7 — Organization identity features

Organizations pay (T2); these tasks give them the identity features teams actually ask for,
without making an admin a key holder.

- [ ] Org identity (`acme.com`) publishes `poweur-sys/public/org.json`; membership attestations
      are signed by both org and member so clients can show "Bob · member of acme.com (verified)".
- [ ] Two membership modes: **org-issued IDs** (`bob.acme.com` via a hosted wildcard the org
      points at poweur.net, bulk-provisioned, enrolled through the E11 device ceremony) and
      **linked personal IDs** (`bob.poweur.net` joins and later leaves with his ID intact).
- [ ] Org-scoped issuer mode for the EPIC-022 bridge: members sign into company tools (Forgejo,
      Nextcloud, …) with group claims derived from org roles.
- [ ] Opt-in org recovery: an admin quorum as an E11 social-recovery guardian for org-issued IDs
      only; never for linked personal IDs.
- [ ] Offboarding: org-owned Space content stays with the org, member grants/devices under the
      org domain are revoked, personal IDs are untouched.

**Acceptance:** an admin provisions three `*.acme.com` members, one signs into a Forgejo instance
through the org issuer, one leaves and loses org access within one event cycle while a linked
personal member keeps their own identity and data.

### E26-T8 — Operator kit

Poweur-the-company is one operator. Letting any operator run the same billing makes "host a
Poweur relay" a business for others and keeps the project honest.

- [ ] The billing service is its own module and origin (the E22/E23 pattern), optional,
      configured entirely by environment/Ansible; plans, prices and provider credentials are the
      operator's.
- [ ] Ansible role and docs: run billing for your own relay with your own Stripe account.
- [ ] Integration test: a second test operator sells a plan end to end; its entitlements are
      honoured only by its own relays.

**Acceptance:** a fresh operator follows the docs and sells a test-mode plan without code changes.

## Non-goals

- Charging self-hosters to use the protocol or interoperable clients.
- Putting plan names, payment status or provider IDs in identity documents.
- Selling access to message/file contents or advertising profiles.
- Pay-to-send settlement, which remains EPIC-016.
- Dedicated managed deployments and SLAs, which are EPIC-028.
- Paid or auctioned premium names (EPIC-018 non-goal stands); a registrar referral for custom
  domains is fine.
- Rate-limiting ordinary contact-to-contact messaging as a pricing lever.
