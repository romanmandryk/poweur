# `@poweur/openclaw-channel` — Poweur ID for OpenClaw

Message your [OpenClaw](https://github.com/openclaw/openclaw) agent from the Poweur app, the
PWA or the CLI. The agent runs on **its own Poweur ID**, never yours: messages are end-to-end
encrypted, and every sender is a verified Poweur ID, so the allowlist and pairing work on real
identities instead of display names.

Status: beta. Messages and shared folders work end to end.

## Set it up

```bash
openclaw plugins install npm:@poweur/openclaw-channel
openclaw channels add --channel poweur --identity yourname-claw.poweur.net --allow-from yourname.poweur.net
```

`channels add` creates the agent's own ID (unless this machine already has its keys) and prints
its recovery phrase once, allows only the given IDs, and labels the agent's profile with who runs
it. Then start the gateway and message the agent from the Poweur app. Full guide:
[poweur.org/docs/agents/openclaw](https://www.poweur.org/docs/agents/openclaw).

Already have the agent's ID (made elsewhere, or before a reinstall)? Add
`--recovery-phrase-file <path>` (or set `POWEUR_RECOVERY_PHRASE`) to restore it instead of
creating one. If a name is taken, `channels add` changes nothing and prints the commands to run
next.

The plugin carries the Poweur library; it installs no command-line tool. On the same machine use
`npx @poweur/client` (the Poweur CLI, same `~/.poweur` folder) for anything else, and see
[Move an ID to another machine](https://www.poweur.org/docs/clients/move-identity).

From a checkout: `pnpm --filter @poweur/openclaw-channel build && pnpm --filter @poweur/openclaw-channel pack`,
then `openclaw plugins install npm-pack:<the .tgz>`.

## Shared folders

Share a folder with the agent's ID (role *Can edit*) and it appears in the agent's workspace at
`shared/<your id>/<folder>/`. What the agent writes there is synced back into your folder; your
changes come down before each turn. Conflicts keep both versions; the agent never deletes your
files. See the [guide](https://www.poweur.org/docs/agents/openclaw#give-it-a-folder-to-work-in).

## Test it

```bash
pnpm --filter @poweur/openclaw-channel test                  # unit tests
node apps/integration/agents/openclaw-e2e.mjs                # real gateway, local relay, fake model
node apps/integration/agents/openclaw-e2e.mjs --live         # the same with a real model (OpenRouter)
```

`--live` reads `OPENROUTER_API_KEY` and `OPENROUTER_MODEL` from
`apps/integration/agents/.env.local` (git-ignored). Both runs check that a stranger who is not
on the allowlist gets no reply.

## How it works

`src/bus.ts` listens on the agent's relay event stream, picks up its inbox, hands each chat
message to OpenClaw and consumes it only afterwards, so a crash means a re-pickup, not a lost
message. `src/folders.ts` keeps shared folders in sync (`planSync` decides, conservatively). `src/channel.ts` plugs that into OpenClaw's shared DM security, pairing, inbound
dispatch and outbound sending, shaped like OpenClaw's own Nostr channel.

Licence: Apache-2.0.
