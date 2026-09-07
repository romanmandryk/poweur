/**
 * `~/.poweur/keys` — the Go CLI's key directory, read and written in place.
 *
 * File format is base64 (raw standard, no padding) of the raw key bytes:
 * 64 bytes for Ed25519 (`seed||public`, Go's PrivateKey wire form) and 32 for
 * X25519. Permissions are 0600 on files and 0700 on the directory, matching
 * the Go CLI, so switching between the two clients never loosens a mode.
 */

import { chmodSync, existsSync, mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";

import { expandEd25519PrivateKey } from "../crypto/index.js";
import type { KeyStore, StoredIdentityKeys } from "../crypto/keys.js";
import { fromBase64, toBase64Std } from "../encoding.js";
import { PoweurError } from "../errors.js";
import { defaultKeysDir, encryptionKeyPath, signingKeyPath } from "./paths.js";

function encodeKey(bytes: Uint8Array): string {
  return toBase64Std(bytes).replace(/=+$/, "");
}

function readKeyFile(path: string, expectedLengths: number[]): Uint8Array | null {
  if (!existsSync(path)) return null;
  const decoded = fromBase64(readFileSync(path, "utf8").trim());
  if (!expectedLengths.includes(decoded.length)) {
    throw new PoweurError(
      "invalid_argument",
      `${path}: unexpected key length ${decoded.length}`,
    );
  }
  return decoded;
}

/** A KeyStore over the Go CLI's `~/.poweur/keys` directory. */
export class FileKeyStore implements KeyStore {
  readonly keysDir: string;

  constructor(keysDir: string = defaultKeysDir()) {
    this.keysDir = keysDir;
  }

  async list(): Promise<string[]> {
    if (!existsSync(this.keysDir)) return [];
    return readdirSync(this.keysDir, { withFileTypes: true })
      .filter((entry) => entry.isFile() && entry.name.endsWith(".key"))
      .map((entry) => entry.name.slice(0, -".key".length))
      .sort();
  }

  async load(identity: string): Promise<StoredIdentityKeys | null> {
    const signing = readKeyFile(signingKeyPath(this.keysDir, identity), [32, 64]);
    if (!signing) return null;
    const encryption = readKeyFile(encryptionKeyPath(this.keysDir, identity), [32]);
    return {
      identity,
      signingPrivateKey: signing,
      ...(encryption ? { encryptionPrivateKey: encryption } : {}),
    };
  }

  async save(keys: StoredIdentityKeys): Promise<void> {
    mkdirSync(this.keysDir, { recursive: true, mode: 0o700 });
    chmodSync(this.keysDir, 0o700);
    // Always store the 64-byte form: that is what the Go CLI expects to read.
    const signingPath = signingKeyPath(this.keysDir, keys.identity);
    writeFileSync(signingPath, encodeKey(expandEd25519PrivateKey(keys.signingPrivateKey)), {
      mode: 0o600,
    });
    if (keys.encryptionPrivateKey) {
      writeFileSync(
        encryptionKeyPath(this.keysDir, keys.identity),
        encodeKey(keys.encryptionPrivateKey),
        { mode: 0o600 },
      );
    }
  }

  async remove(identity: string): Promise<void> {
    rmSync(signingKeyPath(this.keysDir, identity), { force: true });
    rmSync(encryptionKeyPath(this.keysDir, identity), { force: true });
  }

  /** Paths for display (`identity show` prints them). */
  paths(identity: string): { signing: string; encryption: string } {
    return {
      signing: signingKeyPath(this.keysDir, identity),
      encryption: encryptionKeyPath(this.keysDir, identity),
    };
  }
}
