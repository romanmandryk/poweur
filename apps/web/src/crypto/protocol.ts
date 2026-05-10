/**
 * Canonical strings and signing inputs — must match apps/api/internal/crypto and CLI.
 */

export type EncryptionMetaWire = {
  alg: string;
  ephemeral_public_key: string;
  nonce: string;
};

export function canonicalMessageFull(
  sender: string,
  recipient: string,
  timestamp: string,
  payload: string,
  id: string,
  sessionId: string,
  enc: EncryptionMetaWire | null
): string {
  const parts = [sender, recipient, timestamp, payload];
  if (id) parts.push(`id:${id}`);
  if (sessionId) parts.push(`session:${sessionId}`);
  if (enc?.alg) {
    parts.push(`enc:${enc.alg}:${enc.ephemeral_public_key}:${enc.nonce}`);
  }
  return parts.join('\n');
}

export function canonicalAck(
  id: string,
  messageId: string,
  state: string,
  sender: string,
  recipient: string,
  timestamp: string,
  sessionId: string
): string {
  const parts = ['ack', id, messageId, state, sender, recipient, timestamp];
  if (sessionId) parts.push(`session:${sessionId}`);
  return parts.join('\n');
}

export function canonicalIdentityRegistration(
  identity: string,
  publicKey: string,
  encryptionPublicKey: string,
  relayAddress: string,
  issuedAt: string,
  nonce: string
): string {
  return [
    'identity-registration',
    identity,
    publicKey,
    encryptionPublicKey,
    relayAddress,
    issuedAt,
    nonce,
  ].join('\n');
}

export function canonicalEncryptionKeyUpdate(
  identity: string,
  encryptionPublicKey: string,
  issuedAt: string,
  nonce: string
): string {
  return ['identity-encryption-key', identity, encryptionPublicKey, issuedAt, nonce].join('\n');
}

export function canonicalSessionRegistration(
  identity: string,
  sessionPublicKey: string,
  issuedAt: string,
  expiresAt: string,
  nonce: string
): string {
  return `session-registration\n${identity}\n${sessionPublicKey}\n${issuedAt}\n${expiresAt}\n${nonce}`;
}

export function canonicalSessionRevocation(
  identity: string,
  sessionId: string,
  issuedAt: string,
  nonce: string
): string {
  return ['session-revocation', identity, sessionId, issuedAt, nonce].join('\n');
}

export const MSG_ALG = 'x25519-chacha20-poly1305';
