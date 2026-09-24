# EPIC-023 — Email bridge: a default inbox for every Poweur ID

- **Status:** proposed
- **Priority:** P1 (growth: lets every Poweur ID reach, and be reached by, the whole email
  network, and gives each non-Poweur correspondent a reason to join)
- **Depends on:** EPIC-001 (resolver chain), EPIC-006 (`poweur-sys` + PCP registry), EPIC-007
  (contacts, inbox policy), EPIC-009 (typed messages, attachments, push), EPIC-013
  (deployment/observability), EPIC-014 (PoW primitive, reused as an outbound send gate)
- **Interacts with:** EPIC-015 / EPIC-021 (Messages trays, Settings panels), EPIC-018 (onboarding
  step), EPIC-016 (pay-to-send as the outbound fast lane), EPIC-010 (email as an automation
  trigger), EPIC-012 (contact forms), EPIC-020 (message history v2 — email volume), EPIC-022 (the
  pattern this service copies: separate origin, public-protocol-only coupling)
- **Unlocks:** `john@poweur.net` for every hosted ID that opts in; email ↔ Poweur conversation
  upgrades; email-driven automations; masked aliases
- **Plans note (EPIC-026):** receiving is free; outbound volume, masked aliases and custom-domain mail are the paid lines. Email is also a recipient channel for E05-T7 Send and EPIC-030 buyers.

## Progress

| Task | Status | Notes |
|------|--------|-------|
| E23-T1 Spec, addressing & threat model | open | |
| E23-T2 Bridge core + inbound SMTP | open | |
| E23-T3 Outbound: pluggable transports, DKIM, bounces | open | |
| E23-T4 Abuse controls & reputation | open | |
| E23-T5 Opt-in: relay policy, name reservations, SDK, CLI | open | |
| E23-T6 Web app: opt-in, Emails tray, compose, safe HTML rendering | open | |
| E23-T7 Growth footer | open | |
| E23-T8 Packaging, DNS, deploy & operations | open | |
| E23-T9 Growth loops: invites, conversation upgrade, aliases, screener | open | |
| E23-T10 Later: custom domains, forwarding-in, local IMAP bridge | open | |

## Goal

Run an email bridge — `email.poweur.org` for the public deployment, any hostname for a
self-hoster — that gives every **opted-in** hosted Poweur ID a working email address:

- mail sent to `john@poweur.net` is delivered, encrypted to John's key, into
  `john.poweur.net`'s Poweur inbox and shown in an **Emails** tray;
- John writes email from the Poweur app and it arrives in any mailbox as ordinary, well
  authenticated mail from `john@poweur.net`;
- the bridge has **no UI**: all configuration is environment variables / Ansible, and the set of
  domains it answers for defaults to the relay's `HOSTED_DOMAINS`.

The value is two-sided. A Poweur user no longer needs "a real email address as well" — the ID is
the address. A non-Poweur correspondent sees a normal email, optionally a one-line invitation, and
a path to upgrade the thread to end-to-end encrypted Poweur messaging when they join.

## Background (current code & related epics)

- Messages are end-to-end encrypted by the sender (X25519 ephemeral + HKDF +
  ChaCha20-Poly1305) and the relay refuses plaintext (`anon.go`: "Encrypt-only holds for anonymous
  senders too"). An email bridge can meet that rule for inbound mail: it resolves the recipient's
  `encryption_public_key` and encrypts like any sender. The relay keeps storing only ciphertext.
- Typed messages (EPIC-009 E09-T3) are opaque to the relay apart from `sys.*`; the envelope cap is
  **512 KB** and larger payloads travel as attachment *references* plus share grants (E09-T4).
- Inbox policy already has a per-service trust list: `trusted_auth_services`
  (`packages/identity/inboxpolicy.go`), used by the OAuth bridge's push-to-approve (E22-T7).
  Email trust follows the same shape.
- The anonymous tray (`apps/web/src/screens/messages/Messages.tsx`) is the UX precedent: a tray
  shown only when the policy enables it, a toggle in Settings (`PolicyControls.tsx`) and in
  onboarding.
- EPIC-022 set the deployment pattern: `apps/oauth` is its own Go module, image and origin
  (`oauth.poweur.org` behind the shared Caddy), holds no relay credential and talks to relays
  only over the public protocol.
- `ReservedLabels` (`packages/identity/names.go`) already holds `mail`, `mx`, `smtp`, `imap`,
  `pop`. It does **not** hold `postmaster`, `abuse`, `mta-sts`, etc., which email makes special
  (see E23-T5).
- Production is one Hetzner VM (`deploy/ansible/inventory.yml`). Hetzner Cloud blocks outbound
  ports 25/465 on new projects by default, and a fresh VM IP has no sending reputation — both
  argue for a provider as the default outbound path.
- `*.poweur.net` is proxied by Cloudflare to the relay over HTTP. Cloudflare's proxy does not
  carry SMTP, so the MX hostname must be a **DNS-only** record.

## Design direction

### Separate service or relay component?

The request allowed the bridge to live inside the relay if that adds little security or DDoS
exposure. It adds a lot of both, so the recommendation is a **separate process**, deployed the
same way as the OAuth bridge.

| | Component inside `poweur-relay` | Standalone `apps/email` service (recommended) |
|---|---|---|
| **Deploy** | ✅ One binary, one env file; self-hosters flip `EMAIL_ENABLED=1` | ➖ One more container in the same compose/Ansible run (same as `apps/oauth` today) |
| **Data access** | ✅ Direct access to `HOSTED_DOMAINS`, identity store, inbox storage; no resolver round trip | ➖ Resolves recipients and delivers through the public protocol (cacheable; the OAuth bridge already does this) |
| **Attack surface** | ❌ Raw TCP :25 is scanned and spammed 24/7 by the whole internet; hostile MIME/HTML/charset parsing would run in the process holding sessions, DAV tokens, PoW and registration secrets | ✅ Hostile input parsed in a process that holds only mail-transport secrets; container memory/CPU/connection limits contain it |
| **DDoS / availability** | ❌ An SMTP connection flood or a MIME-bomb OOM takes identity, messaging and files down with it | ✅ Mail degrades on its own; the relay is untouched. Spam bursts scale independently |
| **Trust model** | ❌ The relay would see email plaintext, breaking "the relay is a blind store" | ✅ Plaintext stays in the one component that must see it (email is plaintext on the wire anyway); the relay still only stores ciphertext |
| **Secrets** | ❌ Provider API keys, SMTP passwords and DKIM private keys join the relay's environment | ✅ Kept in the bridge's own env file |
| **Reach** | ➖ Only the relay's own hosted domains | ✅ One bridge can serve several relays' domains; a domain owner can run a bridge without a relay |
| **Release cadence** | ❌ Deliverability and anti-spam fixes need a relay release | ✅ Released independently (own `bridge.Version`, as `apps/oauth`) |
| **Networking** | ❌ The relay container must publish :25 and get its own DNS-only name regardless | ✅ Same requirement, isolated to the mail container |

**Decision (for T1 to confirm):** new Go module `apps/email` (binary `poweur-email`), its own image,
own origin `email.poweur.org` (HTTPS: webhooks, blob download, health, metrics, MTA-STS if hosted
there) and MX host `mx.poweur.org` (DNS-only, SMTP). It holds **no relay credential**. The core is
a library with `Run(ctx, cfg)` so a later small-self-hoster "all-in-one" launcher can start it
beside the relay in one container — still as a separate process boundary, never linked into the
relay's handler tree.

### The bridge is a Poweur identity

The bridge owns a Poweur ID (e.g. `email.poweur.org`, web identity at
`https://email.poweur.org/.well-known/poweur/id.json`). It is the sender of every inbound email it
delivers and the recipient of every outbound email a user asks it to send. That reuses signed and
encrypted envelopes, relay forwarding, the outbox, retries and push — no new relay endpoint for
mail.

```
Internet MTA ──SMTP:25──► mx.poweur.org (poweur-email)
                           │ SPF/DKIM/DMARC/ARC verdicts, spam score, size caps
                           │ resolve john.poweur.net → check signed opt-in → encrypt to John
                           ▼
                      POST /messages  type=email.message  from=email.poweur.org
                           ▼
                   john's relay (ciphertext only) ──SSE──► Poweur app: Emails tray

Poweur app ── type=email.send (signed by John, encrypted to the bridge) ──► bridge inbox
                           │ verify John's signature + opt-in + quotas
                           │ build MIME From: john@poweur.net, footer, DKIM
                           ▼
                 transport: smtp | ses | postmark | direct | log ──► recipient MX
                           │ bounces / complaints (webhook or DSN)
                           ▼
                 type=email.status back to John (sent / deferred / bounced / complained)
```

### Addressing and routing

- An address `local@D` is accepted only when `D` is in `EMAIL_DOMAINS` (default: `HOSTED_DOMAINS`)
  and routes to the identity `local.D` (`john@poweur.net` → `john.poweur.net`).
- The local part is normalized with the same rules as ID names (`packages/identity` name policy):
  lowercased; `.` is **not** ignored (IDs are DNS labels). Subaddressing `john+news@poweur.net` →
  `john.poweur.net` with tag `news` kept in the payload for client-side filters.
- **All** recipient checks happen at `RCPT TO`, answering `550 5.1.1` for unknown, not-opted-in or
  suspended recipients. The bridge never accepts and then bounces — accept-then-bounce produces
  backscatter spam and burns reputation.
- RFC 2142 role addresses (`postmaster@`, `abuse@`) for every served domain are always accepted and
  routed to an operator-configured Poweur ID (`EMAIL_POSTMASTER_ID`), not to a user.

### Opt-in is user-signed, twice

Opting in (onboarding or Settings, same pattern as anonymous messages) writes two things, both by
the user's client with the user's key; the relay and the bridge share no secret:

1. **Public, signed** `poweur-sys/public/email.json` —
   `{"version":1, "address":"john@poweur.net", "bridge":"email.poweur.org", "created_at":…}`.
   The bridge resolves and verifies it (short cache, e.g. 5 min) at `RCPT TO` for inbound and on
   every `email.send` for outbound. Opting out deletes it; within the cache TTL the address starts
   answering `550`.
2. **Private inbox policy** gains an `email` block (sibling of `anonymous`):
   `{"allow": true, "bridge": "email.poweur.org"}`. The relay admits `email.*` typed messages from
   that bridge ID even when the inbox is `contacts_only`, exactly as `trusted_auth_services` admits
   the OAuth bridge.

The client renders a message as email **only** when it is `email.message` signed by the bridge
named in the user's own opt-in. Any other ID sending `email.message` gets an ordinary (untrusted)
message — a Poweur user cannot forge "an email from your bank".

### Message types (PCP entries in E23-T1)

- `email.message` (bridge → user): envelope `from`/`to`/`cc`/`reply-to`/`subject`/`date`,
  `message_id`, `in_reply_to`/`references`, `tag` (subaddress), `auth` (SPF/DKIM/DMARC/ARC results +
  spam score), `text`, `html` (pre-sanitized, see below), `attachments[]` (inline if the envelope
  budget allows, otherwise encrypted blob references), optional `raw` blob reference (the original
  `.eml`, for "Show original"). `thread_id` derived from `References` / `In-Reply-To` so replies
  thread with the conversation.
- `email.send` (user → bridge): `to`/`cc`/`bcc`, `subject`, `text`, optional `html` (the composer's
  output, re-sanitized by the bridge), `in_reply_to`, attachments as E09-T4 references with a grant
  to the bridge ID, a client `send_id` for idempotency.
- `email.status` (bridge → user): `send_id`, `state` (`accepted` | `sent` | `deferred` | `bounced` |
  `complained` | `refused`), provider message id, human reason.

### Large content without the bridge keeping plaintext

Envelopes stay capped at 512 KB. For larger mail the bridge encrypts each attachment / the raw
`.eml` with a random content key, stores **only the ciphertext** under `EMAIL_DATA/blobs/` for
`EMAIL_BLOB_TTL` (default 30 days), and puts the key and a download URL inside the E2E-encrypted
`email.message`. The client fetches and can import attachments into the user's home
(`/email/attachments/…`); an ack lets the bridge delete early. After delivery the bridge retains no
plaintext; the spool (mail accepted but not yet delivered to the relay) is short-lived and on
encrypted disk. The privacy page must say plainly: **email is not end-to-end encrypted in
transit**; the bridge sees plaintext while it processes a message; mailboxes are encrypted at rest.

### Outbound transports — deliverability by default, pluggable

`EMAIL_TRANSPORT` selects the sender behind one Go interface:

| Transport | What | When |
|-----------|------|------|
| `smtp` (**default**) | Authenticated submission (587 STARTTLS / 465 TLS) to any provider: Amazon SES, Postmark, Mailgun, Brevo, Resend, Fastmail, a company relay… | Default for poweur.org and most self-hosters: the provider's warmed IPs, feedback loops and bounce handling give far better inbox placement than a fresh VPS IP |
| `ses`, `postmark`, … (API) | Provider HTTP API; message ids and webhooks for bounces/complaints | Better status reporting than SMTP; added per provider on demand |
| `direct` | The bridge is its own MTA: MX lookup, MTA-STS/DANE, queue with retries | Self-hosters with port 25 open, a PTR record and patience to warm an IP |
| `log` | Writes MIME to disk / stdout | Development and the integration suite |

Choosing a provider for the public deployment is part of E23-T8. The provider's acceptable-use
policy must allow person-to-person mail from free sign-ups, and the abuse controls in E23-T4 are
what keep that account in good standing. DKIM is signed by the provider (CNAME-published keys) or by
the bridge itself (`EMAIL_DKIM_KEY_FILE`) for `direct` and plain-SMTP relays that don't sign.

### Safe rendering of HTML email

Hard but well understood; every webmail does it. Three layers, all required:

1. **Bridge-side sanitization** (bluemonday allowlist): drops `script`, `iframe`, `object`, `embed`,
   `form`, event handlers, `javascript:`/`data:` URLs except `data:image/*`, `<base>`, `<meta
   http-equiv>`; rewrites `cid:` references to attachment ids. The unsanitized source only reaches
   the user as the encrypted `raw` blob.
2. **Client-side sanitization** (DOMPurify) of whatever arrives — the client does not trust the
   bridge's HTML either.
3. **Isolation**: render into `<iframe sandbox="allow-popups allow-popups-to-escape-sandbox
   allow-same-origin" srcdoc="…">` — **no `allow-scripts`**, no `allow-forms`, no
   `allow-top-navigation`. Without scripts, `allow-same-origin` is safe and lets the parent measure
   height to auto-size the frame. The document is prefixed with
   `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:">`
   and `<base target="_blank">`. The email's CSS cannot leak into the app and the app's cannot leak
   in; links open in a new browser tab (in the Capacitor shell, via the system browser).

**Remote images are blocked by default** (tracking pixels reveal read time and IP). "Load images"
per message / per sender relaxes `img-src` to `https:` — better, through an optional bridge image
proxy (`/img?u=…`, signed URL, no cookies, size cap) so the sender learns neither the user's IP nor
their device. Emails render on a light card in dark mode (most HTML email assumes a white page).
Plain-text-only mail uses the existing `MessageText` component; a "Plain text" toggle is available
whenever both parts exist.

### Growth footer

Off / first / always via `EMAIL_FOOTER` (default `first`): the first email a Poweur user sends to a
given recipient ends with

> Sent with Poweur ID **john.poweur.net** · Get your own free ID: https://poweur.net

in both text and HTML parts. "Once per recipient" is tracked by the bridge as
`HMAC(bridge_secret, sender_id ‖ lowercase(recipient))` — no plaintext address book on the bridge.
The text is a template (`EMAIL_FOOTER_TEXT`) with `{id}`, `{profile_url}`, `{signup_url}`
placeholders. Users on a self-hosted bridge get whatever their operator configures; a per-user
opt-out can be added later if it becomes a product decision (the operator switch is the v1 control).

### What makes it viable (and helps growth)

Must-haves for "this can be my inbox":

- **Deliverability** — SPF, DKIM and DMARC aligned from day one; MTA-STS and TLS-RPT; PTR on the MX
  host; Gmail/Yahoo bulk-sender rules are met at the *domain* level (poweur.net aggregates every
  user's volume, so complaint rate < 0.3 % is a domain-wide number to watch).
- **Inbound spam filtering** — authentication verdicts shown per message, a scored spam pass
  (rspamd over its HTTP API, optional container), plus Poweur's own contacts model: mail from an
  address in your contacts goes straight in, first-time senders go through a **screener** (E23-T9).
- **Threading, reply, reply-all, forward, attachments** — the basics the tray must not miss.
- **Search** — the store is ciphertext, so search is client-side over decrypted history (the same
  constraint EPIC-020 message history v2 already carries).
- **Quota and retention** — email is the biggest storage consumer the relay will have; per-identity
  caps and a visible usage meter.

Growth loops (E23-T9):

- **Invite by email**: adding a contact by email address sends one invite email; when that person
  claims an ID and proves the address, the email thread offers **"Continue as encrypted Poweur
  messages"** and switches transport for both sides.
- **Conversation upgrade detection**: if a correspondent's address is itself a Poweur address (or
  their domain publishes a Poweur ID), compose routes natively instead of via SMTP, end-to-end
  encrypted.
- **Masked aliases** (`john.k3x9@poweur.net` style, revocable per site) — the privacy feature
  people already pay for elsewhere.
- **Email as an automation trigger** (EPIC-010): `email.message` is a typed message, so agents and
  rules can file receipts into folders, forward newsletters, summarize.
- **Public "email me"** on the identity website (EPIC-012) works for every visitor.

## Configuration (env / Ansible only)

| Variable | Default | Meaning |
|----------|---------|---------|
| `EMAIL_DOMAINS` | `$HOSTED_DOMAINS` | Domains to accept mail for (`local@D` → `local.D`) |
| `EMAIL_BRIDGE_ID` | — | The bridge's own Poweur ID (e.g. `email.poweur.org`) |
| `EMAIL_BRIDGE_KEY_FILE` | — | Its signing/encryption keys (same format as `~/.poweur/keys`) |
| `EMAIL_PUBLIC_URL` | — | HTTPS origin (webhooks, blobs, image proxy, health) |
| `EMAIL_SMTP_LISTEN` | `:25` | Inbound SMTP listener |
| `EMAIL_MX_HOSTNAME` | — | EHLO name; must match PTR and the MX target |
| `EMAIL_TLS_CERT_FILE` / `EMAIL_TLS_KEY_FILE` | — | STARTTLS certificate for the MX host |
| `EMAIL_MAX_MESSAGE_BYTES` | `26214400` | Inbound size limit announced in `SIZE` |
| `EMAIL_TRANSPORT` | `smtp` | `smtp` \| `ses` \| `postmark` \| `direct` \| `log` |
| `EMAIL_SMTP_HOST` / `_PORT` / `_USERNAME` / `_PASSWORD` | — | Submission relay for `smtp` |
| `EMAIL_API_KEY` / `EMAIL_WEBHOOK_SECRET` | — | Provider API transports and their bounce/complaint webhooks |
| `EMAIL_DKIM_SELECTOR` / `EMAIL_DKIM_KEY_FILE` | — | Bridge-side DKIM signing (when the transport doesn't sign) |
| `EMAIL_FOOTER` | `first` | `off` \| `first` \| `always` |
| `EMAIL_FOOTER_TEXT` | built-in | Template with `{id}`, `{profile_url}`, `{signup_url}` |
| `EMAIL_POSTMASTER_ID` | — | Poweur ID receiving `postmaster@` / `abuse@` for every domain |
| `EMAIL_SPAM_FILTER_URL` | — | Optional rspamd HTTP endpoint |
| `EMAIL_OUT_DAILY_LIMIT` / `EMAIL_OUT_NEW_RECIPIENTS_DAILY` | `100` / `20` | Outbound quotas per identity |
| `EMAIL_OUT_GATE` | `pow` | `none` \| `pow` \| `age` (min identity age) — first-send gate |
| `EMAIL_BLOB_TTL` | `720h` | Ciphertext blob retention |
| `EMAIL_DATA` | `/data` | Spool, blobs, suppression list, footer ledger, quotas |

## DNS records

Shown for the hosted domain `poweur.net` with the bridge at `email.poweur.org` / `mx.poweur.org`
and a provider for outbound. Every served domain in `EMAIL_DOMAINS` needs its own set of the
`poweur.net` rows.

| Name | Type | Value | Why |
|------|------|-------|-----|
| `mx.poweur.org` | A / AAAA | bridge IP | MX target — **DNS-only** (grey cloud): Cloudflare does not proxy SMTP |
| PTR for the bridge IP | PTR | `mx.poweur.org` | Set in the Hetzner console; receivers check forward-confirmed reverse DNS |
| `email.poweur.org` | A / CNAME | Caddy | Bridge HTTPS origin (may be proxied) |
| `poweur.net` | MX | `10 mx.poweur.org.` | Route mail for `*@poweur.net` to the bridge |
| `poweur.net` | TXT | `v=spf1 include:<provider-spf> -all` | Only the provider (and, with `direct`, `ip4:<bridge IP>`) may send. Merge with any existing SPF — one SPF record per name |
| `<selector>._domainkey.poweur.net` | CNAME or TXT | provider's DKIM key(s), or `v=DKIM1; k=ed25519…`/`k=rsa; p=…` for bridge signing | DKIM alignment with the `From:` domain |
| `bounce.poweur.net` (provider-specific name) | MX + TXT, or CNAME | per provider (custom MAIL FROM / Return-Path) | SPF alignment for DMARC |
| `_dmarc.poweur.net` | TXT | `v=DMARC1; p=none; rua=mailto:dmarc@poweur.org; adkim=s; aspf=r` | Start at `p=none`, move to `quarantine` then `reject` once reports are clean |
| `_mta-sts.poweur.net` | TXT | `v=STSv1; id=<yyyymmddnn>` | Tells senders to require TLS to our MX |
| `mta-sts.poweur.net` | A / CNAME | Caddy, serving `/.well-known/mta-sts.txt` (`mode: testing` → `enforce`, `mx: mx.poweur.org`) | Must be a valid HTTPS host; add a Caddy block ahead of the `*.poweur.net` relay wildcard |
| `_smtp._tls.poweur.net` | TXT | `v=TLSRPTv1; rua=mailto:tlsrpt@poweur.org` | TLS failure reports |
| `poweur.org` (if it sends nothing) | MX / TXT | `0 .` (null MX), `v=spf1 -all`, DMARC `p=reject` | Keep the bridge's own domain from being spoofed |
| later: `default._bimi.poweur.net` | TXT | `v=BIMI1; l=https://…/logo.svg` | Brand logo in Gmail/Apple Mail once DMARC is at enforcement |

Identity names are DNS labels under the same zone, so `mta-sts`, `bounce`, `_dmarc`-adjacent and
role names must be reserved **before** the MX goes live (E23-T5). The wildcard `*.poweur.net` needs
no MX: addresses are `john@poweur.net`, not `…@john.poweur.net`.

## Tasks

### E23-T1 — Spec, addressing & threat model

- [ ] `apps/docs/docs/email/bridge.md`: architecture above, addressing and subaddress rules, RCPT-time
      acceptance, opt-in documents (public `email.json` + inbox-policy `email` block), trust rule
      for rendering, blob scheme, retention and the honest privacy statement
- [ ] PCP entries (EPIC-006) + JSON schemas for `email.message`, `email.send`, `email.status`,
      `poweur-sys/public/email.json`, and the inbox-policy `email` block; conformance vectors in
      `packages/identity/testdata/vectors/` (`pnpm vectors`)
- [ ] Threat model: open relay (must be impossible by construction), spoofed `From:` on inbound
      (show DMARC verdicts; never display "verified" without alignment), forged `email.message`
      from non-bridge IDs, outbound spam from free sign-ups, backscatter, MIME/zip/charset bombs,
      HTML/CSS exfiltration, tracking pixels, bridge compromise (what an attacker reads: mail in
      flight only), metadata the bridge logs (counters per EPIC-013 rules, no addresses in logs)
- [ ] Confirm the separate-service decision and the bridge-as-identity transport (drain its inbox
      over SSE with the Go client code the CLI uses, as `apps/oauth` sends via the CLI)

**Acceptance:** spec + schemas merged; vectors regenerated; threat model reviewed.

### E23-T2 — Bridge core + inbound SMTP

- [ ] `apps/email` module (`bridge.Version`, `poweur-email serve|version|check-dns`), config
      loading/validation, `/health`, `/metrics`
- [ ] SMTP server (e.g. `github.com/emersion/go-smtp`): STARTTLS, `SIZE`, connection/recipient/
      time limits per IP, early-talker rejection, no AUTH on :25
- [ ] RCPT-time routing: domain in `EMAIL_DOMAINS`, name policy normalization, resolve `local.D`,
      verify the signed `email.json` names this bridge, suspension list → `550 5.1.1` otherwise;
      postmaster/abuse → `EMAIL_POSTMASTER_ID`
- [ ] Authentication verdicts: SPF, DKIM (`go-msgauth`), DMARC, ARC; recorded in `auth`; DMARC
      `p=reject` failures refused at DATA
- [ ] MIME parse with caps (depth, part count, decoded size, charset conversion), text/HTML
      extraction, bluemonday sanitization, `cid:` mapping, `Message-ID`/`References` → `thread_id`
- [ ] Encrypt to the recipient, deliver as `email.message`; blob path for oversized parts; durable
      spool with retry and a dead-letter after N hours (then a DSN to the sender — the only bounce
      the bridge ever generates, and only for mail it accepted)
- [ ] Unit tests (table-driven): routing, normalization, verdicts, MIME caps, sanitizer corpus
      (XSS cheat-sheet + real newsletters); integration test `TestINT_EMAIL_01`: SMTP in →
      `email.message` in the recipient's inbox on a real relay, decryptable by the CLI

**Acceptance:** a message sent by `swaks` to `john@poweur.test` lands, encrypted, in
`john.poweur.test`'s inbox; a non-opted-in address gets `550` at RCPT.

### E23-T3 — Outbound: pluggable transports, DKIM, bounces

- [ ] `Transport` interface; `smtp`, `log` and `direct` implementations; one API transport
      (whichever E23-T8 picks) with webhook ingestion
- [ ] Consume `email.send`: verify sender signature + opt-in, idempotency on `send_id`, fetch
      E09-T4 attachment grants, re-sanitize HTML, build MIME (`From: john@poweur.net`,
      `Message-ID` on the bridge's domain, `In-Reply-To`/`References`), optional bridge DKIM
- [ ] `email.status` back to the sender for every state change; suppression list fed by hard
      bounces and complaints (sends to a suppressed address are refused with a reason)
- [ ] Integration test with the `log` transport and a fake webhook: send → sent → bounced →
      suppressed

**Acceptance:** `poweur email send` produces a DKIM-valid message via the `smtp` transport against
a local test MTA (e.g. mailpit); bounce webhooks update status and suppression.

### E23-T4 — Abuse controls & reputation

- [ ] Outbound quotas per identity (daily messages, new recipients/day), relay-configurable;
      replies to someone who emailed you first are exempt from the new-recipient cap
- [ ] First-send gate: PoW via the E14 primitive (`EMAIL_OUT_GATE=pow`), or minimum identity age;
      EPIC-016 pay-to-send as the later fast lane for higher limits
- [ ] Complaint/bounce-rate circuit breaker per identity and bridge-wide (auto-suspend sending,
      `email.status: refused` with an explanation; operator unsuspend via CLI)
- [ ] Optional outbound spam scan (rspamd) before submission; inbound spam score → `auth.spam`
      and a client-side "Spam" filter
- [ ] Inbound flood protection: per-IP/ASN connection limits, greylisting (optional), per-recipient
      daily cap mirroring the anon queue's `max_per_day`
- [ ] Metrics + alerts (EPIC-013): `email_inbound_total{result}`, `email_outbound_total{state}`,
      bounce and complaint rates, queue depth, suspended identities

**Acceptance:** an integration test drives an identity past its quota and a simulated complaint
spike, and sees refusal + suspension; alert rules committed.

### E23-T5 — Opt-in: relay policy, name reservations, SDK, CLI

- [ ] Inbox-policy `email` block in `packages/identity/inboxpolicy.go` + relay admission of
      `email.*` from the named bridge in every mode; TS client parity + vectors
- [ ] `poweur-sys/public/email.json` write/remove in the Go client and `@poweur/client`
      (`enableEmail(bridge)`, `disableEmail()`, `sendEmail()`, `listEmails()`); relay advertises
      its default bridge in public capabilities so clients know what to offer (as E22 does for the
      OAuth bridge)
- [ ] Reserve names in `ReservedLabels`: `postmaster`, `abuse`, `hostmaster`, `webmaster`,
      `mailer-daemon`, `noreply`, `no-reply`, `mta-sts`, `bounce`, `bounces`, `dmarc`, `email`,
      `security`, `support`; audit production for already-claimed ones before enabling the MX
- [ ] CLI: `poweur email enable|disable|status`, `poweur email send`, `poweur email ls|read`
      (text render; `--html` writes the sanitized HTML to a file)
- [ ] Version bumps: relay, CLI, `@poweur/client`

**Acceptance:** `TestINT_EMAIL_02`: CLI enables email, bridge accepts mail for it, CLI disables it,
bridge refuses within the cache TTL.

### E23-T6 — Web app: opt-in, Emails tray, compose, safe HTML rendering

- [ ] Onboarding step (EPIC-018 handoff) and Settings toggle next to anonymous messages:
      "Get john@poweur.net" — shows the address, what the bridge can see, footer notice; hidden
      when the relay advertises no bridge
- [ ] **Emails** tray in Messages, shown only when opted in (same gating as `anonymous`), unread
      badge, list with sender/subject/snippet, auth badge (verified / unverified / failed)
- [ ] `EmailView`: DOMPurify → sandboxed `srcdoc` iframe with CSP meta and `<base
      target="_blank">`, auto-height, light card in dark mode, remote images blocked with "Load
      images" (per message / per sender), plain-text toggle, "Show original", attachments list with
      import-to-files
- [ ] Compose / reply / reply-all / forward (plain text + light formatting), delivery status from
      `email.status`, quota and suspension messages
- [ ] Tests: Vitest for the renderer (hostile corpus: scripts, event handlers, `<base>`, CSS
      `@import`, `url()` beacons, `<meta refresh>`, SVG, forms) asserting nothing executes or
      loads; Playwright journey: opt in → receive via bridge `log`/SMTP fixture → read → reply
- [ ] Web app version bump

**Acceptance:** the hostile corpus renders with no script execution, no network request and no
style leaking into the app; the Playwright journey passes.

### E23-T7 — Growth footer

- [ ] `EMAIL_FOOTER=off|first|always`, `EMAIL_FOOTER_TEXT` template, text + HTML variants, placed
      above quoted reply text
- [ ] HMAC ledger of (sender, recipient) pairs; rotation-safe; unit tests for once-per-recipient
- [ ] Composer preview shows the footer when it will be added

**Acceptance:** first message to a new recipient carries the footer, the second does not; `off`
never adds it.

### E23-T8 — Packaging, DNS, deploy & operations

- [ ] Dockerfile + `compose.example.yml` (as `apps/oauth`), publishing :25 on the host; container
      memory/CPU/pids limits
- [ ] Ansible: `email.env.j2`, secrets in `secrets.yml` (provider credentials, DKIM key, bridge
      keys), `EMAIL_DOMAINS` defaulting to `hosted_domains`; Caddy blocks for `email.poweur.org`
      and `mta-sts.poweur.net`; STARTTLS certificate for `mx.poweur.org` (shared from Caddy's
      storage or obtained by the bridge — decide here)
- [ ] `poweur-email check-dns` validating every record in the DNS table (MX, SPF, DKIM, DMARC,
      MTA-STS, TLS-RPT, PTR/FCrDNS) for each served domain
- [ ] Choose the public outbound provider (AUP allows free-sign-up person-to-person mail; custom
      MAIL FROM; bounce/complaint webhooks; EU region preferred) and document the choice
- [ ] `deploy/OPS.md`: rollout order (reserve names → DNS at `p=none` + MTA-STS `testing` → enable
      MX → watch DMARC/TLS-RPT → tighten), backups (`EMAIL_DATA`: spool, suppression list, footer
      ledger), privacy and abuse-contact pages, Grafana dashboard
- [ ] Better Stack / blackbox probes: SMTP banner + STARTTLS on :25, `/health`

**Acceptance:** live at `mx.poweur.org` / `email.poweur.org`; mail-tester.com (or equivalent) 10/10
for an outbound message; inbound from Gmail and Outlook lands in the tray.

### E23-T9 — Growth loops: invites, conversation upgrade, aliases, screener

- [ ] Invite by email from Contacts (one invite per address, rate-limited, uses the footer copy)
- [ ] Email-address proof for a Poweur ID (signed claim + verification mail) so a correspondent who
      joins can switch the thread to native E2E messages; compose prefers native routing when the
      recipient address maps to a Poweur ID
- [ ] Masked aliases: create/revoke random aliases routed to the same ID, labelled per use
- [ ] Screener: first-time senders not in contacts land in "New senders" until accepted (reuse the
      EPIC-007 request pattern); accepted senders become contacts with an email address
- [ ] EPIC-010 hook: `email.message` as an automation trigger (file attachments, forward rules)

### E23-T10 — Later

- [ ] Custom domains: `alice@example.com` for `alice.example.com` or any ID, after a DNS TXT proof
      and MX pointing at the bridge (or the owner's own bridge)
- [ ] Forwarding in: accept Gmail/Outlook forwarding confirmations so users can bring an existing
      address while they migrate
- [ ] Local IMAP/SMTP bridge (`poweur email serve`, Proton Bridge pattern): decrypts on the user's
      device and serves standard mail clients without the server ever holding keys
- [ ] Image proxy and BIMI

## Non-goals

- Server-side IMAP/POP/SMTP-submission access: it would require the bridge to hold users' keys or
  plaintext mailboxes. The local bridge in E23-T10 is the answer instead.
- End-to-end encrypted email to non-Poweur recipients (PGP/S/MIME). The upgrade path is to bring
  the correspondent into Poweur messaging.
- Bulk / marketing mail. Quotas and AUP are set for person-to-person mail.
- A web UI for the bridge. Operators configure it with environment variables and Ansible.
