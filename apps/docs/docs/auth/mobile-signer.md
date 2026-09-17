---
id: mobile-signer
sidebar_position: 4
title: Native mobile signer contract
---

# Native mobile signer contract

The iOS and Android shells should implement the same signer contract as the web client. This
is a UI and platform integration note, not a second protocol.

## Link registration and handoff

- Register the custom `poweur://auth?request=…` scheme for same-device links.
- Also register the deployment's HTTPS universal/app link so sites can use a verified link
  with a browser fallback.
- Treat QR contents and pasted codes as untrusted input sent through the same decoder. Never
  place a private key, session secret or relay token in a link.
- Preserve the request while the user unlocks or selects an identity; discard it at expiry.

## Approval screen

Before unlocking a signing key, fetch `/.well-known/poweur.json` from the signed audience with
no redirects. Display the verified app name and origin, action, statement, and every requested
scope in plain language. Approval is one explicit biometric/passcode-gated action; denial and
navigation away sign nothing.

The app you are signing in to is never in this app's browser, so every mobile approval is a
**cross-device** approval: ask for the code shown on the screen that started the sign-in and
send it with the delivery (`{"response": …, "match": …}`). Say plainly that the code must be
on a screen in front of the user, and that a code someone *sent* them means cancel.

After approval, append the consent record first, then deliver to the allow-listed
`response_uri`. If the receipt carries a `resume_uri` — possible only when a request reached
the app without a code — check it is same-origin with the audience before opening it. Never
build a URL containing the approval. If delivery fails, show a copyable response code. A
cross-device flow must remain usable without the two devices sharing an account or push
channel.

## Platform lifecycle

- Redact the request and response from analytics, crash reports, notifications and recent-app
  snapshots.
- Do not approve from a background callback; return to the visible consent screen.
- Use the existing secure key-custody and session-key interfaces. The mobile shell must not
  invent a different key or shared-state format.
- When the app resumes, revalidate the request window and refetch RP metadata before signing.

The conformance target is `packages/identity/testdata/vectors/signin.json`; a native signer
must produce responses accepted by the existing Go and TypeScript verifiers.
