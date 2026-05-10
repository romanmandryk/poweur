import { useState } from 'react';
import { generateX25519Keypair } from '../crypto/e2e';
import { newAdminNonce } from '../crypto/ids';
import { identityPublicKeyRaw, publicKeyToRelayForm, randomIdentitySeed, signEncryptionKeyPayload, signIdentityRegistrationPayload } from '../crypto/signing';
import { postEncryptionKey, postIdentities } from '../relay/client';
import { relayHostPortFromUrl } from '../relay/apiUrl';
import { x25519PublicFromPrivate } from '../relay/routing';
import type { WebPersisted } from '../state/storage';
import { decodeStd, encodeRawURL, encodeStd } from '../util/base64';

export function IdentitiesTab({
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
  const [handle, setHandle] = useState('');
  const [dnsProvider, setDnsProvider] = useState('cloudflare');
  const [dnsToken, setDnsToken] = useState('');
  const [encRotate, setEncRotate] = useState(false);
  const [errLocal, setErrLocal] = useState<string | null>(null);

  const addLocalIdentity = () => {
    setErrLocal(null);
    const h = handle.trim().toLowerCase();
    if (h.length < 8) {
      setErrLocal('Handle must be at least 8 characters.');
      return;
    }
    const parent = st.parentDomain.replace(/^\./, '');
    const full = h.includes('.') ? h : `${h}.${parent}`;
    if (st.identities.some((i) => i.id === full)) {
      setErrLocal('Identity already in list.');
      return;
    }
    const signSeed = randomIdentitySeed();
    const enc = generateX25519Keypair();
    commit((p) => ({
      ...p,
      identities: [...p.identities, { id: full, signSeedB64: encodeStd(signSeed), encPrivB64: encodeStd(enc.privateKey) }],
      activeId: full,
    }));
    setHandle('');
    flashOk(`Created local keys for ${full}.`);
  };

  const registerWithRelay = async () => {
    const id = st.activeId;
    const ent = st.identities.find((i) => i.id === id);
    if (!ent?.encPrivB64) {
      flashErr('Identity needs encryption keys.');
      return;
    }
    if (!dnsToken.trim()) {
      flashErr('DNS token required for registration.');
      return;
    }
    setBusy(true);
    try {
      const seed = decodeStd(ent.signSeedB64);
      const encPriv = decodeStd(ent.encPrivB64);
      const pub = publicKeyToRelayForm(identityPublicKeyRaw(seed));
      const encPub = encodeRawURL(x25519PublicFromPrivate(encPriv));
      const issuedAt = new Date().toISOString().replace(/\.\d{3}Z$/, 'Z');
      const nonce = newAdminNonce();
      const relayAddr = relayHostPortFromUrl(st.relayUrl);
      const sig = signIdentityRegistrationPayload(seed, id, pub, encPub, relayAddr, issuedAt, nonce);
      await postIdentities(st.relayUrl, {
        identity: id,
        public_key: pub,
        encryption_public_key: encPub,
        dns_provider: dnsProvider,
        dns_token: dnsToken.trim(),
        issued_at: issuedAt,
        nonce,
        identity_signature: sig,
      });
      flashOk(`Registered ${id} with relay.`);
    } catch (e) {
      flashErr(e);
    } finally {
      setBusy(false);
    }
  };

  const publishEncryptionKey = async () => {
    const id = st.activeId;
    const ent = st.identities.find((i) => i.id === id);
    if (!ent) {
      flashErr('Select identity.');
      return;
    }
    if (!dnsToken.trim()) {
      flashErr('DNS token required.');
      return;
    }
    setBusy(true);
    try {
      const seed = decodeStd(ent.signSeedB64);
      let encPub: Uint8Array;
      let newPrivB64: string | undefined;
      if (ent.encPrivB64 && !encRotate) {
        const encPriv = decodeStd(ent.encPrivB64);
        encPub = x25519PublicFromPrivate(encPriv);
      } else {
        const enc = generateX25519Keypair();
        encPub = enc.publicKey;
        newPrivB64 = encodeStd(enc.privateKey);
      }
      const encPubStr = encodeRawURL(encPub);
      const issuedAt = new Date().toISOString().replace(/\.\d{3}Z$/, 'Z');
      const nonce = newAdminNonce();
      const sig = signEncryptionKeyPayload(seed, id, encPubStr, issuedAt, nonce);
      await postEncryptionKey(st.relayUrl, id, {
        encryption_public_key: encPubStr,
        dns_provider: dnsProvider,
        dns_token: dnsToken.trim(),
        issued_at: issuedAt,
        nonce,
        identity_signature: sig,
      });
      if (newPrivB64) {
        commit((p) => ({
          ...p,
          identities: p.identities.map((i) => (i.id === id ? { ...i, encPrivB64: newPrivB64 } : i)),
        }));
      }
      flashOk(`Published _eurything-enc for ${id}.`);
    } catch (e) {
      flashErr(e);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div>
      {errLocal ? <div className="err">{errLocal}</div> : null}
      <div className="card">
        <h2>Settings</h2>
        <label>Home relay URL</label>
        <input
          value={st.relayUrl}
          onChange={(e) =>
            commit((p) => ({
              ...p,
              relayUrl: e.target.value.trim(),
            }))
          }
        />
        <label>Parent domain (handles without dots)</label>
        <input
          value={st.parentDomain}
          onChange={(e) =>
            commit((p) => ({
              ...p,
              parentDomain: e.target.value.trim(),
            }))
          }
        />
        <label style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginTop: '0.75rem' }}>
          <input
            type="checkbox"
            checked={st.viaHomeRelay}
            onChange={(e) => commit((p) => ({ ...p, viaHomeRelay: e.target.checked }))}
            style={{ width: 'auto' }}
          />
          Via home relay for outbound sends
        </label>
      </div>

      <div className="card">
        <h2>Create identity</h2>
        <div className="row">
          <div>
            <label>Handle (min 8 chars)</label>
            <input value={handle} onChange={(e) => setHandle(e.target.value)} placeholder="myhandle" />
          </div>
          <div style={{ flex: '0 0 auto', alignSelf: 'flex-end' }}>
            <button type="button" className="primary" disabled={busy} onClick={addLocalIdentity}>
              Generate keys
            </button>
          </div>
        </div>
      </div>

      <div className="card">
        <h2>Identities</h2>
        {st.identities.length === 0 ? (
          <p className="sub">None yet.</p>
        ) : (
          <ul className="msg-list">
            {st.identities.map((i) => (
              <li key={i.id}>
                <button
                  type="button"
                  className="secondary"
                  style={{ marginRight: '0.5rem' }}
                  onClick={() => commit((p) => ({ ...p, activeId: i.id }))}
                >
                  {st.activeId === i.id ? '●' : '○'}
                </button>
                <code>{i.id}</code>
                {i.encPrivB64 ? <span className="badge">E2E</span> : null}
              </li>
            ))}
          </ul>
        )}
      </div>

      <div className="card">
        <h2>Register POST /identities</h2>
        <label>DNS provider</label>
        <select value={dnsProvider} onChange={(e) => setDnsProvider(e.target.value)}>
          <option value="cloudflare">cloudflare</option>
          <option value="hetzner">hetzner</option>
        </select>
        <label>DNS API token</label>
        <input value={dnsToken} onChange={(e) => setDnsToken(e.target.value)} type="password" autoComplete="off" />
        <button type="button" className="primary" style={{ marginTop: '0.75rem' }} disabled={busy || !st.activeId} onClick={() => void registerWithRelay()}>
          Register
        </button>
      </div>

      <div className="card">
        <h2>Encryption key POST /identities/…/encryption-key</h2>
        <p className="sub">Publishes <code>_eurything-enc</code> (CLI <code>identity add-encryption-key</code>).</p>
        <label style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
          <input type="checkbox" checked={encRotate} onChange={(e) => setEncRotate(e.target.checked)} style={{ width: 'auto' }} />
          Rotate (overwrite local encryption private key)
        </label>
        <button type="button" className="secondary" style={{ marginTop: '0.5rem' }} disabled={busy || !st.activeId} onClick={() => void publishEncryptionKey()}>
          Publish encryption key
        </button>
      </div>
    </div>
  );
}
