import { describe, expect, it } from 'vitest';
import { newMessageID, newAckID } from './ids';

describe('ids', () => {
  it('message ids use msg_ prefix', () => {
    expect(newMessageID().startsWith('msg_')).toBe(true);
  });
  it('ack ids use ack_ prefix', () => {
    expect(newAckID().startsWith('ack_')).toBe(true);
  });
});
