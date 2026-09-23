---
id: add-sign-in
sidebar_position: 2
title: Add Sign in with Poweur in 15 minutes
---

# Add Sign in with Poweur to your app in 15 minutes

You need three things: a challenge, a verify call, and a JSON file at a well-known path.
There is no client secret, no registration, no account with Poweur, and no token issued by
anybody. If you are used to OAuth, the surprising part is how much is missing.

The [protocol spec](./sign-in.md) is normative; this page is the shortest path to a working
login. The complete working version of everything below is
[`apps/guestbook`](https://github.com/poweur/poweur/tree/master/apps/guestbook) — a real
site you can run: `GUESTBOOK_ORIGIN=https://you.example go run ./cmd/guestbook`.

## 0. What you are about to build

```
  your app                          the user's signer                the resolver
     │  1. newRequest()                     │                             │
     ├──── QR / deep link / redirect ──────►│                             │
     │◄─── GET /.well-known/poweur.json ────┤  2. verifies YOUR origin    │
     │◄─── the user's signed approval ──────┤  3. user approves, signs    │
     │  4. verify() ────────────────────────────────────────────────────►│
     │  → alice.example.org is signed in                                  │
```

Your server holds no key. The user's key never leaves their device. The only thing your
verifier remembers is a five-minute list of spent nonces.

## 1. Publish who you are (2 minutes)

Serve this at `https://<your origin>/.well-known/poweur.json`, over TLS, with no redirect:

```json
{
  "poweur_auth": "1",
  "origin": "https://you.example",
  "name": "Your App",
  "response_uris": ["https://you.example/auth/callback"],
  "transports": ["redirect", "qr", "poll"]
}
```

This is the entire "registration" step, and you did it yourself. A signer fetches this
document from the origin a request claims to come from — which is how it knows the origin
is really that origin, and it is the **only** string it will show the user as your name.
Listing your `response_uris` is the hardened posture: it stops an open redirect anywhere
else on your site from becoming a way to steal approvals.

## 2. Issue a challenge (5 minutes)

### Go

```go
import "github.com/poweur/identity/signin"

verifier, err := signin.NewVerifier("https://you.example")

req, err := verifier.NewRequest(signin.RequestOptions{
    Statement:   "Sign in to Your App",
    ResponseURI: "https://you.example/auth/callback",
})
encoded, _ := identity.EncodeSignInRequest(req)
deepLink, _ := identity.SignInDeepLink(req)      // poweur://auth?request=…  (also the QR payload)
webLink, _ := identity.SignInWebLink("https://poweur.net/app/", req)
```

### TypeScript

```ts
import { SignInVerifier, encodeSignInRequest, signInDeepLink } from "@poweur/client";

const verifier = new SignInVerifier({ origin: "https://you.example" });

const req = verifier.newRequest({
  statement: "Sign in to Your App",
  responseUri: "https://you.example/auth/callback",
});
const encoded = encodeSignInRequest(req);
const deepLink = signInDeepLink(req);
```

Hand `deepLink` to a same-device signer, render it as a QR code for a phone, or navigate
the browser to `webLink`. All three carry the identical encoded request.

Verification itself needs no state — everything it checks travels inside the signed
approval. *Finishing* a login does: you must hand the session to the browser that started
it, and to nobody else. Keep a short-lived transaction per request (see
[Who may complete a sign-in](./sign-in.md#who-may-complete-a-sign-in)):

- set a binding cookie on the browser that pressed "Sign in";
- give that page a **poll secret** (never the `request_id`, which travels to whoever
  approves) and a two-digit **match code** to display;
- let `signin.NewSecret`, `signin.NewMatchCode` and `signin.HashSecret` make and store them.

The guestbook (`apps/guestbook/server.go`) is a complete, tested implementation.

## 3. Verify the approval (5 minutes)

The signer POSTs the approval to your `response_uri`. Never accept one in a URL — history,
logs and `Referer` headers all keep URLs.

### Go

```go
func (a *App) callback(w http.ResponseWriter, r *http.Request) {
    d, err := signin.ParseDelivery(r)                 // any shape a signer sends
    tx := a.claim(d.Response)                         // your pending transaction; one approval each
    if d.Match != "" && !signin.MatchCodesEqual(tx.Match, d.Match) {
        a.fail(tx)                                    // one attempt, then the sign-in is over
        http.Error(w, signin.ErrMatchCode.Error(), http.StatusForbidden)
        return
    }
    result, err := a.verifier.Verify(r.Context(), d.Response)
    if err != nil {
        http.Error(w, "sign-in failed", http.StatusUnauthorized)
        return
    }
    if d.Match != "" {
        tx.ReadyForPoll(result.Identity)              // approved on another device
        json.NewEncoder(w).Encode(signin.DeliveryReceipt{Status: "ok"})
        return
    }
    code, _ := signin.NewSecret()                     // same device: finish in *that* browser
    tx.ReadyForResume(result.Identity, signin.HashSecret(code))
    json.NewEncoder(w).Encode(signin.DeliveryReceipt{
        Status: "ok", ResumeURI: "https://you.example/auth/resume?code=" + code,
    })
}

// GET /auth/resume: sign the browser in only if it also holds tx's binding cookie.
```

### TypeScript

```ts
const result = await verifier.verify(encoded);   // throws on any failure
login(result.identity);
```

That is the whole integration. `Verify` resolves the identity through the published
resolver chain, checks the signature (identity key, or a delegated session key with its
proof), and enforces every rule in the spec: audience binding, the five-minute window,
canonical scopes, and single-use nonces.

`result` also carries `Relay` (where to ask for storage — see
[Connected apps](./connected-apps.md)), `Scopes`, `AppID`, `ExpiresAt` and the resolved
`Document`.

## 4. Four things to get right

**Hand the session only to the browser that started the login.** Anyone can create a
sign-in request; a login link forwarded to a victim is approved by the victim. A same-device
approval finishes only where the resume code *and* your binding cookie meet, and a
cross-device one only with the match code the starting screen showed. Skip this and a
forwarded link is an account takeover.

**Use a shared nonce cache if you run more than one process.** The default cache is
in-memory and correct for exactly one process; with several, an approval can be replayed
once per process. Any store with an atomic claim works — Redis `SET NX`, a unique index:

```go
verifier.Nonces = myRedisCache{}   // Use(ctx, key, expiresAt) (bool, error)
```

If your cache is unreachable, **fail closed**. The SDKs already do.

**Your origin must be the origin browsers actually reach you at.** It goes inside the
signed bytes. A mismatch between the origin you configured and the one the user visited is
exactly what the audience check exists to catch, and it will (correctly) reject every
login. Behind a proxy, configure the public origin, not the internal one.

**`https://` in production.** `http://` is accepted for local development only; without
TLS the metadata fetch proves nothing about who you are.

## 5. Ask for more than a login (optional)

Add `scopes` to the request and the same approval becomes an authorization grant you can
present at the *user's* relay for path-scoped access to their storage:

```go
req, _ := verifier.NewRequest(signin.RequestOptions{
    Statement: "Sign in and keep your notes in your own storage",
    Scopes:    []string{"dav:rw:apps/example.you"},   // your own namespace, always
})
```

A `dav:` scope may only reach `apps/<reverse-DNS of your origin host>`, derived from the
*signed* audience rather than declared — you cannot ask for another app's directory, and
the relay would not honour it if you did. [Connected apps](./connected-apps.md) covers the
exchange, revocation and the threat model.

## Testing your integration

- The reference RP's test suite (`apps/guestbook/server_test.go`) is a checklist of what a
  correct RP refuses: an approval collected at another origin, a replay, a tampered
  statement, an unresolvable name, an expired window.
- `apps/integration/signin_test.go` drives the whole thing against a real relay and real
  hosted identities.
- To approve from a terminal while developing: `poweur auth approve <request>` — or
  `poweur auth approve <short link> --code <digits>` for a
  [request by reference](./sign-in.md#requests-by-reference), which always needs the code on the
  screen that started the sign-in.

## Frequently asked

**Do I need to run a relay?** No. Login touches only the resolver chain (an HTTPS fetch,
or DNS). You need a relay only if you want the user's storage, and it is *their* relay,
not yours.

**What if the user rotates their key?** Handled. The verifier accepts a retired key that
is still inside the rotation grace window the identity document itself declares.

**Can a bot or an agent sign in?** Yes — `poweur auth approve` signs with an identity key
or a session key. That is the same protocol, not a side door.

**Is this OpenID Connect?** No, and it deliberately does not need to be. If your stack
insists on OIDC, see the bridge design in [Interop bridges](./interop-bridges.md).
