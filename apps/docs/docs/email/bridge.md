---
id: bridge
sidebar_position: 1
title: Email bridge
---

# Email bridge

> **Status: implemented in `apps/email` (EPIC-023).** Inbound delivery, sending, delivery
> reports, attachments, the web Email tray and `poweur email` are built and tested against real
> relays (`apps/integration`, `TestINT_EMAIL_*`) and in the browser (`email.spec.js`). Protocol:
> [PCP-0010](https://github.com/romanmandryk/poweur/blob/master/conventions/pcp-0010-email-bridge.md).
> Operator guide: [`apps/email/README.md`](https://github.com/romanmandryk/poweur/blob/master/apps/email/README.md).

The email bridge gives a hosted Poweur ID an ordinary email address: `john.poweur.net` gets
`john@poweur.net`. Mail sent to it arrives in John's Poweur inbox, **encrypted to John**, and John
writes email from the Poweur app that arrives anywhere as normal, authenticated mail from
`john@poweur.net`. It is **on by default** for every hosted ID — sign-in links and notifications
just work — and one switch in Settings (or `poweur email disable`) turns it off.

## Two ways to run it

**Inbound** — the bridge can be the MX itself (only an MX record needed; it checks SPF, DKIM
and DMARC on its own), or sit behind a mail server such as Stalwart, which asks it at
`RCPT TO` and relays accepted mail over LMTP. **Outbound** is separate and optional: off
(receive only — sign-in links and notifications are what most people need), or submission to
any SMTP server or provider relay. See the operator guide for the settings.

## How it fits together (poweur.net)

```
the email network ──SMTP──► mail server (Stalwart) ──LMTP──► bridge ──encrypted email.message──► john's relay ──► app
                                    ▲  RCPT hook (HTTPS) ──────────┘
app ──encrypted email.send──► bridge's inbox ──► bridge ──SMTP submission──► mail server ──► the email network
                                                   └──encrypted email.status──► app
```

- **The mail server** does what mail servers do: MX, TLS, SPF/DKIM/DMARC checks, spam scoring,
  queueing, DKIM signing, relaying outbound mail. The bridge never listens on port 25.
- **The bridge is a Poweur identity** (`email.poweur.net`). It delivers each email to you as an
  end-to-end encrypted `email.message` and sends what you ask it to with an `email.send`. It
  holds no relay credential, and your relay stores only ciphertext, as for any message.
- **There is no user database.** At `RCPT TO` the bridge resolves `john.poweur.net` and asks its
  relay whether it takes mail (or reads its signed `email.json`); no such ID, or opted out —
  refused before the mail server accepts anything, so nobody is ever sent a bounce for mail that
  never existed. New sign-ups need no setup anywhere.

## On by default, and how to turn it off

Your relay names its operator's bridge, and with nothing in your inbox policy about email, mail
through that bridge is on. Your relay admits `email.*` messages from that bridge, and only from
it, even when your inbox only takes contacts; its public `capabilities.json` tells the bridge
`email_receive: on`.

- **Turning email off** writes `{"email": {"allow": false}}` into your inbox policy. The relay
  starts reporting `off` and refusing the bridge's messages; mail to your address is refused
  within a few minutes.
- **Choosing another bridge** (or turning email back on) publishes a signed
  **`/.well-known/poweur/email.json`** — your identity, your address and the bridge — and names
  that bridge in your policy.

## Who may email you

Settings → Email (or `poweur email filters`): block an address or a whole `@domain` — every email
has a **Block** link too — or take mail **only from people you know**: people you have emailed,
replies to your own mail, delivery reports and the addresses or domains you allow. The rules go to
the bridge privately, in an encrypted `email.settings` message, and the bridge stores only hashes
of them. Filtered mail is accepted and dropped without a word to the sender — no bounce. Blocking a
Poweur ID in Contacts works as before and is separate.

Your app shows a message as email only when it is an `email.*` message **from your bridge** (the
one your policy names, or your relay's by default). Anyone else sending a message that claims to be email gets shown as an untrusted
app message: nobody can forge "an email from your bank" over Poweur.

## Addresses

- `john@poweur.net` → `john.poweur.net`. The part before `@` follows identity name rules; dots
  are not ignored (`jo.hn@` is a different, non-existent name).
- `john+anything@poweur.net` also reaches John; the tag is kept so mail can be filtered by it.
- An operator can serve more domains: with `poweur.org=poweur.net`, `john@poweur.org` reaches
  `john.poweur.net` too. Mail is always sent from your own address (`john@poweur.net`).
- Names that mail infrastructure needs — `postmaster`, `abuse`, `mta-sts`, `autoconfig`, … — are
  reserved and cannot be claimed as identities.

## What arrives

An `email.message` carries the sender and recipients, subject, date, message ids for threading,
the text and the HTML, attachments, and what the mail server concluded about authenticity:

| Shown as | Meaning |
|---|---|
| Verified sender | DMARC passed: the From domain is backed by SPF or DKIM |
| Unverified sender | No DMARC verdict — the From line could be anything |
| Failed authentication | DMARC failed: the message is very likely not from who it says |
| Spam | The mail server's spam filter flagged it |

Small attachments travel inside the encrypted message. Larger ones — and the original message,
for "Show original" — are stored by the bridge **sealed to your key** at an unguessable URL for
30 days (the operator's `EMAIL_BLOB_TTL`); the bridge cannot read them after writing them.

## Reading HTML email safely

HTML email is hostile by default. It is cleaned twice and then isolated:

1. the bridge sanitizes it (an allowlist: no scripts, frames, forms, event handlers, `<base>`,
   `<meta>`, `<style>` blocks or `url()` in styles);
2. the app sanitizes it again — it does not trust the bridge with your screen;
3. it renders in a sandboxed frame **without scripts**, under a content security policy that
   blocks every network request. Remote images stay blocked (they tell the sender when you read
   the mail, and from where) until you tap **Load images**; links open in a new tab.

## Sending

Sending is optional and **paced**: by default ten emails a day, each after the first waiting a
little longer (5 minutes, then 10, 20, 40… up to two hours). An email sent too early is not lost:
it waits and goes out on time, and the app says when. Operators raise the limits for people who
need more, as they would raise a storage quota.

The app (or `poweur email send`) sends the bridge an encrypted `email.send`: recipients,
subject, text, optional HTML, attachments, and the message ids of what you reply to. The bridge
checks your opt-in and your limits, builds the email, and submits it to the mail server; an
`email.status` comes back — `sent`, `deferred` while it waits for its turn or the mail server
is unreachable (with the time it will go), `bounced`
when a recipient's server returns a delivery report, or `refused` with the reason (daily limit,
too many new correspondents, an address that bounced before).

The first email you send to someone ends with a one-line note: *Sent with Poweur ID
john.poweur.net · Get your own free ID*. Later emails to them don't.

## Privacy

- **Email is not end-to-end encrypted on the way.** The sender's mail servers, the mail server in
  front of the bridge and the bridge see a message while it passes. From the bridge onwards it
  is encrypted to you, and it is stored only encrypted.
- The bridge keeps no plaintext after delivery. Its database holds counters, send states and
  **keyed hashes** of addresses (to answer "have these two written before?" and "has this
  address bounced?") — not an address book.
- Its logs carry no addresses or subjects.

## Subjects in Poweur messages

Ordinary Poweur messages can carry a subject too ([Message format →
Subjects](../protocol/message-format.md#subjects)); the Inbox can group by ID or by subject.
