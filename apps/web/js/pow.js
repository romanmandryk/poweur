// Proof-of-work solver (EPIC-014 E14-T2) — the browser side of
// packages/identity/pow.go. Contract: find an ASCII nonce such that
// sha256(token + "." + nonce) has at least `bits` leading zero bits.
//
// Solving runs in chunks so the UI stays responsive; pass onProgress to
// render attempt counts. Expected work is 2^bits hashes — browser JS is
// roughly 5–10× slower than native, so UIs should warn above ~20 bits.

const CHUNK = 512; // hashes per event-loop yield

function leadingZeroBits(bytes) {
  let n = 0;
  for (const b of bytes) {
    if (b === 0) {
      n += 8;
      continue;
    }
    // eslint-disable-next-line no-bitwise
    n += Math.clz32(b) - 24;
    break;
  }
  return n;
}

/**
 * Solve a PoW challenge.
 * @param {string} token   sealed challenge token from the relay
 * @param {number} bits    required leading zero bits
 * @param {object} [opts]  { signal, onProgress(attempts) }
 * @returns {Promise<string>} the solution nonce
 */
export async function solvePow(token, bits, opts = {}) {
  const { signal, onProgress } = opts;
  const encoder = new TextEncoder();
  const prefix = token + '.';
  let counter = 0n;
  for (;;) {
    for (let i = 0; i < CHUNK; i++) {
      const nonce = counter.toString(36);
      const digest = new Uint8Array(
        await crypto.subtle.digest('SHA-256', encoder.encode(prefix + nonce)),
      );
      if (leadingZeroBits(digest) >= bits) {
        return nonce;
      }
      counter++;
    }
    if (signal?.aborted) {
      throw new Error('pow solve aborted');
    }
    if (onProgress) {
      onProgress(Number(counter));
    }
    // Yield to the event loop between chunks.
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
}

/** Check a solution locally (mirrors the relay's difficulty check). */
export async function checkPow(token, solution, bits) {
  const digest = new Uint8Array(
    await crypto.subtle.digest('SHA-256', new TextEncoder().encode(token + '.' + solution)),
  );
  return leadingZeroBits(digest) >= bits;
}
