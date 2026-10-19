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

The keys live in the Poweur folder on that machine (`POWEUR_HOME`, default `~/.poweur`), never in
OpenClaw's config. There is no passkey on a server: whoever can read that folder controls the
agent's ID, so keep it readable only by the user that runs OpenClaw.

## If the name is taken, or the ID already exists

`channels add` stops without changing anything and prints what to run next:

- **The name belongs to someone else:** run it again with another name.
- **The ID is yours** (made in the Poweur app, on another machine, or before a reinstall): restore
  its keys here from its recovery phrase. Put the 24 words in a file only you can read, then

  ```bash
  openclaw channels add --channel poweur --identity yourname-claw.poweur.net --allow-from yourname.poweur.net --recovery-phrase-file ~/agent-phrase.txt
  rm ~/agent-phrase.txt
  ```

  The phrase is checked against the keys the ID publishes; a phrase for another ID is refused.
  `POWEUR_RECOVERY_PHRASE` works instead of the file.

## The agent's keys and the Poweur CLI

The plugin carries the Poweur library inside it; it does not install a command-line tool. For
anything beyond setup, use the Poweur CLI on the same machine. OpenClaw needs Node, so the Node
CLI runs there with nothing to install (the [Go binary](/clients/cli-reference#installation) works
too, with the same commands and the same folder):

```bash
npx @poweur/client identity show                 # which ID, which relay, where the keys are
npx @poweur/client identity lookup yourname.poweur.net
npx @poweur/client key recover yourname-claw.poweur.net --seed-file ~/agent-phrase.txt
```

Point it at the same Poweur folder as the gateway (`POWEUR_HOME`, if you set one there).
**Do not run `inbox` or `listen` as the agent while the gateway runs:** they pick messages up,
and the agent would never see them.

- **Back up** the recovery phrase (shown once at setup; delete the copy in `~/.poweur/recovery/`
  once it is stored safely) or the whole `~/.poweur` folder.
- **Move the agent** to another machine: [Move an ID to another machine](/clients/move-identity).
- **Machine compromised:** `npx @poweur/client key rotate --use-identity yourname-claw.poweur.net`
  on a trusted machine moves the agent onto new keys.

## Give it a folder to work in

Share a folder with the agent's ID, from the Poweur app (Files → share → enter the agent's ID,
role *Can edit*) or the CLI:

```bash
poweur drive share add /Project yourname-claw.poweur.net --role write
```

The agent accepts it and replies *Got your folder "Project"*. The folder appears in its workspace
at `shared/yourname.poweur.net/Project/`, and with every message the agent is told it is there.
Ask it to work: *"read brief.md and write a plan to plan.md"*.

- **What it writes is yours.** New and changed files go back into your folder, in your drive,
  end-to-end encrypted like everything else there. You see them in the app right away.
- **Your edits reach it.** Files you add or change come down before its next turn, and at least
  once a minute.
- **Nothing is lost.** If you both change the same file, your version wins in the folder and
  the agent's is saved beside it as `name (agent).ext`.
- **It never deletes your files.** If the agent deletes a file locally, it comes back on the next
  sync. Files you delete are removed from its copy.
- **Read-only shares** (role *Can view*) are read but never written to.
- **Stop sharing** at any time: the agent stops syncing and keeps only its local copy.

Only people who may message the agent (the allowlist, or approved pairings) can give it folders.

## What to expect

- **One message per reply.** Replies are sent whole (up to 8,000 characters per message), so a
  normal turn costs one message.
- **Limits.** Like every ID on poweur.net, the agent can send 20 messages a minute, 200 an hour
  and 1,000 a day. When it hits a limit, the reply waits and is retried for up to 90 seconds.
- **Threads and subjects** carry over: the agent replies in the thread you wrote in.
- **Not yet:** message attachments, and several agent IDs on one gateway.

## Under the hood

The plugin is open source (Apache-2.0) in
[`packages/openclaw-poweur`](https://github.com/romanmandryk/poweur/tree/master/packages/openclaw-poweur).
It listens on the agent's relay stream, hands each message to OpenClaw's own DM security,
pairing and dispatch, and takes the message off the relay only after OpenClaw accepted it, so a
restart picks it up again instead of losing it. Every change is tested against a real OpenClaw
gateway and a real relay.
