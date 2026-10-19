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
import { readFileSync } from "node:fs";
import { homedir } from "node:os";
import { parseSeedOrMnemonic } from "@poweur/client";
import { DEFAULT_RELAY_URL, ensureAgentIdentity, IdentitySetupError } from "./identity.js";

export type PoweurSetupInput = {
  name?: string;
  identity?: string;
  relayUrl?: string;
  allowFrom?: string;
  inviteCode?: string;
  recoveryPhraseFile?: string;
};

/** The recovery phrase to restore from: --recovery-phrase-file, else POWEUR_RECOVERY_PHRASE. */
export function readRecoveryPhrase(input: PoweurSetupInput, env = process.env): Uint8Array | undefined {
  const file = input.recoveryPhraseFile?.trim().replace(/^~(?=\/)/, env["HOME"] ?? homedir());
  const raw = file ? readFileSync(file, "utf8") : env["POWEUR_RECOVERY_PHRASE"];
  if (!raw?.trim()) return undefined;
  try {
    return parseSeedOrMnemonic(raw);
  } catch {
    throw new Error("The recovery phrase could not be read: it should be the 24 words shown when the ID was created.");
  }
}

/** What to do next, with the exact commands, when the agent's ID cannot be set up. */
export function explainSetupError(error: IdentitySetupError, input: PoweurSetupInput): string {
  const allow = input.allowFrom?.trim() ? ` --allow-from ${input.allowFrom.trim()}` : "";
  const relay = input.relayUrl?.trim() ? ` --relay-url ${input.relayUrl.trim()}` : "";
  const label = error.identity.split(".")[0] ?? error.identity;
  const other = `${label.slice(0, 18).replace(/-+$/, "")}-agent`; // names are at most 24 characters
  const domain = error.identity.slice(label.length + 1) || "poweur.net";
  const add = (identity: string, extra = "") => `  openclaw channels add --channel poweur --identity ${identity}${allow}${relay}${extra}`;
  const restore = [
    "If it is yours (made in the Poweur app, on another machine, or before a reinstall), restore its keys here from its recovery phrase:",
    "  1. Save its 24 words to a file only you can read, for example ~/agent-phrase.txt",
    "  2. Run:",
    "  " + add(error.identity, " --recovery-phrase-file ~/agent-phrase.txt"),
    "  3. Delete the file.",
    "Or copy the Poweur folder (~/.poweur, or POWEUR_HOME) from the machine that has the keys.",
  ];
  switch (error.reason) {
    case "taken":
      return [
        `${error.identity} is already taken, and this machine has no keys for it.`,
        "",
        "If it is not yours, pick another name:",
        add(`${other}.${domain}`),
        "",
        ...restore,
      ].join("\n");
    case "phrase-mismatch":
      return [
        `That recovery phrase belongs to a different ID, not ${error.identity}. Nothing was saved.`,
        "Check you have the phrase shown when this ID was created, or pick a new name:",
        add(`${other}.${domain}`),
      ].join("\n");
    case "refused":
      return [
        `${error.identity} cannot be used: ${error.detail}`,
        "Names on poweur.net are 6 to 24 letters, digits or hyphens, and some words are reserved. Try another:",
        add(`${other}.${domain}`),
      ].join("\n");
  }
}

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
      const recoverySeed = readRecoveryPhrase(input);
      let ensured;
      try {
        ensured = await ensureAgentIdentity({
          identity,
          relayUrl,
          ...(input.inviteCode ? { inviteCode: input.inviteCode } : {}),
          ...(recoverySeed ? { recoverySeed } : {}),
        });
      } catch (error) {
        if (error instanceof IdentitySetupError) throw new Error(explainSetupError(error, input));
        throw error;
      }
      if (ensured.restored) {
        runtime.log(`Restored ${identity} on this machine from its recovery phrase. You can delete the phrase file now.`);
      } else if (ensured.created && recoverySeed) {
        runtime.log(`Created ${identity} from the recovery phrase you supplied.`);
      } else if (ensured.created) {
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
    recoveryPhraseFile: {
      kind: "string",
      cli: { flags: "--recovery-phrase-file <path>", description: "Restore the agent's existing ID from its recovery phrase (or set POWEUR_RECOVERY_PHRASE)" },
      envVars: ["POWEUR_RECOVERY_PHRASE"],
    },
  },
  adapter: createPoweurSetupAdapter(),
});
