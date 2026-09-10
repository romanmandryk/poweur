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
import type { Decryptor, Signer } from "./crypto/keys.js";
import { rfc3339 } from "./encoding.js";
import { ChallengeRequiredError, PoweurError, RelayError } from "./errors.js";
import { RelayClient } from "./http.js";
import { newAckId, newMessageId } from "./ids.js";
import {
  SYSTEM_MESSAGE_TYPES,
  isKnownSystemType,
  isSystemType,
  validateEnvelopeExtensions,
} from "./msgtypes.js";
import { solvePow } from "./pow.js";
import { resolveEncryptionKey, resolveIdentity, type ResolveOptions } from "./resolve.js";
import {
  isSessionValid,
  sessionProofFrom,
  sessionSigner,
  type SessionManager,
  type StoredSession,
} from "./session.js";
import {
  ACK_STATE_DELIVERED_CLIENT,
  ACK_TYPE_DELIVERY,
  ANON_CHALLENGE_POW,
  type Ack,
  type AnonQueueMessage,
  type InboxMessage,
  type InboxResponse,
  type Message,
} from "./types.js";

export type SignWith = "session" | "identity";

/** Validate a sign-with mode (CLI: `--sign-with session|identity`). */
export function assertSignWith(mode: string): SignWith {
  if (mode !== "session" && mode !== "identity") {
    throw new PoweurError(
      "invalid_argument",
      `invalid sign-with: ${mode} (want session or identity)`,
    );
  }
  return mode;
}

export interface MessagingOptions {
  /** Home relay: where sessions register and the inbox is read. */
  client: RelayClient;
  sessions?: SessionManager;
  resolve?: ResolveOptions;
  /** Build a client for another relay when sending direct. */
  clientFor?: (relayUrl: string) => RelayClient;
}

export interface SendOptions {
  signWith?: SignWith;
  /**
   * Envelope type. Leave unset for ordinary chat: absent means `chat.text`,
   * and the absent form is what keeps the canonical string identical to what
   * every pre-typing client produces. Bound into the signature.
   */
  type?: string;
  /** Route via the sender's home relay instead of straight to the recipient's. */
  viaHomeRelay?: boolean;
  /** Skip resolution and POST here. */
  targetRelayUrl?: string;
  /** Conversation thread this message belongs to. Signed; opaque to the relay. */
  threadId?: string;
  /** RFC3339 instant after which the message stops being meaningful. Signed. */
  expiresAt?: string;
  /**
   * Flat, signed, **plaintext** routing metadata. Visible to both relays on
   * the path — addressing, not content.
   */
  metadata?: Record<string, string>;
}

export interface SendResult {
  message: Message;
  status: number;
  targetRelay: string;
  viaHomeRelay: boolean;
  signWith: SignWith;
}

/**
 * Derive the recipient's relay URL from their identity document. The scheme
 * is not carried in DNS, so it is inherited from our own relay URL — http for
 * local integration runs, https in production.
 */
export async function resolveRecipientRelayUrl(
  recipient: string,
  scheme: "http" | "https",
  options: ResolveOptions = {},
): Promise<string> {
  const { document } = await resolveIdentity(recipient, options);
  const host = document.relay || recipient;
  if (!host) {
    throw new PoweurError("resolve_failed", "recipient relay host not resolvable");
  }
  return /^https?:\/\//.test(host) ? host : `${scheme}://${host}`;
}

export class Messaging {
  readonly #options: MessagingOptions;

  constructor(options: MessagingOptions) {
    this.#options = options;
  }

  get client(): RelayClient {
    return this.#options.client;
  }

  /**
   * The scheme direct sends use. DNS carries no scheme, so it is inherited
   * from our own relay URL: http for local integration runs, https in prod.
   */
  #scheme(): "http" | "https" {
    return this.client.relayUrl.startsWith("http://") ? "http" : "https";
  }

  #clientFor(relayUrl: string): RelayClient {
    if (relayUrl === this.client.relayUrl) return this.client;
    return this.#options.clientFor?.(relayUrl) ?? new RelayClient(relayUrl);
  }

  /** Look up the recipient's X25519 key, falling back to the relay's record. */
  async #recipientEncryptionKey(recipient: string): Promise<string> {
    const resolved = await resolveEncryptionKey(recipient, this.#options.resolve ?? {});
    if (resolved) return resolved;
    try {
      const record = await this.client.request<{ encryption_public_key?: string }>({
        method: "GET",
        path: `/identities/${encodeURIComponent(recipient)}`,
      });
      if (record.encryption_public_key) {
        return record.encryption_public_key.replace(/^x25519:/, "");
      }
    } catch {
      // Fall through to the typed error below.
    }
    throw new PoweurError(
      "not_found",
      `${recipient} has no published encryption key; refusing to send in plaintext`,
    );
  }

  /**
   * Encrypt, sign and POST a message. Never sends plaintext: an identity with
   * no published encryption key is a hard failure, not a downgrade.
   */
  async send(
    signer: Signer,
    recipient: string,
    plaintext: string,
    options: SendOptions = {},
  ): Promise<SendResult> {
    const mode = assertSignWith(options.signWith ?? "session");
    // Check the envelope before encrypting or resolving: the relay validates
    // these too, but a caller deserves the error before a round trip, and a
    // metadata value with a newline in it has no unambiguous signing input.
    const envelopeError = validateEnvelopeExtensions(options);
    if (envelopeError) throw new PoweurError("invalid_argument", envelopeError);
    if (isSystemType(options.type) && !isKnownSystemType(options.type)) {
      throw new PoweurError(
        "invalid_argument",
        `"${options.type}" is not a known system message type (sys.* is reserved; known: ${SYSTEM_MESSAGE_TYPES.join(", ")})`,
      );
    }
    const recipientKey = await this.#recipientEncryptionKey(recipient);
    const { payload, encryption } = encryptMessage(recipientKey, plaintext);

    const targetRelay =
      options.targetRelayUrl ??
      (options.viaHomeRelay
        ? this.client.relayUrl
        : await resolveRecipientRelayUrl(recipient, this.#scheme(), this.#options.resolve ?? {}));

    const message: Message = {
      id: newMessageId(),
      sender: signer.identity,
      recipient,
      timestamp: rfc3339(),
      payload,
      signature: "",
      encryption,
    };
    if (options.type) message.type = options.type;
    if (options.threadId) message.thread_id = options.threadId;
    if (options.expiresAt) message.expires_at = options.expiresAt;
    if (options.metadata) message.metadata = options.metadata;

    let session: StoredSession | null = null;
    if (mode === "session") {
      if (!this.#options.sessions) {
        throw new PoweurError(
          "invalid_argument",
          "a SessionManager is required when signWith is 'session'",
        );
      }
      session = await this.#options.sessions.ensure(signer);
      message.session_id = session.sessionId;
      const proof = sessionProofFrom(session);
      if (proof) message.session_proof = proof;
    }

    const signWithKey = async (current: Message, active: StoredSession | null) => {
      const canonical = canonicalMessage({
        sender: current.sender,
        recipient: current.recipient,
        timestamp: current.timestamp,
        payload: current.payload,
        id: current.id,
        ...(current.session_id ? { sessionId: current.session_id } : {}),
        encryption: current.encryption ?? null,
        ...(current.type ? { type: current.type } : {}),
        ...(current.thread_id ? { threadId: current.thread_id } : {}),
        ...(current.expires_at ? { expiresAt: current.expires_at } : {}),
        ...(current.metadata ? { metadata: current.metadata } : {}),
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
      if (proof) message.session_proof = proof;
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
  async inbox(
    signer: Signer,
    decryptor?: Decryptor | null,
    options: { since?: string } = {},
  ): Promise<{ messages: InboxMessage[]; acks: Ack[]; cursor?: string; ackCursor?: string }> {
    const raw = await this.fetchInbox(signer, options);
    const messages = decryptor
      ? await decryptInbox(decryptor, raw.messages ?? [])
      : (raw.messages ?? []).map((m) => ({ ...m, plaintext: null }));
    return {
      messages,
      acks: raw.acks ?? [],
      ...(raw.cursor !== undefined ? { cursor: raw.cursor } : {}),
      ...(raw.ack_cursor !== undefined ? { ackCursor: raw.ack_cursor } : {}),
    };
  }

  /**
   * Tell the relay we have everything through these cursors, so it can forget
   * them (EPIC-009 E09-T1).
   *
   * Separate from the read on purpose: a pickup that deletes as it serves
   * loses messages to a dropped connection, which is the failure the spool
   * exists to end.
   */
  async consume(
    signer: Signer,
    cursors: { through: string; ackThrough?: string },
  ): Promise<{ consumed: number; acks_consumed: number; pending: number }> {
    const { challenge } = await this.client.request<{ challenge: string }>({
      method: "GET",
      path: `/auth/challenge?identity=${encodeURIComponent(signer.identity)}`,
    });
    const signature = await signer.sign(challenge, "base64std");
    return this.client.request({
      method: "POST",
      path: `/messages/${encodeURIComponent(signer.identity)}/consume`,
      headers: {
        "X-Poweur-Identity": signer.identity,
        "X-Poweur-Challenge": challenge,
        "X-Poweur-Signature": signature,
      },
      body: {
        through: cursors.through,
        ...(cursors.ackThrough ? { ack_through: cursors.ackThrough } : {}),
      },
    });
  }

  /**
   * The raw inbox payload, with the session-expiry retry already applied.
   *
   * With `since`, the relay reads without forgetting and returns a cursor;
   * without it, the original drain-on-read.
   */
  async fetchInbox(signer: Signer, options: { since?: string } = {}): Promise<InboxResponse> {
    const attempt = async (session: StoredSession | null): Promise<InboxResponse> => {
      const { challenge } = await this.client.request<{ challenge: string }>({
        method: "GET",
        path: `/auth/challenge?identity=${encodeURIComponent(signer.identity)}`,
      });
      const keyHolder = session ? sessionSigner(session) : signer;
      const signature = await keyHolder.sign(challenge, "base64std");
      const headers: Record<string, string> = {
        "X-Poweur-Identity": signer.identity,
        "X-Poweur-Challenge": challenge,
        "X-Poweur-Signature": signature,
      };
      if (session) headers["X-Poweur-Session-Id"] = session.sessionId;
      const query = options.since === undefined
        ? ""
        : `?since=${encodeURIComponent(options.since)}`;
      return this.client.request<InboxResponse>({
        method: "GET",
        path: `/messages/${encodeURIComponent(signer.identity)}${query}`,
        headers,
      });
    };

    let session: StoredSession | null = null;
    if (this.#options.sessions) {
      session = await this.#options.sessions.ensure(signer);
    }
    try {
      return await attempt(session);
    } catch (error) {
      const expired =
        error instanceof PoweurError && error.code === "session_expired" && this.#options.sessions;
      if (!expired) throw error;
      return attempt(await this.#options.sessions!.refresh(signer));
    }
  }

  /**
   * Emit the tick-2 receipt for a message we actually decrypted. It goes to
   * the *original sender's* home relay, resolved from their identity, so it
   * arrives whether the message came direct or via a forwarding relay.
   */
  async ack(
    signer: Signer,
    message: { id: string; sender: string; recipient?: string },
    options: { session?: StoredSession | null } = {},
  ): Promise<Ack> {
    if (!message.id || !message.sender) {
      throw new PoweurError("invalid_argument", "ack requires a message id and original sender");
    }
    const ack: Ack = {
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
      if (isSessionValid(loaded)) session = loaded;
    }
    if (session) {
      ack.session_id = session.sessionId;
      const proof = sessionProofFrom(session);
      if (proof) ack.session_proof = proof;
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

    const senderRelay = await resolveRecipientRelayUrl(
      message.sender,
      this.#scheme(),
      this.#options.resolve ?? {},
    );
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
  async anonQueue(
    signer: Signer,
    decryptor?: Decryptor | null,
  ): Promise<Array<AnonQueueMessage & { plaintext: string | null }>> {
    const { challenge } = await this.client.request<{ challenge: string }>({
      method: "GET",
      path: `/auth/challenge?identity=${encodeURIComponent(signer.identity)}`,
    });
    const signature = await signer.sign(challenge, "base64std");
    const response = await this.client.request<{ messages?: AnonQueueMessage[] }>({
      method: "GET",
      path: `/anon/${encodeURIComponent(signer.identity)}`,
      headers: {
        "X-Poweur-Identity": signer.identity,
        "X-Poweur-Challenge": challenge,
        "X-Poweur-Signature": signature,
      },
    });
    const messages = response.messages ?? [];
    if (!decryptor) return messages.map((m) => ({ ...m, plaintext: null }));
    const key = await decryptor.privateKeyBytes();
    return messages.map((m) => {
      if (!m.encryption) return { ...m, plaintext: null };
      try {
        return { ...m, plaintext: decryptMessage(key, m.payload, m.encryption) };
      } catch {
        return { ...m, plaintext: null };
      }
    });
  }
}

/** Decrypt a batch of inbox messages; a failure marks one message, not all. */
export async function decryptInbox(
  decryptor: Decryptor,
  messages: Message[],
): Promise<InboxMessage[]> {
  const key = await decryptor.privateKeyBytes();
  return messages.map((message) => {
    if (!message.encryption?.alg) return { ...message, plaintext: message.payload };
    try {
      return {
        ...message,
        plaintext: decryptMessage(key, message.payload, message.encryption),
      };
    } catch (error) {
      return {
        ...message,
        plaintext: null,
        decryptError: error instanceof Error ? error.message : String(error),
      };
    }
  });
}

export interface AnonSendOptions {
  /** Skip resolution and POST here. */
  targetRelayUrl?: string;
  resolve?: ResolveOptions;
  /** Solve a PoW challenge and retry. Default true. */
  solveChallenge?: boolean;
  onChallenge?: (challenge: { type: string; bits: number }) => void;
  /**
   * Attempts so far, between solver chunks. A 24-bit challenge is tens of
   * seconds in a browser, so a UI that cannot show progress can only look
   * frozen — and one that cannot `signal` an abort cannot be cancelled.
   */
  onSolveProgress?: (attempts: number) => void;
  signal?: AbortSignal;
  clientFor?: (relayUrl: string) => RelayClient;
  /** Scheme to assume for the recipient's relay when resolving. */
  scheme?: "http" | "https";
}

/**
 * Send with no identity at all: E2E-encrypted to the recipient, unsigned, and
 * gated by whatever challenge their policy demands. The recipient must have
 * opted in (`policy set … --anon-allow`); the default everywhere is deny.
 */
export async function sendAnonymous(
  recipient: string,
  plaintext: string,
  options: AnonSendOptions = {},
): Promise<{ id: string; status: number; targetRelay: string }> {
  const resolveOptions = options.resolve ?? {};
  const recipientKey = await resolveEncryptionKey(recipient, resolveOptions);
  if (!recipientKey) {
    throw new PoweurError(
      "not_found",
      `${recipient} has no published encryption key; anonymous messages must still be encrypted`,
    );
  }
  const { payload, encryption } = encryptMessage(recipientKey, plaintext);
  const targetRelay =
    options.targetRelayUrl ??
    (await resolveRecipientRelayUrl(recipient, options.scheme ?? "https", resolveOptions));
  const client = options.clientFor?.(targetRelay) ?? new RelayClient(targetRelay);

  const id = newMessageId();
  const body: Record<string, unknown> = {
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
    let envelope: Record<string, unknown> = {};
    try {
      envelope = JSON.parse(text) as Record<string, unknown>;
    } catch {
      throw new PoweurError("relay_error", "malformed challenge from relay", { detail: text });
    }
    const challenge = (envelope["challenge"] ?? {}) as Record<string, unknown>;
    const type = String(challenge["type"] ?? "");
    const bits = Number(challenge["bits"] ?? 0);
    const token = String(challenge["token"] ?? "");
    if (type !== ANON_CHALLENGE_POW || options.solveChallenge === false) {
      throw new ChallengeRequiredError({
        type,
        token,
        bits,
        algo: challenge["algo"] as string | undefined,
        detail: challenge["detail"] as string | undefined,
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
    throw new RelayError(
      `relay rejected anonymous message (${response.status}): ${text.trim()}`,
      { status: response.status, detail: text },
    );
  }
  return { id, status: response.status, targetRelay };
}
