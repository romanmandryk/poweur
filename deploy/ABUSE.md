# Abuse runbook

Who: the operator (Roman Mandryk) until there is a second person. Reports arrive at
`info@poweur.org`, as a message to `support.poweur.net`, or as a signed `poweur report` (the
relay logs those as `abuse: report from … about …`; `docker logs poweur-relay | grep abuse:`).

## Targets

| Report | Acknowledge | Decide | Act |
|--------|-------------|--------|-----|
| Illegal content, threats, child safety, credible impersonation of a real person | same day | within 24 h | immediately once decided |
| Spam, harassment, name squatting | 2 working days | 3 working days | as below |
| Security vulnerability | 3 days ([`SECURITY.md`](../SECURITY.md)) | | |

Reply from `info@poweur.org`. Keep one line per case in a private note: date, ID, what was
reported, what you did, when you told the owner. Message contents are end-to-end encrypted and
the relay can't read them, so a report has to carry its own evidence (links, screenshots,
message IDs). Never ask a reporter to forward private message text to you beyond what they
choose to share.

## Reserved and blocked names (stops new claims only)

- Whole handles: `NAME_RESERVED` in [`docker-compose.prod.yml`](../docker-compose.prod.yml), plus the
  built-in list in `packages/identity/names.go`.
- Terms anywhere in a handle: [`relay/blocked-terms.txt`](relay/blocked-terms.txt), read at start.
- Handles are ASCII `a-z`, `0-9` and `-`, at least 6 characters; other alphabets and `xn--`
  are refused, so look-alikes such as `аdmin` can't be claimed.
- Both are checked at claim time, so they never affect an ID that already exists. To add a
  name: edit the file, push to `master` (Deploy recreates the relay), then confirm with
  `curl -s 'https://relay.poweur.net/hosted/availability?handle=<handle>'` or by trying
  to claim it in the app. Names the operator creates itself (`support`, `hello`) bypass the
  policy with the operator token (see [`OPS.md`](OPS.md) → Operator IDs).

## Stopping an ID

Commands run on the VM, inside the relay container, like `quotas`: there is no admin token.

```bash
docker exec poweur-relay /relay identities                          # list holds
docker exec poweur-relay /relay identities suspend <id> <reason>    # block it now
docker exec poweur-relay /relay identities unsuspend <id>           # undo a suspension
docker exec poweur-relay /relay identities delete <id> --yes        # erase it (no undo)
docker exec poweur-relay /relay identities release <id>             # free a deleted name
```

The relay picks a change up within **15 seconds**, without a restart. A held ID can't send,
receive, sign in or use its drive, nothing can resolve it (410), and its name can't be claimed
again. Suspension is reversible; `delete` also erases its ID document, key backups, inbox and
drive, keeps the name held, and leaves the running relay with a dead entry until the next restart
(harmless: it stays suspended). It does not reach other IDs' copies of anything they already
received. The owner still holds their keys and could publish the same name on another relay.

1. **First spam or harassment report:** message the owner from `support.poweur.net`, then
   `suspend` if it continues.
2. **Illegal content, child safety, credible threat, impersonation of a real person:** `suspend`
   at once, keep the evidence (the log lines and the report), decide within 24 hours, and
   `delete` only when you are sure. Take advice before passing anything to the authorities.
3. **Deletion requested by the owner** (privacy policy: within 30 days): confirm control of the
   ID or the email, `delete --yes`, then `release` so they or anyone can use the name again.
   Backups age out after 7 days, as the policy says.
4. Add the name to the reserved lists above if it was an impersonation, so `release` later
   doesn't reopen it.

## Afterwards

- Tell the owner what happened and why, unless law enforcement or the report itself says
  not to, and how to appeal (reply to `info@poweur.org`).
- Forward to the authorities only when the law requires it or there is a child-safety or
  credible-threat report; take advice first.
- Close the note, and add any new brand or impersonation term to the lists above.
