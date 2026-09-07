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
    }),
    { mode: 0o600 },
  );
}
