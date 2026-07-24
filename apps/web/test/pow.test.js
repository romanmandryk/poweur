// PoW solver unit tests (EPIC-014): the JS solver satisfies the same
// sha256-lead0 contract the Go verifier enforces.
import { describe, expect, it } from 'vitest';
import { checkPow, solvePow } from '../js/pow.js';

describe('pow solver', () => {
  it('solves a low-difficulty challenge and self-verifies', async () => {
    const token = 'eyJmYWtlIjoidG9rZW4ifQ.c2VhbA'; // opaque to the solver
    const bits = 10;
    const solution = await solvePow(token, bits);
    expect(await checkPow(token, solution, bits)).toBe(true);
    // A harder requirement usually fails for the same nonce (sanity that
    // bits actually constrain the digest).
    expect(await checkPow(token, solution, 30)).toBe(false);
  });

  it('verification is token-bound', async () => {
    const bits = 12;
    const a = await solvePow('token-a', bits);
    expect(await checkPow('token-a', a, bits)).toBe(true);
    // The same nonce against a different token misses the target with
    // overwhelming probability at 12 bits.
    expect(await checkPow('token-b', a, bits)).toBe(false);
  });

  it('aborts via signal', async () => {
    const controller = new AbortController();
    setTimeout(() => controller.abort(), 20);
    await expect(
      solvePow('impossible', 60, { signal: controller.signal }),
    ).rejects.toThrow(/aborted/);
  });
});
