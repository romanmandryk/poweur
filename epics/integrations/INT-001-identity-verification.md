# INT-001 — Identity & verification integrations

- **Status:** proposed
- **Poweur prerequisites:** EPIC-001 (resolver chain, did:web), EPIC-008 (native Sign-In),
  EPIC-022 (generic OAuth/OIDC + IndieAuth bridge)
- **Goal:** make Poweur ID a first-party login and verification option in the identity
  infrastructure the self-hosted world already runs — and bind government-grade verification
  (eIDAS/EUDI) to Poweur IDs so an identity can be *both* self-sovereign and legally attested.

Each project below is one upstream task. Tier tags refer to INT-000.

---

## A. Identity providers & IAM platforms (T2 — native provider plugins)

These are multipliers: one merged plugin makes Poweur available to every app behind that IdP.

### Keycloak — `keycloak/keycloak`
The most widely deployed open-source IAM; used by enterprises, governments and countless
self-hosters. Integration: a Poweur **Identity Provider extension** (Keycloak's SPI is built
for this) so every realm can offer "Sign in with Poweur ID" beside Google and GitHub — except
with no OAuth app registration, no client secret rotation, and no dependency on a third-party
uptime: verification is a stateless signature check against the user's published key. For
Keycloak, this fills a real gap in its catalog — a decentralized, phishing-resistant,
self-sovereign provider — and instantly federates the entire Poweur namespace into any realm
that flips it on.

### Authentik — `goauthentik/authentik`
Fast-growing self-hosted IdP with first-class "sources" for external logins. A Poweur source
gives Authentik something its Google/Azure sources can't: logins that keep working when the
admin's cloud account doesn't, and verified external collaborators (contractors, partners) who
bring their own identity instead of being provisioned. Authentik's homelab-heavy community is
exactly the demographic that wants identity without big-tech dependency.

### Authelia — `authelia/authelia`
The standard auth portal in front of self-hosted reverse-proxied apps. A Poweur authentication
backend means a homelab's entire app surface (Jellyfin, Grafana, *everything* behind the proxy)
becomes Poweur-gated in one config block — and family/friends get access by Poweur ID instead
of the admin managing yet another user database. Smallest codebase of the IdPs here; a good
first merge.

### ZITADEL — `zitadel/zitadel`
Cloud-native IAM positioning itself as the modern Keycloak. ZITADEL already markets passwordless
and passkey-first auth; Poweur is the decentralized completion of that story — passkey-protected
keys the user owns outright. An IdP connector here targets startups choosing their auth stack
today.

### Ory Kratos — `ory/kratos`
API-first identity infrastructure embedded in many SaaS products. Poweur as a sign-in method
(their "OIDC provider" config plus, ideally, a native method) lets every Kratos-embedding
product offer decentralized login without each of them integrating individually.

### Dex — `dexidp/dex`
The OIDC shim used across the Kubernetes ecosystem. A tiny Poweur **connector** (Dex connectors
are ~300 lines) puts Poweur login in front of Kubernetes clusters, ArgoCD, and every other
Dex-fronted system — enormous surface for minimal code.

### Casdoor — `casdoor/casdoor`
IAM popular in the Chinese OSS ecosystem with 100+ login providers listed. Adding Poweur is
provider #101 by their architecture — cheap to land, and it carries Poweur into an ecosystem
the other IdPs don't reach.

### oauth2-proxy — `oauth2-proxy/oauth2-proxy`
The de-facto auth sidecar for anything HTTP. With the generic bridge (EPIC-022) it works
*today* with zero upstream changes — the task is a documented provider preset + tutorial, the
lowest-friction win on this list.

### Auth.js (NextAuth) — `nextauthjs/next-auth`
Not an IdP but *the* auth library of the JS/Next.js world. A published `@poweur/auth-js`
provider (their provider interface is a small object) puts "Sign in with Poweur" one npm
install away for tens of thousands of web apps — the highest-volume distribution channel for
T2 sign-in that exists.

---

## B. Government-grade & verifiable-credential verification (T1/T5)

The play: a Poweur ID stays pseudonymous by default, but the owner can attach **selective,
verifiable attestations** ("verified human", "EU resident", "over 18", "registered business")
issued by recognized schemes — stored as W3C Verifiable Credentials in
`poweur-sys/public/attestations/`. Verifiers check the credential signature against the issuing
scheme; the Poweur ID becomes the persistent, user-owned anchor those ecosystems currently lack.

### EUDI Wallet / eIDAS 2.0 reference implementation — `eu-digital-identity-wallet/*`
The EU is shipping a government digital-identity wallet to ~450M citizens, with open-source
reference implementations and a mandate that large platforms accept it. Integration: a
credential-presentation flow where a citizen binds an EUDI attestation (PID, age, residency) to
their Poweur ID as a stored VC — Poweur's did:web (E08-T5) makes the ID a valid VC subject. For
the EUDI ecosystem this answers a question the ARF leaves open: *where does a citizen's
persistent, cross-service pseudonymous identity live between presentations?* For Poweur it
means "verified EU citizen" badges on contacts — the strongest possible anti-spam and
anti-impersonation signal.

### walt.id — `walt-id/waltid-identity`
The leading open-source SSI/VC toolkit (issuer, verifier, wallet libraries), already aligned
with eIDAS 2.0 standards. Integration: walt.id issuers treat Poweur did:web identifiers as
credential subjects, and their verifier libraries resolve Poweur IDs natively. This gives
walt.id customers a concrete, user-friendly answer to "what DID do my users actually hold?",
and gives Poweur a complete, audited VC stack for INT-001-B without writing one.

### Yivi (formerly IRMA) — `privacybydesign/irmago`
Mature attribute-based credential system (Dutch government-adjacent) built around selective
disclosure. Integration: disclose an attribute ("over 18", "BSN-verified") *to* a Poweur-ID
binding ceremony, producing a stored attestation. Yivi gains a persistent decentralized
identity layer its session-based disclosures don't provide; Poweur gains privacy-preserving
verification with zero new cryptography.

### Open-source KYC bridges (e.g. Didit, Ballerine — `ballerine-io/ballerine`)
For services that need business-grade verification (marketplaces, payments), an adapter that
runs a KYC/KYB flow and issues the result as a VC bound to the Poweur ID. Positions Poweur as
the *reusable* KYC layer — verify once, present everywhere — which is precisely the cost these
platforms want to amortize.

---

## Tasks

- [ ] **INT-001-T1** Keycloak Poweur IdP extension (PoC → upstream/marketplace)
- [ ] **INT-001-T2** Dex connector + oauth2-proxy preset + tutorial (quick wins first)
- [ ] **INT-001-T3** Auth.js provider package published to npm
- [ ] **INT-001-T4** Authentik source, Authelia backend, ZITADEL/Kratos/Casdoor connectors
- [ ] **INT-001-T5** Attestation storage convention (PCP): VC format, `attestations/` layout,
      verifier behavior, revocation
- [ ] **INT-001-T6** EUDI/walt.id binding flow PoC: EU PID credential → attestation on a
      Poweur ID, demo verifier showing the badge
- [ ] **INT-001-T7** Yivi disclosure-to-attestation bridge PoC
