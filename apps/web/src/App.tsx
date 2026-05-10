import { useCallback, useMemo, useState } from 'react';
import { registerSession, sessionValid, type SessionBundle } from './crypto/sessionRegister';
import { loadState, saveState, type WebPersisted } from './state/storage';
import { decodeStd } from './util/base64';
import { IdentitiesTab } from './ui/IdentitiesTab';
import { MessagingTab } from './ui/MessagingTab';
import { SessionsTab } from './ui/SessionsTab';
import { RelayTab } from './ui/RelayTab';
import { AuthTab } from './ui/AuthTab';
import { DnsTab } from './ui/DnsTab';

type Tab = 'overview' | 'identities' | 'messaging' | 'sessions' | 'relay' | 'auth' | 'dns';

export default function App() {
  const [tab, setTab] = useState<Tab>('overview');
  const [st, setSt] = useState<WebPersisted>(loadState);
  const [err, setErr] = useState<string | null>(null);
  const [ok, setOk] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const commit = useCallback((fn: (p: WebPersisted) => WebPersisted) => {
    setSt((prev) => {
      const n = fn(prev);
      saveState(n);
      return n;
    });
  }, []);

  const active = useMemo(
    () => st.identities.find((i) => i.id === st.activeId),
    [st.identities, st.activeId],
  );

  const flashErr = useCallback((e: unknown) => {
    setOk(null);
    setErr(e instanceof Error ? e.message : String(e));
  }, []);

  const flashOk = useCallback((m: string) => {
    setErr(null);
    setOk(m);
  }, []);

  const ensureSession = useCallback(async (): Promise<SessionBundle> => {
    const p = loadState();
    const id = p.activeId;
    const relay = p.relayUrl.trim();
    if (!id) throw new Error('Select an identity first.');
    const ent = p.identities.find((i) => i.id === id);
    if (!ent) throw new Error('Identity not found.');
    const seed = decodeStd(ent.signSeedB64);
    const cur = p.sessions[id];
    if (sessionValid(cur, relay)) return cur!;
    const bundle = await registerSession(relay, id, seed);
    setSt((prev) => {
      const n = { ...prev, sessions: { ...prev.sessions, [id]: bundle } };
      saveState(n);
      return n;
    });
    return bundle;
  }, []);

  return (
    <>
      <header className="header-bar">
        <h1>Eurything Web</h1>
        {st.activeId ? <span className="identity-pill">{st.activeId}</span> : null}
      </header>
      <p className="sub">
        Relay client with local Ed25519/X25519 keys. Aligns with the CLI and mobile requirements: identities, E2E messaging, sessions, DNS discovery, and
        auth signing.
      </p>

      <nav className="tabs">
        {(
          [
            ['overview', 'Overview'],
            ['identities', 'Identities'],
            ['messaging', 'Messaging'],
            ['sessions', 'Sessions'],
            ['relay', 'Relay'],
            ['auth', 'Auth'],
            ['dns', 'DNS'],
          ] as const
        ).map(([k, label]) => (
          <button key={k} type="button" className={tab === k ? 'active' : ''} onClick={() => setTab(k)}>
            {label}
          </button>
        ))}
      </nav>

      {err ? <div className="err">{err}</div> : null}
      {ok ? <div className="ok">{ok}</div> : null}

      {tab === 'overview' ? (
        <div className="card">
          <h2>Welcome</h2>
          <p style={{ color: 'var(--muted)', fontSize: '0.9rem', lineHeight: 1.55 }}>
            Use <strong>Identities</strong> to generate keys and optionally call <code>POST /identities</code>. Configure your home relay, then send and poll
            under <strong>Messaging</strong>. Delivery ticks mirror the CLI journal (✓ relay, ✓✓ client decrypt ack).
          </p>
          <div className="passkey-note">
            <strong style={{ color: 'var(--text)' }}>Passkeys.</strong> The web app keeps signing keys in <code>localStorage</code> for protocol
            compatibility. Hardware passkeys (Secure Enclave) are planned for native apps; they cannot yet replace raw Ed25519 signing for these canonical
            strings without relay changes.
          </div>
        </div>
      ) : null}

      {tab === 'identities' ? (
        <IdentitiesTab st={st} commit={commit} busy={busy} setBusy={setBusy} flashErr={flashErr} flashOk={flashOk} />
      ) : null}
      {tab === 'messaging' ? (
        <MessagingTab
          st={st}
          commit={commit}
          busy={busy}
          setBusy={setBusy}
          flashErr={flashErr}
          flashOk={flashOk}
          ensureSession={ensureSession}
        />
      ) : null}
      {tab === 'sessions' ? (
        <SessionsTab st={st} commit={commit} busy={busy} setBusy={setBusy} flashErr={flashErr} flashOk={flashOk} />
      ) : null}
      {tab === 'relay' ? <RelayTab st={st} busy={busy} setBusy={setBusy} flashErr={flashErr} flashOk={flashOk} /> : null}
      {tab === 'auth' ? <AuthTab active={active} /> : null}
      {tab === 'dns' ? <DnsTab flashErr={flashErr} flashOk={flashOk} /> : null}
    </>
  );
}
