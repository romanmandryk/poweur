import { useState } from 'react';
import { decryptFromSender, encryptForRecipient } from '../crypto/e2e';
import { MSG_ALG } from '../crypto/protocol';
import { newAckID, newMessageID } from '../crypto/ids';
import { decodeSessionSeed, registerSession, sessionProofFrom, type SessionBundle } from '../crypto/sessionRegister';
import { signAckWire, signMessagePayload, signUtf8 } from '../crypto/signing';
import { lookupDns, parseEncryptionKeyFromTxt } from '../dns/doh';
import { fetchChallenge, fetchInbox, postAckAbsolute, postMessageAbsolute, SessionExpiredError } from '../relay/client';
import { recipientRelayBase } from '../relay/recipientRelay';
import type { JournalEntry, WebPersisted } from '../state/storage';
import { loadState } from '../state/storage';
import { decodeStd } from '../util/base64';

function tickClass(state: JournalEntry['state']): string {
  if (state === 'tick1') return 'tick1';
  if (state === 'tick2') return 'tick2';
  return '';
}

function tickLabel(state: JournalEntry['state']): string {
  switch (state) {
    case 'queued':
      return '·';
    case 'tick1':
      return '✓';
    case 'tick2':
      return '✓✓';
    case 'failed':
      return '✗';
    default:
      return '';
  }
}

async function emitAck(
  p: WebPersisted,
  sess: SessionBundle,
  messageId: string,
  originalSender: string,
  originalRecipient: string,
) {
  const senderRelay = await recipientRelayBase(originalSender, p.relayUrl);
  const ts = new Date().toISOString().replace(/\.\d{3}Z$/, 'Z');
  const ackId = newAckID();
  const ack = {
    type: 'ack',
    id: ackId,
    message_id: messageId,
    state: 'delivered_client',
    sender: originalRecipient,
    recipient: originalSender,
    timestamp: ts,
    session_id: sess.sessionId,
    session_proof: sessionProofFrom(sess),
    signature: '',
  };
  ack.signature = signAckWire(
    decodeSessionSeed(sess),
    ack.id,
    ack.message_id,
    ack.state,
    ack.sender,
    ack.recipient,
    ack.timestamp,
    ack.session_id,
  );
  await postAckAbsolute(senderRelay, ack);
}

export function MessagingTab({
  st,
  commit,
  busy,
  setBusy,
  flashErr,
  flashOk,
  ensureSession,
}: {
  st: WebPersisted;
  commit: (fn: (p: WebPersisted) => WebPersisted) => void;
  busy: boolean;
  setBusy: (b: boolean) => void;
  flashErr: (e: unknown) => void;
  flashOk: (m: string) => void;
  ensureSession: () => Promise<SessionBundle>;
}) {
  const [recipient, setRecipient] = useState('');
  const [body, setBody] = useState('');
  const [signWith, setSignWith] = useState<'session' | 'identity'>('session');
  const [inboxLines, setInboxLines] = useState<string[]>([]);
  const [ackLines, setAckLines] = useState<string[]>([]);

  const sendMessage = async () => {
    setBusy(true);
    try {
      const p = loadState();
      const id = p.activeId;
      const ent = p.identities.find((i) => i.id === id);
      if (!ent) throw new Error('No active identity.');
      const to = recipient.trim();
      const plaintext = new TextEncoder().encode(body);
      if (!to || !body.trim()) throw new Error('Recipient and message required.');
      const dns = await lookupDns(to);
      const encPub = parseEncryptionKeyFromTxt(dns.encryptionKeyTXT);
      if (!encPub) throw new Error('Recipient has no _eurything-enc TXT.');
      const sealed = encryptForRecipient(encPub, plaintext);
      const enc = {
        alg: MSG_ALG,
        ephemeral_public_key: sealed.ephemeral_public_key,
        nonce: sealed.nonce,
      };
      const msgId = newMessageID();
      const ts = new Date().toISOString().replace(/\.\d{3}Z$/, 'Z');
      const idSeedFull = decodeStd(ent.signSeedB64);
      let sess: SessionBundle | undefined;
      let sessionId = '';
      let proof = undefined;
      let signingKey = idSeedFull;
      if (signWith === 'session') {
        sess = await ensureSession();
        sessionId = sess.sessionId;
        proof = sessionProofFrom(sess);
        signingKey = decodeSessionSeed(sess);
      }
      const sig = signMessagePayload(signingKey, id, to, ts, sealed.ciphertext, msgId, sessionId, enc);
      const msg = {
        id: msgId,
        sender: id,
        recipient: to,
        timestamp: ts,
        payload: sealed.ciphertext,
        signature: sig,
        session_id: sessionId || undefined,
        session_proof: proof,
        encryption: enc,
      };
      const target = p.viaHomeRelay ? p.relayUrl.trim() : await recipientRelayBase(to, p.relayUrl);
      await postMessageAbsolute(target, msg);
      commit((prev) => ({
        ...prev,
        journal: [
          ...prev.journal.filter((j) => j.messageId !== msgId),
          { messageId: msgId, recipient: to, state: 'tick1' },
        ],
      }));
      setBody('');
      flashOk(`Sent ${msgId} (✓).`);
    } catch (e) {
      flashErr(e);
    } finally {
      setBusy(false);
    }
  };

  const pullInbox = async () => {
    setBusy(true);
    setInboxLines([]);
    setAckLines([]);
    try {
      const p = loadState();
      const id = p.activeId;
      const ent = p.identities.find((i) => i.id === id);
      if (!ent) throw new Error('No active identity.');
      const idSeed = decodeStd(ent.signSeedB64);
      const encPriv = ent.encPrivB64 ? decodeStd(ent.encPrivB64) : null;
      let sess = await ensureSession();
      const challengeOnce = async (s: SessionBundle) => {
        const ch = await fetchChallenge(p.relayUrl, id);
        return signUtf8(decodeSessionSeed(s), ch.challenge);
      };
      let sig = await challengeOnce(sess);
      let inbox;
      try {
        inbox = await fetchInbox(p.relayUrl, id, sig, sess.sessionId);
      } catch (e) {
        if (e instanceof SessionExpiredError) {
          const bundle = await registerSession(p.relayUrl, id, idSeed);
          commit((prev) => ({ ...prev, sessions: { ...prev.sessions, [id]: bundle } }));
          sess = bundle;
          sig = await challengeOnce(sess);
          inbox = await fetchInbox(p.relayUrl, id, sig, sess.sessionId);
        } else {
          throw e;
        }
      }
      const lines: string[] = [];
      for (const m of inbox.messages ?? []) {
        let text = m.payload;
        let decrypted = false;
        try {
          if (m.encryption && encPriv) {
            const pt = decryptFromSender(encPriv, {
              ciphertext: m.payload,
              ephemeral_public_key: m.encryption.ephemeral_public_key,
              nonce: m.encryption.nonce,
            });
            text = new TextDecoder().decode(pt);
            decrypted = true;
          }
        } catch (de) {
          text = `[decrypt error: ${de}]`;
        }
        lines.push(`${m.timestamp} ${m.sender}: ${text}`);
        if (decrypted && m.id && m.sender) {
          try {
            await emitAck(p, sess, m.id, m.sender, m.recipient || id);
          } catch {
            lines.push(`  (ack failed for ${m.id})`);
          }
        }
      }
      setInboxLines(lines);
      const al: string[] = [];
      for (const a of inbox.acks ?? []) {
        al.push(`${a.timestamp} ${a.state} msg=${a.message_id} peer=${a.sender}`);
        commit((prev) => ({
          ...prev,
          journal: prev.journal.map((row): JournalEntry =>
            row.messageId === a.message_id && a.state === 'delivered_client' ? { ...row, state: 'tick2' } : row,
          ),
        }));
      }
      setAckLines(al);
      flashOk(`Drained ${lines.length} messages, ${al.length} acks.`);
    } catch (e) {
      flashErr(e);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div>
      <div className="card">
        <h2>Send</h2>
        <label>Recipient</label>
        <input value={recipient} onChange={(e) => setRecipient(e.target.value)} placeholder="bob.example.com" />
        <label>Message</label>
        <textarea value={body} onChange={(e) => setBody(e.target.value)} />
        <label>Sign with</label>
        <select value={signWith} onChange={(e) => setSignWith(e.target.value as 'session' | 'identity')}>
          <option value="session">Session</option>
          <option value="identity">Identity</option>
        </select>
        <button type="button" className="primary" style={{ marginTop: '0.6rem' }} disabled={busy} onClick={() => void sendMessage()}>
          Send
        </button>
      </div>

      <div className="card">
        <h2>Inbox</h2>
        <button type="button" className="secondary" disabled={busy} onClick={() => void pullInbox()}>
          Poll
        </button>
        {inboxLines.length > 0 ? (
          <ul className="msg-list" style={{ marginTop: '0.75rem' }}>
            {inboxLines.map((l, i) => (
              <li key={i}>{l}</li>
            ))}
          </ul>
        ) : null}
        {ackLines.length > 0 ? (
          <>
            <h3 style={{ fontSize: '0.85rem', color: 'var(--muted)', marginTop: '1rem' }}>Acks</h3>
            <ul className="msg-list">
              {ackLines.map((l, i) => (
                <li key={i} className="tick2">
                  {l}
                </li>
              ))}
            </ul>
          </>
        ) : null}
      </div>

      <div className="card">
        <h2>Outbound journal</h2>
        {st.journal.length === 0 ? (
          <p className="sub">Empty.</p>
        ) : (
          <ul className="msg-list">
            {st.journal.map((j) => (
              <li key={j.messageId} className={tickClass(j.state)}>
                {tickLabel(j.state)} {j.messageId} → {j.recipient} ({j.state})
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}
