# poweur-email — the Poweur email bridge

EPIC-023. Gives every **opted-in** hosted Poweur ID a working address: mail to
`john@poweur.net` arrives, end-to-end encrypted, in `john.poweur.net`'s Poweur inbox
(the web app's **Email** tray, `poweur email ls`), and John sends mail as
`john@poweur.net` from the app.

The bridge does not face the internet on port 25. A mail server — **Stalwart** at
`mail.poweur.net` for the public deployment — owns the MX, TLS, SPF/DKIM/DMARC checks, spam
scoring, queues, DKIM signing and outbound relaying (through Brevo today). The bridge sits
behind it on three network paths, so the two can run on different hosts:

```
internet ──SMTP:25──► Stalwart (mail.poweur.net)
                        │ 1. RCPT hook  ── HTTPS POST /stalwart/hook ──► bridge: does john@ exist?
                        │                                                (resolves john.poweur.net,
                        │                                                 verifies its signed email.json)
                        │ 2. queue relay ── LMTP :2525 (STARTTLS+AUTH) ──► bridge: parse, sanitize,
                        │                                                  encrypt to John, deliver as
                        │                                                  email.message (per-recipient
                        │                                                  250/451/550)
                        ▼
Poweur app ── email.send (encrypted to the bridge) ──► bridge's inbox
                                                        │ 3. SMTP submission :587 (AUTH) ──► Stalwart ──► Brevo ──► recipient
                                                        ▼
                                          email.status back to John (sent / deferred / bounced / refused)
```

The bridge is itself a Poweur identity (`email.poweur.net`). It holds no relay credential,
keeps no plaintext after delivery, and its relays store only ciphertext. Large attachments and
the original message are kept only **sealed to the recipient**, at unguessable URLs, for
`EMAIL_BLOB_TTL`.

Protocol: [PCP-0010](../../conventions/pcp-0010-email-bridge.md) ·
`packages/identity/email.go` · prose in [`apps/docs/docs/email/bridge.md`](../docs/docs/email/bridge.md).

## Opting in

A user opts in from Settings (web app) or `poweur email enable`. Their client writes, with
their own key:

1. `.poweur/public/email.json` — signed `{identity, address, bridge, created_at}`;
2. the inbox policy's `email: {allow: true, bridge: "email.poweur.net"}` block.

The relay advertises the bridge to its users when it runs with `EMAIL_BRIDGE_ID`.

## Configuration

Environment only. Every secret also takes a `_FILE` variant.

| Variable | Default | Meaning |
|---|---|---|
| `EMAIL_BRIDGE_ID` | — | The bridge's Poweur ID (`email.poweur.net`) |
| `EMAIL_BRIDGE_SEED` / `_FILE` | — | Its 32-byte seed (base64url) or recovery phrase; derives its keys |
| `EMAIL_RELAY_URL` | — | The bridge identity's home relay (`https://relay.poweur.net`) |
| `EMAIL_PUBLIC_URL` | — | HTTPS origin of the bridge (blobs, uploads); advertised as `email_api`. Without it attachments over ~350 KB are dropped and uploads are off |
| `EMAIL_DOMAINS` | — | Mail domains, `poweur.net,poweur.org=poweur.net` (`a=b`: mail for `x@a` goes to `x.b`) |
| `EMAIL_ALIASES` | — | Whole addresses to an identity: `postmaster@poweur.net=support.poweur.net,…` (the target must be opted in) |
| `EMAIL_ADDR` | `:8091` | HTTP: `/stalwart/hook`, `/blobs/{id}`, `/v1/uploads/{id}`, `/health`, `/metrics` |
| `EMAIL_HOOK_SECRET` / `_FILE` | — | Shared secret for the RCPT hook (`Authorization: Bearer …`, or Basic with it as password) |
| `EMAIL_LMTP_ADDR` | `:2525` | Inbound LMTP (plain SMTP `DATA` also works) |
| `EMAIL_LMTP_USERNAME`, `EMAIL_LMTP_PASSWORD` / `_FILE` | — | AUTH PLAIN credential the mail server uses |
| `EMAIL_LMTP_TLS_CERT_FILE`, `_KEY_FILE` | — | STARTTLS for LMTP (required unless `EMAIL_LMTP_INSECURE=1`) |
| `EMAIL_LMTP_INSECURE` | — | `1`: AUTH without TLS — only on a private network between the two |
| `EMAIL_LMTP_ALLOW` | — | Comma-separated IPs/CIDRs allowed to connect |
| `EMAIL_AUTHSERV_ID` | — | The mail server's authserv-id; only its `Authentication-Results` and spam headers are trusted |
| `EMAIL_MAX_MESSAGE_BYTES` | `26214400` | Largest message accepted |
| `EMAIL_TRANSPORT` | `smtp` | `smtp` or `log` (writes `.eml` files to `EMAIL_LOG_DIR`) |
| `EMAIL_SMTP_HOST`, `_PORT` (`587`), `_USERNAME`, `_PASSWORD` / `_FILE`, `_TLS` (`starttls`\|`tls`\|`none`) | — | Submission to the mail server |
| `EMAIL_MESSAGE_ID_DOMAIN` | first domain | Right-hand side of minted Message-IDs |
| `EMAIL_DATA` | `/data` | SQLite state and blobs |
| `EMAIL_BLOB_TTL` | `720h` | How long sealed attachments and originals stay downloadable |
| `EMAIL_OPTIN_CACHE` | `2m` | How long an opt-in answer is cached (opting out takes effect within it, plus the relay's 60 s) |
| `EMAIL_OUT_DAILY_LIMIT` | `100` | Messages per identity per day |
| `EMAIL_OUT_NEW_RECIPIENTS_DAILY` | `20` | New correspondents per identity per day (replying to someone who wrote first is not new) |
| `EMAIL_OUT_MIN_AGE` | `0` | Wait after an identity's first send attempt before it may send (e.g. `24h`) |
| `EMAIL_IN_DAILY_PER_RECIPIENT` | `500` | Inbound messages per identity per day (then 452 at RCPT) |
| `EMAIL_FOOTER` | `first` | `off`, `first` (first message to each recipient) or `always` |
| `EMAIL_FOOTER_TEXT` | `Sent with Poweur ID {id} · Get your own free ID: {signup_url}` | Placeholders `{id}`, `{profile_url}`, `{signup_url}` |
| `EMAIL_SIGNUP_URL` | `https://poweur.net` | |
| `EMAIL_RESOLVE_SCHEME`, `EMAIL_RESOLVE_ALLOW_PRIVATE`, `EMAIL_RESOLVE_DIAL` | `https`, —, — | Identity resolution (tests/dev only; `_DIAL` sends every lookup to one host:port) |

`poweur-email check-config` validates the environment; `poweur-email version` prints the version.

On the relay, set `EMAIL_BRIDGE_ID=email.poweur.net`: hosted users then see the bridge in
their capabilities, and the bridge's sends get their own rate limit
(`EMAIL_BRIDGE_RATE_LIMIT_MINUTE|HOUR|DAY`, default 600 / 20000 / 200000) instead of the
per-person one.

## Setting it up

1. **The bridge identity.** `email` is a reserved name, so create it with the relay's
   operator token, from a seed you keep:

   ```bash
   openssl rand -base64 32 | tr '+/' '-_' | tr -d '=' > email-seed.txt
   poweur identity create email.poweur.net --seed="$(cat email-seed.txt)" --operator-token="$OPERATOR_TOKEN"
   ```

2. **The bridge** (`compose.example.yml`, or the Ansible role in `deploy/ansible`). On start
   it publishes its own `capabilities.json` (`email_api`) so clients find the upload endpoint.

3. **The mail server.** Three settings in Stalwart (managed in the `famooss` repo for
   poweur.net) — check the names against your Stalwart version:

   - an **MTA hook** at the `rcpt` stage, `url = "https://email.poweur.org/stalwart/hook"`,
     an `Authorization: Bearer <EMAIL_HOOK_SECRET>` header, `tempfail-on-error = true`,
     enabled for recipients in the bridged domains;
   - a **relay route** for those recipients to the bridge: host and port of
     `EMAIL_LMTP_ADDR`, protocol **LMTP**, STARTTLS, `auth.username`/`auth.secret` =
     `EMAIL_LMTP_USERNAME`/`PASSWORD`;
   - a **submission account** for the bridge (`EMAIL_SMTP_USERNAME`) that may send as any
     address in the bridged domains; Stalwart DKIM-signs it and relays it through Brevo.

   DNS (MX, SPF, DKIM, DMARC, MTA-STS, TLS-RPT, PTR) belongs to the mail server; the
   relevant labels (`mta-sts`, `autoconfig`, `autodiscover`, role names) are reserved so no
   user can claim them as identities.

### More domains later

Point another domain's MX at the same mail server and add it to `EMAIL_DOMAINS`:
`poweur.net,poweur.org=poweur.net` delivers `john@poweur.org` to `john.poweur.net` too
(sending stays from the address in the user's `email.json`).

### Operator mailboxes beside the bridge

An identity such as `support.poweur.net` reads and sends `support@poweur.net` in the Poweur
app like anyone else: opt it in (`poweur email enable --use-identity=support.poweur.net`).
`EMAIL_ALIASES` can route `postmaster@` and `abuse@` to it.

To *also* read it in a webmail such as Bulwark, the mail server must keep a plaintext copy —
something the bridge never does. Make `support` a local Stalwart account and copy its mail
to the bridge through an internal alias domain: add `in.poweur.net=poweur.net` to
`EMAIL_DOMAINS`, route `in.poweur.net` to the bridge only (no MX), and give the account a
Sieve rule `redirect :copy "support@in.poweur.net";`. Webmail login is the mail server's
business (OIDC to Authentik, or to the Poweur OAuth bridge).

## Development

```bash
go test ./apps/email/...
```

The integration suite (`apps/integration`, `TestINT_EMAIL_*`) runs the bridge against real
relays, with a test LMTP client standing in for the mail server and the `log` transport.

## What the bridge keeps

| Where | What | How long |
|---|---|---|
| SQLite `email.db` | inbox cursor; HMACs of (identity, correspondent) pairs, suppressed addresses and delivered message keys (keyed by a secret derived from the seed — not an address book); send ids and states; daily counters; queued `email.send` envelopes (still encrypted to the bridge) | dedupe keys 7 days; the rest while relevant |
| `blobs/` | attachments and originals sealed to the recipient | `EMAIL_BLOB_TTL` |
| `uploads/` | outbound attachments sealed to the bridge | until sent, at most 24 h |

Logs carry no addresses or subjects. Back up `email.db` (send states, suppression list,
footer ledger); blobs are disposable.
