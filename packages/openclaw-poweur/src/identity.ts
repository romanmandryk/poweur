/**
 * The agent's own Poweur ID: reuse its keys when the Poweur home already has
 * them, otherwise create and register it (hosted names only; an ID on your
 * own domain is created with the CLI, which also sets up DNS). The recovery
 * phrase is written once, next to the keys, and shown to the operator.
 */
import { chmodSync, existsSync, mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import {
  createIdentity,
  IdentityApi,
  identityKeysFromSeed,
  newRecoveryKit,
  RelayClient,
} from "@poweur/client";
import { newSeed } from "@poweur/client/crypto";
import { defaultKeysDir, FileKeyStore, loadConfig, poweurHome, saveConfig } from "@poweur/client/node";

export const DEFAULT_RELAY_URL = "https://poweur.net";

export interface EnsuredIdentity {
  identity: string;
  created: boolean;
  /** Where the recovery phrase was saved (only when created). */
  recoveryFile?: string;
  mnemonic?: string;
}

export interface EnsureOptions {
  identity: string;
  relayUrl: string;
  inviteCode?: string;
  /** For tests: register through this API instead of one built from relayUrl. */
  api?: IdentityApi;
}

export function keysDir(): string {
  return loadConfig().keys_dir || defaultKeysDir();
}

export async function hasLocalKeys(identity: string): Promise<boolean> {
  return (await new FileKeyStore(keysDir()).load(identity)) !== null;
}

export async function ensureAgentIdentity(options: EnsureOptions): Promise<EnsuredIdentity> {
  const { identity, relayUrl } = options;
  const store = new FileKeyStore(keysDir());
  if (await store.load(identity)) return { identity, created: false };

  const seed = newSeed();
  const keys = identityKeysFromSeed(identity, seed);
  const api = options.api ?? new IdentityApi(new RelayClient(relayUrl));
  await createIdentity(api, identity, {
    hosted: true,
    keys,
    ...(options.inviteCode ? { inviteCode: options.inviteCode } : {}),
  });
  await store.save(keys);
  const config = loadConfig();
  // The agent's home: make it the active ID only when nothing else is.
  if (!config.identity) saveConfig({ ...config, identity, keys_dir: keysDir(), relay_url: relayUrl });

  const kit = newRecoveryKit(identity, "", seed);
  const dir = join(poweurHome(), "recovery");
  mkdirSync(dir, { recursive: true, mode: 0o700 });
  const recoveryFile = join(dir, `${identity}.txt`);
  if (!existsSync(recoveryFile)) {
    writeFileSync(
      recoveryFile,
      `Recovery phrase for ${identity}. Whoever has it controls this ID: store it somewhere safe, then delete this file.\n\n${kit.mnemonic}\n`,
      { mode: 0o600 },
    );
    chmodSync(recoveryFile, 0o600);
  }
  return { identity, created: true, recoveryFile, mnemonic: kit.mnemonic };
}
