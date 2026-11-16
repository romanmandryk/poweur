/**
 * `~/.poweur/config.toml` — the same file the Go CLI reads and writes, with
 * the same environment-variable precedence, so the two CLIs can be used
 * interchangeably against one identity.
 */

import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname } from "node:path";

import { configPath, defaultKeysDir } from "./paths.js";
import { parseToml, stringifyToml, tomlBool, tomlString } from "./toml.js";

export interface PoweurConfig {
  relay_url: string;
  identity: string;
  keys_dir: string;
  parent_domain: string;
  /**
   * Route outbound messages through your own relay, which forwards them on.
   * Hides your IP from the recipient's relay at the cost of showing your own
   * relay every message you send. Default false = direct send.
   */
  via_home_relay: boolean;
  /**
   * How old a cached identity document may be and still be sent to when the
   * recipient can't be resolved: a Go duration ("720h"), or "0" for never.
   * Empty or absent means 30 days. Written by either CLI, kept by both.
   */
  max_stale?: string;
}

function envOr(name: string, fallback: string): string {
  const value = process.env[name];
  return value && value !== "" ? value : fallback;
}

export function defaultConfig(): PoweurConfig {
  return {
    relay_url: envOr("RELAY_URL", ""),
    identity: envOr("IDENTITY", ""),
    keys_dir: envOr("KEYS_DIR", defaultKeysDir()),
    parent_domain: envOr("PARENT_DOMAIN", ""),
    via_home_relay:
      process.env["VIA_HOME_RELAY"] === "1" || process.env["VIA_HOME_RELAY"] === "true",
  };
}

/** Read the config, applying env fallbacks exactly as the Go CLI does. */
export function loadConfig(): PoweurConfig {
  const config = defaultConfig();
  let raw: string;
  try {
    raw = readFileSync(configPath(), "utf8");
  } catch {
    return config;
  }
  const table = parseToml(raw);
  const merged: PoweurConfig = {
    relay_url: tomlString(table, "relay_url") || config.relay_url,
    identity: tomlString(table, "identity") || config.identity,
    keys_dir: tomlString(table, "keys_dir") || config.keys_dir,
    parent_domain: tomlString(table, "parent_domain") || config.parent_domain,
    via_home_relay: tomlBool(table, "via_home_relay", config.via_home_relay),
  };
  const maxStale = tomlString(table, "max_stale");
  if (maxStale) merged.max_stale = maxStale;
  if (!merged.keys_dir) merged.keys_dir = defaultKeysDir();
  return merged;
}

/** Write the config back in the Go struct's field order. */
export function saveConfig(config: PoweurConfig): void {
  const path = configPath();
  mkdirSync(dirname(path), { recursive: true, mode: 0o755 });
  writeFileSync(
    path,
    stringifyToml({
      relay_url: config.relay_url,
      identity: config.identity,
      keys_dir: config.keys_dir,
      parent_domain: config.parent_domain,
      via_home_relay: config.via_home_relay,
      ...(config.max_stale ? { max_stale: config.max_stale } : {}),
    }),
    { mode: 0o600 },
  );
}

const DURATION_UNITS: Record<string, number> = {
  ns: 1e-6,
  us: 1e-3,
  "µs": 1e-3,
  ms: 1,
  s: 1000,
  m: 60_000,
  h: 3_600_000,
};

/**
 * Parse a Go duration string ("720h", "1h30m", "0") into milliseconds, the
 * format `max_stale` and `POWEUR_MAX_STALE` share with the Go CLI. Returns
 * null for anything Go's `time.ParseDuration` would refuse, or a negative.
 */
export function parseGoDuration(raw: string): number | null {
  const value = raw.trim();
  if (value === "0") return 0;
  if (!/^(\d+(\.\d+)?(ns|us|µs|ms|s|m|h))+$/.test(value)) return null;
  let total = 0;
  for (const [, amount, , unit] of value.matchAll(/(\d+(\.\d+)?)(ns|us|µs|ms|s|m|h)/g)) {
    total += Number(amount) * (DURATION_UNITS[unit as string] ?? 0);
  }
  return total;
}

/**
 * The limit the Go CLI would use: `POWEUR_MAX_STALE`, then `max_stale` in
 * config.toml, then `undefined` (the client's 30-day default). An unparseable
 * value throws, as it does in Go, rather than silently meaning "30 days".
 */
export function configuredMaxStaleMs(config: PoweurConfig): number | undefined {
  const env = (process.env["POWEUR_MAX_STALE"] ?? "").trim();
  const raw = env || (config.max_stale ?? "").trim();
  if (!raw) return undefined;
  const parsed = parseGoDuration(raw);
  if (parsed === null) {
    throw new Error(
      `invalid ${env ? "POWEUR_MAX_STALE" : "max_stale in config.toml"} "${raw}": use a duration such as 720h, or 0`,
    );
  }
  return parsed;
}
