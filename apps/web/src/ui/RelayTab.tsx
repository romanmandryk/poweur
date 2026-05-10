import { useState } from 'react';
import { fetchHealth } from '../relay/client';
import type { WebPersisted } from '../state/storage';

export function RelayTab({
  st,
  busy,
  setBusy,
  flashErr,
  flashOk,
}: {
  st: WebPersisted;
  busy: boolean;
  setBusy: (b: boolean) => void;
  flashErr: (e: unknown) => void;
  flashOk: (m: string) => void;
}) {
  const [out, setOut] = useState<string>('');
  const run = async () => {
    setBusy(true);
    setOut('');
    try {
      const h = await fetchHealth(st.relayUrl);
      setOut(JSON.stringify(h, null, 2));
      flashOk(`Relay ${h.status} ${h.version}`);
    } catch (e) {
      flashErr(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="card">
      <h2>Relay status</h2>
      <p className="sub">GET /health on your configured home relay.</p>
      <button type="button" className="primary" disabled={busy} onClick={() => void run()}>
        Check
      </button>
      {out ? (
        <pre style={{ marginTop: '0.75rem', fontSize: '0.8rem' }}>{out}</pre>
      ) : null}
    </div>
  );
}
