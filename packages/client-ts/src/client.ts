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
  HISTORY_QUEUE_REQUESTS,
  HISTORY_QUEUE_SENT,
  MessageHistory,
  type HistoryRecord,
} from "./history.js";
import { IdentityApi } from "./identity.js";
import { GroupMessaging, type GroupSendOptions, type GroupSendResult } from "./groups.js";
import { KeystoreApi } from "./keystore.js";
import { Messaging, resolveRecipientRelayUrl, type SendOptions, type SendResult } from "./messages.js";
import { readInboxPolicy, writeInboxPolicy } from "./policy.js";
import {
  MSG_TYPE_AUTH_REQUEST,
  MSG_TYPE_SHARE_ACCEPT,
  MSG_TYPE_SHARE_CLAIM,
  MSG_TYPE_SHARE_OFFER,
  MSG_TYPE_SHARE_REVOKED,
} from "./msgtypes.js";
import { readProfile, writeProfile } from "./profile.js";
import { resolveIdentity, type ResolveOptions } from "./resolve.js";
import { MemorySessionStore, SessionManager, type SessionStore } from "./session.js";
import { buildShareOffer, grantExpired, grantIsLink, linkToken, Shares, validateShareClaim, validateShareOffer } from "./shares.js";
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
  type ShareAccept,
  type ShareClaim,
  type ShareGrant,
  type ShareOffer,
  type ShareRevoked,
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

  /** A DAV client for our own tree, minted once and reused. */
  async dav(options: { audience?: string; scope?: string; force?: boolean } = {}): Promise<DavClient> {
    if (this.#dav && !options.force && !options.audience) return this.#dav;
    let relay = this.relay;
    if (options.audience && options.audience.toLowerCase() !== this.signer.identity.toLowerCase()) {
      const scheme = this.relay.relayUrl.startsWith("http://") ? "http" : "https";
      const relayUrl = await resolveRecipientRelayUrl(options.audience, scheme, this.#resolveOptions);
      if (relayUrl.replace(/\/+$/, "") !== this.relay.relayUrl) {
        relay = new RelayClient(relayUrl, {
          ...(this.#resolveOptions.fetch ? { fetch: this.#resolveOptions.fetch } : {}),
        });
      }
    }
    const dav = await DavClient.connect(relay, this.signer, options);
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

  /**
   * Create a direct-ID grant and notify each recipient with an encrypted
   * `sys.share.offer`. A failed notification does not roll the signed grant
   * back: callers get the per-recipient result and can retry it safely.
   */
  async offerShare(
    path: string,
    recipients: string[],
    options: { permissions?: "read" | "rw"; expiresAt?: string; sourceShareId?: string } = {},
  ): Promise<{
    grant: ShareGrant;
    offer: ShareOffer;
    deliveries: Array<{ recipient: string; delivered: boolean; error?: string }>;
  }> {
    const targets = [...new Set(recipients.map((value) => value.trim().toLowerCase()).filter(Boolean))];
    if (targets.length === 0) {
      throw new PoweurError("invalid_argument", "at least one direct recipient is required");
    }
    const shares = await this.shares();
    const grant = await shares.add(this.signer, path, {
      with: targets,
      ...(options.permissions ? { permissions: options.permissions } : {}),
      ...(options.expiresAt ? { expiresAt: options.expiresAt } : {}),
      ...(options.sourceShareId ? { sourceShareId: options.sourceShareId } : {}),
    });
    const offer = buildShareOffer(grant);
    const plaintext = JSON.stringify(offer);
    const defaultExpiry = new Date(Date.now() + 7 * 24 * 60 * 60 * 1000).toISOString();
    const requestedExpiry = options.expiresAt ? Date.parse(options.expiresAt) : Number.NaN;
    const envelopeExpiry = Number.isFinite(requestedExpiry) && requestedExpiry < Date.parse(defaultExpiry)
      ? options.expiresAt!
      : defaultExpiry;
    const deliveries = [] as Array<{ recipient: string; delivered: boolean; error?: string }>;
    for (const recipient of targets) {
      try {
        await this.send(recipient, plaintext, {
          type: MSG_TYPE_SHARE_OFFER,
          expiresAt: envelopeExpiry,
          metadata: { share_id: grant.share_id },
        });
        deliveries.push({ recipient, delivered: true });
      } catch (error) {
        deliveries.push({
          recipient,
          delivered: false,
          error: error instanceof Error ? error.message : String(error),
        });
      }
    }
    return { grant, offer, deliveries };
  }

  /** Verify, mount and acknowledge a decrypted `sys.share.offer`. */
  async acceptShareOffer(
    offer: ShareOffer,
    options: { name?: string } = {},
  ): Promise<{
    mount: Awaited<ReturnType<Shares["acceptOffer"]>>;
    acceptance: ShareAccept;
    notified: boolean;
  }> {
    const resolved = await resolveIdentity(offer.grant.owner, this.#resolveOptions);
    validateShareOffer(offer, this.signer.identity, resolved.document.public_key);
    const acceptedAt = new Date().toISOString();
    const shares = await this.shares();
    const mount = await shares.acceptOffer(
      offer,
      this.signer.identity,
      resolved.document.public_key,
      { ...(options.name ? { name: options.name } : {}), acceptedAt },
    );
    const acceptance: ShareAccept = {
      version: 1,
      share_id: offer.grant.share_id,
      owner: offer.grant.owner,
      recipient: this.signer.identity,
      mount_path: mount.mountPath,
      accepted_at: acceptedAt,
    };
    try {
      await this.send(offer.grant.owner, JSON.stringify(acceptance), {
        type: MSG_TYPE_SHARE_ACCEPT,
        expiresAt: new Date(Date.now() + 7 * 24 * 60 * 60 * 1000).toISOString(),
        metadata: {
          share_id: offer.grant.share_id,
          ...(offer.grant.source_share_id ? { source_share_id: offer.grant.source_share_id } : {}),
        },
      });
      return { mount, acceptance, notified: true };
    } catch {
      return { mount, acceptance, notified: false };
    }
  }

  /** Ask a capability owner to replace the anonymous relationship with an explicit ID grant. */
  async requestShareClaim(input: Omit<ShareClaim, "version" | "claimant" | "claimed_at">): Promise<ShareClaim> {
    const claim: ShareClaim = {
      version: 1,
      share_id: input.share_id,
      owner: input.owner.trim().toLowerCase(),
      token: input.token.trim().toLowerCase(),
      claimant: this.signer.identity,
      action: input.action,
      claimed_at: new Date().toISOString(),
    };
    validateShareClaim(claim);
    await this.send(claim.owner, JSON.stringify(claim), {
      type: MSG_TYPE_SHARE_CLAIM,
      expiresAt: new Date(Date.now() + 7 * 24 * 60 * 60 * 1000).toISOString(),
      metadata: { share_id: claim.share_id },
    });
    return claim;
  }

  /** Owner approval: verify capability continuity, issue a direct grant, and optionally consume the link. */
  async approveShareClaim(
    claim: ShareClaim,
    options: { consumeLink?: boolean; permissions?: "read" | "rw" } = {},
  ): Promise<Awaited<ReturnType<PoweurClient["offerShare"]>> & { linkRevoked: boolean }> {
    validateShareClaim(claim);
    if (claim.owner.trim().toLowerCase() !== this.signer.identity.toLowerCase()) {
      throw new PoweurError("policy_rejected", "share claim owner does not match this identity");
    }
    const shares = await this.shares();
    const source = (await shares.list()).find((grant) => grant.share_id === claim.share_id);
    if (!source || !grantIsLink(source) || linkToken(source) !== claim.token || grantExpired(source)) {
      throw new PoweurError("policy_rejected", "source capability is missing, expired, revoked, or does not match");
    }
    const offered = await this.offerShare(source.path, [claim.claimant], {
      permissions: options.permissions ?? "rw",
      ...(source.expires_at ? { expiresAt: source.expires_at } : {}),
      sourceShareId: source.share_id,
    });
    let linkRevoked = false;
    if (options.consumeLink) linkRevoked = await shares.revoke(source.share_id);
    return { ...offered, linkRevoked };
  }

  /** Delete a grant first, then best-effort notify every direct recipient. */
  async revokeShareAndNotify(shareId: string): Promise<{
    revoked: boolean;
    notifications: Array<{ recipient: string; delivered: boolean }>;
  }> {
    const shares = await this.shares();
    const grant = (await shares.list()).find((entry) => entry.share_id === shareId);
    const revoked = await shares.revoke(shareId);
    if (!revoked || !grant) return { revoked, notifications: [] };
    const payload: ShareRevoked = {
      version: 1,
      share_id: shareId,
      owner: this.signer.identity,
      revoked_at: new Date().toISOString(),
    };
    const recipients = [...new Set(grant.audience.map((entry) => entry.id?.trim().toLowerCase()).filter((value): value is string => Boolean(value)))];
    const notifications: Array<{ recipient: string; delivered: boolean }> = [];
    for (const recipient of recipients) {
      try {
        await this.send(recipient, JSON.stringify(payload), {
          type: MSG_TYPE_SHARE_REVOKED,
          metadata: { share_id: shareId },
        });
        notifications.push({ recipient, delivered: true });
      } catch {
        notifications.push({ recipient, delivered: false });
      }
    }
    return { revoked, notifications };
  }

  async contacts(): Promise<Contacts> {
    return new Contacts(await this.dav(), this.#resolveOptions);
  }

  /** Pending contact requests, with their intros decrypted when we can. */
  requests(): Promise<ContactRequestEntry[]> {
    return fetchRequests(this.relay, this.signer, this.decryptor);
  }

  /** Drain pending consent gestures and durably archive decrypted entries. */
  async requestsAndArchive(): Promise<{
    requests: ContactRequestEntry[];
    archived: number;
    lost: number;
  }> {
    const requests = await this.requests();
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
    return readInboxPolicy(await this.dav());
  }

  async setPolicy(
    mode: InboxMode,
    anonymous?: AnonymousPolicy,
    readReceipts?: InboxPolicy["read_receipts"],
    trustedAuthServices?: string[],
  ): Promise<InboxPolicy> {
    return writeInboxPolicy(await this.dav(), mode, anonymous, readReceipts, trustedAuthServices);
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
