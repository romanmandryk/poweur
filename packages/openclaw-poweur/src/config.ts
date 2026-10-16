/**
 * `channels.poweur` in openclaw.json: which Poweur ID the agent is, and who
 * may message it. The ID's keys live in the Poweur home (`POWEUR_HOME`,
 * default ~/.poweur), created with `poweur identity create` — never in
 * OpenClaw's config.
 */
import { AllowFromListSchema, DmPolicySchema } from "openclaw/plugin-sdk/channel-config-schema";
import { z } from "zod";

export const PoweurConfigSchema = z.object({
  name: z.string().optional(),
  enabled: z.boolean().optional(),
  /** The agent's own Poweur ID, e.g. `myagent.poweur.net`. */
  identity: z.string().optional(),
  /** Its home relay; defaults to the one in the Poweur home's config.toml. */
  relayUrl: z.string().url().optional(),
  dmPolicy: DmPolicySchema.optional(),
  /** Poweur IDs that may message the agent, e.g. `alice.poweur.net`. */
  allowFrom: AllowFromListSchema,
});

export type PoweurAccountConfig = z.input<typeof PoweurConfigSchema>;

export interface ResolvedPoweurAccount {
  accountId: string;
  name?: string;
  enabled: boolean;
  configured: boolean;
  identity: string;
  relayUrl?: string;
  config: PoweurAccountConfig;
}

type ConfigSource = { channels?: Record<string, unknown> | undefined };

export function getPoweurConfig(cfg: ConfigSource): PoweurAccountConfig | undefined {
  return cfg.channels?.["poweur"] as PoweurAccountConfig | undefined;
}

/** Lowercase, no scheme, no trailing dot: the form Poweur IDs compare in. */
export function normalizePoweurId(raw: string): string {
  return raw.trim().replace(/^poweur:/i, "").replace(/^https?:\/\//i, "").replace(/\/.*$/, "").replace(/\.$/, "").toLowerCase();
}

/** A Poweur ID is a DNS name with at least one dot. */
export function looksLikePoweurId(raw: string): boolean {
  const id = normalizePoweurId(raw);
  return id.length <= 253 && /^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$/.test(id);
}

export function resolvePoweurAccount(cfg: ConfigSource, accountId = "default"): ResolvedPoweurAccount {
  const section = getPoweurConfig(cfg);
  const identity = section?.identity ? normalizePoweurId(section.identity) : "";
  return {
    accountId,
    ...(typeof section?.name === "string" ? { name: section.name } : {}),
    enabled: section?.enabled !== false,
    configured: looksLikePoweurId(identity),
    identity,
    ...(section?.relayUrl ? { relayUrl: section.relayUrl } : {}),
    config: section ?? {},
  };
}
