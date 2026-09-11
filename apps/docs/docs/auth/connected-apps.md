---
id: connected-apps
sidebar_position: 3
title: Connected apps and scoped grants
---

# Connected apps and scoped grants

A Sign-In approval can authorize an app to use a narrow part of the user's home. The app
presents the signed approval to `POST /auth/grant` at the user's relay. The relay verifies
the same audience, signature, lifetime and scope rules as a relying party, records the
connection, and returns a one-hour `app_…` bearer token.

This is not a login token. It is a resource token issued by the relay that owns the resource.
Re-presenting a still-valid approval refreshes it; obtaining broader access requires a new
approval.

## Scope enforcement

The app id is the reverse DNS form of the signed audience host. For
`https://guestbook.poweur.net`, it is `net.poweur.guestbook`. A DAV scope may reach only
`/apps/net.poweur.guestbook/`; the relay rejects paths in another app's namespace even if a
malicious signer UI included them.

Each DAV request is checked against both the token and the current record in
`poweur-sys/relay/connected-apps.json`. Deleting or revoking that record therefore cuts off
an already-issued token on its next request. The web client's **Settings → Connected apps**
view edits the same file, so the file remains the portable source of truth.

Approvals are appended to `poweur-sys/private/logs/auth.log`. Each line records the relying
party, action, scopes, identity, request id and approval time. The log is private to the user
and survives across enrolled devices.

## Threat model

- **Scope escalation:** the signed scope list is canonical and audience-derived namespace
  checks run in the signer and relay. Neither the RP nor a proxy may alter it after consent.
- **Confused deputy:** `response_uri` must be same-origin with the signed audience and, when
  RP metadata lists response URIs, must match that allow-list.
- **Token theft:** a stolen app token lasts at most one hour, reaches only its recorded scopes,
  and is invalid immediately after connected-app revocation. It cannot sign in as the user.
- **Replay:** grant exchange is intentionally repeatable only while the short Sign-In request
  remains valid. It returns a fresh short-lived token but cannot add scopes.

The reference guestbook demonstrates the whole flow: request consent for
`dav:rw:apps/net.poweur.guestbook`, exchange the approval, and write entries into the user's
own home rather than the guestbook server's database.
