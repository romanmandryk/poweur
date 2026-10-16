/**
 * `openclaw channels add --channel poweur`: one command from nothing to an
 * agent people can message.
 *
 *   openclaw channels add --channel poweur \
 *     --identity romanman-claw.poweur.net --allow-from romanman.poweur.net
 *
 * creates the agent's own Poweur ID when this machine has no keys for it
 * (showing its recovery phrase once), points the channel at it, allows only
 * the given IDs, and labels the agent's public profile with who runs it.
 */
import { openClient } from "@poweur/client/node";
import { defineChannelSetupContract, type ChannelSetupAdapter } from "openclaw/plugin-sdk/channel-setup";
import type { OpenClawConfig } from "openclaw/plugin-sdk/config-contracts";
import { patchTopLevelChannelConfigSection, splitSetupEntries } from "openclaw/plugin-sdk/setup";
import { getPoweurConfig, looksLikePoweurId, normalizePoweurId } from "./config.js";
import { DEFAULT_RELAY_URL, ensureAgentIdentity } from "./identity.js";

export type PoweurSetupInput = {
  name?: string;
  identity?: string;
  relayUrl?: string;
  allowFrom?: string;
  inviteCode?: string;
};

const CHANNEL = "poweur" as const;

export function parseAllowFrom(raw: string | undefined): { ids: string[]; error?: string } {
  const ids: string[] = [];
  for (const entry of splitSetupEntries(raw ?? "")) {
    if (!looksLikePoweurId(entry)) return { ids: [], error: `Not a Poweur ID: ${entry} (expected e.g. you.poweur.net)` };
    const id = normalizePoweurId(entry);
    if (!ids.includes(id)) ids.push(id);
  }
  return { ids };
}

/** The agent's public profile says it is an agent and who runs it. */
export function agentProfile(identity: string, operator: string | undefined) {
  const label = identity.split(".")[0] ?? identity;
  return {
    version: 1,
    display_name: `${label} (agent)`,
    bio: operator ? `AI agent run by ${operator}, on OpenClaw.` : "AI agent on OpenClaw.",
  };
}

export function createPoweurSetupAdapter(): ChannelSetupAdapter<PoweurSetupInput> {
  return {
    resolveAccountId: () => "default",
    validateInput: ({ input }) => {
      const identity = input.identity?.trim();
      if (!identity) return "Poweur needs --identity: the agent's own ID, e.g. yourname-claw.poweur.net";
      if (!looksLikePoweurId(identity)) return `Not a Poweur ID: ${identity}`;
      if (input.relayUrl?.trim() && !/^https?:\/\//.test(input.relayUrl.trim())) return "--relay-url must be an http(s) URL";
      const allow = parseAllowFrom(input.allowFrom);
      if (allow.error) return allow.error;
      if (allow.ids.includes(normalizePoweurId(identity))) return "--allow-from should be your ID, not the agent's";
      return null;
    },
    // The one async step: make sure the ID exists before the config names it.
    prepareAccountConfigInput: async ({ input, runtime }) => {
      const identity = normalizePoweurId(input.identity ?? "");
      const relayUrl = input.relayUrl?.trim() || DEFAULT_RELAY_URL;
      const ensured = await ensureAgentIdentity({ identity, relayUrl, ...(input.inviteCode ? { inviteCode: input.inviteCode } : {}) });
      if (ensured.created) {
        runtime.log(
          `Created the agent's Poweur ID ${identity}.\n` +
            `Its recovery phrase (shown once; also saved to ${ensured.recoveryFile}, delete that file once stored safely):\n\n  ${ensured.mnemonic}\n`,
        );
      } else {
        runtime.log(`Using the existing keys for ${identity}.`);
      }
      return { ...input, identity, relayUrl };
    },
    applyAccountConfig: ({ cfg, input }) => {
      const existing = getPoweurConfig(cfg);
      const allow = parseAllowFrom(input.allowFrom).ids;
      const merged = [...new Set([...(existing?.allowFrom ?? []).map(String), ...allow])];
      return patchTopLevelChannelConfigSection({
        cfg,
        channel: CHANNEL,
        enabled: true,
        patch: {
          identity: normalizePoweurId(input.identity ?? ""),
          ...(input.relayUrl?.trim() ? { relayUrl: input.relayUrl.trim() } : {}),
          ...(merged.length ? { allowFrom: merged, dmPolicy: "allowlist" } : {}),
          ...(input.name?.trim() ? { name: input.name.trim() } : {}),
        },
      }) as OpenClawConfig;
    },
    afterAccountConfigWritten: async ({ input, runtime }) => {
      const identity = normalizePoweurId(input.identity ?? "");
      const operator = parseAllowFrom(input.allowFrom).ids[0];
      try {
        const { client } = await openClient({ identity, ...(input.relayUrl ? { relayUrl: input.relayUrl } : {}) });
        const { explicit } = await client.profile();
        if (!explicit) await client.setProfile(agentProfile(identity, operator));
      } catch (error) {
        runtime.error(`Could not label ${identity}'s profile: ${String(error)}`);
      }
      runtime.log(
        `Done. ${operator ? `Message ${identity} from ${operator} in the Poweur app` : `Message ${identity} from the Poweur app (you will get a pairing code)`} once the gateway is running.`,
      );
    },
  };
}

export const poweurSetupContract = defineChannelSetupContract({
  fields: {
    identity: {
      kind: "string",
      cli: { flags: "--identity <id>", description: "The agent's own Poweur ID (created if this machine has no keys for it)" },
    },
    allowFrom: {
      kind: "string",
      cli: { flags: "--allow-from <ids>", description: "Poweur IDs that may message the agent, comma-separated (yours)" },
    },
    relayUrl: {
      kind: "string",
      cli: { flags: "--relay-url <url>", description: `The ID's relay (default ${DEFAULT_RELAY_URL})` },
    },
    inviteCode: {
      kind: "string",
      cli: { flags: "--invite-code <code>", description: "Registration invite code, if the relay requires one" },
    },
  },
  adapter: createPoweurSetupAdapter(),
});
