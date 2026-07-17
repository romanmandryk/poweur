# PCP-0004 — Contacts, inbox policy & `sys.contact.*` messages

- **Status:** experimental (shipped with EPIC-007 core)
- **Owner:** poweur core
- **Registry entries:** `sys.contact.request`, `sys.contact.accept`, `sys.contact.block`

## Convention

- `poweur-sys/relay/contacts.json` (schema `contacts.schema.json`): per contact
  `identity`, `state` (`requested|accepted|blocked`), `pinned_key` (TOFU pin of the
  signing key; rotation covered by `previous_keys`, PCP-0002), `petname`, `tags`,
  `added_at`, `source`.
- `poweur-sys/relay/inbox-policy.json` (schema `inbox-policy.schema.json`): `mode` =
  `open` | `contacts_only` | `contacts_and_requests`. No file = `open` (compatibility);
  clients SHOULD write `contacts_and_requests` for human identities. Anonymous ingress
  is a separate opt-in block (EPIC-014).
- Envelope-level message `type` (plaintext, signature-bound): `sys.contact.request`
  carries an E2E-encrypted intro (≤ 4 KB envelope payload) and lands in the recipient's
  requests queue — one pending slot per sender, 7-day re-request cooldown.
  `sys.contact.accept` is only accepted from a peer the recipient lists as `requested`.

Full spec: `apps/docs/docs/trust/contacts.md`.

## Compatibility

Both files are relay-readable (enforcement zone), never visible to other users. Unknown
states/modes are invalid (schema-governed writes).
