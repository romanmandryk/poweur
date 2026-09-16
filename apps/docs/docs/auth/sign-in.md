---
id: sign-in
sidebar_position: 1
title: Sign in with Poweur ID
---

# Sign in with Poweur ID

Any website, app or service can authenticate a user by their Poweur ID. The user proves
control of the key published at their name; the relying party checks the signature
against the resolver chain. **Nothing is registered with anyone**, no token is issued by
Poweur, and the verifier keeps no state but a five-minute list of spent nonces.

This page is the normative specification (EPIC-008 E08-T1). The canonical Go
implementation is `packages/identity/signin.go` plus `packages/identity/signin/`; the
TypeScript twin is `packages/client-ts/src/signin.ts`, pinned to Go by the conformance
vectors in `packages/identity/testdata/vectors/signin.json`.

## The shape of it

```
  relying party                     signer (web app / CLI / mobile)          resolver
       │                                          │                             │
       │ 1. build request  (audience = own origin)│                             │
       ├───── QR / deep link / redirect ─────────►│                             │
       │                                          │ 2. fetch                    │
       │◄──── GET /.well-known/poweur.json ───────┤    RP metadata over TLS     │
       ├──────────────────────────────────────────►    (this VERIFIES origin)   │
       │                                          │                             │
       │                                          │ 3. show consent, user approves
       │                                          │ 4. sign canonical string    │
       │◄──── response_uri POST / pasted code ────┤                             │
       │                                                                        │
       │ 5. resolve identity ──────────────────────────────────────────────────►│
       │ 6. verify signature, spend nonce → user is signed in                   │
       │                                                                        │
       │ 7. (optional) POST the same approval to the user's relay for a         │
       │    path-scoped DAV token — see "Scoped resource grants" below.         │
```

Step 7 is deliberately a *separate* step against a *different* server. Login costs the RP
one signature check; access to the user's home costs an exchange at the user's relay,
which is the resource server and the only party that can enforce revocation.

## Request object

The relying party builds this. It is **not signed by the RP** — an RP signature would
prove nothing that a TLS-served `/.well-known/poweur.json` does not already prove, and
would push every RP into key management. Authenticity of the request comes from the
signer fetching the RP's metadata at `audience` over TLS (step 2).

```json
{
  "poweur_auth": "1",
  "request_id": "req_9mJ0zvQb2hR1",
  "domain": "guestbook.poweur.net",
  "audience": "https://guestbook.poweur.net",
  "nonce": "8Kf3s2mQvX1pQ0aB",
  "issued_at": "2026-01-15T09:29:00Z",
  "expires_at": "2026-01-15T09:31:00Z",
  "action": "signin",
  "statement": "Sign in to the Poweur Guestbook",
  "response_uri": "https://guestbook.poweur.net/auth/callback",
  "scopes": ["dav:rw:apps/net.poweur.guestbook"]
}
```

| Field | Required | Rules |
|-------|----------|-------|
| `poweur_auth` | yes | protocol version, `"1"` |
| `request_id` | yes | opaque, RP-chosen; echoed in the response so a cross-device RP can match a pending login |
| `domain` | no | human-facing host; defaults to the audience's host. **Display only** — never trusted over the metadata fetch |
| `audience` | yes | the RP's origin. Normalized: lowercase scheme+host, default port dropped, no path, query, fragment or userinfo. `http://` is accepted only for local development |
| `nonce` | yes | single-use, unpredictable, ≥ 16 bytes of entropy recommended |
| `issued_at` / `expires_at` | yes | RFC3339. `expires_at - issued_at` ≤ **5 minutes** |
| `action` | yes | `signin`, `signup` or `link`. A closed set, because the signer must be able to say in one line what the user is approving |
| `statement` | no | one line, ≤ 300 bytes, no CR/LF or control characters (the canonical string is line-oriented) |
| `response_uri` | no | where the approval is delivered. **MUST be same-origin with `audience`** and MUST appear in the RP's published `response_uris` when it publishes any |
| `scopes` | no | resource scopes for step 7; normalized, de-duplicated and sorted. Empty means login only |

### Transport bindings

One encoded form serves every transport: compact JSON, `base64url` without padding.

| Transport | Form |
|-----------|------|
| Deep link / QR | `poweur://auth?request=<b64url>` |
| Web signer handoff | `https://poweur.net/app/?auth=<b64url>` (URL-escaped) |
| Redirect | RP navigates the browser to the signer with the same parameter |
| Cross-device paste | the user copies the encoded **response** back into the RP |
| Cross-device poll | the signer POSTs to `response_uri`; the RP's own front end polls the RP's `poll_uri` |

Decoders also accept raw JSON and padded base64url: a user pasting a code should not have
to know which they copied.

## Response object

```json
{
  "poweur_auth": "1",
  "request_id": "req_9mJ0zvQb2hR1",
  "identity": "alice.poweur.net",
  "audience": "https://guestbook.poweur.net",
  "nonce": "8Kf3s2mQvX1pQ0aB",
  "issued_at": "2026-01-15T09:29:00Z",
  "expires_at": "2026-01-15T09:31:00Z",
  "action": "signin",
  "statement": "Sign in to the Poweur Guestbook",
  "scopes": ["dav:rw:apps/net.poweur.guestbook"],
  "key_id": "identity",
  "signature": "…"
}
```

The response copies the request's validity window **verbatim**: an approval is valid
exactly as long as the challenge was, never longer. `key_id` is `identity` for the
long-lived key or `session:<session id>` for a delegated session key (see below), in
which case `session_proof` is attached.

### Canonical signing string

Line-oriented, `\n`-joined, no trailing newline. Every field a verifier decides on is
inside it:

```
poweur-signin
<poweur_auth>
<request_id>
<identity>
<audience>
<nonce>
<issued_at>
<expires_at>
<action>
<statement>
<scopes>
<key_id>
```

`<statement>` is the empty string when absent. `<scopes>` is the normalized, sorted list
joined with `,` — sorting is what makes the string order-independent, so a signer may
reorder scopes for display without breaking the signature. `<identity>` is lowercased and
trimmed. The signature is Ed25519 over the UTF-8 bytes, `base64url` (verifiers accept any
base64 variant, matching the rest of the protocol).

### Scope vocabulary

| Scope | Meaning |
|-------|---------|
| `profile:read` | read the user's public profile |
| `messages:send` | send messages as the user |
| `dav:read:<path>` | read files under a tree path |
| `dav:rw:<path>` | read and write files under a tree path |

`dav:` paths are normalized by stripping leading and trailing slashes, so
`dav:rw:/apps/net.example/` and `dav:rw:apps/net.example` are the same scope and produce
the same signature. `.` and `..` segments are rejected. At most 16 scopes — a consent
screen a user will not read is not consent.

A `dav:` scope may only reach into the RP's **own** app namespace, `apps/<app id>`, where
the app id is the reverse-DNS of the audience host: `https://guestbook.poweur.net` →
`net.poweur.guestbook`. The namespace is *derived from the signed audience*, never
declared by the RP, which is what makes cross-app escalation structurally impossible
rather than policy-enforced.

## Verification rules

A verifier runs these in order — cheapest and most local first, so a replayed or
misaddressed response never costs a DNS or HTTPS lookup:

1. **Shape and version.** `poweur_auth == "1"`; `request_id`, `nonce`, `signature`,
   `identity` present; identity is a valid Poweur name.
2. **Audience.** The normalized `audience` MUST equal the verifier's own origin. This is
   the anti-phishing check and it is not optional.
3. **Action, statement, scopes.** Known action; statement within limits; scopes in
   canonical form (**reject**, do not silently re-sort — a re-sorted list would verify
   against bytes the user never saw); every `dav:` scope inside `apps/<app id>`.
4. **Window.** RFC3339; `expires_at > issued_at`; `expires_at - issued_at ≤ 5 min`;
   `issued_at ≤ now + 2 min` (clock skew); `now ≤ expires_at`.
5. **Nonce.** Single use. Key the cache by `audience|identity|request_id|nonce` so one
   user cannot lock another out and a cache shared between RPs stays correct. Entries may
   be dropped once `expires_at` has passed — which is why the 5-minute cap exists at all.
6. **Resolution.** Resolve the identity through the published chain (HTTPS well-known,
   then DNS TXT; key mismatch fails closed).
7. **Signature.** Against the key that was valid at `issued_at` — the document's current
   key, or a `previous_keys` entry still inside its rotation grace window, so a sign-in
   signed moments before a rotation still verifies.

A verifier that keeps more than the nonce cache is doing something the protocol does not
ask for.

### Session-key delegation

A daily sign-in should not touch the long-lived identity key. A response may therefore be
signed by a registered **session key**, with `key_id: "session:<id>"` and the same
`session_proof` object the relay accepts on a forwarded message:

```json
"session_proof": {
  "session_public_key": "…",
  "issued_at": "2026-01-15T09:00:00Z",
  "expires_at": "2026-01-15T21:00:00Z",
  "nonce": "…",
  "identity_signature": "…"
}
```

The proof is the long-lived key's signature over:

```
session-registration
<identity>
<session_public_key>
<issued_at>
<expires_at>
<nonce>
```

The verifier validates the proof chain to the identity key and then verifies the response
with the session key. This is **the same code path the relay runs** —
`identity.VerifySessionProof`, which the relay's `acceptSessionProof` also calls — so
there is exactly one implementation to audit: completeness, RFC3339 timestamps, expiry,
the 24-hour session TTL cap, key encoding, and the identity signature. `key_id` naming a
session with no proof attached, or a proof attached with `key_id: "identity"`, is
rejected: the two must agree.

Delegation narrows nothing else. A session-signed approval carries the same scopes and
the same five-minute window; the relay additionally refuses to mint a grant on a proof
whose session has since been revoked.

## Relying-party metadata

`GET https://<rp-origin>/.well-known/poweur.json`

```json
{
  "poweur_auth": "1",
  "origin": "https://guestbook.poweur.net",
  "name": "Poweur Guestbook",
  "app_id": "net.poweur.guestbook",
  "response_uris": ["https://guestbook.poweur.net/auth/callback"],
  "poll_uri": "https://guestbook.poweur.net/auth/poll",
  "scopes": ["profile:read", "dav:rw:apps/net.poweur.guestbook"],
  "transports": ["redirect", "qr", "poll"],
  "contact_uri": "https://guestbook.poweur.net/abuse"
}
```

This is the only "federation" document in the protocol, it is served by the RP itself,
and **nobody registers it anywhere**. Rules a signer enforces:

- `origin` must equal the origin the document was fetched from.
- Redirects are **not followed** on the fetch — a redirect would let one origin answer
  for another, which is precisely the confusion the fetch exists to prevent.
- `logo_uri`, `poll_uri` and every `response_uris` entry must be same-origin. A signer
  that loads a third-party logo leaks the pending approval to that third party.
- `app_id`, when present, must equal the reverse-DNS of the origin host.
- Every advertised scope must sit in the RP's own namespace.
- Body capped at 16 KiB.

An RP that publishes no `response_uris` accepts any same-origin one — a permissive
default for a demo. **Publishing the list is the hardened posture**: it stops an open
redirect elsewhere on the RP from turning into an approval leak.

## Phishing and replay analysis

### Origin binding (the WebAuthn property)

The signed bytes contain the *verified* RP origin, exactly as WebAuthn puts the origin
inside `clientDataJSON`. Consider `evil.example` presenting itself as the guestbook:

- If it sets `audience: "https://guestbook.poweur.net"`, the signer's metadata fetch goes
  to the **real** guestbook, and `response_uri` — which must be same-origin with the
  audience and published by the RP — cannot point at `evil.example`. The approval is
  delivered to the party being impersonated, not the impersonator.
- If it sets `audience: "https://evil.example"`, it can collect a perfectly valid
  approval. That approval is signed over `https://evil.example` and the real guestbook's
  verifier rejects it at check 2. The credential is worthless anywhere but the site that
  minted it — the same containment WebAuthn gets from origin-scoped credentials.

The residual risk is the one WebAuthn also carries: a user who *chooses* to sign in at a
malicious site has an account there. Nothing is stolen; nothing crosses over.

What the analysis depends on:

- **The signer must fetch RP metadata before rendering any RP-supplied string.** A signer
  that shows `domain` or `statement` from the request alone is showing the user
  attacker-controlled text. `domain` is display sugar; the metadata `name` and the
  audience host are the trusted labels.
- **A signer must never accept `response_uri` off-origin.** This is the confused-deputy
  guard, enforced twice: at request validation and against the RP's published list.
- **`http://` audiences are development-only.** A signer SHOULD warn loudly, and a
  production RP MUST publish over TLS — without TLS the metadata fetch proves nothing.

### Replay

The nonce is single-use and the window is ≤ 5 minutes, so a captured approval is
replayable only inside that window and only until the honest verifier spends the nonce.
Two things follow:

- A multi-process RP **needs a shared nonce cache** (Redis, a unique index in Postgres).
  A per-process cache permits one replay per process. The SDK's default in-memory cache
  is correct for a single process and documented as such.
- The cache must be **atomic**: two concurrent claims of the same key must not both
  succeed, or the guard has a race an attacker can drive.

A verifier that cannot reach its cache must **fail closed**.

### Not addressed here

Malware on the signing device, a compromised RP after login, and a hostile relay serving
a forged identity document are out of scope. The last is mitigated by the resolver
chain's fail-closed key-mismatch rule (EPIC-001) and by contact key pinning (EPIC-007);
the first two are the same trust assumptions every login protocol makes.

## Who may complete a sign-in

> **Planned, not implemented (EPIC-022 E22-T4).** This section records a rule the protocol is
> gaining. Reviewing the shipped flows while designing the OAuth bridge found the reference RP
> handing its session to whoever *started* a login rather than to whoever *approved* it.

**Anyone may create a request, and that is fine.** A request is a challenge: it carries no
authority, it names no user, and holding one grants nothing. The protocol also cannot demand a
signature on this first leg — until the challenge exists there is nothing to sign, and it is the
relying party that must mint it. Asking "how do we stop a stranger starting a login?" therefore
has no answer. The right question is who may *finish* one:

> A sign-in completes only for a browser that presents **both** proof that it started the
> transaction **and** a completion secret that was handed exclusively to the signer.

Two halves, deliberately held in two places. In the ordinary same-browser journey both live in
the one browser, and nothing changes for the user. When the two halves are in different places,
the login simply does not complete — which is exactly the outcome wanted.

### The attack this closes

An attacker starts a login at an RP, keeps the pending transaction, and sends the victim the
signer link (`https://poweur.net/app/?auth=…`). The signer honestly displays the *real* RP's
name, because the request really is for that RP, so a victim expecting to sign in there may well
approve.

| Holds | Attacker | Victim |
|-------|----------|--------|
| Transaction / initiator binding | ✅ started it | ❌ |
| Completion secret (minted on approval, returned to the signer) | ❌ | ✅ |

Note what does *not* help: binding the pending login to the browser that started it. The
attacker **is** the initiator, so any initiator-only binding authenticates the attacker. The
binding that matters is to the approver's device — which is why the completion secret must be
returned to the signer, and never be derivable from the request.

### Mechanism

1. The signer POSTs the encoded approval to `response_uri`, as today.
2. The RP verifies it and answers `200 {"resume_uri": "<same-origin URL with a one-time code>"}`.
   The code is ≥128 bits, single-use, and expires with the request.
3. The signer navigates the same browser to `resume_uri` (checking it is same-origin with the
   audience it just verified). It must **not** put the approval itself in a URL.
4. The RP completes the login only if the resume code and the transaction's own cookie both
   match, and then issues its session.

This is additive: the request object, canonical signing string, signature and test vectors are
unchanged. It governs only how an already-signed approval is redeemed, so no conformance vector
moves. An RP that publishes no `resume_uri` keeps today's behaviour, and a signer that receives
one MUST prefer it over building a `?response=` URL.

### Cross-device approval is weaker, and must say so

When the approving device is not the browser being signed in — QR and poll transports — no
completion secret can reach the initiating browser without also being available to whoever
forwarded the request. This is the residual risk in every QR login, and it is handled with
disclosure rather than cryptography:

- The poll handle is a high-entropy secret bound to the initiating browser, **never**
  `request_id` (which the initiator hands out by construction). This stops a bystander who saw
  the QR from claiming the session; it does not stop a forwarded request.
- The signer shows **number matching** against the initiating screen, plus the initiator context
  the RP recorded: coarse location, browser and how long ago the login started.
- The signer states plainly that the user is approving a sign-in *started somewhere else*, and
  says to cancel unless the code is on a screen in front of them.
- Short TTL, single use, and no silent re-issue of a request inside one transaction.

### Current status of the shipped code

- The web signer's "Continue" button builds a `?response=` URL
  (`apps/web/src/screens/SignInApproval.tsx`) — to be replaced by `resume_uri`.
- `apps/guestbook`'s `/auth/poll` authenticates with `request_id` alone and sets the login
  cookie for any caller that knows it (`handleAuthPoll`), so it is vulnerable to both the
  bystander and the forwarded-link cases above.

## Scoped resource grants

The second step, specified in full in [Connected apps](./connected-apps.md): the RP
presents the *same* user-signed approval to the **user's relay**, which mints a
path-scoped DAV token. The relay is the resource server, the user's signature is the
authorization grant, and revocation lives in the user's own tree.

## Test vectors

`packages/identity/testdata/vectors/signin.json` pins the protocol for both
implementations: canonical strings, and a labelled case per rule —

| Case | Pins |
|------|------|
| `valid-identity` | happy path, identity-key signature |
| `valid-session-delegated` | session-key signature + proof chain |
| `valid-no-scopes` | login-only approval, empty scope line |
| `expired` | `now > expires_at` |
| `ttl-exceeded` | window longer than 5 minutes |
| `wrong-audience` | signature valid, audience is another origin |
| `replayed-nonce` | second presentation of an accepted response |
| `scope-out-of-namespace` | `dav:` scope outside `apps/<app id>` |
| `scopes-unsorted` | non-canonical scope order |
| `tampered-statement` | statement edited after signing |

Regenerate with `pnpm vectors`. Go is canonical; TypeScript conforms.
