import { encodeRawURL } from '../util/base64.js';

function randomSuffix(): string {
  const buf = new Uint8Array(9);
  crypto.getRandomValues(buf);
  return encodeRawURL(buf);
}

export function newMessageID(): string {
  return `msg_${Date.now()}_${randomSuffix()}`;
}

export function newAckID(): string {
  return `ack_${Date.now()}_${randomSuffix()}`;
}

export function newAdminNonce(): string {
  const buf = new Uint8Array(16);
  crypto.getRandomValues(buf);
  return encodeRawURL(buf);
}
