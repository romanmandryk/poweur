/**
 * Short-lived session keys. The identity key authorizes a fresh Ed25519
 * session keypair once per TTL (24h, matching the CLI); everything after that
 * signs with the session key, so an unlock prompt happens at most once a day
 * rather than once a message.
 *
 * The `SessionProof` carried on each message lets a relay that never issued
 * the session verify it anyway — that is what makes direct cross-relay send
 * work without the recipient relay trusting ours.
 */

import { canonicalSessionRegistration, canonicalSessionRevocation } from "./canonical.js";
import { ed25519PublicKey, generateSigningKeypair } from "./crypto/index.js";
import { LocalSigner, type Signer } from "./crypto/keys.js";
import { fromBase64, rfc3339, toBase64Std, toBase64url } from "./encoding.js";
import { RelayClient } from "./http.js";
import { newNonce } from "./ids.js";
import type { SessionProof } from "./types.js";

/** Session TTL. The CLI registers for 24h and treats <30s left as expired. */
export const SESSION_TTL_MS = 24 * 60 * 60 * 1000;
const EXPIRY_SKEW_MS = 30_000;

/**
 * Issued and expiry from one clock reading. Two `Date.now()` calls can cross a
 * second boundary; after RFC3339 truncation that becomes 24h+1s, and the
 * relay rejects it (`session exceeds max TTL`).
 */
export function sessionValidityWindow(
  now: Date = new Date(),
  ttlMs = SESSION_TTL_MS,
): { issuedAt: string; expiresAt: string } {
  return {
    issuedAt: rfc3339(now),
    expiresAt: rfc3339(new Date(now.getTime() + ttlMs)),
  };
}

export interface StoredSession {
  identity: string;
  sessionId: string;
  /** Base64 (raw std, Go's encoding) of the 64-byte Ed25519 private key. */
  sessionPrivateKey: string;
  sessionPublicKey: string;
  issuedAt: string;
  expiresAt: string;
  nonce: string;
  identitySignature: string;
  relayUrl: string;
}

/** Where sessions persist. Node uses `~/.poweur/sessions`; browsers, memory. */
export interface SessionStore {
  load(identity: string): Promise<StoredSession | null>;
  save(session: StoredSession): Promise<void>;
  remove(identity: string): Promise<void>;
}

export class MemorySessionStore implements SessionStore {
  readonly #sessions = new Map<string, StoredSession>();

  async load(identity: string): Promise<StoredSession | null> {
    return this.#sessions.get(identity) ?? null;
  }

  async save(session: StoredSession): Promise<void> {
    this.#sessions.set(session.identity, session);
  }

  async remove(identity: string): Promise<void> {
    this.#sessions.delete(identity);
  }
}

/** Valid = has an id and more than 30s of life left, same rule as the CLI. */
export function isSessionValid(session: StoredSession | null): session is StoredSession {
  if (!session?.sessionId || !session.sessionPrivateKey) return false;
  const expiry = Date.parse(session.expiresAt);
  if (Number.isNaN(expiry)) return false;
  return Date.now() + EXPIRY_SKEW_MS < expiry;
}

export function sessionProofFrom(session: StoredSession): SessionProof | null {
  if (
    !session.sessionPublicKey ||
    !session.issuedAt ||
    !session.expiresAt ||
    !session.nonce ||
    !session.identitySignature
  ) {
    return null;
  }
  return {
    session_public_key: session.sessionPublicKey,
    issued_at: session.issuedAt,
    expires_at: session.expiresAt,
    nonce: session.nonce,
    identity_signature: session.identitySignature,
  };
}

/** A Signer over a stored session's key, for signing messages and acks. */
export function sessionSigner(session: StoredSession): LocalSigner {
  return new LocalSigner(session.identity, fromBase64(session.sessionPrivateKey));
}

export class SessionManager {
  readonly #client: RelayClient;
  readonly #store: SessionStore;

  constructor(client: RelayClient, store: SessionStore = new MemorySessionStore()) {
    this.#client = client;
    this.#store = store;
  }

  get store(): SessionStore {
    return this.#store;
  }

  /** Load a valid cached session for this relay, or register a fresh one. */
  async ensure(signer: Signer): Promise<StoredSession> {
    const existing = await this.#store.load(signer.identity);
    if (isSessionValid(existing) && existing.relayUrl === this.#client.relayUrl) {
      return existing;
    }
    return this.register(signer);
  }

  async register(signer: Signer): Promise<StoredSession> {
    const keypair = generateSigningKeypair();
    // Go stores the 64-byte seed||public form; keep the same bytes on disk so
    // the Go CLI can pick the session up from the shared ~/.poweur tree.
    const privateKey = new Uint8Array(64);
    privateKey.set(keypair.privateKey, 0);
    privateKey.set(ed25519PublicKey(keypair.privateKey), 32);

    const sessionPublicKey = toBase64url(keypair.publicKey);
    const { issuedAt, expiresAt } = sessionValidityWindow();
    const nonce = newNonce();
    const identitySignature = await signer.sign(
      canonicalSessionRegistration(
        signer.identity,
        sessionPublicKey,
        issuedAt,
        expiresAt,
        nonce,
      ),
      "base64std",
    );

    const response = await this.#client.request<{ session_id: string }>({
      method: "POST",
      path: "/sessions",
      body: {
        identity: signer.identity,
        session_public_key: sessionPublicKey,
        issued_at: issuedAt,
        expires_at: expiresAt,
        nonce,
        identity_signature: identitySignature,
      },
    });

    const session: StoredSession = {
      identity: signer.identity,
      sessionId: response.session_id,
      sessionPrivateKey: toBase64Std(privateKey).replace(/=+$/, ""),
      sessionPublicKey,
      issuedAt,
      expiresAt,
      nonce,
      identitySignature,
      relayUrl: this.#client.relayUrl,
    };
    await this.#store.save(session);
    return session;
  }

  /** Drop the local session and tell the relay to forget it too. */
  async revoke(signer: Signer): Promise<{ relayRevoked: boolean }> {
    const existing = await this.#store.load(signer.identity);
    let relayRevoked = false;
    if (existing?.sessionId) {
      const issuedAt = rfc3339();
      const nonce = newNonce();
      const signature = await signer.sign(
        canonicalSessionRevocation(signer.identity, existing.sessionId, issuedAt, nonce),
        "base64std",
      );
      try {
        await this.#client.request({
          method: "DELETE",
          path: `/sessions/${encodeURIComponent(existing.sessionId)}`,
          body: {
            identity: signer.identity,
            issued_at: issuedAt,
            nonce,
            identity_signature: signature,
          },
          // The relay answers 404 when the id is already gone, which is the
          // desired post-condition, and 204 on success.
          allowStatus: [404],
        });
        relayRevoked = true;
      } catch {
        // A relay that refuses still leaves the local session deletable —
        // the local cache is what `session status` reads.
      }
    }
    await this.#store.remove(signer.identity);
    return { relayRevoked };
  }

  async status(identity: string): Promise<{ session: StoredSession | null; valid: boolean }> {
    const session = await this.#store.load(identity);
    return { session, valid: isSessionValid(session) };
  }

  /** Force a fresh session, discarding any cached one. */
  async refresh(signer: Signer): Promise<StoredSession> {
    await this.#store.remove(signer.identity);
    return this.register(signer);
  }
}
