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

You store nothing. Everything the verify step needs travels inside the signed approval.
The one exception is cross-device UX: if a tab is waiting for a phone to approve, keep
`request_id` in a short-lived map so the tab can poll — see `handleAuthPoll` in the
guestbook.

## 3. Verify the approval (5 minutes)

The signer delivers the approval to your `response_uri` (POST) or as a `?response=`
parameter on a redirect. Either way:

### Go

```go
func (a *App) callback(w http.ResponseWriter, r *http.Request) {
    encoded := readApproval(r)                        // body, form field, or ?response=
    result, err := a.verifier.Verify(r.Context(), encoded)
    if err != nil {
        http.Error(w, "sign-in failed", http.StatusUnauthorized)
        return
    }
    a.login(w, result.Identity)                       // result.Identity is the user
}
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

## 4. Three things to get right

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
- To approve from a terminal while developing: `poweur auth approve <request>`.

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
