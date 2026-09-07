/**
 * `PoweurClient` — the one object most callers need. It wires a relay URL, a
 * `Signer` and (optionally) a `Decryptor` into the per-area modules so a
 * bot is a few lines rather than a lot of plumbing.
 *
 * Everything it composes is usable standalone; this is convenience, not a
 * layer you have to go through.
 */
import { Contacts, fetchRequests } from "./contacts.js";
import { DavClient, mintDavToken } from "./files.js";
import { RelayClient } from "./http.js";
import { IdentityApi } from "./identity.js";
import { Messaging } from "./messages.js";
import { readInboxPolicy, writeInboxPolicy } from "./policy.js";
import { MemorySessionStore, SessionManager } from "./session.js";
import { Shares } from "./shares.js";
import { SyncClient } from "./sync.js";
export class PoweurClient {
    relay;
    signer;
    decryptor;
    identity;
    sessions;
    messages;
    #resolveOptions;
    #dav = null;
    constructor(options) {
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
    get identityName() {
        return this.signer.identity;
    }
    /** Send an encrypted message (session-signed by default). */
    send(recipient, plaintext, options = {}) {
        return this.messages.send(this.signer, recipient, plaintext, options);
    }
    /** Fetch and decrypt the inbox. */
    inbox() {
        return this.messages.inbox(this.signer, this.decryptor);
    }
    /**
     * Fetch the inbox and emit a tick-2 receipt for every message that actually
     * decrypted — proof it reached a client, not just a relay. Messages we
     * could not decrypt stay at tick 1 on the sender's side, deliberately.
     */
    async inboxAndAck() {
        const { messages, acks } = await this.inbox();
        const acked = [];
        for (const message of messages) {
            if (message.plaintext === null || !message.id || !message.sender)
                continue;
            try {
                await this.messages.ack(this.signer, {
                    id: message.id,
                    sender: message.sender,
                    ...(message.recipient ? { recipient: message.recipient } : {}),
                });
                acked.push(message.id);
            }
            catch {
                // A failed receipt must not lose the message the caller just read.
            }
        }
        return { messages, acks, acked };
    }
    /** A DAV client for our own tree, minted once and reused. */
    async dav(options = {}) {
        if (this.#dav && !options.force && !options.audience)
            return this.#dav;
        const dav = await DavClient.connect(this.relay, this.signer, options);
        if (!options.audience)
            this.#dav = dav;
        return dav;
    }
    /** Mint a raw token — for handing to rclone, a mount, or another process. */
    davToken(options = {}) {
        return mintDavToken(this.relay, this.signer, options);
    }
    async sync(options = {}) {
        const dav = await this.dav(options.audience ? { audience: options.audience } : {});
        return new SyncClient(this.relay, dav.identity, dav.token);
    }
    async shares() {
        const dav = await this.dav();
        return new Shares(dav, new SyncClient(this.relay, dav.identity, dav.token));
    }
    async contacts() {
        return new Contacts(await this.dav(), this.#resolveOptions);
    }
    requests() {
        return fetchRequests(this.relay, this.signer);
    }
    async policy() {
        return readInboxPolicy(await this.dav());
    }
    async setPolicy(mode, anonymous) {
        return writeInboxPolicy(await this.dav(), mode, anonymous);
    }
    /** Drain the anonymous queue. */
    anon() {
        return this.messages.anonQueue(this.signer, this.decryptor);
    }
}
