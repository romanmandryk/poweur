import { decodeStd, encodeStd } from '../util/base64';
import type { SessionProofWire } from '../relay/client';
import { getPublicKey, utils as edUtils } from '@noble/ed25519';
import { newAdminNonce } from './ids';
import {
  buildSessionCanonical,
  publicKeyToRelayForm,
  signSessionRegistrationCanonical,
} from './signing';
import { postSession } from '../relay/client';

export type SessionBundle = {
  sessionId: string;
  sessionSeedB64: string;
  sessionPubRawURL: string;
  issuedAtRaw: string;
  expiresAtRaw: string;
  nonce: string;
  identitySigStdB64: string;
  relayUrl: string;
};

function toRFC3339(d: Date): string {
  return d.toISOString().replace(/\.\d{3}Z$/, 'Z');
}

export async function registerSession(
  relayUrl: string,
  identity: string,
  identitySeed32: Uint8Array
): Promise<SessionBundle> {
  const sessSeed = edUtils.randomPrivateKey();
  const sessPub = getPublicKey(sessSeed);
  const sessionPubB64URL = publicKeyToRelayForm(sessPub);
  const issued = new Date();
  const expires = new Date(issued.getTime() + 24 * 3600 * 1000);
  const issuedAtStr = toRFC3339(issued);
  const expiresAtStr = toRFC3339(expires);
  const nonce = newAdminNonce();
  const canonical = buildSessionCanonical(identity, sessionPubB64URL, issuedAtStr, expiresAtStr, nonce);
  const identitySig = signSessionRegistrationCanonical(identitySeed32, canonical);
  const { session_id } = await postSession(relayUrl, {
    identity,
    session_public_key: sessionPubB64URL,
    issued_at: issuedAtStr,
    expires_at: expiresAtStr,
    nonce,
    identity_signature: identitySig,
  });
  return {
    sessionId: session_id,
    sessionSeedB64: encodeStd(sessSeed),
    sessionPubRawURL: sessionPubB64URL,
    issuedAtRaw: issuedAtStr,
    expiresAtRaw: expiresAtStr,
    nonce,
    identitySigStdB64: identitySig,
    relayUrl,
  };
}

export function sessionProofFrom(s: SessionBundle): SessionProofWire {
  return {
    session_public_key: s.sessionPubRawURL,
    issued_at: s.issuedAtRaw,
    expires_at: s.expiresAtRaw,
    nonce: s.nonce,
    identity_signature: s.identitySigStdB64,
  };
}

export function decodeSessionSeed(s: SessionBundle): Uint8Array {
  return decodeStd(s.sessionSeedB64);
}

export function sessionValid(s: SessionBundle | undefined, relayUrl: string): boolean {
  if (!s?.sessionId) return false;
  if (s.relayUrl !== relayUrl) return false;
  const exp = Date.parse(s.expiresAtRaw);
  return !Number.isNaN(exp) && exp > Date.now() + 30_000;
}
