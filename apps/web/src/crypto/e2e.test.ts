import { describe, expect, it } from 'vitest';
import { decryptFromSender, encryptForRecipient, generateX25519Keypair } from './e2e';

describe('e2e x25519-chacha20-poly1305', () => {
  it('roundtrips', () => {
    const { publicKey, privateKey } = generateX25519Keypair();
    const plain = new TextEncoder().encode('hello eurything');
    const sealed = encryptForRecipient(publicKey, plain);
    const out = decryptFromSender(privateKey, sealed);
    expect(new TextDecoder().decode(out)).toBe('hello eurything');
  });
});
