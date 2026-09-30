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

The relay has **no suspend or delete command yet**. `abuse.go` only counts reports, and the
identity store has no remove call, so the terms' promise to "suspend the hosting of your ID"
and the privacy policy's deletion within 30 days are both manual today. What exists:

1. **Block the name from being re-claimed** (above), so removing it isn't undone.
2. **Stop uploads:** `docker exec poweur-relay /relay quotas set <id> 1B`.
3. **Block it in your own app** and tell the reporter you've acted.
4. **Remove it for real** (spam run, illegal content): stop the relay, delete
   `relay/identities/<id>.json` (the signed ID document) and, to free the data,
   `drives/<id>/` from the Hetzner bucket `poweur1`, then start the relay: the identity index
   is read only at start-up. There is no S3 tool on the VM; use the Hetzner console or an
   S3 client on your laptop with the bucket keys from `.env.prod`. This takes the relay
   down for a minute, so do it in a quiet moment unless the content is severe. The owner
   still holds their keys and can publish the same name on another relay they choose.

Order for a serious case: 1 → 4, then write to the owner. Order for a first spam report:
message the owner, then 1 and 2.

**Build this properly** before a real abuse wave: `relay identities suspend|unsuspend|delete
<id>` beside `quotas`, enforced on send, resolve, drive and sign-in, with the reason kept in
the store. It is tracked on the launch checklist.

## Afterwards

- Tell the owner what happened and why, unless law enforcement or the report itself says
  not to, and how to appeal (reply to `info@poweur.org`).
- Forward to the authorities only when the law requires it or there is a child-safety or
  credible-threat report; take advice first.
- Close the note, and add any new brand or impersonation term to the lists above.
