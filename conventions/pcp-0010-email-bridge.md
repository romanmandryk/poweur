# PCP-0010 — Email bridge (`email.*`, `email.json`)

- **Status:** experimental (EPIC-023)
- **Owner:** poweur core
- **Registry entries:** none in `message_types` (that list is `sys.*` only); this PCP claims the
  `email.` message-type namespace and the `poweur-sys` document `public/email.json`
  (`schemas/poweur-sys/email-optin.schema.json`).

## Convention

An email bridge is a Poweur identity that carries mail between the email network and the Poweur
IDs that opted into it. It delivers each inbound email as an end-to-end encrypted typed message
and sends mail on a user's behalf when the user asks it to with one.

### Opt-in

1. `.poweur/public/email.json`, served at `/.well-known/poweur/email.json`:
   `{version: 1, identity, address, bridge, created_at, signature}`. `address` MUST be the
   identity's leftmost label `@` the rest (`john.poweur.net` → `john@poweur.net`). `signature`
   is base64url Ed25519 by the identity key over

   ```text
   poweur-email-optin
   v1
   <identity>
   <address>
   <bridge>
   <created_at>
   ```

   (names lowercased). A bridge MUST verify it before accepting mail for the address and before
   sending from it; removing the document opts out.
2. The inbox policy's `email: {allow: true, bridge: <bridge id>}` block. The recipient's relay
   admits `email.*` from that bridge in every inbox mode, and only from it.

A client MUST render a message as email only when its type is `email.*` and its sender is the
bridge its own inbox policy names. Any other sender's `email.*` is shown as an untrusted app
message.

### Addresses

`local[+tag]@domain` routes to the identity `local.<identity domain>`, where the bridge maps each
mail domain it serves to an identity domain (by default the same name). The local part follows
the identity label rules — `.` is significant, not ignored. The tag after `+` is kept in the
payload for client-side filters. Unknown, not-opted-in and suspended recipients are refused at
`RCPT TO`; a bridge never accepts and then bounces.

### Message types (payload = JSON, `version: 1`)

- `email.message` (bridge → user): `message_id`, `in_reply_to`, `references[]`, `from`, `to[]`,
  `cc[]`, `reply_to[]` (`{name?, address}`), `envelope_to`, `tag`, `subject`, `date`, `auth`
  (`spf`/`dkim`/`dmarc`/`arc` results from the bridge's own MTA, `verdict` =
  `verified` only on a DMARC pass, `failed` on a DMARC fail, else `unverified`; `spam`,
  `spam_score`), `text`, `html` (sanitized by the bridge — clients sanitize again and render
  isolated, scripts off, remote content blocked by default), `attachments[]` (`name`, `mime`,
  `size`, `content_id`, `inline`, and `data` base64 or a `blob` reference), `raw` (blob of the
  original message), `truncated`, and `bounce` for delivery reports.
- `email.send` (user → bridge): `send_id` (idempotency key), `from_name`, `to[]`, `cc[]`,
  `bcc[]`, `subject`, `text`, `html`, `in_reply_to`, `references[]`, `attachments[]` (`data`
  base64 or an `upload` id). The From address is always the sender's opt-in address.
- `email.status` (bridge → user): `send_id`, `state` (`accepted` | `sent` | `deferred` |
  `bounced` | `complained` | `refused`), `message_id`, `recipient`, `reason`, `at`.

The envelope carries no thread and no metadata: subjects and addresses are content. Clients
group mail by `EmailConversationRoot` (first reference, else `in_reply_to`, else the message id).

### Blobs and uploads

A blob is an `identity.SealedPayload` JSON document sealed to the recipient's X25519 key with
domain `poweur/email/blob/v1` and context `email-blob:<id>`; the reference carries `id`, `url`,
`size`, `sha256` (of the plaintext) and `expires_at`. Outbound attachments too large to inline
are uploaded to the bridge sealed to *its* key with domain `poweur/email/upload/v1` (same
context rule), authenticated by a signature over

```text
poweur-email-upload
<identity>
<upload id>
<sha256 of the uploaded bytes, hex>
<size>
<issued_at>
<nonce>
```

## Reference

Go `packages/identity/email.go` is canonical; vectors `testdata/vectors/email.json`; TS twin
`packages/client-ts/src/email.ts`. The bridge is `apps/email`; prose in
`apps/docs/docs/email/bridge.md`.
