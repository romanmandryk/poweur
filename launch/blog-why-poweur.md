---
title: "Why I'm building Poweur: one open ID for an internet you don't have to rent"
description: "Identity, messaging and data sharing are the three things almost everything online is built on. Right now we rent all three. Poweur is an attempt to make them open."
author: Roman Mandryk
date: 2026-10-13
draft: true
---

<!--
Draft for the launch post. Anything marked [YOU: …] is yours to write; the rest is a starting
point to cut or rewrite in your own voice. Aim for ~1,200 words when done. Title
alternatives are at the bottom.
-->

# Why I'm building Poweur

[YOU: an opening moment, 3–5 sentences. The more specific the better. Examples of the kind
of moment that works: an account locked with years of photos or messages in it; handing out a
phone number to a stranger and regretting it; seeing a team keep the same files in four
places because four apps couldn't talk; an AI agent that needed access to "just one folder"
and the only option was the whole Google account. Whatever actually made you start.]

That was when it clicked for me: most of what we do online is the same three things, over
and over.

## Three things, rented twelve times

Almost every app you use is some mix of:

1. **Proving who you are.** Logging in, signing something, showing that this account is
   really you.
2. **Talking to someone.** A message, a comment, a notification, an invite.
3. **Working on shared data.** A file, a folder, a document, a list, a game board.

We never got open, everyday versions of these three. So every company rebuilt them inside
its own walls, and we rent a separate copy from each one: an email address for being
reachable, a phone number for being verified, a Google or Apple account for logging in, a
Drive for files, WhatsApp for friends, Slack for work. Each copy has its own contact list, its
own permissions and its own terms, and each can be switched off.

Email came close. It is from 1971, it is open, and anyone can run it. But it can't prove who
sent a message, it doesn't encrypt by default, it can't share a folder, and software can't
really understand it. Phone numbers belong to your carrier and get recycled to strangers.
Your big-tech account belongs to them. The one thing that follows you everywhere online, your
identity, is the thing you own least.

## What Poweur is

Poweur is an open, cryptographic ID that does all three.

- **An ID you own.** A human-readable name like `alice.poweur.net`, or a name on your own
  domain. It's backed by keys that are made on your device and never leave it: the server
  hosting your name never sees them. Anyone can check that a message or a signature really
  came from you.
- **Messaging.** Signed, end-to-end encrypted messages between any two IDs, on any server:
  plain chat for people, typed messages that apps and agents can understand. Strangers go
  through a contact request, so your inbox is yours by default.
- **Data sharing.** Every ID has a home: a drive you can sync. Share a folder with another ID,
  a group or an agent, read-only or read-write, and take the access back in one click. It
  works across servers, the way email does.

And because those three fit together, a fourth thing falls out: you can **sign in** to other
apps with your ID. There's a standard OAuth / OpenID Connect bridge, so any app with a "Sign in
with…" button can accept Poweur without changing its code.

It's built on standards that have held up for decades: DNS and HTTPS for finding people,
Ed25519 and X25519 for keys, passkeys to unlock them, WebDAV for files, OAuth and OpenID
Connect for sign-in. The new part is how they're stitched together, not a new silo.

## What I believe about this

A few principles decide most of the design:

- **Your identity isn't a billing account.** No plan, suspension or payment problem can
  invalidate your ID or lock your exported data into a proprietary format.
- **Hosting is a choice.** Start on poweur.net, move your name to your own domain, or run the
  whole thing yourself: it's one Go binary or container. Same protocol everywhere, and
  servers talk to each other.
- **Keys stay on your devices.** Servers carry messages and store files; they don't hold
  your identity.
- **Some things are never paid.** Your ID, recovery, end-to-end messaging, sign-in and
  exporting your data stay free. If poweur.net charges for anything, it will be for what
  actually costs money: lots of storage, heavy public downloads, bridges to other systems,
  and guarantees.
- **Agents are first-class, and accountable.** An AI agent gets its own ID. You can message
  it, give it one folder, see what it did, and fire it. The more software acts for us, the
  more it needs a name, a scope and an off switch.

[YOU: a paragraph on why *you* care: sovereignty, privacy, an engineer's frustration with
rebuilding the same thing, Europe, your kids, whatever is true. This is the paragraph people
will quote.]

## Where this leads

Today Poweur does IDs, E2E messaging, files and sharing, sign-in for other apps, a web app, a
CLI and a TypeScript SDK. Here's where I want it to go, roughly in this order:

1. **Send.** Transfer a file to anyone, with a verified sender, a receipt and an expiry date.
   Think WeTransfer, but you know who it came from.
2. **Spaces.** A group, its files, its discussion and its activity as one object: a family,
   a club, a small team, a trip.
3. **An email bridge.** Opt in to `you@poweur.net` so the old world can reach you, with that
   mail landing in its own filtered tray inside your ID.
4. **Real-time collaboration.** Documents that several people (and agents) edit together,
   across servers, that still work offline and export to plain files.
5. **Apps without servers.** If every user already has an identity, an inbox and a drive,
   an app doesn't need its own backend. It signs you in, asks for one folder in your home,
   and keeps its data there. Uninstalling revokes the grant; your data stays with you. Two
   apps from different vendors can share one file.
6. **Proofs and payments.** Sign documents with your ID, prove you own something, send a
   payment request and a proof of payment as typed messages.

The long-term picture is an internet where your name, your contacts and your data are the
constant, and apps, servers and companies are interchangeable around them. Not one more
platform, but the layer platforms plug into.

## What's honest to say today

- It's **pre-1.0**. The protocol and main flows are covered by a lot of tests, including
  end-to-end tests against real servers and a Go and TypeScript implementation checked against
  each other, but formats can still change.
- It **hasn't had an external security audit** yet. The design and code are public; please
  read them and tell me what's wrong.
- Creating an ID in the browser needs a **passkey that supports PRF**. Current Safari and
  Chrome do; [YOU: list what's confirmed after the device testing]. The mobile apps are
  in beta.
- Right now, it's [YOU: "me" / "a small team"]. That shapes what gets built first, and it's
  why your feedback matters so much.

## Try it

- **Claim a name** at [poweur.net/app](https://poweur.net/app/). It takes about a minute.
- **Message** `hello.poweur.net` and it'll answer. Then message me: `[YOU: your ID]`.
- **Run your own relay:** [the self-hosting guide](https://poweur.org/docs/relay/self-hosting).
- **Build on it:** [`@poweur/client`](https://poweur.org/docs/clients/js-sdk) for the browser,
  Node, Bun and Deno.
- **Read the code, star it, open an issue:**
  [github.com/romanmandryk/poweur](https://github.com/romanmandryk/poweur).

If you've ever lost an account, handed out a phone number you wish you hadn't, or wired
five services together to share one folder, I'd love to know whether this solves it for
you, and what's missing.

[YOU: sign-off]

---

<!--
Title alternatives:
- "Email is from 1971. It's time identity had an open standard too"
- "We rent our identity from a dozen companies. Here's an open alternative"
- "One open ID to sign in, message and share"
- "Your name, your inbox, your files: why I'm building Poweur"

For Show HN, use a plain title ("Show HN: Poweur – open ID for sign-in, E2E messaging and
file sharing") and paste a 150-word version of "What Poweur is" + "What's honest to say
today" as the first comment, with a link to this post.
-->
