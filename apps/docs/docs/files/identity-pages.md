---
id: identity-pages
sidebar_position: 4
title: Identity Pages
---

# Identity Pages

Every identity hosted by a Poweur relay has a small public page at its canonical URL:

```text
https://alice.poweur.net/
```

The relay generates this page from the identity name and the validated public
`.poweur/public/profile.json`. It is a fixed, server-rendered Poweur template: profile data can
never supply HTML, CSS, JavaScript, fonts or third-party images. An identity without a profile
still gets a useful page containing its Poweur ID.

The page can show a display name, avatar, bio and safe `http` or `https` links. **Copy ID** lets a
visitor take the ID to their existing Poweur app. When the owner explicitly advertises anonymous
messages, **Write anonymously** opens the signed-out encrypted composer at `/app/?anonymous=1`.
That setting is only a public invitation; the private inbox policy still decides whether the
relay accepts the message.

## Profile settings

The optional public `identity_page` profile block controls presentation:

```json
{
  "version": 1,
  "identity_page": {
    "enabled": true,
    "indexable": false,
    "advertise_anonymous_messages": false
  }
}
```

Each field is optional. The defaults are page enabled, search indexing **off**, and anonymous
messaging not advertised. Existing profiles therefore gain a page without a migration, but no
search engine lists it until its owner opts in: a profile written for contacts should not become
searchable without being asked. Onboarding asks ("Let search engines show my page", unchecked),
and owners can change all three values in the web app's profile settings.

Without indexing the page stays available but carries both an HTML robots directive and
`X-Robots-Tag: noindex, nofollow`. Disabling the page makes HTML requests return a minimal `404`
without profile content. Neither setting disables identity discovery or relay APIs.

## Root content negotiation

The identity root remains machine-readable as well as human-readable:

| Request | Response |
|---|---|
| `GET /` and `Accept` includes `text/html` | Generated page, or the disabled-page `404` |
| `Accept` includes `application/json` | Existing relay JSON document |
| No clear HTML preference, including `*/*` | Existing relay JSON document |
| Any path other than `/` | The existing handler for that path. An unknown path stays the relay JSON document |

Responses include `Vary: Accept`. Routes under `/.well-known/`, `/app/`, the relay APIs and file
endpoints keep their existing handlers. Canonical identity discovery remains
`/.well-known/poweur/id.json`. If the advertised anonymous invitation no longer matches the
inbox policy, the composer says the identity is not accepting anonymous messages. It does not
show the policy, its limits, or its challenge settings.

## Caching and security

Pages are rendered on demand; the relay does not keep one HTML document per identity in memory.
Each page has an `ETag`, supports `If-None-Match`, and uses
`Cache-Control: public, max-age=60, stale-while-revalidate=300`. A reverse proxy or CDN cache key
must include the normalized Host, path and representation.

The renderer uses Go's context-aware HTML templates and accepts only safe profile-link schemes.
It sends a restrictive Content Security Policy, denies framing, disables credential creation on
the page, prevents MIME sniffing and sends no profile-selected response headers. The small CSS
and copy-button script are fixed, embedded relay assets.

## Self-hosting

A self-hosted identity routed through the Poweur relay gets the same trusted page. Operators can
change or front the open-source handler if they need a different root today. An operator-wide
template-path setting is deliberately deferred until the template data contract is stable; V1
has no user-selected templates or arbitrary website hosting.

An owner serving the identity domain from other infrastructure remains free to replace the root
site while preserving the Poweur well-known documents.

## Related

- [Web identity resolution](/protocol/web-identity)
- [Storage v2 and public system files](/files/storage-v2)
- [Anonymous messaging and challenges](/trust/anonymous-and-challenges)
