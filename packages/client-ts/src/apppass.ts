/**
 * App passwords — the twin of `packages/identity/apppass.go`.
 *
 * Named Basic-auth credentials for legacy WebDAV clients (Finder, davfs2,
 * rclone) that cannot present a bearer token. The client generates the
 * password, hashes it with argon2id, and stores only the hash in
 * `poweur-sys/relay/app-passwords.json`; the relay verifies Basic auth
 * against that file, so the plaintext exists exactly once, in the terminal
 * output of `dav password add`.
 */

import { argon2id } from "@noble/hashes/argon2.js";

import { equalBytes, fromBase64, randomBytes, toBase64Std, toBase64url, utf8 } from "./encoding.js";

/** Parameters must match Go's, or a hash written here fails to verify there. */
const ARGON_TIME = 1;
const ARGON_MEMORY = 64 * 1024; // KiB
const ARGON_THREADS = 4;
const ARGON_KEY_LEN = 32;
const ARGON_SALT_LEN = 16;
const ARGON_VERSION = 19;

export interface AppPassword {
  name: string;
  /** PHC-format argon2id hash. */
  hash: string;
  scope?: string;
  created_at?: string;
}

export interface AppPasswordsFile {
  passwords: AppPassword[];
}

export const APP_PASSWORDS_PATH = "poweur-sys/relay/app-passwords.json";

function rawStd(bytes: Uint8Array): string {
  return toBase64Std(bytes).replace(/=+$/, "");
}

/** A new random password in the `poweur-ap-…` form. */
export function generateAppPassword(): string {
  return `poweur-ap-${toBase64url(randomBytes(24))}`;
}

/** PHC-format argon2id hash of `password`. */
export async function hashAppPassword(password: string): Promise<string> {
  const salt = randomBytes(ARGON_SALT_LEN);
  const key = argon2id(utf8(password), salt, {
    t: ARGON_TIME,
    m: ARGON_MEMORY,
    p: ARGON_THREADS,
    dkLen: ARGON_KEY_LEN,
  });
  return `$argon2id$v=${ARGON_VERSION}$m=${ARGON_MEMORY},t=${ARGON_TIME},p=${ARGON_THREADS}$${rawStd(salt)}$${rawStd(key)}`;
}

/** Does `password` match a PHC argon2id hash? Constant-time on the digest. */
export async function verifyAppPassword(password: string, encoded: string): Promise<boolean> {
  // "", "argon2id", "v=19", "m=…,t=…,p=…", salt, hash
  const parts = encoded.split("$");
  if (parts.length !== 6 || parts[1] !== "argon2id") return false;
  const version = /^v=(\d+)$/.exec(parts[2] as string);
  if (!version || Number(version[1]) !== ARGON_VERSION) return false;
  const params = /^m=(\d+),t=(\d+),p=(\d+)$/.exec(parts[3] as string);
  if (!params) return false;
  let salt: Uint8Array;
  let want: Uint8Array;
  try {
    salt = fromBase64(parts[4] as string);
    want = fromBase64(parts[5] as string);
  } catch {
    return false;
  }
  const got = argon2id(utf8(password), salt, {
    m: Number(params[1]),
    t: Number(params[2]),
    p: Number(params[3]),
    dkLen: want.length,
  });
  return equalBytes(got, want);
}

export function parseAppPasswordsFile(raw: string): AppPasswordsFile {
  const parsed = JSON.parse(raw) as Partial<AppPasswordsFile>;
  return { passwords: parsed.passwords ?? [] };
}
