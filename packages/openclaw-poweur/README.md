# `@poweur/openclaw-channel` — Poweur ID for OpenClaw

Message your [OpenClaw](https://github.com/openclaw/openclaw) agent from the Poweur app, the
PWA or the CLI. The agent runs on **its own Poweur ID**, never yours: messages are end-to-end
encrypted, and every sender is a verified Poweur ID, so the allowlist and pairing work on real
identities instead of display names.

Status: beta. Direct messages work end to end; shared folders are next.

## Set it up

```bash
openclaw plugins install npm:@poweur/openclaw-channel
openclaw channels add --channel poweur --identity yourname-claw.poweur.net --allow-from yourname.poweur.net
```

`channels add` creates the agent's own ID (unless this machine already has its keys) and prints
its recovery phrase once, allows only the given IDs, and labels the agent's profile with who runs
it. Then start the gateway and message the agent from the Poweur app. Full guide:
[poweur.org/docs/agents/openclaw](https://www.poweur.org/docs/agents/openclaw).

From a checkout: `pnpm --filter @poweur/openclaw-channel build && pnpm --filter @poweur/openclaw-channel pack`,
then `openclaw plugins install npm-pack:<the .tgz>`.

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
message. `src/channel.ts` plugs that into OpenClaw's shared DM security, pairing, inbound
dispatch and outbound sending, shaped like OpenClaw's own Nostr channel.

Licence: Apache-2.0.
