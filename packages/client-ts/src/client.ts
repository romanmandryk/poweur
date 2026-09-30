/**
 * `PoweurClient` — the one object most callers need. It wires a relay URL, a
 * `Signer` and (optionally) a `Decryptor` into the per-area modules so a
 * bot is a few lines rather than a lot of plumbing.
 *
 * Everything it composes is usable standalone; this is convenience, not a
 * layer you have to go through.
 */

import { readAnalyticsPreference, writeAnalyticsPreference } from "./analytics.js";
import type { Decryptor, Signer } from "./crypto/keys.js";
import { PoweurError } from "./errors.js";
import { Contacts, fetchRequests } from "./contacts.js";
import { RelayClient, type RelayClientOptions } from "./http.js";
import { EnrollApi } from "./enroll.js";
import {
  HISTORY_QUEUE_ANONYMOUS,
  HISTORY_QUEUE_INBOX,
  HISTORY_QUEUE_REQUESTS,
  HISTORY_QUEUE_SENT,
  MessageHistory,
  type HistoryRecord,
} from "./history.js";
import { IdentityApi } from "./identity.js";
import { GroupMessaging, type GroupSendOptions, type GroupSendResult } from "./groups.js";
import { KeystoreApi } from "./keystore.js";
import { Messaging, type SendOptions, type SendResult } from "./messages.js";
import { readInboxPolicy, writeInboxPolicy } from "./policy.js";
import { MSG_TYPE_AUTH_REQUEST } from "./msgtypes.js";
import { readProfile, writeProfile } from "./profile.js";
import type { ResolveOptions } from "./resolve.js";
import { MemorySessionStore, SessionManager, type SessionStore } from "./session.js";
import { DriveClient } from "./drive/client.js";
import { DriveFiles } from "./drive/files.js";
import { DriveJsonLog } from "./drive/jsonlog.js";

/** Where the sign-in consent log lives (EPIC-008 E08-T3). */
export const AUTH_LOG_PATH = ".poweur/private/logs/auth.log";
import { DeviceRegistry, SystemFiles } from "./systemfiles.js";
import {
  CONTACT_ACCEPTED,
  CONTACT_BLOCKED,
  CONTACT_REQUESTED,
  MSG_TYPE_CONTACT_ACCEPT,
  MSG_TYPE_CONTACT_REQUEST,
  type Ack,
  type AnonymousPolicy,
  type Contact,
  type ContactRequestEntry,
  type InboxMessage,
  type InboxMode,
  type InboxPolicy,
  type Profile,
} from "./types.js";

export interface PoweurClientOptions extends RelayClientOptions {
  /** Home relay base URL, e.g. https://poweur.net */
  relayUrl: string;
  signer: Signer;
  /** Needed to read encrypted inbound messages. */
  decryptor?: Decryptor | null;
  sessionStore?: SessionStore;
  resolve?: ResolveOptions;
}

export class PoweurClient {
  async analyticsPreference() { return readAnalyticsPreference(this.system()); }
  async setAnalyticsConsent(granted: boolean) { return writeAnalyticsPreference(this.system(), granted); }
  readonly relay: RelayClient;
  readonly signer: Signer;
  readonly decryptor: Decryptor | null;
  readonly identity: IdentityApi;
  /** Wrapped seed copies, one per enrolled authenticator (EPIC-011 E11-T1). */
  readonly keystore: KeystoreApi;
  /** The new-device enrollment rendezvous (EPIC-011 E11-T3). */
  readonly enroll: EnrollApi;
  readonly sessions: SessionManager;
  readonly messages: Messaging;
  readonly groups: GroupMessaging;
  readonly #resolveOptions: ResolveOptions;
  #system: SystemFiles | null = null;

  constructor(options: PoweurClientOptions) {
    const { relayUrl, signer, decryptor, sessionStore, resolve, ...clientOptions } = options;
    this.relay = new RelayClient(relayUrl, clientOptions);
    this.signer = signer;
    this.decryptor = decryptor ?? null;
    this.#resolveOptions = resolve ?? {};
    this.identity = new IdentityApi(this.relay);
    this.keystore = new KeystoreApi(this.relay);
    this.enroll = new EnrollApi(this.relay);
    this.sessions = new SessionManager(this.relay, sessionStore ?? new MemorySessionStore());
    this.messages = new Messaging({
      client: this.relay,
      sessions: this.sessions,
      resolve: this.#resolveOptions,
    });
    this.groups = new GroupMessaging({ client: this.relay, resolve: this.#resolveOptions });
  }

  get identityName(): string {
    return this.signer.identity;
  }

  /** Send an encrypted message (session-signed by default). */
  send(recipient: string, plaintext: string, options: SendOptions = {}): Promise<SendResult> {
    return this.messages.send(this.signer, recipient, plaintext, options);
  }

  /** Send one per-member-encrypted message through an addressable group. */
  sendGroup(group: string, plaintext: string, options: GroupSendOptions = {}): Promise<GroupSendResult> {
    return this.groups.send(this.signer, group, plaintext, options);
  }

  /** Send to a group and archive one sender-side conversation record. */
  async sendGroupAndArchive(group: string, plaintext: string, options: GroupSendOptions = {}) {
    const result = await this.sendGroup(group, plaintext, options);
    const first = result.envelopes[0]!;
    const { archived, lost } = await this.archive([{
      id: first.id,
      sender: this.signer.identity,
      recipient: result.group.group,
      timestamp: first.timestamp,
      ...(options.type ? { type: options.type } : {}),
      thread_id: first.thread_id,
      queue: HISTORY_QUEUE_SENT,
      body: plaintext,
    }]);
    return { ...result, archived, lost };
  }

  /** Fetch and decrypt the inbox. */
  inbox(): Promise<{ messages: InboxMessage[]; acks: Ack[] }> {
    return this.messages.inbox(this.signer, this.decryptor);
  }

  /**
   * The message archive in `.poweur/private/` (EPIC-009 E09-T1; storage in
   * EPIC-020 E20-T11).
   *
   * Requires a decryptor: history is sealed to the identity's own encryption
   * key, so a client that cannot read messages cannot keep them either.
   */
  async history(): Promise<MessageHistory> {
    if (!this.decryptor) {
      throw new PoweurError("invalid_argument", "message history needs a decryptor to seal to");
    }
    // One archive per client: it remembers the folders it resolved and how
    // far it has read each conversation, so a refresh reads only what is new.
    this.#history ??= (async () => {
      const encryptionPrivateKey = await this.decryptor!.privateKeyBytes();
      const signBytes = this.signer.signBytes?.bind(this.signer);
      const files = signBytes
        ? new DriveFiles(new DriveClient(this.relay, this.signer), { sign: signBytes, encryptionPrivateKey })
        : undefined;
      return new MessageHistory(this.signer.identity, this.decryptor!, files);
    })();
    try {
      return await this.#history;
    } catch (error) {
      this.#history = undefined;
      throw error;
    }
  }
  #history?: Promise<MessageHistory>;

  /**
   * The sign-in consent log (EPIC-008 E08-T3): every approval this identity
   * gave, end-to-end encrypted on its own drive at `.poweur/private/logs/auth.log`.
   */
  async consentLog(): Promise<DriveJsonLog<Record<string, unknown>>> {
    if (!this.decryptor) throw new PoweurError("invalid_argument", "the consent log needs the identity's encryption key");
    const signBytes = this.signer.signBytes?.bind(this.signer);
    if (!signBytes) throw new PoweurError("unsupported", "the consent log needs a signer for the drive");
    const files = new DriveFiles(new DriveClient(this.relay, this.signer), { sign: signBytes, encryptionPrivateKey: await this.decryptor.privateKeyBytes() });
    return new DriveJsonLog(files, AUTH_LOG_PATH);
  }

  /**
   * Archive whatever just came off a queue, best-effort.
   *
   * Never allowed to throw: the pickup already drained the relay, so failing
   * the caller here would lose the message twice. `archived`/`lost` say what
   * happened so a UI can warn rather than pretend.
   */
  async archive(records: HistoryRecord[]): Promise<{ archived: number; lost: number }> {
    if (!records.length) return { archived: 0, lost: 0 };
    try {
      const history = await this.history();
      const { written, failed } = await history.appendAll(records);
      return { archived: written, lost: failed };
    } catch {
      return { archived: 0, lost: records.length };
    }
  }

  /**
   * `inboxAndAck` plus the archive write — the call a durable client wants.
   *
   * Reading and keeping are one step on purpose. Any gap between them is a
   * window where the relay has forgotten a message and nothing has written it
   * down, and a client that offers the two separately will eventually take it.
   */
  async inboxAndArchive(): Promise<{
    messages: InboxMessage[];
    acks: Ack[];
    acked: string[];
    archived: number;
    lost: number;
  }> {
    const { messages, acks, acked } = await this.inboxAndAck();
    const { archived, lost } = await this.archive(
      messages
        // A sign-in prompt is a notification, not conversation: never archived.
        .filter((m) => m.plaintext !== null && m.type !== MSG_TYPE_AUTH_REQUEST)
        .map((m) => ({
          id: m.id,
          sender: m.sender ?? "",
          recipient: m.recipient || this.signer.identity,
          timestamp: m.timestamp,
          ...(m.type ? { type: m.type } : {}),
          ...(m.thread_id ? { thread_id: m.thread_id } : {}),
          ...(m.expires_at ? { expires_at: m.expires_at } : {}),
          ...(m.metadata ? { metadata: m.metadata } : {}),
          queue: HISTORY_QUEUE_INBOX as typeof HISTORY_QUEUE_INBOX,
          body: m.plaintext ?? "",
        })),
    );
    return { messages, acks, acked, archived, lost };
  }

  /** Send, then keep our own copy — the relay never hands a sender one back. */
  async sendAndArchive(
    recipient: string,
    plaintext: string,
    options: SendOptions = {},
  ): Promise<SendResult & { archived: number; lost: number }> {
    const result = await this.send(recipient, plaintext, options);
    const { archived, lost } = await this.archive([
      {
        id: result.message.id,
        sender: this.signer.identity,
        recipient: result.message.recipient,
        timestamp: result.message.timestamp,
        ...(options.type ? { type: options.type } : {}),
        ...(options.threadId ? { thread_id: options.threadId } : {}),
        ...(options.expiresAt ? { expires_at: options.expiresAt } : {}),
        ...(options.metadata ? { metadata: options.metadata } : {}),
        queue: HISTORY_QUEUE_SENT,
        body: plaintext,
      },
    ]);
    return { ...result, archived, lost };
  }

  /**
   * Drain the anonymous queue and keep it. Anonymous records carry no sender:
   * the archive must not invent a name the relay could not verify.
   */
  async anonAndArchive(): Promise<{ messages: Awaited<ReturnType<PoweurClient["anon"]>>; archived: number; lost: number }> {
    const messages = await this.anon();
    const { archived, lost } = await this.archive(
      messages
        .filter((m) => m.plaintext !== null)
        .map((m) => ({
          id: m.id,
          recipient: this.signer.identity,
          timestamp: m.timestamp,
          queue: HISTORY_QUEUE_ANONYMOUS as typeof HISTORY_QUEUE_ANONYMOUS,
          body: m.plaintext ?? "",
        })),
    );
    return { messages, archived, lost };
  }

  /**
   * Fetch the inbox and emit a tick-2 receipt for every message that actually
   * decrypted — proof it reached a client, not just a relay. Messages we
   * could not decrypt stay at tick 1 on the sender's side, deliberately.
   */
  async inboxAndAck(): Promise<{ messages: InboxMessage[]; acks: Ack[]; acked: string[] }> {
    const { messages, acks } = await this.inbox();
    const acked: string[] = [];
    for (const message of messages) {
      if (message.plaintext === null || !message.id || !message.sender) continue;
      try {
        await this.messages.ack(this.signer, {
          id: message.id,
          sender: message.sender,
          ...(message.recipient ? { recipient: message.recipient } : {}),
        });
        acked.push(message.id);
      } catch {
        // A failed receipt must not lose the message the caller just read.
      }
    }
    return { messages, acks, acked };
  }

  /** Our own system files (.poweur/...): contacts, policy, profile, avatar. */
  system(): SystemFiles {
    this.#system ??= new SystemFiles(this.relay, this.signer);
    return this.#system;
  }

  /** Our device registry: list and revoke. */
  devices(): DeviceRegistry {
    return new DeviceRegistry(this.system());
  }

  async contacts(): Promise<Contacts> {
    return new Contacts(this.system(), this.#resolveOptions);
  }

  /** Pending contact requests, with their intros decrypted when we can. */
  async requests(): Promise<ContactRequestEntry[]> {
    const requests = await fetchRequests(this.relay, this.signer, this.decryptor);
    // Never discard a drained answer if its contact write fails. Callers can retry it.
    await this.processContactAccepts(requests).catch(() => []);
    return requests;
  }

  #contactAccepts: Promise<string[]> = Promise.resolve([]);

  /** Also usable when replaying archived answers after an interrupted handshake. */
  async processContactAccepts(entries: { type?: string; sender?: string }[]): Promise<string[]> {
    const senders = entries.filter((entry) => entry.type === MSG_TYPE_CONTACT_ACCEPT && entry.sender)
      .map((entry) => entry.sender!);
    if (!senders.length) return [];
    const previous = this.#contactAccepts;
    this.#contactAccepts = previous.catch(() => []).then(async () =>
      (await this.contacts()).promoteAccepted(senders));
    return this.#contactAccepts;
  }

  /** Drain pending consent gestures and durably archive decrypted entries. */
  async requestsAndArchive(): Promise<{
    requests: ContactRequestEntry[];
    archived: number;
    lost: number;
  }> {
    const requests = await fetchRequests(this.relay, this.signer, this.decryptor);
    const { archived, lost } = await this.archive(
      requests
        .filter((entry) => entry.plaintext !== null && entry.plaintext !== undefined)
        .map((entry) => ({
          id: entry.id,
          sender: entry.sender,
          recipient: entry.recipient || this.signer.identity,
          timestamp: entry.timestamp,
          ...(entry.type ? { type: entry.type } : {}),
          ...(entry.thread_id ? { thread_id: entry.thread_id } : {}),
          ...(entry.expires_at ? { expires_at: entry.expires_at } : {}),
          ...(entry.metadata ? { metadata: entry.metadata } : {}),
          queue: HISTORY_QUEUE_REQUESTS as typeof HISTORY_QUEUE_REQUESTS,
          body: entry.plaintext ?? "",
        })),
    );
    // Archive before promotion: a failed write can be retried from history.
    await this.processContactAccepts(requests).catch(() => []);
    return { requests, archived, lost };
  }

  /**
   * Ask to be someone's contact: pin the key we resolved, record the intent
   * as `requested`, then send the typed envelope the recipient's relay routes
   * into their requests queue.
   *
   * The local write happens first on purpose — if the send fails (their
   * policy, their relay), the intent is still on record and retryable, which
   * is what `poweur contacts request` does too.
   */
  async requestContact(
    identity: string,
    options: { intro?: string; petname?: string } = {},
  ): Promise<{ contact: Contact; result: SendResult }> {
    const target = identity.trim().toLowerCase();
    const contacts = await this.contacts();
    const existing = (await contacts.load()).contacts.find((c) => c.identity === target);
    if (existing?.state === CONTACT_ACCEPTED) {
      throw new PoweurError("invalid_argument", `${target} is already an accepted contact`);
    }
    const contact = await contacts.set(target, CONTACT_REQUESTED, {
      pin: true,
      ...(options.petname ? { petname: options.petname } : {}),
    });
    const result = await this.send(target, options.intro || "contact request", {
      type: MSG_TYPE_CONTACT_REQUEST,
    });
    return { contact, result };
  }

  /**
   * Accept someone: pin their current key (TOFU) and tell them, best-effort —
   * `notified` is false when their own policy or relay refused the note, which
   * must not undo an acceptance the user already made.
   */
  async acceptContact(
    identity: string,
    options: { petname?: string } = {},
  ): Promise<{ contact: Contact; notified: boolean }> {
    const target = identity.trim().toLowerCase();
    const contacts = await this.contacts();
    const contact = await contacts.set(target, CONTACT_ACCEPTED, {
      ...(options.petname ? { petname: options.petname } : {}),
    });
    try {
      await this.send(target, "contact request accepted", { type: MSG_TYPE_CONTACT_ACCEPT });
      return { contact, notified: true };
    } catch {
      return { contact, notified: false };
    }
  }

  /**
   * Block someone. No message is sent: telling a sender they were blocked is
   * information they can act on, and the relay enforces the state anyway.
   */
  async blockContact(identity: string): Promise<Contact> {
    const contacts = await this.contacts();
    return contacts.set(identity.trim().toLowerCase(), CONTACT_BLOCKED, {});
  }

  async policy(): Promise<{ policy: InboxPolicy; explicit: boolean }> {
    return readInboxPolicy(this.system());
  }

  async setPolicy(
    mode: InboxMode,
    anonymous?: AnonymousPolicy,
    readReceipts?: InboxPolicy["read_receipts"],
    trustedAuthServices?: string[],
  ): Promise<InboxPolicy> {
    return writeInboxPolicy(this.system(), mode, anonymous, readReceipts, trustedAuthServices);
  }

  /** Our own public profile document, and whether one has been written. */
  async profile(): Promise<{ profile: Profile; explicit: boolean }> {
    return readProfile(this.system());
  }

  async setProfile(profile: Profile): Promise<Profile> {
    return writeProfile(this.system(), profile);
  }

  /** Drain the anonymous queue. */
  anon() {
    return this.messages.anonQueue(this.signer, this.decryptor);
  }
}
