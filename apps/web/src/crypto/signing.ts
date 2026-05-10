import { etc, getPublicKey, sign, utils } from '@noble/ed25519';
import { sha512 } from '@noble/hashes/sha2';
import { encodeRawURL, encodeStd } from '../util/base64';
import {
  canonicalAck,
  canonicalEncryptionKeyUpdate,
  canonicalIdentityRegistration,
  canonicalMessageFull,
  canonicalSessionRegistration,
  canonicalSessionRevocation,
  type EncryptionMetaWire,
} from './protocol';

function concatBytes(...messages: Uint8Array[]): Uint8Array {
  const len = messages.reduce((a, m) => a + m.length, 0);
  const out = new Uint8Array(len);
  let o = 0;
  for (const m of messages) {
    out.set(m, o);
    o += m.length;
  }
  return out;
}

etc.sha512Sync = (...messages: Uint8Array[]) => sha512(concatBytes(...messages));

export function randomIdentitySeed(): Uint8Array {
  return utils.randomPrivateKey();
}

export function identityPublicKeyRaw(seed32: Uint8Array): Uint8Array {
  return getPublicKey(seed32);
}

export function publicKeyToRelayForm(pub32: Uint8Array): string {
  return encodeRawURL(pub32);
}

export function signBytes(seed32: Uint8Array, message: Uint8Array): string {
  const sig = sign(message, seed32);
  return encodeStd(sig);
}

export function signUtf8(seed32: Uint8Array, text: string): string {
  return signBytes(seed32, new TextEncoder().encode(text));
}

export function signMessagePayload(
  signSeed32: Uint8Array,
  sender: string,
  recipient: string,
  timestamp: string,
  payload: string,
  id: string,
  sessionId: string,
  enc: EncryptionMetaWire | null
): string {
  const c = canonicalMessageFull(sender, recipient, timestamp, payload, id, sessionId, enc);
  return signUtf8(signSeed32, c);
}

export function signAckWire(
  signSeed32: Uint8Array,
  id: string,
  messageId: string,
  state: string,
  sender: string,
  recipient: string,
  timestamp: string,
  sessionId: string
): string {
  const c = canonicalAck(id, messageId, state, sender, recipient, timestamp, sessionId);
  return signUtf8(signSeed32, c);
}

export function signIdentityRegistrationPayload(
  seed32: Uint8Array,
  identity: string,
  publicKey: string,
  encryptionPublicKey: string,
  relayAddress: string,
  issuedAt: string,
  nonce: string
): string {
  const c = canonicalIdentityRegistration(identity, publicKey, encryptionPublicKey, relayAddress, issuedAt, nonce);
  return signUtf8(seed32, c);
}

export function signEncryptionKeyPayload(
  seed32: Uint8Array,
  identity: string,
  encPublicKey: string,
  issuedAt: string,
  nonce: string
): string {
  const c = canonicalEncryptionKeyUpdate(identity, encPublicKey, issuedAt, nonce);
  return signUtf8(seed32, c);
}

export function signSessionRegistrationCanonical(seed32: Uint8Array, canonical: string): string {
  return signUtf8(seed32, canonical);
}

export function buildSessionCanonical(
  identity: string,
  sessionPubB64URL: string,
  issuedAt: string,
  expiresAt: string,
  nonce: string
): string {
  return canonicalSessionRegistration(identity, sessionPubB64URL, issuedAt, expiresAt, nonce);
}

export function signSessionRevokePayload(
  seed32: Uint8Array,
  identity: string,
  sessionId: string,
  issuedAt: string,
  nonce: string
): string {
  const c = canonicalSessionRevocation(identity, sessionId, issuedAt, nonce);
  return signUtf8(seed32, c);
}
