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
       │    v1 resource grants removed; v2 scoped handles are planned.         │
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
  "scopes": ["profile:read"]
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
| Deep link | `poweur://auth?request=<b64url>`, or `poweur://auth?request_uri=<reference>` |
| QR, copied link | the identity's **approve page** with a request by reference in its fragment, `https://<identity>/id/approve#auth=<reference>`, when the RP knows the identity; otherwise the reference itself, `https://<rp>/…/<code>` (below) |
| Web signer handoff | `https://<identity>/id/?auth=<b64url>` (URL-escaped), or `?auth=<reference>` |
| Redirect | RP navigates the browser to the signer with the same parameter |
| Paste back | an RP with no `response_uri`: the user copies the encoded **response** into the RP |
| Cross-device poll | the signer POSTs to `response_uri` with `"finish": "poll"`; the starting page polls the RP with its poll secret |
| Same-device return | the signer POSTs to `response_uri`, then opens the `resume_uri` from the receipt |

Decoders also accept raw JSON and padded base64url: a user pasting a code should not have
to know which they copied.

#### The approve page

When the RP knows which identity is signing in — the OAuth bridge asks first; the guestbook
remembers who last signed in — its QR is that identity's own **approve page**, carrying the
request link in the fragment (EPIC-028):

```text
https://alice.poweur.net/id/approve#auth=https%3A%2F%2Foauth.poweur.org%2Fr%2FK7QM4XP2
```

The page sits beside the identity's web signer (`endpoints.web_signer` + `approve`). The fragment
keeps the request out of server logs; it is not a secret. A phone's camera opening it lands where
the keys are with nothing to choose:

1. **The store app, through universal links / App Links**, for hosted identities: the relay's
   `apple-app-site-association` and `assetlinks.json` claim `/id/approve` and nothing else.
2. **Otherwise the identity's web app,** which approves if this browser holds the keys. If it
   does not: on Android it hands the request to the app (`intent://auth?request_uri=…` with
   this page as the fallback); on iOS it offers *Open in Poweur app* and, when the RP publishes a
   `push_uri`, *Send to my home-screen app* — Safari cannot open an installed web app, whose
   storage it cannot see either; on a desktop it shows the same link as a QR.
3. **The CLI** takes the same link: `poweur auth approve <link>`.

The same page approves new devices: `#pair=<code>.<commitment>&id=<identity>` (see
[key management](../security/key-management.md)). Helpers: Go `identity.ApprovePath`,
`ApproveURL`, `ApprovePageForSigner`, `ApproveLink`; TS `APPROVE_PATH`, `approveUrl`,
`approveLink`. `SignInRequestURI` / `signInRequestUri` read the fragment form.

### Requests by reference

An encoded request is about 600 characters; as a QR code that is a dense symbol many phone
cameras will not read, and as a link nobody can read out. An RP may instead serve the request at
a short link on its **own origin** — `https://oauth.poweur.org/r/K7QM4XP2` (an 8-character code,
Crockford base32, living as long as the request) — and put that in the QR:

- `GET <link>` with `Accept: application/json` answers `{"request": "<encoded request>"}`, with
  `Access-Control-Allow-Origin: *` (nothing in a request is secret). Expired or used: `404`/`410`.
- A signer given the link — itself, `poweur://auth?request_uri=<link>`, or a web signer's
  `?auth=<link>` — fetches it without following redirects and **requires the link to be
  same-origin with the request's `audience`**. The request is then exactly as authentic as one the
  RP handed over directly: it came from that origin over TLS.
- The same link opened in a browser — a phone's camera app — is the RP's to answer with a page
  offering the signers (the OAuth bridge does), or a redirect into one (the guestbook does). No
  in-app scanner is needed.
- A request by reference always comes from another screen, so a signer approves it as a
  **cross-device** sign-in (`"finish": "poll"`, below). No number is asked for it.

Helpers: Go `identity.SignInRequestURI`, `CheckSignInRequestURI`, `FetchSignInRequest`,
`SignInReferenceDeepLink`; TS `signInRequestUri`, `checkSignInRequestUri`, `fetchSignInRequest`.
Vectors: `signin-reference.json`.

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
  "scopes": ["profile:read"],
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

Storage path scopes and `/auth/grant` were removed with v1 storage.
V2 resource authorization is tracked in EPIC-020. At most 16 scopes are accepted.

## Verification rules

A verifier runs these in order — cheapest and most local first, so a replayed or
misaddressed response never costs a DNS or HTTPS lookup:

1. **Shape and version.** `poweur_auth == "1"`; `request_id`, `nonce`, `signature`,
   `identity` present; identity is a valid Poweur name.
2. **Audience.** The normalized `audience` MUST equal the verifier's own origin. This is
   the anti-phishing check and it is not optional.
3. **Action, statement, scopes.** Known action; statement within limits; scopes in
   canonical form (**reject**, do not silently re-sort — a re-sorted list would verify
   against bytes the user never saw); unsupported scopes are rejected.
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
  "scopes": ["profile:read"],
  "transports": ["redirect", "qr", "poll"],
  "contact_uri": "https://guestbook.poweur.net/abuse",
  "context_uri": "https://guestbook.poweur.net/auth/context"
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
- `context_uri`, when present, must be same-origin. `GET <context_uri>?request_id=…` answers,
  while the request can still be approved, with where it was started:
  `{"request_id", "started_at", "browser", "client", "client_host"}` — a coarse browser label,
  never a precise location. A signer approving from another device shows it
  (`signin.FetchContext`/`DescribeContext`, TS `fetchSignInContext`/`describeSignInContext`)
  and ignores it if it cannot be fetched.
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

> Added while designing the OAuth bridge (EPIC-022), after a review found the reference RP
> handing its session to whoever *started* a login rather than to whoever *approved* it. The
> Go helpers are in `packages/identity/signin/delivery.go`; the TypeScript twins are
> `checkResumeUri` and `normalizeMatchCode` in `@poweur/client`.

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
signer link (`https://poweur.net/id/?auth=…`). The signer honestly displays the *real* RP's
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

The **approval** — not the page that started the sign-in — decides how it finishes, because
only the approval can say whether the approver was looking at the starting screen.

When the RP creates a request it keeps a transaction holding: the hash of a binding cookie it
sets on the starting browser and the hash of a poll secret it gives the starting page. If it
**pushes** the request to the user's app, it also draws a two-digit **match number** and two
distinct decoys (EPIC-028).

The signer POSTs a delivery to `response_uri`:

```json
{ "response": "<encoded approval>", "finish": "poll", "match": "42" }
```

`finish: "poll"` marks an approval made on another device: a QR, a link, a pushed prompt, or the
app shell. `match` is the number picked from the choices of a pushed sign-in, and only then. A
same-device delivery may be the bare encoded approval as the whole body (the original wire form).
An older signer sends a cross-device delivery as `match` alone; the RP reads either as
cross-device. The RP processes **one** approval per transaction and then:

| Delivery | RP checks | Receipt | Finishes at |
|----------|-----------|---------|-------------|
| same-device (no `finish`, no `match`) | signature | `{"status":"ok","resume_uri":"…?code=…"}` | `resume_uri`, only with the binding cookie |
| cross-device, never pushed | signature (any `match` is ignored) | `{"status":"ok"}` | the starting page's poll, only with the poll secret |
| cross-device, pushed | `match` equals the number (one attempt), then signature | `{"status":"ok"}` | the starting page's poll, only with the poll secret |

- The resume code is ≥128 bits, single-use, and short-lived (the guestbook allows one minute).
- The signer checks `resume_uri` is same-origin with the audience it verified, then navigates
  the same browser there. It **never** puts the approval itself in a URL, and an RP never
  accepts one there.
- A wrong or missing number on a pushed sign-in ends the transaction; the right one afterwards
  does not revive it. The bridge also pauses pushes to that identity for 15 minutes: the pick is
  the signal that someone else started the sign-in, so a wrong one costs them the channel.
- A poll before a same-device approval is resumed reports `approved` and hands out nothing.

This is additive to the signed protocol: the request object, canonical signing string,
signature and test vectors are unchanged. It governs only how an already-signed approval is
redeemed, so no conformance vector moves. `finish` and `match` are deliberately outside the
signed bytes — they say where the approver was, not who they are.

The RP's context document (`context_uri`) carries what a cross-device signer needs:

| Field | Meaning |
|-------|---------|
| `match_choices` | three distinct two-digit numbers, once the sign-in was pushed; never which is right. The signer shows them in place of *Approve* |
| `push_uri` | POST here (no body) to have the RP push this request to the user's app — for a phone that opened the QR in a browser without the keys. Same-origin with the RP; using it makes the sign-in a pushed one |

### Cross-device approval is weaker, and must say so

When the approving device is not the browser being signed in — QR, link and push transports —
no completion secret can reach the initiating browser without also being available to whoever
forwarded the request. This is the residual risk in every QR login.

**Codes by channel (EPIC-028).** Until EPIC-028 every cross-device approval needed a two-digit
code typed from the starting screen. It now depends on the channel:

| Channel | Number | Why |
|---------|--------|-----|
| Same browser | none | binding cookie + single-use `resume_uri` |
| QR scanned, link opened, CLI given a link | **none** | the attacker needs a channel to the victim, and on that channel — a message, or a phishing page showing the RP's live QR — the code can be shown too. It only stopped an attacker who forgot to send it |
| Push to the app | **pick one of three** | unsolicited: anyone who knows an ID can trigger it, and one reflexive tap would be the account (push fatigue). The pick, the one attempt and the 15-minute pause after a miss stop that |

**Accepted residual risk:** a forwarded QR or link is a one-tap takeover, as in WhatsApp,
Discord and Steam QR login. It is handled with disclosure rather than cryptography:

- The poll handle is a high-entropy secret bound to the initiating browser, **never**
  `request_id` (which the initiator hands out by construction). This stops a bystander who saw
  the QR from claiming the session; it does not stop a forwarded request.
- The signer shows the initiator context the RP recorded — which browser started the sign-in,
  how long ago, and for which application — and says *Only approve a sign-in you just started
  yourself*.
- Short TTL, single use, and no silent re-issue of a request inside one transaction.

The number can return per channel without a protocol change if abuse shows up.

### Where it is implemented

| Component | Behaviour |
|-----------|-----------|
| Web signer (`apps/web`) | sends `finish: "poll"` for a request by reference, an approve page, a pushed prompt and the app shell; three number buttons when the context has `match_choices`; follows `resume_uri` only for a same-device approval; no approval in any URL |
| Go CLI | `poweur auth approve <link>` approves cross-device with nothing to type; for a pushed sign-in, `--code <number>`, which must be one of the choices (listed when it is missing); prints the resume link for a same-device approval |
| OAuth bridge | QR = approve page; number drawn on the first push, shown on the starting page (`"match"` in the status poll); `push_uri` at `/r/{code}/push`; 15-minute push pause after a wrong pick; remembers the last signed-in ID and pushes to it as the page opens |
| Guestbook | never pushes, so never asks for a number; remembers the last signed-in ID and opens its approve page |

Initiator context is served by the guestbook and the OAuth bridge (`context_uri`) and shown by
the web signer and `poweur auth approve`: which browser started the sign-in, how long ago, and —
for the bridge — which application it is for. Coarse location is not offered.

## Scoped resource grants

The v1 resource grant exchange is removed. Planned node-scoped resource access
is tracked in EPIC-020; see [Connected apps](./connected-apps.md).

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
