/**
 * Messaging: send (session- or identity-signed), inbox, acks, and the
 * anonymous/stranger path. The twin of `poweur send` / `poweur inbox` /
 * `poweur anon`.
 *
 * Routing mirrors the CLI's two modes. By default a message goes *directly*
 * to the recipient's relay, so the sender's home relay sees no outbound
 * traffic at all. `viaHomeRelay` POSTs to the sender's own relay instead,
 * which forwards on — that hides the sender's IP from the recipient relay,
 * trading one exposure for the other.
 */
import { canonicalAck, canonicalMessage } from "./canonical.js";
import { decryptMessage, encryptMessage } from "./crypto/index.js";
import { rfc3339 } from "./encoding.js";
import { ChallengeRequiredError, PoweurError, RelayError } from "./errors.js";
import { RelayClient } from "./http.js";
import { newAckId, newMessageId } from "./ids.js";
import { solvePow } from "./pow.js";
import { resolveEncryptionKey, resolveIdentity } from "./resolve.js";
import { isSessionValid, sessionProofFrom, sessionSigner, } from "./session.js";
import { ACK_STATE_DELIVERED_CLIENT, ACK_TYPE_DELIVERY, ANON_CHALLENGE_POW, } from "./types.js";
/** Validate a sign-with mode (CLI: `--sign-with session|identity`). */
export function assertSignWith(mode) {
    if (mode !== "session" && mode !== "identity") {
        throw new PoweurError("invalid_argument", `invalid sign-with: ${mode} (want session or identity)`);
    }
    return mode;
}
/**
 * Derive the recipient's relay URL from their identity document. The scheme
 * is not carried in DNS, so it is inherited from our own relay URL — http for
 * local integration runs, https in production.
 */
export async function resolveRecipientRelayUrl(recipient, scheme, options = {}) {
    const { document } = await resolveIdentity(recipient, options);
    const host = document.relay || recipient;
    if (!host) {
        throw new PoweurError("resolve_failed", "recipient relay host not resolvable");
    }
    return /^https?:\/\//.test(host) ? host : `${scheme}://${host}`;
}
export class Messaging {
    #options;
    constructor(options) {
        this.#options = options;
    }
    get client() {
        return this.#options.client;
    }
    /**
     * The scheme direct sends use. DNS carries no scheme, so it is inherited
     * from our own relay URL: http for local integration runs, https in prod.
     */
    #scheme() {
        return this.client.relayUrl.startsWith("http://") ? "http" : "https";
    }
    #clientFor(relayUrl) {
        if (relayUrl === this.client.relayUrl)
            return this.client;
        return this.#options.clientFor?.(relayUrl) ?? new RelayClient(relayUrl);
    }
    /** Look up the recipient's X25519 key, falling back to the relay's record. */
    async #recipientEncryptionKey(recipient) {
        const resolved = await resolveEncryptionKey(recipient, this.#options.resolve ?? {});
        if (resolved)
            return resolved;
        try {
            const record = await this.client.request({
                method: "GET",
                path: `/identities/${encodeURIComponent(recipient)}`,
            });
            if (record.encryption_public_key) {
                return record.encryption_public_key.replace(/^x25519:/, "");
            }
        }
        catch {
            // Fall through to the typed error below.
        }
        throw new PoweurError("not_found", `${recipient} has no published encryption key; refusing to send in plaintext`);
    }
    /**
     * Encrypt, sign and POST a message. Never sends plaintext: an identity with
     * no published encryption key is a hard failure, not a downgrade.
     */
    async send(signer, recipient, plaintext, options = {}) {
        const mode = assertSignWith(options.signWith ?? "session");
        const recipientKey = await this.#recipientEncryptionKey(recipient);
        const { payload, encryption } = encryptMessage(recipientKey, plaintext);
        const targetRelay = options.targetRelayUrl ??
            (options.viaHomeRelay
                ? this.client.relayUrl
                : await resolveRecipientRelayUrl(recipient, this.#scheme(), this.#options.resolve ?? {}));
        const message = {
            id: newMessageId(),
            sender: signer.identity,
            recipient,
            timestamp: rfc3339(),
            payload,
            signature: "",
            encryption,
        };
        if (options.type)
            message.type = options.type;
        if (options.threadId)
            message.thread_id = options.threadId;
        if (options.expiresAt)
            message.expires_at = options.expiresAt;
        if (options.metadata)
            message.metadata = options.metadata;
        let session = null;
        if (mode === "session") {
            if (!this.#options.sessions) {
                throw new PoweurError("invalid_argument", "a SessionManager is required when signWith is 'session'");
            }
            session = await this.#options.sessions.ensure(signer);
            message.session_id = session.sessionId;
            const proof = sessionProofFrom(session);
            if (proof)
                message.session_proof = proof;
        }
        const signWithKey = async (current, active) => {
            const canonical = canonicalMessage({
                sender: current.sender,
                recipient: current.recipient,
                timestamp: current.timestamp,
                payload: current.payload,
                id: current.id,
                ...(current.session_id ? { sessionId: current.session_id } : {}),
                encryption: current.encryption ?? null,
                ...(current.type ? { type: current.type } : {}),
            });
            const keyHolder = active ? sessionSigner(active) : signer;
            return keyHolder.sign(canonical, "base64std");
        };
        message.signature = await signWithKey(message, session);
        const client = this.#clientFor(targetRelay);
        let response = await client.raw({ method: "POST", path: "/messages", body: message });
        // A 401 means the relay dropped our session (restart, revocation, TTL
        // rounding). Re-register once and retry rather than surfacing a failure
        // the caller can do nothing useful about.
        if (response.status === 401 && mode === "session" && this.#options.sessions) {
            session = await this.#options.sessions.refresh(signer);
            message.session_id = session.sessionId;
            const proof = sessionProofFrom(session);
            if (proof)
                message.session_proof = proof;
            message.signature = await signWithKey(message, session);
            response = await client.raw({ method: "POST", path: "/messages", body: message });
        }
        if (response.status >= 400) {
            const text = await response.text();
            throw new RelayError(`relay rejected message (${response.status}): ${text.trim()}`, {
                status: response.status,
                detail: text,
            });
        }
        return {
            message,
            status: response.status,
            targetRelay,
            viaHomeRelay: options.viaHomeRelay === true,
            signWith: mode,
        };
    }
    /** Fetch the inbox (challenge-signed) and decrypt what we can. */
    async inbox(signer, decryptor) {
        const raw = await this.fetchInbox(signer);
        const messages = decryptor
            ? await decryptInbox(decryptor, raw.messages ?? [])
            : (raw.messages ?? []).map((m) => ({ ...m, plaintext: null }));
        return { messages, acks: raw.acks ?? [] };
    }
    /** The raw inbox payload, with the session-expiry retry already applied. */
    async fetchInbox(signer) {
        const attempt = async (session) => {
            const { challenge } = await this.client.request({
                method: "GET",
                path: `/auth/challenge?identity=${encodeURIComponent(signer.identity)}`,
            });
            const keyHolder = session ? sessionSigner(session) : signer;
            const signature = await keyHolder.sign(challenge, "base64std");
            const headers = {
                "X-Poweur-Identity": signer.identity,
                "X-Poweur-Challenge": challenge,
                "X-Poweur-Signature": signature,
            };
            if (session)
                headers["X-Poweur-Session-Id"] = session.sessionId;
            return this.client.request({
                method: "GET",
                path: `/messages/${encodeURIComponent(signer.identity)}`,
                headers,
            });
        };
        let session = null;
        if (this.#options.sessions) {
            session = await this.#options.sessions.ensure(signer);
        }
        try {
            return await attempt(session);
        }
        catch (error) {
            const expired = error instanceof PoweurError && error.code === "session_expired" && this.#options.sessions;
            if (!expired)
                throw error;
            return attempt(await this.#options.sessions.refresh(signer));
        }
    }
    /**
     * Emit the tick-2 receipt for a message we actually decrypted. It goes to
     * the *original sender's* home relay, resolved from their identity, so it
     * arrives whether the message came direct or via a forwarding relay.
     */
    async ack(signer, message, options = {}) {
        if (!message.id || !message.sender) {
            throw new PoweurError("invalid_argument", "ack requires a message id and original sender");
        }
        const ack = {
            type: ACK_TYPE_DELIVERY,
            id: newAckId(),
            message_id: message.id,
            state: ACK_STATE_DELIVERED_CLIENT,
            sender: message.recipient || signer.identity,
            recipient: message.sender,
            timestamp: rfc3339(),
            signature: "",
        };
        let session = options.session ?? null;
        if (session === null && this.#options.sessions) {
            const loaded = await this.#options.sessions.store.load(signer.identity);
            if (isSessionValid(loaded))
                session = loaded;
        }
        if (session) {
            ack.session_id = session.sessionId;
            const proof = sessionProofFrom(session);
            if (proof)
                ack.session_proof = proof;
        }
        const canonical = canonicalAck({
            id: ack.id,
            messageId: ack.message_id,
            state: ack.state,
            sender: ack.sender,
            recipient: ack.recipient,
            timestamp: ack.timestamp,
            ...(ack.session_id ? { sessionId: ack.session_id } : {}),
        });
        ack.signature = await (session ? sessionSigner(session) : signer).sign(canonical, "base64std");
        const senderRelay = await resolveRecipientRelayUrl(message.sender, this.#scheme(), this.#options.resolve ?? {});
        const response = await this.#clientFor(senderRelay).raw({
            method: "POST",
            path: "/acks",
            body: ack,
        });
        if (response.status >= 400) {
            const text = await response.text();
            throw new RelayError(`ack rejected (${response.status}): ${text.trim()}`, {
                status: response.status,
                detail: text,
            });
        }
        return ack;
    }
    /** Drain the anonymous queue (`poweur anon`) and decrypt what we can. */
    async anonQueue(signer, decryptor) {
        const { challenge } = await this.client.request({
            method: "GET",
            path: `/auth/challenge?identity=${encodeURIComponent(signer.identity)}`,
        });
        const signature = await signer.sign(challenge, "base64std");
        const response = await this.client.request({
            method: "GET",
            path: `/anon/${encodeURIComponent(signer.identity)}`,
            headers: {
                "X-Poweur-Identity": signer.identity,
                "X-Poweur-Challenge": challenge,
                "X-Poweur-Signature": signature,
            },
        });
        const messages = response.messages ?? [];
        if (!decryptor)
            return messages.map((m) => ({ ...m, plaintext: null }));
        const key = await decryptor.privateKeyBytes();
        return messages.map((m) => {
            if (!m.encryption)
                return { ...m, plaintext: null };
            try {
                return { ...m, plaintext: decryptMessage(key, m.payload, m.encryption) };
            }
            catch {
                return { ...m, plaintext: null };
            }
        });
    }
}
/** Decrypt a batch of inbox messages; a failure marks one message, not all. */
export async function decryptInbox(decryptor, messages) {
    const key = await decryptor.privateKeyBytes();
    return messages.map((message) => {
        if (!message.encryption?.alg)
            return { ...message, plaintext: message.payload };
        try {
            return {
                ...message,
                plaintext: decryptMessage(key, message.payload, message.encryption),
            };
        }
        catch (error) {
            return {
                ...message,
                plaintext: null,
                decryptError: error instanceof Error ? error.message : String(error),
            };
        }
    });
}
/**
 * Send with no identity at all: E2E-encrypted to the recipient, unsigned, and
 * gated by whatever challenge their policy demands. The recipient must have
 * opted in (`policy set … --anon-allow`); the default everywhere is deny.
 */
export async function sendAnonymous(recipient, plaintext, options = {}) {
    const resolveOptions = options.resolve ?? {};
    const recipientKey = await resolveEncryptionKey(recipient, resolveOptions);
    if (!recipientKey) {
        throw new PoweurError("not_found", `${recipient} has no published encryption key; anonymous messages must still be encrypted`);
    }
    const { payload, encryption } = encryptMessage(recipientKey, plaintext);
    const targetRelay = options.targetRelayUrl ??
        (await resolveRecipientRelayUrl(recipient, options.scheme ?? "https", resolveOptions));
    const client = options.clientFor?.(targetRelay) ?? new RelayClient(targetRelay);
    const id = newMessageId();
    const body = {
        id,
        recipient,
        timestamp: rfc3339(),
        payload,
        encryption,
    };
    const post = async () => client.raw({ method: "POST", path: "/messages", body });
    let response = await post();
    if (response.status === 428) {
        const text = await response.text();
        let envelope = {};
        try {
            envelope = JSON.parse(text);
        }
        catch {
            throw new PoweurError("relay_error", "malformed challenge from relay", { detail: text });
        }
        const challenge = (envelope["challenge"] ?? {});
        const type = String(challenge["type"] ?? "");
        const bits = Number(challenge["bits"] ?? 0);
        const token = String(challenge["token"] ?? "");
        if (type !== ANON_CHALLENGE_POW || options.solveChallenge === false) {
            throw new ChallengeRequiredError({
                type,
                token,
                bits,
                algo: challenge["algo"],
                detail: challenge["detail"],
            });
        }
        options.onChallenge?.({ type, bits });
        body["challenge_token"] = token;
        body["challenge_solution"] = await solvePow(token, bits, {
            ...(options.onSolveProgress ? { onProgress: options.onSolveProgress } : {}),
            ...(options.signal ? { signal: options.signal } : {}),
        });
        response = await post();
    }
    if (response.status >= 400) {
        const text = await response.text();
        throw new RelayError(`relay rejected anonymous message (${response.status}): ${text.trim()}`, { status: response.status, detail: text });
    }
    return { id, status: response.status, targetRelay };
}
