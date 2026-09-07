/**
 * Recovery kit encoding — the TypeScript twin of `packages/identity/kit.go`
 * (EPIC-011 E11-T1).
 *
 * A kit is the master seed rendered for whoever carries it. BIP39 is an
 * *encoding of the same 32 bytes*, not a second secret: 24 words with a
 * checksum that catches transcription errors. It earns its keep only where a
 * human copies the seed by hand; for scripts and secret stores, base64url
 * (`encodeSeed`) remains the right form.
 *
 * The carrier is deliberately unspecified — a printed card, a PDF, a text file
 * and a password-manager entry are all valid. This module only converts.
 */

import { entropyToMnemonic, mnemonicToEntropy, validateMnemonic } from "@scure/bip39";
import { wordlist } from "@scure/bip39/wordlists/english.js";

import { fromBase64, toBase64url } from "./encoding.js";
import { PoweurError } from "./errors.js";
import { SEED_LEN } from "./crypto/seed.js";

/** Word count for a 32-byte seed (256 bits + 8 checksum bits). */
export const MNEMONIC_WORDS = 24;

/** Lowercase and collapse whitespace — a kit read off paper rarely arrives clean. */
export function normalizeMnemonic(mnemonic: string): string {
  return mnemonic.trim().toLowerCase().split(/\s+/).filter(Boolean).join(" ");
}

/** Encode a master seed as a 24-word BIP39 mnemonic. */
export function seedToMnemonic(seed: Uint8Array): string {
  if (seed.length !== SEED_LEN) {
    throw new PoweurError("invalid_argument", `seed must be ${SEED_LEN} bytes`);
  }
  return entropyToMnemonic(seed, wordlist);
}

/**
 * Decode a 24-word mnemonic back to the master seed. Case and spacing are
 * normalized first, so only a real typo is rejected — by the checksum.
 */
export function mnemonicToSeed(mnemonic: string): Uint8Array {
  const normalized = normalizeMnemonic(mnemonic);
  const words = normalized ? normalized.split(" ") : [];
  if (words.length !== MNEMONIC_WORDS) {
    throw new PoweurError(
      "invalid_argument",
      `expected ${MNEMONIC_WORDS} words, got ${words.length}`,
    );
  }
  let entropy: Uint8Array;
  try {
    entropy = mnemonicToEntropy(normalized, wordlist);
  } catch (err) {
    throw new PoweurError("invalid_argument", `invalid recovery mnemonic: ${String(err)}`);
  }
  if (entropy.length !== SEED_LEN) {
    throw new PoweurError("invalid_argument", `mnemonic decoded ${entropy.length} bytes`);
  }
  return entropy;
}

/** Whether a mnemonic decodes to a usable seed. */
export function validMnemonic(mnemonic: string): boolean {
  const normalized = normalizeMnemonic(mnemonic);
  if (normalized.split(" ").length !== MNEMONIC_WORDS) return false;
  try {
    return validateMnemonic(normalized, wordlist);
  } catch {
    return false;
  }
}

export interface RecoveryKit {
  identity: string;
  relay?: string;
  mnemonic: string;
  seed: string;
}

/** Build a kit from a seed: words for paper, base64url for machines. */
export function newRecoveryKit(
  identity: string,
  relay: string,
  seed: Uint8Array,
): RecoveryKit {
  return {
    identity,
    ...(relay ? { relay } : {}),
    mnemonic: seedToMnemonic(seed),
    seed: toBase64url(seed),
  };
}

/**
 * Accept either encoding, so any seed input takes a pasted kit without a
 * second option. Multi-word input is a mnemonic; anything else is base64url.
 */
export function parseSeedOrMnemonic(value: string): Uint8Array {
  const trimmed = value.trim();
  if (/\s/.test(trimmed)) return mnemonicToSeed(trimmed);
  const seed = fromBase64(trimmed);
  if (seed.length !== SEED_LEN) {
    throw new PoweurError("invalid_argument", `seed must decode to ${SEED_LEN} bytes`);
  }
  return seed;
}
