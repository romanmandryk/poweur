# EPIC-008 — Sign in with Poweur ID (third-party auth)

- **Status:** complete
- **Priority:** P1
- **Depends on:** EPIC-001 (resolver chain); interacts with EPIC-003 (scoped resource access)
- **Unlocks:** EPIC-010 (apps acting on user homes), ecosystem adoption

## Progress

| Task | Status | Notes |
|------|--------|-------|
| E08-T1 Protocol spec | **done** | [`apps/docs/docs/auth/sign-in.md`](../apps/docs/docs/auth/sign-in.md); wire objects, canonical string, origin normalization and scope vocabulary in `packages/identity/signin.go`; reference verifier/signer/metadata in `packages/identity/signin/`. Session delegation **reuses** the relay's proof chain — `CanonicalSessionRegistration` and `VerifySessionProof` moved into `packages/identity` and the relay's `acceptSessionProof` now calls them, so there is one implementation, not two. Vectors: `packages/identity/testdata/vectors/signin.json` (10 cases incl. expired, wrong audience, replayed nonce, session-delegated) |
| E08-T2 Verifier SDKs + reference RP | **done** | Go SDK is [`packages/identity/signin`](../packages/identity/signin) (`signin.NewVerifier(origin)` + one `Verify` call), not a separate `pkg/signin-verifier` — E08-T1 already landed the reference verifier there and a second copy would be a second thing to audit. TS twin: [`packages/client-ts/src/signin.ts`](../packages/client-ts/src/signin.ts), same surface, pinned to Go by all ten `signin.json` vectors (`test/signin.test.ts`, 109 tests). Reference RP: [`apps/guestbook`](../apps/guestbook) — deployable (`go run ./cmd/guestbook`), serves its own `/.well-known/poweur.json`, does redirect + deep-link + cross-device poll. Tutorial: [`apps/docs/docs/auth/add-sign-in.md`](../apps/docs/docs/auth/add-sign-in.md) |
| E08-T3 Signer UX | **done** | Web approval/paste/deep-link flow with origin and scope consent, CLI `auth approve`, durable audit log, and native mobile signer contract |
| E08-T4 Scoped resource grants | **done** | `POST /auth/grant`, one-hour path-scoped app tokens, `connected-apps.json`, immediate file-driven revocation, web list/revoke and audit UI; guestbook writes to the user's app namespace |
| E08-T5 Interop bridges | **done** | Mechanical `did:web` projection at `/.well-known/did.json` with resolution coverage; OIDC bridge and SIOPv2/OpenID4VP decision record |

## Goal

Any website, app or service can authenticate users by their Poweur ID — challenge-response
against the published key, verifiable by anyone via the resolver chain, no relay account
needed by the verifier. Beyond login, a relying party can request **scoped access to the
user's home and messaging** (the EPIC-003 token scopes), which is the "superpower": signing in
to a task app can grant it `dav:rw:/apps/net.poweur.tasks/` in *your* storage, making your data
portable across apps by construction.

## Background

- `requirements.md` and `apps/docs/docs/future/capabilities.md` already sketch the flow
  (request object with `domain/audience/nonce/issued_at/expires_at/action/statement`, QR/deep
  link handoff, mobile app as signer, verification against DNS) — this epic turns the sketch
  into a spec + reference implementations. The relay's own challenge-response
  (`GET /auth/challenge` in `apps/api/internal/relay/server.go`) is the in-house instance of
  the same pattern.
- The web client already does passkey-gated key usage (`apps/web/js/passkey.js`) — the consent
  surface exists in embryonic form even before native mobile apps land.
- Interop guidance from the existing docs stands: keep the DNS/HTTPS name canonical, bridge to
  `did:web` pragmatically, keep the response shape close enough to wallet ecosystems that an
  OIDC/SIOP bridge can be layered on (E08-T5), without bending our identity model to them.

## Design direction

Sign-In is **stateless for the verifier**: construct request → user signs with identity (or
delegated session) key → verifier resolves the ID and checks the signature. No tokens from us,
no federation metadata, no registration with a Poweur authority. Resource access (optional)
is a second step where the verifier exchanges the signed approval at the *user's relay* for a
scoped DAV/messaging token — the relay is the resource server, the user's signature is the
authorization grant (deliberately rhymes with OAuth so the OIDC bridge is thin).

## Tasks

### E08-T1 — Sign-In protocol spec

- [x] Spec `apps/docs/docs/auth/sign-in.md`: request object (canonical JSON + the fields
      already listed in capabilities.md), response object (signed payload incl. audience
      binding + key id), transport bindings (QR, deep link `poweur://auth?…`, redirect,
      cross-device polling endpoint), replay rules (nonce single-use, expiry ≤ 5 min),
      phishing analysis (origin binding: signed payload includes the *verified* RP origin;
      compare WebAuthn's clientDataJSON approach)
- [x] Session-key delegation: allow signing by a registered session key + `SessionProof`
      (verifier validates the proof chain to the identity key — reuse the exact logic from
      `resolveSigningKey`) so daily sign-ins don't touch the long-lived key
- [x] Verifier metadata at `/.well-known/poweur.json` (RP side) per the existing docs sketch
- [x] Test vectors: valid flow, expired, wrong audience, replayed nonce, session-delegated

**Acceptance:** spec merged with vectors; security review issue checklist closed.

**Shipped.** The canonical string is `CanonicalSignInResponse` (line-oriented,
`poweur-signin` tagged). Two decisions worth recording:

- The **RP does not sign the request.** An RP signature proves nothing a TLS-served
  `/.well-known/poweur.json` does not, and would put every RP into key management.
  Authenticity comes from the signer fetching RP metadata at `audience`, no redirects.
- The **app namespace is derived from the signed audience**, not declared. `dav:` scopes
  are capped to `apps/<reverse-DNS of the audience host>`, which makes cross-app escalation
  structurally impossible rather than policy-enforced.

### E08-T2 — Verifier SDKs + reference RP

- [x] Go package: parse/validate response, resolver-chain key fetch (E01-T3), nonce cache
      interface — usable by any Go backend in <20 lines. **Shipped as
      `packages/identity/signin`, not `pkg/signin-verifier`** (see the note below)
- [x] TypeScript package with the same surface for Node/edge backends —
      `packages/client-ts/src/signin.ts`, exported from `@poweur/client`
- [x] Reference relying party: "Poweur Guestbook" (`apps/guestbook`), deployable from the
      repo, exercising deep-link + redirect + cross-device poll flows against real
      identities — doubles as the integration fixture and the adoption tutorial
- [x] Tutorial doc: "Add Sign in with Poweur to your app in 15 minutes"

**Acceptance:** demo RP works against a hosted identity end-to-end in CI (headless browser
test); both SDKs published.

**Shipped.** One deviation from the task text, recorded rather than silently taken: the Go
SDK is `packages/identity/signin`, the package E08-T1 already put the reference verifier
in. A `pkg/signin-verifier` would have been a copy of it, and AGENTS.md asks for extending
existing packages over new top-level ones. Import path `github.com/poweur/identity/signin`.

End-to-end coverage is `apps/integration/signin_test.go` — real relay, real hosted
identity, the CLI as the signer — rather than a headless browser: the browser adds a
rendering surface, not a protocol surface, and `apps/web` already owns the Playwright
suite. The guestbook's own `server_test.go` covers what a correct RP *refuses*.

### E08-T3 — Signer UX in web client and CLI

Native mobile apps are the end-state signer (per `requirements.md`), but the ecosystem needs
signers *now*:

- [x] Web client: handle `poweur://auth` / pasted request / QR scan — show origin, action,
      statement, requested scopes; passkey-gated approval (`apps/web/js/passkey.js`);
      return via `response_uri` redirect or copy-paste code for cross-device
- [x] CLI: `poweur auth approve <request>` for bots/agents (identity-key or session-key
      signing — flag mirrors the existing `--sign-with=identity` convention from the README)
- [x] Consent records: approvals appended to `poweur-sys/private/logs/auth.log` (what was
      granted to whom, when) — surfaced later in E08-T4 UI
- [x] Mobile design note for `apps/ios`/`apps/android`: deep-link registration, approval
      screen spec referencing this flow

**Acceptance:** sign in to the reference RP from the web client on a second device via QR;
bot signs in via CLI.

### E08-T4 — Scoped resource grants at sign-in ("connect your home")

The interop superpower: RP requests scopes, approval mints a relay token.

- [x] Extend request object with `scopes` (`dav:rw:/apps/<app-id>/`, `messages:send`,
      `profile:read`) and human-readable scope rendering rules in the signer UX
- [x] Relay endpoint `POST /auth/grant`: verifier exchanges the user-signed approval for a
      scoped token (TTL + refresh via re-presentation; builds directly on E03-T3 token store)
- [x] App registrations file `poweur-sys/relay/connected-apps.json` (relay enforces
      revocation): granted scopes per RP,
      revocation by file edit; web UI list + revoke buttons
- [x] Worked example: the reference RP stores guestbook entries in the *user's* home under
      `/apps/net.poweur.guestbook/` — the data-portability demo
- [x] Threat analysis: scope escalation, confused-deputy via `response_uri`, token exfil
      blast-radius (path-scoped tokens cap it)

**Acceptance:** RP writes to its app namespace in the user's home after consent; revoking in
the web UI cuts access; audit trail visible.

### E08-T5 — Interop bridges: did:web and OIDC (design first)

- [x] `did:web` document generation from the identity document (mechanical mapping; serve at
      `/.well-known/did.json` per did:web spec) — enables DID-consuming ecosystems to verify
      Poweur IDs without learning our formats
- [x] Design doc: OIDC bridge — a stateless OP that fronts the Sign-In flow and issues
      id_tokens, letting any off-the-shelf OIDC RP accept Poweur IDs; enumerate what we do
      NOT bend on (no central OP requirement, bridge is optional and self-hostable)
- [x] Survey note: SIOPv2/OpenID4VP overlap — adopt vocabulary where free, ignore where it
      drags in wallet-stack complexity

**Acceptance:** did:web docs served and resolvable by a standard DID resolver lib in a test;
OIDC bridge design doc merged with go/no-go recommendation.

**Shipped.** `DIDWeb` emits the JSON/JWK shape consumed by did:web resolvers and the hosted
relay test follows the standard method's `did:web:<host>` → `/.well-known/did.json` resolution
algorithm, asserting both the DID subject and byte-identical key material. The bridge decision
record is [`apps/docs/docs/auth/interop-bridges.md`](../apps/docs/docs/auth/interop-bridges.md):
ship did:web; defer the optional, self-hostable OIDC OP until a named integration needs it.
