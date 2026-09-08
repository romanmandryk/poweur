---
id: claim-your-id
sidebar_position: 2
title: Claiming an ID
---

# Claiming an ID

How a person with nothing — no identity, no CLI, no invitation to a particular relay —
ends up signed in. The short version: you pick a name, the relay says yes before you
commit anything, and one passkey carries you from the signup host to your own.

## For the person signing up

1. **Open the launcher.** A relay that hosts identities serves the app at
   `id.<its domain>` (`id.poweur.net` for the reference deployment) — the same app you
   will use afterwards, in no-identity mode.
2. **Type a name.** It is checked as you type. "Taken", "reserved by the operator", "use
   only a-z, 0-9 and hyphen" — whatever the answer is, it arrives *before* anything else
   happens, and the Next button stays disabled until the relay says the name is free.
3. **Create a passkey.** Touch ID, Windows Hello, a security key — whatever your device
   offers. This is the only ceremony in the flow.
4. **You land on your own address.** `alice.poweur.net/app/`, signed in, with the setup
   flow ([walkthrough](/web/walkthrough#first-run)) waiting.

Step 4 needs no second passkey: the credential is scoped to the domain the launcher and
your identity share, so the one you just made opens on both. See
[credential scope](/security/key-management#credential-scope-rpid).

If the relay gates signup behind proof-of-work, step 3 says so and shows the work
happening rather than appearing to hang.

## Why the order matters

Checking the name *after* the passkey is the obvious way to build this and the wrong one:
a WebAuthn ceremony is a real interruption, and a credential created for a name you cannot
have is litter in the user's authenticator that nobody ever cleans up. So the relay
answers first, through
[`GET /hosted/availability`](/relay/api-reference#get-hostedavailability), and the answer
carries the operator's policy so the app can validate the *next* attempt inline without
another round trip.

## For operators

The launcher is not a second application. It is the same static tree, served on a
dedicated host so that someone with no identity has somewhere to land:

- `LAUNCHER_HOST` (default `id.<first hosted domain>`) — the relay redirects `/` there to
  `/app/`, and advertises the host at `GET /` so a client knows where it is running.
- `id` and `launcher` are reserved labels, so the launcher host can never collide with an
  identity somebody claimed.
- Name rules — length, reserved names, a blocked-terms file — are
  [configuration](/relay/configuration#hosted-handle-policy). The character set is not:
  handles are ASCII `a-z 0-9 -` on every deployment, because a handle becomes a DNS label
  and a name under your wildcard certificate.
- A **self-hoster needs none of this.** Their relay serves the app at `<identity>/app/`
  from the released image; there is no fork to maintain and nothing to publish to an app
  store.

## What the hand-off carries

The identity record moves from the launcher host to the identity's origin in the URL
*fragment*, which browsers never send to a server. What travels is the same AES-GCM blob
the launcher had in `localStorage` — the wrapping secret comes from the passkey and is
not in it — and the receiving page clears the fragment as soon as it has stored it, so
the blob does not linger in the address bar or in history.
