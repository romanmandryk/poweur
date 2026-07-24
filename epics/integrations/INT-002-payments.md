# INT-002 — Payments integrations: crypto & conventional

- **Status:** proposed
- **Poweur prerequisites:** EPIC-001 (well-known serving), EPIC-006 (`poweur-sys/public` conventions), EPIC-009 (typed messages for receipts/requests)
- **Goal:** make a Poweur ID a **universal payment handle**: look up `alice.poweur.net`, see
  every way she accepts money — self-custodial crypto rails deeply integrated, conventional
  fintech handles published as a directory. "Send €20 to alice.poweur.net" should work no
  matter which rail the two parties share.

The foundation is one artifact: a **`payments.json` convention** (PCP) at
`poweur-sys/public/payments.json` — signed like everything else in the identity, listing
methods in preference order — superseding and absorbing the `_poweur-pay` TXT sketch already in
`apps/docs/docs/future/capabilities.md` (DNS TXT remains the equivalent for DNS-published IDs).

---

## A. Self-custodial / crypto rails (T1 → T4, deepest fits first)

### Lightning Address / LNURL — `lnurl/luds` (LUD-16)
The single most elegant fit on this list: a Lightning Address is *literally* a name that serves
`/.well-known/lnurlp/<user>` — and Poweur relays already serve well-known paths per identity
(EPIC-001). One relay endpoint makes **every hosted Poweur ID a working Lightning address** with
zero user setup: `alice.poweur.net` becomes payable from hundreds of existing wallets on day
one. For the LNURL ecosystem, Poweur is a massive new namespace of self-custodial addresses
backed by verified keys instead of custodial email-style handles — a direct strengthening of
their decentralization story.

### BTCPay Server — `btcpayserver/btcpayserver`
The standard self-hosted payment processor for merchants. Integration: a plugin (their plugin
system is active and welcoming) that (a) lets stores resolve a customer's Poweur ID for refunds
and receipts — receipts delivered as signed, E2E-encrypted Poweur messages instead of email —
and (b) offers "Sign in with Poweur" for store operators. BTCPay's whole ethos is
self-sovereignty without intermediaries; Poweur extends that ethos from payments to the
identity and communication around payments, which today still leak through email.

### Alby Hub — `getAlby/hub`
Popular open-source lightning node/wallet hub that popularized lightning addresses. Integration:
let Alby users attach their existing lightning address into `payments.json`, and accept
"pay to Poweur ID" lookups in the Alby extension. Alby gains address portability — users keep
`alice.poweur.net` even if they switch from Alby to another node — which turns a potential
lock-in objection into a selling point.

### OpenAlias (Monero ecosystem) — `openalias/openalias`
OpenAlias is a DNS-TXT standard mapping names to Monero/crypto addresses — structurally
identical to Poweur's original DNS records. Self-hosted Poweur IDs can carry OpenAlias TXT
records **today** with no code; the contribution is extending OpenAlias-consuming wallets to
also check the well-known/`payments.json` path, giving the OpenAlias ecosystem coverage of
hosted (wildcard) identities its DNS-only model can't reach.

### Ethereum wallets / ENS-adjacent tooling (e.g. Rainbow — `rainbow-me/rainbow`, frame.sh)
`payments.json` carries an `eth:` entry; wallets resolve `alice.poweur.net` → address the same
way they resolve ENS — except registration is free, renewable-fee-free, off-chain, and the same
name also receives messages and files. Pitch to wallet teams: name resolution users don't have
to buy, with a built-in encrypted messaging channel to the payee for invoices and receipts —
features ENS can't bundle.

### Interledger / Open Payments — `interledger/rafiki`
The Interledger Foundation's Open Payments standard uses **payment pointers** —
`$alice.poweur.net` — resolved via HTTPS, again exactly the well-known pattern Poweur already
implements. A relay endpoint exposing Open Payments account discovery makes Poweur IDs valid
payment pointers for the whole Web-Monetization/Rafiki ecosystem; in return Interledger gets a
user population whose pointers come with verified keys, contacts and messaging — the account
relationship layer their protocol deliberately leaves out.

### GNU Taler — `taler.net`
Privacy-preserving payments with EU institutional backing (and eIDAS adjacency — synergy with
INT-001). Integration: Taler merchants publish their Taler endpoint in `payments.json`, and
Taler's receipts/refund flows ride Poweur messaging. Taler gets discoverability (its chronic
gap: how do I find who accepts Taler?); Poweur gets a GDPR-native non-crypto rail.

### Fedimint — `fedimint/fedimint` & Cashu — `cashubtc`
Ecash systems searching for an addressing layer (both currently lean on LNURL bridges).
A Poweur ID as the recipient handle — encrypted ecash notes delivered as Poweur messages — is a
natural transport: the mint never learns the social graph, and recipients get tokens even when
offline (relay spools them). For these communities, that's a censorship-resistant payment inbox
with no new infrastructure.

---

## B. Conventional fintech & fiat handles (T1 — directory tier, deliberately shallow)

These are closed platforms; no partnership is required because the integration is **publishing,
not API access**: `payments.json` stores the user's handle, every Poweur client renders a
"pay alice" affordance that deep-links to the platform's existing pay-me URL.

### Revolut (`revolut:@handle` → revolut.me link)
Store the Revtag; clients render a Revolut button that opens the user's revolut.me page.
Users publish once and every Poweur-aware app — chat, invoicing, task tools — can show it.

### Wise (`wise:` → wise.com/pay link)
Same pattern; Wise's pay-links make it the best cross-currency fiat option for international
contacts and the natural default for freelancer use cases (pairs with task-convention work in
EPIC-006).

### PayPal (`paypal:` → paypal.me), Venmo, Cash App ($cashtag)
Commodity handles; included in the v1 `payments.json` schema so US-centric users see their
rails on day one.

### IBAN / SEPA (`iban:`) and UPI (`upi:` VPA)
Raw bank rails: an IBAN entry (already sketched in the existing capabilities doc) lets EU users
publish a transfer target with the owner's display name pre-verified by signature — mitigating
the IBAN-fraud/typo problem; UPI VPAs do the same for India's dominant rail.

### Open-source invoicing — Invoice Ninja `invoiceninja/invoiceninja`, Crater
Deeper than a handle: invoices generated by these tools are delivered as typed Poweur messages
(`sys.invoice` candidate PCP) with the PDF in a shared file, payment status flowing back as
acks. For invoicing tools this kills the "invoice lost in spam folder" support burden — invoices
arrive on a verified, spam-free channel with cryptographic delivery receipts.

---

## Tasks

- [ ] **INT-002-T1** `payments.json` PCP: schema (method, handle, label, preference order),
      signing, client rendering rules, DNS-TXT equivalence
- [ ] **INT-002-T2** Lightning-address endpoint on the relay (`/.well-known/lnurlp/`) mapping
      to a wallet configured in `payments.json` — demo with two major wallets
- [ ] **INT-002-T3** BTCPay Server plugin: Poweur sign-in + receipts-over-messaging PoC
- [ ] **INT-002-T4** Open Payments pointer endpoint PoC against Rafiki test network
- [ ] **INT-002-T5** Fiat-handle support in web/CLI clients (render + deep-link Revolut, Wise,
      PayPal, IBAN, UPI from `payments.json`)
- [ ] **INT-002-T6** OpenAlias coexistence doc + wallet outreach issue
- [ ] **INT-002-T7** `sys.invoice` message-type PCP + Invoice Ninja PoC
- [ ] **INT-002-T8** Payment-request UX study: "request €20 from bob" as a typed message with
      deep links per shared rail (design doc; feeds clients)

> **Inbound counterpart:** this epic publishes *how alice gets paid in general*
> (`payments.json`, LNURL/x402 endpoints). [EPIC-016](../EPIC-016-pow-v2-and-pay-to-send.md)
> *spends* those rails to gate an inbound message on a recipient-priced payment (pay-to-send),
> reusing the x402/L402/Stripe adapters and the `/.well-known/lnurlp/` endpoint from here.
