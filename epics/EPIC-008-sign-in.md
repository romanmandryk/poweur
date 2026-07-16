# EPIC-008 — Sign in with Poweur ID (third-party auth)

- **Status:** proposed
- **Priority:** P1
- **Depends on:** EPIC-001 (resolver chain); interacts with EPIC-003 (scoped resource access)
- **Unlocks:** EPIC-010 (apps acting on user homes), ecosystem adoption

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

- [ ] Spec `apps/docs/docs/auth/sign-in.md`: request object (canonical JSON + the fields
      already listed in capabilities.md), response object (signed payload incl. audience
      binding + key id), transport bindings (QR, deep link `poweur://auth?…`, redirect,
      cross-device polling endpoint), replay rules (nonce single-use, expiry ≤ 5 min),
      phishing analysis (origin binding: signed payload includes the *verified* RP origin;
      compare WebAuthn's clientDataJSON approach)
- [ ] Session-key delegation: allow signing by a registered session key + `SessionProof`
      (verifier validates the proof chain to the identity key — reuse the exact logic from
      `resolveSigningKey`) so daily sign-ins don't touch the long-lived key
- [ ] Verifier metadata at `/.well-known/poweur.json` (RP side) per the existing docs sketch
- [ ] Test vectors: valid flow, expired, wrong audience, replayed nonce, session-delegated

**Acceptance:** spec merged with vectors; security review issue checklist closed.

### E08-T2 — Verifier SDKs + reference RP

- [ ] Go package `pkg/signin-verifier`: parse/validate response, resolver-chain key fetch
      (E01-T3), nonce cache interface — usable by any Go backend in <20 lines
- [ ] TypeScript package (npm, monorepo `apps/`/`packages/`) with the same surface for
      Node/edge backends
- [ ] Reference relying party: a tiny demo site ("Poweur Guestbook") deployable from the repo,
      exercising QR + redirect flows against real identities — doubles as the integration test
      and the adoption tutorial
- [ ] Tutorial doc: "Add Sign in with Poweur to your app in 15 minutes"

**Acceptance:** demo RP works against a hosted identity end-to-end in CI (headless browser
test); both SDKs published.

### E08-T3 — Signer UX in web client and CLI

Native mobile apps are the end-state signer (per `requirements.md`), but the ecosystem needs
signers *now*:

- [ ] Web client: handle `poweur://auth` / pasted request / QR scan — show origin, action,
      statement, requested scopes; passkey-gated approval (`apps/web/js/passkey.js`);
      return via `response_uri` redirect or copy-paste code for cross-device
- [ ] CLI: `poweur auth approve <request>` for bots/agents (identity-key or session-key
      signing — flag mirrors the existing `--sign-with=identity` convention from the README)
- [ ] Consent records: approvals appended to `poweur-sys/private/logs/auth.log` (what was
      granted to whom, when) — surfaced later in E08-T4 UI
- [ ] Mobile design note for `apps/ios`/`apps/android`: deep-link registration, approval
      screen spec referencing this flow

**Acceptance:** sign in to the reference RP from the web client on a second device via QR;
bot signs in via CLI.

### E08-T4 — Scoped resource grants at sign-in ("connect your home")

The interop superpower: RP requests scopes, approval mints a relay token.

- [ ] Extend request object with `scopes` (`dav:rw:/apps/<app-id>/`, `messages:send`,
      `profile:read`) and human-readable scope rendering rules in the signer UX
- [ ] Relay endpoint `POST /auth/grant`: verifier exchanges the user-signed approval for a
      scoped token (TTL + refresh via re-presentation; builds directly on E03-T3 token store)
- [ ] App registrations file `poweur-sys/relay/connected-apps.json` (relay enforces
      revocation): granted scopes per RP,
      revocation by file edit; web UI list + revoke buttons
- [ ] Worked example: the reference RP stores guestbook entries in the *user's* home under
      `/apps/net.poweur.guestbook/` — the data-portability demo
- [ ] Threat analysis: scope escalation, confused-deputy via `response_uri`, token exfil
      blast-radius (path-scoped tokens cap it)

**Acceptance:** RP writes to its app namespace in the user's home after consent; revoking in
the web UI cuts access; audit trail visible.

### E08-T5 — Interop bridges: did:web and OIDC (design first)

- [ ] `did:web` document generation from the identity document (mechanical mapping; serve at
      `/.well-known/did.json` per did:web spec) — enables DID-consuming ecosystems to verify
      Poweur IDs without learning our formats
- [ ] Design doc: OIDC bridge — a stateless OP that fronts the Sign-In flow and issues
      id_tokens, letting any off-the-shelf OIDC RP accept Poweur IDs; enumerate what we do
      NOT bend on (no central OP requirement, bridge is optional and self-hostable)
- [ ] Survey note: SIOPv2/OpenID4VP overlap — adopt vocabulary where free, ignore where it
      drags in wallet-stack complexity

**Acceptance:** did:web docs served and resolvable by a standard DID resolver lib in a test;
OIDC bridge design doc merged with go/no-go recommendation.
