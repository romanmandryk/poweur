# poweur-email — the Poweur email bridge

EPIC-023. Gives every hosted Poweur ID a working address — **on by default**, one switch to
opt out: mail to
`john@poweur.net` arrives, end-to-end encrypted, in `john.poweur.net`'s Poweur inbox (the web
app's **Email** tray, `poweur email ls`) — sign-in links, notifications, newsletters, replies.
Optionally, John can also send mail as `john@poweur.net` from the app, paced and limited.

The bridge is itself a Poweur identity (`email.poweur.net`). It holds no relay credential,
keeps no plaintext after delivery, and its relays store only ciphertext. Large attachments and
the original message are kept only **sealed to the recipient**, at unguessable URLs, for
`EMAIL_BLOB_TTL`.

Protocol: [PCP-0010](../../conventions/pcp-0010-email-bridge.md) ·
`packages/identity/email.go` · prose in [`apps/docs/docs/email/bridge.md`](../docs/docs/email/bridge.md).

## Two halves, chosen separately

**Inbound** (`EMAIL_INBOUND`) — how mail reaches the bridge:

| Mode | You need | The bridge does |
|---|---|---|
| `smtp` (default) — **the bridge is the MX** | an `MX` record for each domain pointing at the bridge's host, port 25 open, ideally a TLS certificate for that name | listens on :25, refuses unknown and opted-out addresses at `RCPT TO`, checks SPF, DKIM and DMARC itself (refusing DMARC `p=reject` failures), limits connections per IP |
| `lmtp` — **behind a mail server** (Stalwart, Postfix with an RCPT hook…) | the mail server's RCPT hook pointed at `/stalwart/hook`, and a relay route to the bridge over LMTP | answers the hook, takes mail over LMTP with AUTH, trusts only that server's `Authentication-Results` and spam headers |

**Outbound** (`EMAIL_TRANSPORT`) — whether and how users send:

| Transport | Use it when |
|---|---|
| `none` | **receive only.** The app shows no Write or Reply; `email.send` is refused politely |
| `smtp` | submit to any SMTP server: your own mail server (which signs and relays), or a provider's relay directly (`smtp-relay.brevo.com:587`, Postmark, SES, …). Set `EMAIL_DKIM_SELECTOR`/`_KEY_FILE` if that server does not DKIM-sign as your domain |
| `log` | development: write each message to `EMAIL_LOG_DIR` |
| *(later)* provider APIs | e.g. Brevo's HTTP API, for delivery events without a mail server |

To keep a transport but let only some IDs send (a soft launch, or while a domain builds its
reputation), set `EMAIL_SEND_MODE=allowlist` and `EMAIL_SEND_ALLOW=support.example.org,…`.
Everyone else's `email.send` is refused with a polite "sending is paused" status, and the
bridge publishes `email_send: "paused"` so apps can say so before anyone writes. The list
itself is never published.

### The smallest setup: receive only

For a self-hoster who wants `name@example.org` to receive sign-in links and notifications —
no mail server, no outbound provider:

```bash
EMAIL_BRIDGE_ID=email.example.org        # its own Poweur ID (create it first, see below)
EMAIL_BRIDGE_SEED_FILE=/run/secrets/seed
EMAIL_RELAY_URL=https://relay.example.org
EMAIL_DOMAINS=example.org
EMAIL_MX_HOSTNAME=mx.example.org         # what the MX points at, and its PTR
EMAIL_TLS_CERT_FILE=/certs/mx.crt        # optional, recommended (senders use STARTTLS)
EMAIL_TLS_KEY_FILE=/certs/mx.key
EMAIL_TRANSPORT=none
EMAIL_PUBLIC_URL=https://email.example.org   # optional: lets attachments over ~350 KB through
```

DNS: `mx.example.org` A → the host; `example.org` MX → `10 mx.example.org`. That is all. (For
sending later, add SPF/DKIM/DMARC for the domain and pick a transport.)

### poweur.net: behind Stalwart, on the same VM

```
internet ──SMTP:25──► Stalwart (famooss-stalwart, mail.poweur.net)
   │  RCPT hook ── http://poweur-email:8091/stalwart/hook ──► bridge: does john@ exist and opt in?
   │  relay route ── LMTP poweur-email:2525 (AUTH) ──────────► bridge: parse, sanitize, encrypt,
   ▼                                                            deliver email.message to John
Poweur app ── email.send ──► bridge ── submission famooss-stalwart:587 ──► Stalwart ──► Brevo ──► recipient
                               └── email.status back to John (sent / waiting until … / bounced / refused)
```

All three hops stay on the Docker network `infra_net`; nothing extra is published on the host.
`https://email.poweur.org` (Caddy) serves only blobs and uploads to browsers.
[`deploy/docker-compose.prod.yml`](deploy/docker-compose.prod.yml) is the exact configuration.

## Sending limits

Receiving is what the address is for; sending from free addresses is what gets a domain onto
blocklists. So sending is **paced**, per identity, in a rolling 24 hours:

- at most `EMAIL_OUT_DAILY_LIMIT` messages (default 10);
- after each send the next waits `EMAIL_OUT_SPACING` × 2ⁿ⁻¹ — 5 min, 10, 20, 40, 80, then
  `EMAIL_OUT_SPACING_MAX` (2 h);
- at most `EMAIL_OUT_NEW_RECIPIENTS_DAILY` new correspondents (default 10; replying to someone who
  wrote first is not new);
- the whole bridge stays under `EMAIL_OUT_GLOBAL_DAILY_LIMIT` (set it below your provider's cap;
  Brevo's free plan allows 300 a day).

A send that comes too early is **not refused**: it waits in the bridge's queue, and the user's
app shows "waiting — goes out at 14:05". At most `EMAIL_OUT_MAX_PENDING` (20) can wait.

People ask for more the way they ask for more storage, and the operator grants it:

```bash
docker exec poweur-email poweur-email limits set john.poweur.net --daily=50 --spacing=1m --note="newsletter for the club"
docker exec poweur-email poweur-email limits list
docker exec poweur-email poweur-email limits reset john.poweur.net
```

## On by default, opt-out, filters

With `EMAIL_BRIDGE_ID` set on the relay, every hosted identity with an address takes mail
through that bridge from the moment it exists — no step for the user, no rule per user on the
mail server. The relay's `capabilities.json` says so per identity
(`endpoints.email_bridge` plus `features.email_receive: "on"|"off"`, cached 60 s), and the
bridge reads that when there is no `email.json`. Unknown names get `550 5.1.1` at `RCPT TO`.

- **Opt out** — Settings → Email, or `poweur email disable`: the inbox policy gets
  `email: {allow: false}`; the relay reports `off` and refuses the bridge's messages.
- **Another bridge, or turning back on** — `poweur email enable [--bridge=<id>]` writes the
  signed `.poweur/public/email.json` (`{identity, address, bridge, created_at}`) and
  `email: {allow: true, bridge}`. An explicit `email.json` always wins over the default.
- **Filters** — who may email you, private to you and the bridge: block addresses or
  `@domains`, or take mail only from people you know (whom you emailed, replies to your mail,
  delivery reports, an allow list). The client sends them to the bridge as an encrypted
  `email.settings` message; the bridge keeps HMACs of the lists only and **drops filtered mail
  silently** (accepted, never delivered, no bounce). The user's own copy is that message in
  their archive. Settings → Email, a **Block** link on each email, or `poweur email filters`.

## Configuration

Environment only. Every secret also takes a `_FILE` variant.

| Variable | Default | Meaning |
|---|---|---|
| `EMAIL_BRIDGE_ID` | — | The bridge's Poweur ID (`email.poweur.net`) |
| `EMAIL_BRIDGE_SEED` / `_FILE` | — | Its 32-byte seed (base64url) or recovery phrase; derives its keys |
| `EMAIL_RELAY_URL` | — | The bridge identity's home relay (`https://relay.poweur.net`) |
| `EMAIL_DOMAINS` | — | Mail domains, `poweur.net,poweur.org=poweur.net` (`a=b`: mail for `x@a` goes to `x.b`) |
| `EMAIL_ALIASES` | — | Whole addresses to an identity: `postmaster@poweur.net=support.poweur.net,…` (the target must take email) |
| `EMAIL_PUBLIC_URL` | — | HTTPS origin for blobs and uploads; advertised as `email_api`. Without it attachments over ~350 KB are dropped and uploads are off |
| `EMAIL_ADDR` | `:8091` | HTTP: `/blobs/{id}`, `/v1/uploads/{id}`, `/health`, `/metrics`, and in `lmtp` mode `/stalwart/hook` |
| **Inbound** | | |
| `EMAIL_INBOUND` | `smtp` | `smtp` (the bridge is the MX) or `lmtp` (behind a mail server) |
| `EMAIL_SMTP_LISTEN` | `:25` | `smtp` mode: the public listener |
| `EMAIL_MX_HOSTNAME` | — | `smtp` mode: the greeting name; the MX target, matching the host's PTR |
| `EMAIL_TLS_CERT_FILE`, `_KEY_FILE` | — | `smtp` mode: STARTTLS certificate for the MX name |
| `EMAIL_HOOK_SECRET` / `_FILE` | — | `lmtp` mode: shared secret for the RCPT hook (`Authorization: Bearer …`, or Basic with it as password) |
| `EMAIL_LMTP_ADDR` | `:2525` | `lmtp` mode: listener (plain SMTP `DATA` also works) |
| `EMAIL_LMTP_USERNAME`, `EMAIL_LMTP_PASSWORD` / `_FILE` | — | `lmtp` mode: AUTH PLAIN credential the mail server uses |
| `EMAIL_LMTP_TLS_CERT_FILE`, `_KEY_FILE` | — | `lmtp` mode: STARTTLS (required unless `EMAIL_LMTP_INSECURE=1`) |
| `EMAIL_LMTP_INSECURE` | — | `1`: AUTH without TLS — only on a private network between the two |
| `EMAIL_LMTP_ALLOW` | — | IPs/CIDRs allowed to connect |
| `EMAIL_AUTHSERV_ID` | — | `lmtp` mode: the mail server's authserv-id; only its `Authentication-Results` and spam headers are trusted |
| `EMAIL_MAX_MESSAGE_BYTES` | `26214400` | Largest message accepted |
| `EMAIL_IN_DAILY_PER_RECIPIENT` | `500` | Inbound messages per identity per day (then 452 at RCPT) |
| **Outbound** | | |
| `EMAIL_TRANSPORT` | `smtp` | `smtp`, `none` (receive only) or `log` |
| `EMAIL_SMTP_HOST`, `_PORT` (`587`), `_USERNAME`, `_PASSWORD` / `_FILE`, `_TLS` (`starttls`\|`tls`\|`none`) | — | Submission server |
| `EMAIL_SMTP_TLS_SERVER_NAME` | `$EMAIL_SMTP_HOST` | Certificate name to expect when the host is an internal name (`famooss-stalwart` → `mail.poweur.net`) |
| `EMAIL_DKIM_SELECTOR`, `EMAIL_DKIM_KEY_FILE` | — | Sign outbound mail as the sender's domain (PEM, RSA or Ed25519) when the submission server does not |
| `EMAIL_MESSAGE_ID_DOMAIN` | first domain | Right-hand side of minted Message-IDs |
| `EMAIL_OUT_DAILY_LIMIT` | `10` | Messages per identity per rolling 24 h |
| `EMAIL_OUT_SPACING`, `EMAIL_OUT_SPACING_MAX` | `5m`, `2h` | First gap between sends, doubling up to the max |
| `EMAIL_OUT_NEW_RECIPIENTS_DAILY` | `10` | New correspondents per identity per day |
| `EMAIL_OUT_GLOBAL_DAILY_LIMIT` | `0` (none) | The whole bridge per rolling 24 h |
| `EMAIL_OUT_MAX_PENDING` | `20` | Sends one identity may have waiting |
| `EMAIL_SEND_MODE` | `open` | `allowlist`: only `EMAIL_SEND_ALLOW` may send; others are told sending is paused |
| `EMAIL_SEND_ALLOW` | — | Comma-separated IDs allowed to send in `allowlist` mode |
| `EMAIL_OUT_MIN_AGE` | `0` | Wait after an identity's first send attempt before it may send (e.g. `24h`) |
| `EMAIL_FOOTER` | `first` | `off`, `first` (first message to each recipient) or `always` |
| `EMAIL_FOOTER_TEXT` | `Sent with Poweur ID {id} · Get your own free ID: {signup_url}` | Placeholders `{id}`, `{profile_url}`, `{signup_url}` |
| `EMAIL_SIGNUP_URL` | `https://poweur.net` | |
| **Storage** | | |
| `EMAIL_DATA` | `/data` | SQLite state and blobs |
| `EMAIL_BLOB_TTL` | `720h` | How long sealed attachments and originals stay downloadable |
| `EMAIL_OPTIN_CACHE` | `2m` | How long an opt-in answer is cached (opting out takes effect within it, plus the relay's 60 s) |
| `EMAIL_RESOLVE_SCHEME`, `EMAIL_RESOLVE_ALLOW_PRIVATE`, `EMAIL_RESOLVE_DIAL` | `https`, —, — | Identity resolution (tests/dev only; `_DIAL` sends every lookup to one host:port) |

`poweur-email check-config` validates the environment; `poweur-email version` prints the version;
`poweur-email limits …` manages per-identity sending limits.

On the relay, set `EMAIL_BRIDGE_ID=email.poweur.net`: hosted users then see the bridge in
their capabilities, and the bridge's deliveries get their own rate limit
(`EMAIL_BRIDGE_RATE_LIMIT_MINUTE|HOUR|DAY`, default 600 / 20000 / 200000) instead of the
per-person one.

## Setting it up

1. **The bridge identity.** `email` is a reserved name, so create it with the relay's
   operator token, from a seed you keep:

   ```bash
   openssl rand -base64 32 | tr '+/' '-_' | tr -d '=' > email-seed.txt
   poweur identity create email.poweur.net --hosted --seed="$(cat email-seed.txt)" --operator-token="$OPERATOR_TOKEN"
   ```

2. **The bridge** (`compose.example.yml`, or `deploy/` for poweur.net). On start it publishes its
   own `capabilities.json` (`email_api`, `email_send`) so clients find the upload endpoint and
   know whether to offer sending.

3. **Inbound:** either the MX record (`smtp` mode), or the mail server's three settings
   (`lmtp` mode):
   - an **MTA hook** at the `rcpt` stage → `/stalwart/hook` with
     `Authorization: Bearer <EMAIL_HOOK_SECRET>`, `tempfail-on-error = true`;
   - a **relay route** for bridged recipients to the bridge over **LMTP**, AUTH
     `EMAIL_LMTP_USERNAME`/`PASSWORD`;
   - a **submission account** for the bridge (`EMAIL_SMTP_USERNAME`) allowed to send as any
     address in the bridged domains.

The labels mail infrastructure needs (`mta-sts`, `autoconfig`, `autodiscover`, role names) are
reserved so no user can claim them as identities.

### More domains later

Point another domain's MX at the same place and add it to `EMAIL_DOMAINS`:
`poweur.net,poweur.org=poweur.net` delivers `john@poweur.org` to `john.poweur.net` too
(sending stays from the address in the user's `email.json`).

### Operator mailboxes beside the bridge

An identity such as `support.poweur.net` reads and sends `support@poweur.net` in the Poweur
app like anyone else (email is on for it by default), and raise its sending limits. `EMAIL_ALIASES` can route `postmaster@` and `abuse@` to it.

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
| SQLite `email.db` | inbox cursor; HMACs of (identity, correspondent) pairs, suppressed addresses, delivered message keys and send times (keyed by a secret derived from the seed — not an address book); send ids and states; per-identity limit overrides; queued `email.send` envelopes (still encrypted to the bridge) | dedupe keys 7 days, send times 48 h; the rest while relevant |
| `blobs/` | attachments and originals sealed to the recipient | `EMAIL_BLOB_TTL` |
| `uploads/` | outbound attachments sealed to the bridge | until sent, at most 24 h |

Logs carry no addresses or subjects. Back up `email.db` (send states, suppression list, footer
ledger, limit overrides); blobs are disposable.
