/**
 * Proof-of-work — the twin of `packages/identity/pow.go`.
 *
 * Contract: find an ASCII nonce such that sha256(token + "." + nonce) has at
 * least `bits` leading zero bits. Expected work is 2^bits hashes, so each
 * extra bit doubles the cost; JS is roughly 5–10x slower than the Go solver,
 * which is why recipient-facing UIs should warn above ~20 bits.
 *
 * The nonce alphabet is not part of the contract — Go emits base64url of a
 * counter, this emits base36 — so solutions from either side verify on both.
 */

import { sha256Bytes } from "./crypto/index.js";
import { utf8 } from "./encoding.js";

export const POW_MIN_BITS = 8;
export const POW_MAX_BITS = 30;
export const POW_DEFAULT_BITS = 16;
export const POW_ALGO = "sha256-lead0";

/** Normalize a requested difficulty into the allowed window (ClampPowBits). */
export function clampPowBits(bits: number): number {
  if (!Number.isFinite(bits) || bits <= 0) return POW_DEFAULT_BITS;
  if (bits < POW_MIN_BITS) return POW_MIN_BITS;
  if (bits > POW_MAX_BITS) return POW_MAX_BITS;
  return Math.floor(bits);
}

function leadingZeroBits(digest: Uint8Array): number {
  let count = 0;
  for (const byte of digest) {
    if (byte === 0) {
      count += 8;
      continue;
    }
    count += Math.clz32(byte) - 24;
    break;
  }
  return count;
}

/** Does `solution` satisfy the difficulty? Mirrors CheckPowSolution. */
export function checkPow(token: string, solution: string, bits: number): boolean {
  return leadingZeroBits(sha256Bytes(utf8(`${token}.${solution}`))) >= bits;
}

export interface SolveOptions {
  signal?: AbortSignal;
  /** Called between chunks with the attempt count so UIs can show progress. */
  onProgress?: (attempts: number) => void;
  /** Hashes per event-loop yield. Larger is faster but less responsive. */
  chunkSize?: number;
  /** Give up after this many attempts (0 = never). */
  maxAttempts?: number;
}

/**
 * Solve a challenge. Yields to the event loop between chunks so a browser tab
 * (or a Node server handling other requests) stays responsive.
 */
export async function solvePow(
  token: string,
  bits: number,
  options: SolveOptions = {},
): Promise<string> {
  const chunk = options.chunkSize ?? 4096;
  const prefix = `${token}.`;
  let counter = 0;
  for (;;) {
    for (let i = 0; i < chunk; i++) {
      const nonce = counter.toString(36);
      if (leadingZeroBits(sha256Bytes(utf8(prefix + nonce))) >= bits) {
        return nonce;
      }
      counter++;
    }
    if (options.signal?.aborted) {
      throw new Error("pow solve aborted");
    }
    if (options.maxAttempts && counter >= options.maxAttempts) {
      throw new Error(`pow solve gave up after ${counter} attempts`);
    }
    options.onProgress?.(counter);
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
}

/**
 * Synchronous solver for the CLI, where blocking the loop is the point and
 * the yield overhead is pure cost.
 */
export function solvePowSync(token: string, bits: number, maxAttempts = 0): string {
  const prefix = `${token}.`;
  let counter = 0;
  for (;;) {
    const nonce = counter.toString(36);
    if (leadingZeroBits(sha256Bytes(utf8(prefix + nonce))) >= bits) {
      return nonce;
    }
    counter++;
    if (maxAttempts && counter >= maxAttempts) {
      throw new Error(`pow solve gave up after ${counter} attempts`);
    }
  }
}
