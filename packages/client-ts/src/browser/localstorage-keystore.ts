/**
 * A `localStorage`-backed KeyStore.
 *
 * This stores key material in plain `localStorage`, which any script on the
 * origin can read. It is here for demos, tests and throwaway identities — a
 * production browser app should wrap keys with a passkey PRF
 * (see `apps/web/src/lib/passkey.js`) and implement `KeyStore` over that instead.
 */

import type { KeyStore, StoredIdentityKeys } from "../crypto/keys.js";
import { fromBase64, toBase64url } from "../encoding.js";

const PREFIX = "poweur:keys:";

interface SerializedKeys {
  identity: string;
  signing: string;
  encryption?: string;
}

export class LocalStorageKeyStore implements KeyStore {
  readonly #storage: Storage;

  constructor(storage: Storage = globalThis.localStorage) {
    this.#storage = storage;
  }

  async list(): Promise<string[]> {
    const out: string[] = [];
    for (let i = 0; i < this.#storage.length; i++) {
      const key = this.#storage.key(i);
      if (key?.startsWith(PREFIX)) out.push(key.slice(PREFIX.length));
    }
    return out.sort();
  }

  async load(identity: string): Promise<StoredIdentityKeys | null> {
    const raw = this.#storage.getItem(PREFIX + identity);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as SerializedKeys;
    return {
      identity: parsed.identity,
      signingPrivateKey: fromBase64(parsed.signing),
      ...(parsed.encryption ? { encryptionPrivateKey: fromBase64(parsed.encryption) } : {}),
    };
  }

  async save(keys: StoredIdentityKeys): Promise<void> {
    const payload: SerializedKeys = {
      identity: keys.identity,
      signing: toBase64url(keys.signingPrivateKey),
      ...(keys.encryptionPrivateKey
        ? { encryption: toBase64url(keys.encryptionPrivateKey) }
        : {}),
    };
    this.#storage.setItem(PREFIX + keys.identity, JSON.stringify(payload));
  }

  async remove(identity: string): Promise<void> {
    this.#storage.removeItem(PREFIX + identity);
  }
}
