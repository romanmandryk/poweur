# PCP-0011 — Handles on other networks (`handles.json`)

- **Status:** experimental (INT-006-T5)
- **Owner:** poweur core
- **Registry entries:** none (a public system document)

## Convention

An identity MAY publish `.poweur/public/handles.json` to use its own name on other networks:

```json
{
  "version": 1,
  "atproto_did": "did:plc:z72i7hdynmk6r22z27h6tvur",
  "nostr_pubkey": "3bf0c63fcb93463407af97a5e5ee64fa883d107ef9e558472c4eb9aaaefa459d",
  "nostr_relays": ["wss://relay.example"],
  "fediverse_acct": "alice@mastodon.social",
  "fediverse_actor": "https://mastodon.social/users/alice"
}
```

Every field is optional. The document is public, owner-written and validated by the relay on write:

- `atproto_did`: `did:plc:` + 24 base32 characters, or a `did:web:` identifier.
- `nostr_pubkey`: 64 lowercase hex characters. Clients convert an `npub` (NIP-19) before writing.
- `nostr_relays`: up to 8 `wss://` URLs, only together with `nostr_pubkey`.
- `fediverse_acct` and `fediverse_actor` go together: `user@server` and its ActivityPub actor's `https`
  URL. The client looks the account up with WebFinger at its own server when it links it.

## What a relay serves

A relay hosting the identity serves, from what the document says (and nothing for an identity that has
no document, or that the operator suspended):

| Request | Answer |
|---|---|
| `GET https://<id>/.well-known/atproto-did` | `atproto_did` as `text/plain`: the ID is a Bluesky handle |
| `GET https://<id>/.well-known/nostr.json?name=_` | NIP-05 for `_@<id>` (shown as `<id>`) |
| `GET https://<domain>/.well-known/nostr.json?name=<label>` | NIP-05 for `<label>@<domain>`, when `<domain>` is a hosted domain of the relay and `<label>.<domain>` is hosted there |
| `GET https://<domain>/.well-known/webfinger?resource=acct:<label>@<domain>` | a JRD whose `subject` is `acct:<fediverse_acct>` and whose `self` link is `fediverse_actor`: a WebFinger redirect. Mastodon confirms it at the account's own server, so searching `@<label>@<domain>` finds the linked account |

A NIP-05 answer contains only the name asked for: the relay never lists who opted in. NIP-05 and
WebFinger answers carry `Access-Control-Allow-Origin: *`.

## Reference

Go `packages/identity/handles.go` is canonical; vectors in
`packages/identity/testdata/vectors/handles.json`; TS twin `packages/client-ts/src/handles.ts`
(also `normalizeNostrPubkey`, `lookupFediverseAccount`). Relay: `apps/api/internal/relay/handles.go`.
