/**
 * The key-custody seam. Nothing above this file ever sees a raw private key:
 * callers hand the SDK a `Signer`, and a passkey-gated browser key, an
 * OS-keychain key and an unattended agent key on disk all drive the same
 * code paths.
 */
import { toBase64Std, toBase64url, utf8 } from "../encoding.js";
import { PoweurError } from "../errors.js";
import { ed25519PublicKey, generateEncryptionKeypair, generateSigningKeypair, signBytes, x25519PublicKey, } from "./index.js";
// Imported from the leaf module, not ./index.js, to keep this file acyclic.
import { deriveEncryptionKey, deriveSigningKey } from "./seed.js";
/** A Signer backed by in-process key bytes. */
export class LocalSigner {
    identity;
    publicKey;
    #privateKey;
    constructor(identity, privateKey) {
        this.identity = identity;
        this.#privateKey = privateKey;
        this.publicKey = `ed25519:${toBase64url(ed25519PublicKey(privateKey))}`;
    }
    async sign(canonical, encoding = "base64url") {
        const signature = signBytes(this.#privateKey, utf8(canonical));
        return encoding === "base64std" ? toBase64Std(signature) : toBase64url(signature);
    }
    /** Raw key access, for the session manager that must persist it. */
    privateKeyBytes() {
        return this.#privateKey;
    }
}
/** A Decryptor backed by in-process key bytes. */
export class LocalDecryptor {
    encryptionPublicKey;
    #privateKey;
    constructor(privateKey) {
        this.#privateKey = privateKey;
        this.encryptionPublicKey = `x25519:${toBase64url(x25519PublicKey(privateKey))}`;
    }
    async privateKeyBytes() {
        return this.#privateKey;
    }
}
/** Volatile KeyStore — the default for tests and ephemeral agents. */
export class MemoryKeyStore {
    #keys = new Map();
    async list() {
        return [...this.#keys.keys()].sort();
    }
    async load(identity) {
        return this.#keys.get(identity) ?? null;
    }
    async save(keys) {
        this.#keys.set(keys.identity, keys);
    }
    async remove(identity) {
        this.#keys.delete(identity);
    }
}
/** Fresh signing + encryption keys for a new identity. */
export function generateIdentityKeys(identity) {
    const signing = generateSigningKeypair();
    const encryption = generateEncryptionKeypair();
    return {
        identity,
        signingPrivateKey: signing.privateKey,
        encryptionPrivateKey: encryption.privateKey,
    };
}
/**
 * Reconstruct an identity's keys from its master seed (EPIC-011 E11-T1).
 *
 * This is the recovery path: given the 32 bytes behind a recovery kit or a
 * keystore entry, the identity is whole again — no relay call, no network,
 * nothing else to remember. Derivation is pinned to Go by
 * `test/seed.test.ts`.
 */
export function identityKeysFromSeed(identity, seed) {
    return {
        identity,
        signingPrivateKey: deriveSigningKey(seed).privateKey,
        encryptionPrivateKey: deriveEncryptionKey(seed).privateKey,
    };
}
/** Build the Signer/Decryptor pair for stored key material. */
export function signerFor(keys) {
    return {
        signer: new LocalSigner(keys.identity, keys.signingPrivateKey),
        decryptor: keys.encryptionPrivateKey ? new LocalDecryptor(keys.encryptionPrivateKey) : null,
    };
}
export function requireKeys(keys, identity) {
    if (!keys) {
        throw new PoweurError("not_found", `no keys stored for ${identity}`);
    }
    return keys;
}
