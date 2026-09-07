/**
 * The key-custody seam. Nothing above this file ever sees a raw private key:
 * callers hand the SDK a `Signer`, and a passkey-gated browser key, an
 * OS-keychain key and an unattended agent key on disk all drive the same
 * code paths.
 */

import { toBase64Std, toBase64url, utf8 } from "../encoding.js";
import { PoweurError } from "../errors.js";
import {
  ed25519PublicKey,
  generateEncryptionKeypair,
  generateSigningKeypair,
  signBytes,
  x25519PublicKey,
} from "./index.js";

/** Which base64 flavour a signature is rendered in. */
export type SignatureEncoding = "base64url" | "base64std";

/**
 * Signs canonical strings with an identity's (or session's) Ed25519 key.
 *
 * `encoding` exists because the Go CLI emits standard base64 for message,
 * ack and admin-envelope signatures but base64url for identity documents and
 * grants. The relay accepts either; the parameter keeps byte-for-byte parity
 * with the CLI's output where tests compare it.
 */
export interface Signer {
  /** "alice.poweur.net" */
  readonly identity: string;
  /** "ed25519:<base64url>" */
  readonly publicKey: string;
  sign(canonical: string, encoding?: SignatureEncoding): Promise<string>;
}

/** Decrypts inbound payloads with an identity's X25519 key. */
export interface Decryptor {
  readonly encryptionPublicKey: string;
  /** Raw 32-byte X25519 private key, handed only to the crypto layer. */
  privateKeyBytes(): Promise<Uint8Array>;
}

/** Persisted key material for one identity. */
export interface StoredIdentityKeys {
  identity: string;
  /** Ed25519 signing key: 32-byte seed or Go's 64-byte seed||public. */
  signingPrivateKey: Uint8Array;
  /** X25519 encryption key, 32 bytes. Absent for identities without one. */
  encryptionPrivateKey?: Uint8Array;
}

/**
 * Where key material lives. The browser implementation wraps keys with a
 * passkey PRF or PIN; the Node implementation is `~/.poweur/keys`, the same
 * directory the Go CLI uses.
 */
export interface KeyStore {
  list(): Promise<string[]>;
  load(identity: string): Promise<StoredIdentityKeys | null>;
  save(keys: StoredIdentityKeys): Promise<void>;
  remove(identity: string): Promise<void>;
}

/** A Signer backed by in-process key bytes. */
export class LocalSigner implements Signer {
  readonly identity: string;
  readonly publicKey: string;
  readonly #privateKey: Uint8Array;

  constructor(identity: string, privateKey: Uint8Array) {
    this.identity = identity;
    this.#privateKey = privateKey;
    this.publicKey = `ed25519:${toBase64url(ed25519PublicKey(privateKey))}`;
  }

  async sign(canonical: string, encoding: SignatureEncoding = "base64url"): Promise<string> {
    const signature = signBytes(this.#privateKey, utf8(canonical));
    return encoding === "base64std" ? toBase64Std(signature) : toBase64url(signature);
  }

  /** Raw key access, for the session manager that must persist it. */
  privateKeyBytes(): Uint8Array {
    return this.#privateKey;
  }
}

/** A Decryptor backed by in-process key bytes. */
export class LocalDecryptor implements Decryptor {
  readonly encryptionPublicKey: string;
  readonly #privateKey: Uint8Array;

  constructor(privateKey: Uint8Array) {
    this.#privateKey = privateKey;
    this.encryptionPublicKey = `x25519:${toBase64url(x25519PublicKey(privateKey))}`;
  }

  async privateKeyBytes(): Promise<Uint8Array> {
    return this.#privateKey;
  }
}

/** Volatile KeyStore — the default for tests and ephemeral agents. */
export class MemoryKeyStore implements KeyStore {
  readonly #keys = new Map<string, StoredIdentityKeys>();

  async list(): Promise<string[]> {
    return [...this.#keys.keys()].sort();
  }

  async load(identity: string): Promise<StoredIdentityKeys | null> {
    return this.#keys.get(identity) ?? null;
  }

  async save(keys: StoredIdentityKeys): Promise<void> {
    this.#keys.set(keys.identity, keys);
  }

  async remove(identity: string): Promise<void> {
    this.#keys.delete(identity);
  }
}

/** Fresh signing + encryption keys for a new identity. */
export function generateIdentityKeys(identity: string): StoredIdentityKeys {
  const signing = generateSigningKeypair();
  const encryption = generateEncryptionKeypair();
  return {
    identity,
    signingPrivateKey: signing.privateKey,
    encryptionPrivateKey: encryption.privateKey,
  };
}

/** Build the Signer/Decryptor pair for stored key material. */
export function signerFor(keys: StoredIdentityKeys): {
  signer: LocalSigner;
  decryptor: LocalDecryptor | null;
} {
  return {
    signer: new LocalSigner(keys.identity, keys.signingPrivateKey),
    decryptor: keys.encryptionPrivateKey ? new LocalDecryptor(keys.encryptionPrivateKey) : null,
  };
}

export function requireKeys(keys: StoredIdentityKeys | null, identity: string): StoredIdentityKeys {
  if (!keys) {
    throw new PoweurError("not_found", `no keys stored for ${identity}`);
  }
  return keys;
}
