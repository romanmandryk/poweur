/**
 * `PoweurClient` — the one object most callers need. It wires a relay URL, a
 * `Signer` and (optionally) a `Decryptor` into the per-area modules so a
 * bot is a few lines rather than a lot of plumbing.
 *
 * Everything it composes is usable standalone; this is convenience, not a
 * layer you have to go through.
 */
import { PoweurError } from "./errors.js";
import { Contacts, fetchRequests } from "./contacts.js";
import { DavClient, mintDavToken } from "./files.js";
import { RelayClient } from "./http.js";
import { EnrollApi } from "./enroll.js";
import { IdentityApi } from "./identity.js";
import { KeystoreApi } from "./keystore.js";
import { Messaging } from "./messages.js";
import { readInboxPolicy, writeInboxPolicy } from "./policy.js";
import { readProfile, writeProfile } from "./profile.js";
import { MemorySessionStore, SessionManager } from "./session.js";
import { Shares } from "./shares.js";
import { SyncClient } from "./sync.js";
import { CONTACT_ACCEPTED, CONTACT_BLOCKED, CONTACT_REQUESTED, MSG_TYPE_CONTACT_ACCEPT, MSG_TYPE_CONTACT_REQUEST, } from "./types.js";
export class PoweurClient {
    relay;
    signer;
    decryptor;
    identity;
    /** Wrapped seed copies, one per enrolled authenticator (EPIC-011 E11-T1). */
    keystore;
    /** The new-device enrollment rendezvous (EPIC-011 E11-T3). */
    enroll;
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
        this.keystore = new KeystoreApi(this.relay);
        this.enroll = new EnrollApi(this.relay);
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
    /**
     * Ask to be someone's contact: pin the key we resolved, record the intent
     * as `requested`, then send the typed envelope the recipient's relay routes
     * into their requests queue.
     *
     * The local write happens first on purpose — if the send fails (their
     * policy, their relay), the intent is still on record and retryable, which
     * is what `poweur contacts request` does too.
     */
    async requestContact(identity, options = {}) {
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
    async acceptContact(identity, options = {}) {
        const target = identity.trim().toLowerCase();
        const contacts = await this.contacts();
        const contact = await contacts.set(target, CONTACT_ACCEPTED, {
            ...(options.petname ? { petname: options.petname } : {}),
        });
        try {
            await this.send(target, "contact request accepted", { type: MSG_TYPE_CONTACT_ACCEPT });
            return { contact, notified: true };
        }
        catch {
            return { contact, notified: false };
        }
    }
    /**
     * Block someone. No message is sent: telling a sender they were blocked is
     * information they can act on, and the relay enforces the state anyway.
     */
    async blockContact(identity) {
        const contacts = await this.contacts();
        return contacts.set(identity.trim().toLowerCase(), CONTACT_BLOCKED, {});
    }
    async policy() {
        return readInboxPolicy(await this.dav());
    }
    async setPolicy(mode, anonymous) {
        return writeInboxPolicy(await this.dav(), mode, anonymous);
    }
    /** Our own public profile document, and whether one has been written. */
    async profile() {
        return readProfile(await this.dav());
    }
    async setProfile(profile) {
        return writeProfile(await this.dav(), profile);
    }
    /** Drain the anonymous queue. */
    anon() {
        return this.messages.anonQueue(this.signer, this.decryptor);
    }
}
