import { useState } from 'react';
import { signBytes } from '../crypto/signing';
import type { IdentityEntry } from '../state/storage';
import { decodeStd } from '../util/base64';

export function AuthTab({ active }: { active: IdentityEntry | undefined }) {
  const [rawInput, setRawInput] = useState('{"challenge":"example"}');
  const [out, setOut] = useState('');
  const sign = () => {
    if (!active) {
      setOut('Select identity.');
      return;
    }
    const seed = decodeStd(active.signSeedB64);
    const signatureB64 = signBytes(seed, new TextEncoder().encode(rawInput));
    const issued = new Date().toISOString();
    setOut(JSON.stringify({ identity: active.id, issued_at: issued, signature: signatureB64 }, null, 2));
  };
  return (
    <div className="card">
      <h2>Auth sign</h2>
      <p className="sub">Sign UTF-8 payload bytes (CLI <code>eurything auth sign</code>).</p>
      <label>Payload</label>
      <textarea value={rawInput} onChange={(e) => setRawInput(e.target.value)} />
      <button type="button" className="primary" style={{ marginTop: '0.5rem' }} onClick={sign} disabled={!active}>
        Sign
      </button>
      {out ? (
        <pre style={{ marginTop: '0.75rem', fontSize: '0.8rem', whiteSpace: 'pre-wrap' }}>{out}</pre>
      ) : null}
    </div>
  );
}
