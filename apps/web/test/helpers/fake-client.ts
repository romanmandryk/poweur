import { vi } from "vitest";

/**
 * A `PoweurClient`-shaped fake covering what the messaging and contacts
 * actions call. Every method is a spy with a quiet default; tests override
 * what they assert on.
 */
export function fakeClient(overrides: Record<string, unknown> = {}) {
  const store = {
    load: vi.fn(async () => [] as any[]),
    putReadState: vi.fn(async () => {}),
    readState: vi.fn(async () => ({ conversations: {} as Record<string, unknown> })),
    markConversationRead: vi.fn(async (peer: string, records: any[]) => {
      const last = records.at(-1);
      return { conversations: { [peer]: { timestamp: last?.timestamp, id: last?.id ?? "" } } };
    }),
  };
  const contactsApi = {
    load: vi.fn(async () => ({ contacts: [] as any[] })),
    checkPin: vi.fn(async () => ({ status: "unpinned" }) as any),
    set: vi.fn(async () => {}),
    repin: vi.fn(async () => {}),
    remove: vi.fn(async () => {}),
  };
  let sent = 0;
  const requests = vi.fn(async () => [] as any[]);
  const systemFiles = {
    get: vi.fn(async () => null as any),
    readOptional: vi.fn(async () => null as string | null),
    write: vi.fn(async () => '"etag"'),
    writeJson: vi.fn(async () => '"etag"'),
    remove: vi.fn(async () => true),
  };
  const deviceRegistry = {
    list: vi.fn(async () => ({ identity: "alice.poweur.net", devices: [] as any[] })),
    revoke: vi.fn(async () => ({ device_id: "", sessions_revoked: 0 })),
  };
  return {
    identity: "alice.poweur.net",
    signer: {},
    relay: {},
    decryptor: null as any,
    store,
    contactsApi,
    systemFiles,
    system: vi.fn(() => systemFiles),
    consentLog: vi.fn(async () => ({ append: vi.fn(async () => {}), recent: vi.fn(async () => []) })),
    devices: vi.fn(() => deviceRegistry),
    history: vi.fn(async () => store),
    contacts: vi.fn(async () => contactsApi),
    processContactAccepts: vi.fn(async () => [] as string[]),
    inboxAndArchive: vi.fn(async () => ({ messages: [] as any[], acks: [] as any[], lost: 0 })),
    requests,
    requestsAndArchive: vi.fn(async () => ({ requests: await requests(), archived: 0, lost: 0 })),
    anonAndArchive: vi.fn(async () => ({ messages: [] as any[], lost: 0 })),
    policy: vi.fn(async () => ({ policy: { version: 1, mode: "open" } as any, explicit: false })),
    profile: vi.fn(async () => ({ profile: { version: 1 } as any, explicit: false })),
    groups: { roster: vi.fn(async (): Promise<any> => Promise.reject(new Error("not a group"))) },
    messages: { ack: vi.fn(async () => {}) },
    sendAndArchive: vi.fn(async (to: string) => ({
      message: { id: `sent-${++sent}`, recipient: to, timestamp: new Date(Date.now() + sent).toISOString() },
      lost: 0,
    })),
    sendGroupAndArchive: vi.fn(),
    acceptContact: vi.fn(async () => ({ notified: true })),
    blockContact: vi.fn(async () => {}),
    requestContact: vi.fn(async () => {}),
    sessions: { ensure: vi.fn(async () => ({})) },
    ...overrides,
  };
}

export type FakeClient = ReturnType<typeof fakeClient>;

/** An inbound message in the shape the SDK decrypts to. */
export function inbound(sender: string, plaintext: string, at: number, extra: Record<string, unknown> = {}) {
  return {
    id: `m-${sender}-${at}`,
    sender,
    recipient: "alice.poweur.net",
    timestamp: new Date(Date.UTC(2026, 8, 15, 10, 0, at)).toISOString(),
    type: "chat.text",
    plaintext,
    ...extra,
  };
}
