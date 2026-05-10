import { newAdminNonce } from '../crypto/ids';
import { registerSession, sessionValid, type SessionBundle } from '../crypto/sessionRegister';
import { signSessionRevokePayload } from '../crypto/signing';
import { deleteSession } from '../relay/client';
import type { WebPersisted } from '../state/storage';
import { decodeStd } from '../util/base64';

export function SessionsTab({
  st,
  commit,
  busy,
  setBusy,
  flashErr,
  flashOk,
}: {
  st: WebPersisted;
  commit: (fn: (p: WebPersisted) => WebPersisted) => void;
  busy: boolean;
  setBusy: (b: boolean) => void;
  flashErr: (e: unknown) => void;
  flashOk: (m: string) => void;
}) {
  const id = st.activeId;
  const sess = id ? st.sessions[id] : undefined;
  const okRelay = sessionValid(sess, st.relayUrl);

  const refresh = async () => {
    if (!id) {
      flashErr('Select identity.');
      return;
    }
    const ent = st.identities.find((i) => i.id === id);
    if (!ent) return;
    setBusy(true);
    try {
      const seed = decodeStd(ent.signSeedB64);
      const bundle = await registerSession(st.relayUrl, id, seed);
      commit((p) => ({ ...p, sessions: { ...p.sessions, [id]: bundle } }));
      flashOk(`Session ${bundle.sessionId}`);
    } catch (e) {
      flashErr(e);
    } finally {
      setBusy(false);
    }
  };

  const revoke = async () => {
    if (!id || !sess) return;
    const ent = st.identities.find((i) => i.id === id);
    if (!ent) return;
    setBusy(true);
    try {
      const seed = decodeStd(ent.signSeedB64);
      const issuedAt = new Date().toISOString().replace(/\.\d{3}Z$/, 'Z');
      const nonce = newAdminNonce();
      const sig = signSessionRevokePayload(seed, id, sess.sessionId, issuedAt, nonce);
      await deleteSession(st.relayUrl, sess.sessionId, { identity: id, issued_at: issuedAt, nonce, identity_signature: sig });
      commit((p) => {
        const next = { ...p.sessions };
        delete next[id];
        return { ...p, sessions: next };
      });
      flashOk('Session revoked.');
    } catch (e) {
      flashErr(e);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="card">
      <h2>Session</h2>
      {!id ? (
        <p className="sub">Pick an identity.</p>
      ) : (
        <>
          <p>
            <span className="badge">{okRelay ? 'valid' : 'missing/expired'}</span>
          </p>
          {sess ? (
            <pre style={{ fontSize: '0.75rem', overflow: 'auto' }}>
              {JSON.stringify(
                { session_id: sess.sessionId, expires_at: sess.expiresAtRaw, relay: sess.relayUrl },
                null,
                2,
              )}
            </pre>
          ) : (
            <p className="sub">No session cached.</p>
          )}
          <button type="button" className="primary" disabled={busy} onClick={() => void refresh()}>
            Refresh / register
          </button>{' '}
          <button type="button" className="secondary" disabled={busy || !sess} onClick={() => void revoke()}>
            Revoke
          </button>
        </>
      )}
    </div>
  );
}
