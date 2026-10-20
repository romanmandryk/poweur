---
id: matrix
title: "Recipe: a Matrix chat where people sign in with Poweur"
---

# Recipe: a Matrix chat where people sign in with Poweur

[Matrix](https://matrix.org) gives you a Discord-like chat (spaces, channels, threads, voice and
video calls, apps on every platform) that you host yourself. Its server, **Synapse**, has
OpenID Connect login built in, so with the [Poweur bridge](./oauth-oidc-bridge.md) people sign in
with their Poweur ID: no passwords, no separate sign-up, and their chat name follows from their ID,
`@alice.poweur.net:chat.example.org`.

This exact setup is tested against a real Synapse (v1.162) on every full CI run
(`apps/web/test/e2e/oauth-live-matrix.spec.js`).

## 1. Register your chat as an application

Sign in to the developer console at **https://oauth.poweur.org/developers** with your Poweur ID and
add an application:

- **Name:** what people see on the consent screen, e.g. *Poweur community chat*
- **Redirect URI:** `https://chat.example.org/_synapse/client/oidc/callback`
- **Client authentication:** client secret (basic)

Keep the client ID and secret it shows.

## 2. Configure Synapse

Add this to `homeserver.yaml` (or put it in a second file and pass both with `--config-path`):

```yaml
public_baseurl: "https://chat.example.org/"

# Poweur is the only way in: no passwords, no open registration.
enable_registration: false
password_config:
  enabled: false

sso:
  # Element Web, so it skips Synapse's extra "continue to…" page.
  client_whitelist: ["https://chat.example.org/"]
  # Names and photos follow the Poweur profile on each sign-in.
  update_profile_information: true

oidc_providers:
  - idp_id: oidc-poweur
    idp_name: "Poweur ID"
    issuer: "https://oauth.poweur.org"
    client_id: "<from the console>"
    client_secret: "<from the console>"
    client_auth_method: client_secret_basic
    pkce_method: always
    scopes: ["openid", "poweur_id", "profile"]
    user_mapping_provider:
      config:
        localpart_template: "{{ user.poweur_id }}"
        display_name_template: "{{ user.name or user.poweur_id }}"
        picture_template: "{{ user.picture }}"
```

- `pkce_method: always`: the bridge requires PKCE for every application.
- `localpart_template`: people become `@<their Poweur ID>:<your server>`. The consent screen asks
  them to share their Poweur ID; if someone unticks it, Synapse cannot name them and the sign-in
  stops, so the screen's default is what you want.
- Add `"email"` to `scopes` and `email_template: "{{ user.email }}"` if you want Synapse to know
  each person's Poweur email address (they can untick it).

Restart Synapse. `GET https://chat.example.org/_matrix/client/v3/login` should now list
`m.login.sso` with the `oidc-poweur` provider and no `m.login.password`.

## 3. Element Web

Serve [Element Web](https://github.com/element-hq/element-web) (for example at
`https://chat.example.org/` in front of Synapse) with a `config.json` that points at your server:

```json
{
  "default_server_config": {
    "m.homeserver": { "base_url": "https://chat.example.org", "server_name": "chat.example.org" }
  },
  "disable_custom_urls": true,
  "disable_guests": true
}
```

The sign-in page then shows **Continue with Poweur ID**. People approve the request in their Poweur
app, the same way they sign in anywhere else.

## What to know

- **Federation.** Your server can talk to the rest of Matrix: people with accounts elsewhere can
  join your public rooms, and your users can join rooms elsewhere. Turn federation off in Synapse if
  you want a closed community.
- **Accounts are tied to the issuer.** Synapse links each account to the bridge's pairwise
  subject. Keep using `https://oauth.poweur.org` (or your own bridge) and accounts stay put.
- **Mobile.** The classic Element apps use the same single sign-on as Element Web. Element X is
  moving to Matrix's next-generation authentication (Matrix Authentication Service); check its
  current status before relying on it for your community.
- **Moderation** is Synapse's own: room permissions, server notices and the admin API.
