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
  EnrollApi,
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
  /** The keys came from a recovery phrase the operator supplied. */
  restored?: boolean;
  /** Where the recovery phrase was saved (only when created). */
  recoveryFile?: string;
  mnemonic?: string;
}

export interface EnsureOptions {
  identity: string;
  relayUrl: string;
  inviteCode?: string;
  /** Restore this ID from its recovery phrase (or seed) instead of creating new keys. */
  recoverySeed?: Uint8Array;
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

  if (options.recoverySeed) {
    const seed = options.recoverySeed;
    let registered = true;
    let matches = false;
    try {
      matches = await new EnrollApi(new RelayClient(relayUrl)).publishedKeyMatches(identity, seed);
    } catch (error) {
      if ((error as { status?: number }).status !== 404) throw error;
      registered = false;
    }
    if (registered && !matches) throw new IdentitySetupError("phrase-mismatch", identity);
    const keys = identityKeysFromSeed(identity, seed);
    if (!registered) {
      const api = options.api ?? new IdentityApi(new RelayClient(relayUrl));
      await registerOrExplain(() => createIdentity(api, identity, { hosted: true, keys, ...(options.inviteCode ? { inviteCode: options.inviteCode } : {}) }), identity);
    }
    await store.save(keys);
    const config = loadConfig();
    if (!config.identity) saveConfig({ ...config, identity, keys_dir: keysDir(), relay_url: relayUrl });
    return { identity, created: !registered, restored: registered };
  }

  const seed = newSeed();
  const keys = identityKeysFromSeed(identity, seed);
  const api = options.api ?? new IdentityApi(new RelayClient(relayUrl));
  await registerOrExplain(
    () => createIdentity(api, identity, { hosted: true, keys, ...(options.inviteCode ? { inviteCode: options.inviteCode } : {}) }),
    identity,
  );
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

/** Why the agent's ID could not be set up; setup turns it into next steps. */
export class IdentitySetupError extends Error {
  constructor(
    readonly reason: "taken" | "phrase-mismatch" | "refused",
    readonly identity: string,
    readonly detail = "",
  ) {
    super(reason === "taken" ? `${identity} is already taken` : reason === "phrase-mismatch" ? `that recovery phrase is not ${identity}'s` : `${identity} cannot be registered: ${detail}`);
    this.name = "IdentitySetupError";
  }
}

async function registerOrExplain(register: () => Promise<unknown>, identity: string): Promise<void> {
  try {
    await register();
  } catch (error) {
    const e = error as { status?: number; relayCode?: string; message?: string };
    if (e.status === 409 || e.relayCode === "identity_exists") throw new IdentitySetupError("taken", identity);
    if (e.status && e.status >= 400 && e.status < 500) throw new IdentitySetupError("refused", identity, e.message ?? String(error));
    throw error;
  }
}
