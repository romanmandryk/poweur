/**
 * A `localStorage`-backed KeyStore.
 *
 * This stores key material in plain `localStorage`, which any script on the
 * origin can read. It is here for demos, tests and throwaway identities — a
 * production browser app should wrap keys with a passkey PRF
 * (see `apps/web/js/passkey.js`) and implement `KeyStore` over that instead.
 */
import { fromBase64, toBase64url } from "../encoding.js";
const PREFIX = "poweur:keys:";
export class LocalStorageKeyStore {
    #storage;
    constructor(storage = globalThis.localStorage) {
        this.#storage = storage;
    }
    async list() {
        const out = [];
        for (let i = 0; i < this.#storage.length; i++) {
            const key = this.#storage.key(i);
            if (key?.startsWith(PREFIX))
                out.push(key.slice(PREFIX.length));
        }
        return out.sort();
    }
    async load(identity) {
        const raw = this.#storage.getItem(PREFIX + identity);
        if (!raw)
            return null;
        const parsed = JSON.parse(raw);
        return {
            identity: parsed.identity,
            signingPrivateKey: fromBase64(parsed.signing),
            ...(parsed.encryption ? { encryptionPrivateKey: fromBase64(parsed.encryption) } : {}),
        };
    }
    async save(keys) {
        const payload = {
            identity: keys.identity,
            signing: toBase64url(keys.signingPrivateKey),
            ...(keys.encryptionPrivateKey
                ? { encryption: toBase64url(keys.encryptionPrivateKey) }
                : {}),
        };
        this.#storage.setItem(PREFIX + keys.identity, JSON.stringify(payload));
    }
    async remove(identity) {
        this.#storage.removeItem(PREFIX + identity);
    }
}
