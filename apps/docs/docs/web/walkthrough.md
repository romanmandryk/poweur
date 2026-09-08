---
id: walkthrough
sidebar_position: 1
title: Web app walkthrough
---

# Web app walkthrough

The web client (`apps/web`, served at `/app/`) is the whole product without a
terminal: identity, messages, contacts, files, sharing and inbox policy. This page
follows a new user through it and says which document or endpoint each screen writes,
so a behaviour you see here can be traced to the spec that defines it.

It is vanilla JS with no build step. The protocol lives in
[`@poweur/client`](../clients/js-sdk.md), vendored into the served tree; the app owns
key custody (passkey/PIN) and the screens.

## Five destinations

| Destination | What it is |
|-------------|------------|
| **Messages** | conversations, plus **Requests** and **Anonymous** as separate trays |
| **Contacts** | your `contacts.json`, with states, petnames and search |
| **Files** | your tree, sharing, and other people's shares |
| **Apps** | the launcher (forward-looking) |
| **Settings** | identity, profile, inbox policy, keys and devices, relay |

Everything is laid out for a 375px screen first, because the Capacitor shell wraps this
same UI.

## First run

Creating an identity (Apps → Add new ID, or the welcome screen) registers it, wraps the
keys under a passkey or PIN, and enrolls this browser in the relay keystore so clearing
site data is not fatal — see [key management](../security/key-management.md). Starting
from nothing on a hosted relay, [claiming an ID](claim-your-id.md) is the flow that gets
you here.

Then a three-step setup runs. Every step is skippable, and skipping writes nothing:

1. **Who can message you** — the inbox policy. It opens on `contacts_and_requests`,
   the [recommended human default](../trust/contacts.md), and can also turn on
   anonymous messages.
2. **How people see you** — display name, bio, avatar, one link →
   `poweur-sys/public/profile.json`.
3. **You're set.**

The policy step exists because its default (`open`, anyone may message you) is the one
setting nobody knowingly chooses and nobody goes looking for until the first unwanted
message arrives.

## Contacts and requests

- **Add** takes a Poweur ID, resolves it before anything is sent — a typo fails at the
  input, not silently later — and sends `sys.contact.request`. The key that resolves at
  that moment is pinned.
- **Messages → Requests** merges two sources: the relay's requests queue (used under
  `contacts_and_requests`) and contact requests that arrived in the inbox as typed
  messages (what happens under `open`). Accept pins their key and sends
  `sys.contact.accept`; Block writes `blocked` and sends nothing.
- When someone accepts a request **you** sent, reading your requests queue promotes them
  to `accepted` on your side too — without that, your own policy would keep refusing
  their first message.
- A message from someone you hold no entry for carries a one-tap **Add**.

Every write goes to `poweur-sys/relay/contacts.json` over DAV, so contacts sync to your
other devices and to the CLI.

## When a key changes

Sending to a pinned contact whose key no longer matches raises a blocking dialog with
both keys and refuses to send until you press **Trust new key** — the web twin of the
CLI's `--accept-new-key`. A change covered by a signed rotation statement re-pins
silently and says so. See [key pinning](../trust/contacts.md#key-pinning-the-safety-number-model).

## Inbox policy, anonymous and proof-of-work

**Settings → Who can message you** edits the mode and the anonymous block as one
document (`poweur-sys/relay/inbox-policy.json`). Anonymous messages are off until you
turn them on; the difficulty slider is labelled with what the challenge costs the
sender's *browser*, and warns past 20 bits where a phone stops feeling like it is
working. `verified` and `payment` appear disabled: designed policy slots relays answer
but do not enforce.

Accepted anonymous messages land in **Messages → Anonymous** and nowhere else, rendered
without a sender and with no reply affordance — there is nobody to reply to. Compose can
also *send* anonymously; the proof-of-work is solved in the page, with progress.

## Files and sharing

Your tree, with the layout roots and their audiences
([storage model](../files/storage-model.md)). Uploads switch to the resumable chunked
endpoint above 64 MB. The folder in view refreshes itself from the changes feed.

**🔗 on a folder or file** under `/shared` or `/apps` opens the share dialog: audience
(contacts or a typed identity), read or read-write, optional expiry. The grant is signed
in the browser and stored in your own tree; **🔗 at the root** lists every grant with a
Revoke button. **Shared with me** opens someone else's tree — you name the owner,
because grants live in *their* `poweur-sys` and there is no "shared with me" listing
until EPIC-005's offer flow. You will see only what they granted you.

## Keys, devices and recovery

**Settings → Keys & devices** lists every enrolled authenticator, marks this one, and
can remove one (revoking its sessions). **Recovery kit** renders 24 words derived from
the identity seed and checks them back. A device that holds no key can join an existing
identity through the six-digit comparison ceremony — see
[key management](../security/key-management.md).

## What is not here yet

- Unread badge counts on the message trays.
- Group editing (the audience picker consumes owner-local groups; the editor is
  EPIC-005 T5).
- Rotating a pre-EPIC-011 identity onto a recovery seed, and nominating a recovery
  master, both owned by EPIC-011.
- Nothing here is blocked by the Host-routed well-known path. A profile card fetches
  `https://<identity>/.well-known/poweur/profile.json`, which is that identity's own
  hostname, so the browser sets `Host` for it and the relay serves the right tree
  (`Access-Control-Allow-Origin: *`). Only a local dev relay is different: many
  identities behind one IP that DNS does not know, so cards there fall back to the
  identity document.
