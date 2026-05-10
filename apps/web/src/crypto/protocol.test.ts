import { describe, expect, it } from 'vitest';
import {
  canonicalAck,
  canonicalEncryptionKeyUpdate,
  canonicalIdentityRegistration,
  canonicalMessageFull,
  canonicalSessionRegistration,
  canonicalSessionRevocation,
  MSG_ALG,
} from './protocol';

describe('protocol canonical strings', () => {
  it('session-registration', () => {
    expect(canonicalSessionRegistration('alice.test', 'pk', 'i1', 'i2', 'n')).toBe(
      'session-registration\nalice.test\npk\ni1\ni2\nn',
    );
  });

  it('message full with id session enc', () => {
    const enc = { alg: MSG_ALG, ephemeral_public_key: 'eph', nonce: 'non' };
    const c = canonicalMessageFull('a', 'b', 't', 'pay', 'mid', 'sid', enc);
    expect(c).toBe(`a\nb\nt\npay\nid:mid\nsession:sid\nenc:${MSG_ALG}:eph:non`);
  });

  it('identity-registration', () => {
    expect(canonicalIdentityRegistration('id', 'pk', 'enc', 'relay:443', 'iat', 'non')).toBe(
      ['identity-registration', 'id', 'pk', 'enc', 'relay:443', 'iat', 'non'].join('\n'),
    );
  });

  it('encryption key update', () => {
    expect(canonicalEncryptionKeyUpdate('id', 'encpk', 'iat', 'non')).toBe('identity-encryption-key\nid\nencpk\niat\nnon');
  });

  it('session revocation', () => {
    expect(canonicalSessionRevocation('id', 'sess', 'iat', 'non')).toBe('session-revocation\nid\nsess\niat\nnon');
  });

  it('ack with session', () => {
    expect(canonicalAck('a1', 'm1', 'delivered_client', 's', 'r', 'ts', 'sid')).toBe(
      ['ack', 'a1', 'm1', 'delivered_client', 's', 'r', 'ts', 'session:sid'].join('\n'),
    );
  });
});
