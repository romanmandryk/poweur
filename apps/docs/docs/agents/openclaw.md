---
id: openclaw
title: OpenClaw — talk to your agent from Poweur
---

# Talk to your OpenClaw agent from Poweur

[OpenClaw](https://github.com/openclaw/openclaw) runs a personal AI agent behind your chat
apps. The Poweur channel adds one more: you message the agent from the Poweur app, the web app on
your phone, or the `poweur` CLI, end to end encrypted.

**The agent gets its own Poweur ID.** It is never you: it has its own keys, kept on the machine
that runs OpenClaw, and its public profile says who runs it. Because every message is signed by a
Poweur ID, the allowlist holds real identities, not display names that anyone can copy.

## Set it up (two commands)

On the machine that runs OpenClaw:

```bash
openclaw plugins install npm:@poweur/openclaw-channel
```

```bash
openclaw channels add --channel poweur --identity yourname-claw.poweur.net --allow-from yourname.poweur.net
```

The second command:

1. creates the agent's ID, `yourname-claw.poweur.net` (pick any free name, 6–24 characters),
   unless this machine already has its keys, and prints its **recovery phrase once**: store it
   like a password;
2. points the channel at it and allows only `yourname.poweur.net` to message the agent;
3. labels the agent's public profile *"AI agent run by yourname.poweur.net, on OpenClaw."*

Restart the gateway (`openclaw gateway restart`, or start it), then send the agent a message
from the Poweur app.

| Option | Meaning |
|---|---|
| `--identity <id>` | The agent's own Poweur ID. Created on poweur.net if this machine has no keys for it. For an ID on your own domain, create it first with `poweur identity create` and pass it here. |
| `--allow-from <ids>` | Who may message the agent, comma-separated. Without it the agent is in *pairing* mode: a stranger gets a code that you approve with `openclaw pairing approve poweur <code>`. |
| `--relay-url <url>` | The ID's relay, default `https://poweur.net`. |
| `--invite-code <code>` | For relays that require an invite to register. |

The keys live in the Poweur home on that machine (`POWEUR_HOME`, default `~/.poweur`), never in
OpenClaw's config. Back up that folder or keep the recovery phrase.

## What to expect

- **One message per reply.** Replies are sent whole (up to 8,000 characters per message), so a
  normal turn costs one message.
- **Limits.** Like every ID on poweur.net, the agent can send 20 messages a minute, 200 an hour
  and 1,000 a day. When it hits a limit, the reply waits and is retried for up to 90 seconds.
- **Threads and subjects** carry over: the agent replies in the thread you wrote in.
- **Not yet:** attachments, and several agent IDs on one gateway.

## Under the hood

The plugin is open source (Apache-2.0) in
[`packages/openclaw-poweur`](https://github.com/romanmandryk/poweur/tree/master/packages/openclaw-poweur).
It listens on the agent's relay stream, hands each message to OpenClaw's own DM security,
pairing and dispatch, and takes the message off the relay only after OpenClaw accepted it, so a
restart picks it up again instead of losing it. Every change is tested against a real OpenClaw
gateway and a real relay.
