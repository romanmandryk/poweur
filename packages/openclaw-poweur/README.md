# `@poweur/openclaw-channel` — Poweur ID for OpenClaw

Message your [OpenClaw](https://github.com/openclaw/openclaw) agent from the Poweur app, the
PWA or the CLI. The agent runs on **its own Poweur ID**, never yours: messages are end-to-end
encrypted, and every sender is a verified Poweur ID, so the allowlist and pairing work on real
identities instead of display names.

Status: early (INT-005-T2). Direct messages work end to end; shared folders are next.

## Set it up

1. **Give the agent an ID** on the machine that runs the gateway. Its keys stay in that
   machine's Poweur home (`POWEUR_HOME`, default `~/.poweur`):

   ```bash
   npx @poweur/client identity create myagent.poweur.net --hosted --relay https://poweur.net
   ```

2. **Install the plugin** (from a checkout, until it is published):

   ```bash
   openclaw plugins install --link /path/to/poweur/packages/openclaw-poweur
   ```

3. **Configure the channel** in `openclaw.json`:

   ```json5
   {
     channels: {
       poweur: {
         identity: "myagent.poweur.net",
         // pairing (default): strangers get a code you approve; allowlist: only these IDs
         dmPolicy: "allowlist",
         allowFrom: ["you.poweur.net"],
       },
     },
   }
   ```

   `relayUrl` is optional; it defaults to the relay in the Poweur home's `config.toml`.

4. Start the gateway and send `myagent.poweur.net` a message from the Poweur app.

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
