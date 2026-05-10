import type { EncryptionMetaWire } from '../crypto/protocol';
import { relayBaseFromInput } from './apiUrl';

export type MessageWire = {
  id: string;
  sender: string;
  recipient: string;
  timestamp: string;
  payload: string;
  signature: string;
  session_id?: string;
  session_proof?: SessionProofWire;
  encryption?: EncryptionMetaWire;
};

export type SessionProofWire = {
  session_public_key: string;
  issued_at: string;
  expires_at: string;
  nonce: string;
  identity_signature: string;
};

export type AckWire = {
  type: string;
  id: string;
  message_id: string;
  state: string;
  sender: string;
  recipient: string;
  timestamp: string;
  signature: string;
  session_id?: string;
  session_proof?: SessionProofWire;
};

export type InboxPayload = {
  messages: MessageWire[];
  acks: AckWire[];
};

export type ChallengePayload = { challenge: string; expires_at: string };

export class SessionExpiredError extends Error {
  readonly code = 'session_expired' as const;
  constructor() {
    super('session_expired');
    this.name = 'SessionExpiredError';
  }
}

export function apiUrl(path: string, relayUrl: string): string {
  const base = relayBaseFromInput(relayUrl);
  if (typeof window !== 'undefined') {
    try {
      const u = new URL(base);
      if (u.origin === window.location.origin) {
        return path;
      }
    } catch {
      /* ignore */
    }
  }
  return `${base}${path}`;
}

export async function fetchHealth(relayUrl: string): Promise<{ status: string; version: string }> {
  const r = await fetch(apiUrl('/health', relayUrl));
  if (!r.ok) throw new Error(`health: ${r.status}`);
  return r.json();
}

export async function fetchChallenge(relayUrl: string, identity: string): Promise<ChallengePayload> {
  const url = new URL(apiUrl('/auth/challenge', relayUrl));
  url.searchParams.set('identity', identity);
  const r = await fetch(url.toString());
  if (!r.ok) throw new Error(`challenge: ${r.status}`);
  return r.json();
}

export async function fetchInbox(
  relayUrl: string,
  identity: string,
  signatureStdB64: string,
  sessionId: string
): Promise<InboxPayload> {
  const url = apiUrl('/messages/' + encodeURIComponent(identity), relayUrl);
  const r = await fetch(url, {
    headers: {
      'X-Eurything-Identity': identity,
      'X-Eurything-Signature': signatureStdB64,
      ...(sessionId ? { 'X-Eurything-Session-Id': sessionId } : {}),
    },
  });
  if (r.status === 401) {
    let j: { error?: string; detail?: string } = {};
    try {
      j = await r.json();
    } catch {
      /* ignore */
    }
    const d = (j.detail ?? '').toLowerCase();
    if (j.error === 'session_expired' || d.includes('session expired')) {
      throw new SessionExpiredError();
    }
  }
  if (!r.ok) throw new Error(`inbox: ${r.status}`);
  return r.json();
}

export async function postMessageAbsolute(targetRelayBase: string, msg: MessageWire): Promise<void> {
  const base = relayBaseFromInput(targetRelayBase);
  const r = await fetch(`${base}/messages`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(msg),
  });
  if (r.status < 200 || r.status >= 300) {
    const t = await r.text();
    throw new Error(`send: ${r.status} ${t}`);
  }
}

export async function postAckAbsolute(targetRelayBase: string, ack: AckWire): Promise<void> {
  const base = relayBaseFromInput(targetRelayBase);
  const r = await fetch(`${base}/acks`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(ack),
  });
  if (r.status < 200 || r.status >= 300) {
    const t = await r.text();
    throw new Error(`ack: ${r.status} ${t}`);
  }
}

export async function postSession(
  relayUrl: string,
  body: {
    identity: string;
    session_public_key: string;
    issued_at: string;
    expires_at: string;
    nonce: string;
    identity_signature: string;
  }
): Promise<{ session_id: string }> {
  const r = await fetch(apiUrl('/sessions', relayUrl), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!r.ok) {
    const t = await r.text();
    throw new Error(`session: ${r.status} ${t}`);
  }
  return r.json();
}

export async function deleteSession(
  relayUrl: string,
  sessionId: string,
  body: {
    identity: string;
    issued_at: string;
    nonce: string;
    identity_signature: string;
  }
): Promise<void> {
  const r = await fetch(apiUrl('/sessions/' + encodeURIComponent(sessionId), relayUrl), {
    method: 'DELETE',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (r.status !== 204 && r.status !== 404) {
    const t = await r.text();
    throw new Error(`session delete: ${r.status} ${t}`);
  }
}

export async function postIdentities(
  relayUrl: string,
  body: {
    identity: string;
    public_key: string;
    encryption_public_key?: string;
    dns_provider: string;
    dns_token: string;
    issued_at: string;
    nonce: string;
    identity_signature: string;
  }
): Promise<void> {
  const r = await fetch(apiUrl('/identities', relayUrl), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (r.status !== 201) {
    const t = await r.text();
    throw new Error(`register: ${r.status} ${t}`);
  }
}

export async function postEncryptionKey(
  relayUrl: string,
  identity: string,
  body: {
    encryption_public_key: string;
    dns_provider: string;
    dns_token: string;
    issued_at: string;
    nonce: string;
    identity_signature: string;
  }
): Promise<void> {
  const r = await fetch(
    apiUrl('/identities/' + encodeURIComponent(identity) + '/encryption-key', relayUrl),
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }
  );
  if (!r.ok) {
    const t = await r.text();
    throw new Error(`enc key: ${r.status} ${t}`);
  }
}
