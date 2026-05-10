import { chacha20poly1305 } from '@noble/ciphers/chacha';
import { x25519 } from '@noble/curves/ed25519';
import { hkdf } from '@noble/hashes/hkdf';
import { sha256 } from '@noble/hashes/sha2';
import { decodeAnyBase64, encodeRawURL } from '../util/base64';

const HKDF_INFO = new TextEncoder().encode('eurything/msg/v1');

function buildAAD(ephemeralPub: Uint8Array, recipientPub: Uint8Array): Uint8Array {
  const p = new TextEncoder().encode('eurything/msg/v1\n');
  const out = new Uint8Array(p.length + ephemeralPub.length + recipientPub.length);
  out.set(p, 0);
  out.set(ephemeralPub, p.length);
  out.set(recipientPub, p.length + ephemeralPub.length);
  return out;
}

function deriveKey(shared: Uint8Array, ephemeralPub: Uint8Array, recipientPub: Uint8Array): Uint8Array {
  const salt = new Uint8Array(ephemeralPub.length + recipientPub.length);
  salt.set(ephemeralPub, 0);
  salt.set(recipientPub, ephemeralPub.length);
  return hkdf(sha256, shared, salt, HKDF_INFO, 32);
}

export function generateX25519Keypair(): { publicKey: Uint8Array; privateKey: Uint8Array } {
  const privateKey = x25519.utils.randomSecretKey();
  const publicKey = x25519.getPublicKey(privateKey);
  return { publicKey, privateKey };
}

export type EncryptedPayloadWire = {
  ciphertext: string;
  ephemeral_public_key: string;
  nonce: string;
};

export function encryptForRecipient(recipientPublicKey: Uint8Array, plaintext: Uint8Array): EncryptedPayloadWire {
  if (recipientPublicKey.length !== 32) throw new Error('recipient key must be 32 bytes');
  const ephemeralPriv = x25519.utils.randomSecretKey();
  const ephemeralPub = x25519.getPublicKey(ephemeralPriv);
  const shared = x25519.getSharedSecret(ephemeralPriv, recipientPublicKey);
  const key = deriveKey(shared, ephemeralPub, recipientPublicKey);
  const nonce = new Uint8Array(12);
  crypto.getRandomValues(nonce);
  const ad = buildAAD(ephemeralPub, recipientPublicKey);
  const aead = chacha20poly1305(key, nonce, ad);
  const ciphertext = aead.encrypt(plaintext);
  return {
    ciphertext: encodeRawURL(ciphertext),
    ephemeral_public_key: encodeRawURL(ephemeralPub),
    nonce: encodeRawURL(nonce),
  };
}

export function decryptFromSender(recipientPrivateKey: Uint8Array, payload: EncryptedPayloadWire): Uint8Array {
  const epub = decodeAnyBase64(payload.ephemeral_public_key);
  const nonce = decodeAnyBase64(payload.nonce);
  const ciphertext = decodeAnyBase64(payload.ciphertext);
  if (epub.length !== 32) throw new Error('invalid ephemeral public key');
  const recipientPub = x25519.getPublicKey(recipientPrivateKey);
  const shared = x25519.getSharedSecret(recipientPrivateKey, epub);
  const key = deriveKey(shared, epub, recipientPub);
  const ad = buildAAD(epub, recipientPub);
  const aead = chacha20poly1305(key, nonce, ad);
  return aead.decrypt(ciphertext);
}
