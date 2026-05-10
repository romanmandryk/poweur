import { useState } from 'react';
import { lookupDns } from '../dns/doh';

export function DnsTab({ flashErr, flashOk }: { flashErr: (e: unknown) => void; flashOk: (m: string) => void }) {
  const [id, setId] = useState('');
  const [txt, setTxt] = useState('');
  const run = async () => {
    setTxt('');
    const v = id.trim();
    if (!v) {
      flashErr('Identity required');
      return;
    }
    try {
      const d = await lookupDns(v);
      setTxt(JSON.stringify(d, null, 2));
      flashOk('DNS lookup complete');
    } catch (e) {
      flashErr(e);
    }
  };
  return (
    <div className="card">
      <h2>DNS lookup</h2>
      <p className="sub">Browser DoH (Cloudflare) — same records the CLI resolves.</p>
      <label>Identity</label>
      <input value={id} onChange={(e) => setId(e.target.value)} placeholder="alice.example.com" />
      <button type="button" className="primary" style={{ marginTop: '0.5rem' }} onClick={() => void run()}>
        Lookup
      </button>
      {txt ? (
        <pre style={{ marginTop: '0.75rem', fontSize: '0.78rem', overflow: 'auto' }}>{txt}</pre>
      ) : null}
    </div>
  );
}
