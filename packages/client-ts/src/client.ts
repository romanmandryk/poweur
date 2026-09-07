/**
 * `PoweurClient` — the one object most callers need. It wires a relay URL, a
 * `Signer` and (optionally) a `Decryptor` into the per-area modules so a
 * bot is a few lines rather than a lot of plumbing.
 *
 * Everything it composes is usable standalone; this is convenience, not a
 * layer you have to go through.
 */

import type { Decryptor, Signer } from "./crypto/keys.js";
import { Contacts, fetchRequests } from "./contacts.js";
import { DavClient, mintDavToken } from "./files.js";
import { RelayClient, type RelayClientOptions } from "./http.js";
import { IdentityApi } from "./identity.js";
import { Messaging, type SendOptions, type SendResult } from "./messages.js";
import { readInboxPolicy, writeInboxPolicy } from "./policy.js";
import type { ResolveOptions } from "./resolve.js";
import { MemorySessionStore, SessionManager, type SessionStore } from "./session.js";
import { Shares } from "./shares.js";
import { SyncClient } from "./sync.js";
import type {
  Ack,
  AnonymousPolicy,
  ContactRequestEntry,
  InboxMessage,
  InboxMode,
  InboxPolicy,
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
  readonly relay: RelayClient;
  readonly signer: Signer;
  readonly decryptor: Decryptor | null;
  readonly identity: IdentityApi;
  readonly sessions: SessionManager;
  readonly messages: Messaging;
  readonly #resolveOptions: ResolveOptions;
  #dav: DavClient | null = null;

  constructor(options: PoweurClientOptions) {
    const { relayUrl, signer, decryptor, sessionStore, resolve, ...clientOptions } = options;
    this.relay = new RelayClient(relayUrl, clientOptions);
    this.signer = signer;
    this.decryptor = decryptor ?? null;
    this.#resolveOptions = resolve ?? {};
    this.identity = new IdentityApi(this.relay);
    this.sessions = new SessionManager(this.relay, sessionStore ?? new MemorySessionStore());
    this.messages = new Messaging({
      client: this.relay,
      sessions: this.sessions,
      resolve: this.#resolveOptions,
    });
  }

  get identityName(): string {
    return this.signer.identity;
  }

  /** Send an encrypted message (session-signed by default). */
  send(recipient: string, plaintext: string, options: SendOptions = {}): Promise<SendResult> {
    return this.messages.send(this.signer, recipient, plaintext, options);
  }

  /** Fetch and decrypt the inbox. */
  inbox(): Promise<{ messages: InboxMessage[]; acks: Ack[] }> {
    return this.messages.inbox(this.signer, this.decryptor);
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

  requests(): Promise<ContactRequestEntry[]> {
    return fetchRequests(this.relay, this.signer);
  }

  async policy(): Promise<{ policy: InboxPolicy; explicit: boolean }> {
    return readInboxPolicy(await this.dav());
  }

  async setPolicy(mode: InboxMode, anonymous?: AnonymousPolicy): Promise<InboxPolicy> {
    return writeInboxPolicy(await this.dav(), mode, anonymous);
  }

  /** Drain the anonymous queue. */
  anon() {
    return this.messages.anonQueue(this.signer, this.decryptor);
  }
}
