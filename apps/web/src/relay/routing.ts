import { x25519 } from '@noble/curves/ed25519';

export function x25519PublicFromPrivate(priv: Uint8Array): Uint8Array {
  return x25519.getPublicKey(priv);
}

export function relaySchemeFromUrl(relayUrl: string): 'http' | 'https' {
  return relayUrl.trim().startsWith('http://') ? 'http' : 'https';
}
