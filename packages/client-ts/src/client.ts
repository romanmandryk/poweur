/**
 * `PoweurClient` — the one object most callers need. It wires a relay URL, a
 * `Signer` and (optionally) a `Decryptor` into the per-area modules so a
 * bot is a few lines rather than a lot of plumbing.
 *
 * Everything it composes is usable standalone; this is convenience, not a
 * layer you have to go through.
 */

import { readAnalyticsPreference, writeAnalyticsPreference } from "./analytics.js";
import { attachmentMetadata, downloadAttachment, uploadAttachment, type AttachmentRef } from "./attachments.js";
import type { Decryptor, Signer } from "./crypto/keys.js";
import { PoweurError } from "./errors.js";
import { Contacts, fetchRequests } from "./contacts.js";
import { DavClient, mintDavToken } from "./files.js";
import { RelayClient, type RelayClientOptions } from "./http.js";
import { EnrollApi } from "./enroll.js";
import {
  HISTORY_QUEUE_ANONYMOUS,
  HISTORY_QUEUE_INBOX,
  HISTORY_QUEUE_SENT,
  MessageHistory,
  type HistoryRecord,
} from "./history.js";
import { IdentityApi } from "./identity.js";
import { GroupMessaging, type GroupSendOptions, type GroupSendResult } from "./groups.js";
import { KeystoreApi } from "./keystore.js";
import { Messaging, type SendOptions, type SendResult } from "./messages.js";
import { readInboxPolicy, writeInboxPolicy } from "./policy.js";
import { readProfile, writeProfile } from "./profile.js";
import type { ResolveOptions } from "./resolve.js";
import { MemorySessionStore, SessionManager, type SessionStore } from "./session.js";
import { Shares } from "./shares.js";
import { SyncClient } from "./sync.js";
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
  async analyticsPreference() { return readAnalyticsPreference(await this.dav()); }
  async setAnalyticsConsent(granted: boolean) { return writeAnalyticsPreference(await this.dav(), granted); }
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
  #dav: DavClient | null = null;

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

  /** Upload, grant, and send a small encrypted reference in one action. */
  async sendAttachment(recipient: string, bytes: Uint8Array, options: { name: string; mime?: string; caption?: string; threadId?: string }) {
    const ref = await uploadAttachment(await this.dav(), this.signer, recipient, bytes, options);
    const sent = await this.sendAndArchive(recipient, options.caption || ref.name, {
      type: "chat.attachment", metadata: attachmentMetadata(ref),
      ...(options.threadId ? { threadId: options.threadId } : {}),
    });
    return { ref, ...sent };
  }

  downloadAttachment(metadata: Record<string, string>) {
    return downloadAttachment(this.signer, metadata, this.#resolveOptions);
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
   * The archive under `poweur-sys/private/messages/` (EPIC-009 E09-T1).
   *
   * Requires a decryptor: history is sealed to the identity's own encryption
   * key, so a client that cannot read messages cannot keep them either.
   */
  async history(): Promise<MessageHistory> {
    if (!this.decryptor) {
      throw new PoweurError("invalid_argument", "message history needs a decryptor to seal to");
    }
    return new MessageHistory(await this.dav(), this.signer.identity, this.decryptor);
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
        .filter((m) => m.plaintext !== null)
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

  /** A DAV client for our own tree, minted once and reused. */
  async dav(options: { audience?: string; scope?: string; force?: boolean } = {}): Promise<DavClient> {
    if (this.#dav && !options.force && !options.audience) return this.#dav;
    const dav = await DavClient.connect(this.relay, this.signer, options);
    if (!options.audience) this.#dav = dav;
    return dav;
  }

  /** Mint a raw token — for handing to rclone, a mount, or another process. */
  davToken(options: { audience?: string; scope?: string } = {}) {
    return mintDavToken(this.relay, this.signer, options);
  }

  async sync(options: { audience?: string } = {}): Promise<SyncClient> {
    const dav = await this.dav(options.audience ? { audience: options.audience } : {});
    return new SyncClient(this.relay, dav.identity, dav.token);
  }

  async shares(): Promise<Shares> {
    const dav = await this.dav();
    return new Shares(dav, new SyncClient(this.relay, dav.identity, dav.token));
  }

  async contacts(): Promise<Contacts> {
    return new Contacts(await this.dav(), this.#resolveOptions);
  }

  /** Pending contact requests, with their intros decrypted when we can. */
  requests(): Promise<ContactRequestEntry[]> {
    return fetchRequests(this.relay, this.signer, this.decryptor);
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
    return readInboxPolicy(await this.dav());
  }

  async setPolicy(mode: InboxMode, anonymous?: AnonymousPolicy, readReceipts?: InboxPolicy["read_receipts"]): Promise<InboxPolicy> {
    return writeInboxPolicy(await this.dav(), mode, anonymous, readReceipts);
  }

  /** Our own public profile document, and whether one has been written. */
  async profile(): Promise<{ profile: Profile; explicit: boolean }> {
    return readProfile(await this.dav());
  }

  async setProfile(profile: Profile): Promise<Profile> {
    return writeProfile(await this.dav(), profile);
  }

  /** Drain the anonymous queue. */
  anon() {
    return this.messages.anonQueue(this.signer, this.decryptor);
  }
}
