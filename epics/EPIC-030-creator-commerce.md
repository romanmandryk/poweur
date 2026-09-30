# EPIC-030 — Creator commerce: paid shares, subscriptions, tips & paid apps

- **Status:** proposed; demand-led — build after sharing, groups and identity pages show
  repeat use and creators ask for it
- **Priority:** P3
- **Depends on:** EPIC-016 (payment challenge protocol, hosted gateway and cut accounting — reuse,
  do not build a second gateway), INT-002 (`payments.json`, rails), EPIC-005 / EPIC-020-T11
  (grants, time-boxed shares), EPIC-026 (cut rate as an entitlement, invoices, tax)
- **Interacts with:** EPIC-012 (storefront on the identity site), EPIC-009 (broadcast channels),
  EPIC-023 (buyers by email), EPIC-029 (paid apps)
- **Monetizes (hosted):** a percentage on payments settled through the operator's gateway; **no
  cut** when a creator brings their own gateway/wallet on a self-hosted relay

## Progress

| Task | Status | Notes |
|------|--------|-------|
| E30-T1 Paid grant protocol | open | payment settles → owner's relay issues a signed grant |
| E30-T2 Subscriptions as renewing grants | open | each successful charge extends `expires_at` |
| E30-T3 Tips & pay-what-you-want | open | profile, site and message surfaces |
| E30-T4 Storefront block | open | typed EPIC-012 identity-page block listing public offers |
| E30-T5 Broadcast channels | open | 1 → many subscriber fan-out; distinct from small E2E groups |
| E30-T6 Paid apps & in-app purchases | open | EPIC-029 bridge `purchase()` |
| E30-T7 Payouts, refunds & receipts | open | Connect-style onboarding; refund revokes grant |

## Goal

Let anyone sell access to things in their home — a folder of templates, a course, an album, a
newsletter, an app — to buyers identified by Poweur IDs (or by email via EPIC-023). The primitive
is small because grants already exist: **payment → signed grant**. A subscription is a grant whose
expiry moves forward on each successful charge; a lapse is simply expiry.

## Background

- EPIC-016 turns the reserved `payment` challenge slot real and defines the hosted-gateway cut
  model for pay-to-send. The same gateway, settlement callback and accounting serve grants.
- Grants honour `expires_at` on every request with no cache window (E05); time-boxed and
  per-audience shares are planned in E20-T16.

## Tasks

### E30-T1 — Paid grants
- [ ] Offer document (`poweur-sys/public/offers/*.json`): target path or app product, price,
      currency, rails, duration; settlement → the owner's relay writes a grant to the buyer ID.

**Acceptance:** a buyer on another relay pays through a stub rail and can read the folder.

### E30-T2 — Subscriptions
- [ ] Recurring rails (card subscriptions first; Lightning/NWC later); dunning → expiry at period end.

**Acceptance:** a failed renewal expires access at period end; resubscribing restores it.

### E30-T3 — Tips
- [ ] Tip button on profile, site and messages; optional note; receipt as a typed message.

**Acceptance:** a tip arrives with a receipt and the cut is recorded.

### E30-T4 — Storefront
- [ ] Typed EPIC-012 identity-page block rendering public offers; checkout by Poweur ID or by email with a claim link.

**Acceptance:** a visitor without an ID buys by email and claims an ID that already holds the grant.

### E30-T5 — Broadcast channels
- [ ] Channel convention: subscribers are a group with a read grant; posts fan out across relays
      within advertised limits. Small E2E groups (E09-T5) stay unmetered; large channels meter
      fan-out as an EPIC-026 resource.

**Acceptance:** a 1 000-subscriber channel post reaches subscribers on three relays.

### E30-T6 — Paid apps
- [ ] `app.json` pricing; `purchase(productId)` over the EPIC-029 bridge issues a grant the app
      can check.

**Acceptance:** a demo game sells a cosmetic item end to end.

### E30-T7 — Payouts & refunds
- [ ] Seller onboarding with the payment provider (no custody of creator funds by the operator);
      refunds and chargebacks revoke the grant and notify the buyer.

**Acceptance:** a refund revokes access and both parties receive receipts.

## Non-goals

- Custody of creator funds; tokens/NFTs; advertising.
- Paid ranking in the EPIC-029 directory.
