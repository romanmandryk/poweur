/**
 * The `~/.poweur` tree, shared byte-for-byte with the Go CLI:
 *
 *   ~/.poweur/config.toml            active identity, relay, keys dir
 *   ~/.poweur/keys/<identity>.key    Ed25519 private key (base64, raw std)
 *   ~/.poweur/keys/<identity>.enc    X25519 private key (base64, raw std)
 *   ~/.poweur/sessions/<id>.toml     short-lived session record
 *   ~/.poweur/pending/<id>.jsonl     outbound delivery journal
 *
 * `POWEUR_HOME` overrides the root, which is how the test suite gets an
 * isolated tree without touching the developer's real one.
 */

import { homedir } from "node:os";
import { join } from "node:path";

export function poweurHome(): string {
  const override = process.env["POWEUR_HOME"];
  return override && override.trim() !== "" ? override : join(homedir(), ".poweur");
}

export function configPath(): string {
  return join(poweurHome(), "config.toml");
}

export function defaultKeysDir(): string {
  return join(poweurHome(), "keys");
}

export function sessionsDir(): string {
  return join(poweurHome(), "sessions");
}

export function journalDir(): string {
  return join(poweurHome(), "pending");
}

export function signingKeyPath(keysDir: string, identity: string): string {
  return join(keysDir, `${identity}.key`);
}

export function encryptionKeyPath(keysDir: string, identity: string): string {
  return join(keysDir, `${identity}.enc`);
}
