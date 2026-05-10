import type { SessionBundle } from '../crypto/sessionRegister';

export const LS_KEY = 'eurything.web.v1';

export type JournalEntry = {
  messageId: string;
  recipient: string;
  state: 'queued' | 'tick1' | 'tick2' | 'failed';
  detail?: string;
};

export type IdentityEntry = {
  id: string;
  signSeedB64: string;
  encPrivB64?: string;
};

export type WebPersisted = {
  relayUrl: string;
  parentDomain: string;
  viaHomeRelay: boolean;
  activeId: string;
  identities: IdentityEntry[];
  sessions: Record<string, SessionBundle>;
  journal: JournalEntry[];
};

const defaultState: WebPersisted = {
  relayUrl: 'http://127.0.0.1:8080',
  parentDomain: 'poweur.net',
  viaHomeRelay: false,
  activeId: '',
  identities: [],
  sessions: {},
  journal: [],
};

export function loadState(): WebPersisted {
  try {
    const raw = localStorage.getItem(LS_KEY);
    if (!raw) return { ...defaultState };
    const j = JSON.parse(raw) as WebPersisted;
    return { ...defaultState, ...j, identities: j.identities ?? [], sessions: j.sessions ?? {}, journal: j.journal ?? [] };
  } catch {
    return { ...defaultState };
  }
}

export function saveState(s: WebPersisted): void {
  localStorage.setItem(LS_KEY, JSON.stringify(s));
}
