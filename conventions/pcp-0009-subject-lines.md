# PCP-0009 — Subject lines (`body_format: headers-v1`)

- **Status:** experimental (EPIC-009 E09-T13)
- **Owner:** poweur core
- **Registry entries:** none (an envelope metadata value, not a message type)

## Convention

A `chat.text` message MAY carry a subject. Its plaintext is then an email-style header block, a
blank line and the text:

```text
Subject: <subject>

<text>
```

and the signed envelope metadata holds `body_format: headers-v1`. The flag is what makes the
plaintext structured: without it, a plaintext is text even when it starts with `Subject:`.

- Header lines are `Name: value`; names are `A–Z a–z 0–9 -` and compared case-insensitively.
  The block ends at the first `\n\n`. v1 defines `Subject` only; unknown names are ignored.
- A subject is UTF-8, trimmed, at most 256 bytes, with no C0/C1 control characters, DEL,
  U+2028 or U+2029. An empty subject is no subject: send neither the header nor the flag.
- A flagged plaintext that does not parse is shown whole as the text, with no subject.
- The subject is never placed in `thread_id` or `metadata`. A new subject mints a random thread
  (ULID; `GroupThreadID(group, <ulid>)` in a group).
- Grouping key: `thread_id`, else `subj:` + the normalized subject, else the default thread.
  Normalized = trimmed, whitespace collapsed, lowercased per code point, leading
  `Re:`/`Fwd:`/`Fw:`/`Aw:`/`Sv:` markers (also `Re[n]:`) removed.

Other bodies that need a subject reuse the block: `sys.contact.message` (E09-T7) and the email
bridge's mapping of `Subject:` (EPIC-023, [PCP-0010](pcp-0010-email-bridge.md)).

## Reference

Go `packages/identity/subject.go` is canonical; vectors in
`packages/identity/testdata/vectors/message-subject.json` and `history-threads.json`; TS twin
`packages/client-ts/src/subject.ts`. Prose: `apps/docs/docs/protocol/message-format.md#subjects`.
