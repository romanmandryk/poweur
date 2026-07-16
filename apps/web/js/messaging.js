/**
 * Relay messaging protocol helpers (session + identity-signed send, inbox).
 * Shared by the SPA and Vitest/Playwright clients — mirrors CLI send/inbox.
 */

import {
  generateSigningKeypair,
  generateEncryptionKeypair,
  sign,
  encryptMessage,
  decryptMessage,
  buildSignedIdentityDocument,
  canonicalIdentityRegistration,
  canonicalSessionRegistration,
  canonicalMessage,
  generateMessageId,
  now,
  randomNonce,
  toBase64url,
} from "./crypto.js";
import {
  registerIdentity,
  registerSession,
  sendMessage,
  getChallenge,
  fetchInbox,
  fetchRelayAddress,
  resolveEncryptionKey,
  getIdentityKey,
} from "./api.js";
import { signChallenge } from "./passkey.js";

/** @typedef {'session' | 'identity'} SignWith */

/**
 * Validate sign-with mode (CLI: --sign-with session|identity).
 * @param {string} mode
 * @returns {SignWith}
 */
export function assertSignWith(mode) {
  if (mode !== "session" && mode !== "identity") {
    throw new Error(`invalid --sign-with: ${mode} (want session or identity)`);
  }
  return mode;
}

/**
 * Register a hosted identity with a signed identity_document.
 * Returns local key material (not persisted).
 */
export async function registerHostedIdentity(relayUrl, { identity, signingJWK, encJWK, publicKey, encPublicKey }) {
  const relayAddr = await fetchRelayAddress(relayUrl);
  const issuedAt = now();
  const nonce = randomNonce();
  const identitySignature = await sign(
    signingJWK,
    canonicalIdentityRegistration(identity, publicKey, encPublicKey, relayAddr, issuedAt, nonce),
  );
  const identityDocument = await buildSignedIdentityDocument(signingJWK, {
    identity,
    publicKey,
    encPublicKey,
    relay: relayAddr,
    updatedAt: issuedAt,
  });
  const resp = await registerIdentity(relayUrl, {
    identity,
    public_key: publicKey,
    encryption_public_key: encPublicKey,
    issued_at: issuedAt,
    nonce,
    identity_signature: identitySignature,
    identity_document: identityDocument,
  });
  return { resp, relayAddr, issuedAt };
}

/** Generate keys and register a hosted identity in one step. */
export async function createHostedIdentity(relayUrl, identity) {
  const { publicKeyBytes: sigPub, privateKeyJWK: signingJWK } = await generateSigningKeypair();
  const { publicKeyBytes: encPub, privateKeyJWK: encJWK } = await generateEncryptionKeypair();
  const publicKey = toBase64url(sigPub);
  const encPublicKey = toBase64url(encPub);
  await registerHostedIdentity(relayUrl, { identity, signingJWK, encJWK, publicKey, encPublicKey });
  return { identity, signingJWK, encJWK, publicKey, encPublicKey };
}

/**
 * Register a short-lived session (CLI default send path).
 * Returns the session record (caller may persist).
 */
export async function createSession(relayUrl, identity, identitySigningJWK) {
  const { publicKeyBytes: sesPub, privateKeyJWK: sessionSigningJWK } = await generateSigningKeypair();
  const sessionPublicKey = toBase64url(sesPub);
  const issuedAt = now();
  const expiresAt = new Date(Date.now() + 23 * 3600_000).toISOString().replace(/\.\d{3}Z$/, "Z");
  const nonce = randomNonce();
  const identitySignature = await sign(
    identitySigningJWK,
    canonicalSessionRegistration(identity, sessionPublicKey, issuedAt, expiresAt, nonce),
  );
  const res = await registerSession(relayUrl, {
    identity,
    session_public_key: sessionPublicKey,
    issued_at: issuedAt,
    expires_at: expiresAt,
    nonce,
    identity_signature: identitySignature,
  });
  return {
    sessionId: res.session_id,
    sessionPublicKey,
    sessionSigningJWK,
    issuedAt,
    expiresAt,
    nonce,
    identitySignature,
  };
}

async function resolveRecipientEncKey(relayUrl, recipient) {
  let key = await resolveEncryptionKey(recipient, { relayUrl });
  if (!key) {
    try {
      const r = await getIdentityKey(relayUrl, recipient);
      key = r.encryption_public_key;
      if (key?.startsWith("x25519:")) key = key.slice("x25519:".length);
    } catch {
      /* ignore */
    }
  }
  if (!key) throw new Error(`Cannot resolve encryption key for ${recipient}`);
  return key;
}

/**
 * Encrypt, sign, and POST a message (CLI `poweur send`).
 * @param {object} opts
 * @param {SignWith} [opts.signWith='session']
 * @param {object|null} [opts.session] — required when signWith=session
 */
export async function sendEncryptedMessage({
  relayUrl,
  sender,
  recipient,
  plaintext,
  identitySigningJWK,
  signWith = "session",
  session = null,
}) {
  const mode = assertSignWith(signWith);
  if (mode === "session" && !session?.sessionId) {
    throw new Error("session required when signWith=session");
  }

  const recipientEncKey = await resolveRecipientEncKey(relayUrl, recipient);
  const { ciphertext, ephemeralPublicKey, nonce } = await encryptMessage(plaintext, recipientEncKey);
  const msgId = generateMessageId();
  const timestamp = now();
  const enc_ = { alg: "x25519-chacha20-poly1305", ephemeralPublicKey, nonce };
  const sessionId = mode === "session" ? session.sessionId : "";
  const canonical = canonicalMessage(sender, recipient, timestamp, ciphertext, msgId, sessionId, enc_);
  const sigJWK = mode === "session" ? session.sessionSigningJWK : identitySigningJWK;
  const signature = await sign(sigJWK, canonical);

  const message = {
    id: msgId,
    sender,
    recipient,
    timestamp,
    payload: ciphertext,
    signature,
    encryption: {
      alg: enc_.alg,
      ephemeral_public_key: ephemeralPublicKey,
      nonce,
    },
  };
  if (mode === "session") {
    message.session_id = session.sessionId;
    message.session_proof = {
      session_public_key: session.sessionPublicKey,
      issued_at: session.issuedAt,
      expires_at: session.expiresAt,
      nonce: session.nonce,
      identity_signature: session.identitySignature,
    };
  }

  await sendMessage(relayUrl, message);
  return message;
}

/** Authenticate and pull inbox (+ acks). */
export async function pullInbox(relayUrl, identity, identitySigningJWK) {
  const { challenge } = await getChallenge(relayUrl, identity);
  const signature = await signChallenge(identitySigningJWK, challenge);
  return fetchInbox(relayUrl, identity, challenge, signature);
}

/** Decrypt message payloads; skips failures. */
export async function decryptInboxMessages(encJWK, messages = []) {
  const out = [];
  for (const m of messages) {
    try {
      const plaintext = await decryptMessage(encJWK, {
        ciphertext: m.payload,
        ephemeralPublicKey: m.encryption?.ephemeral_public_key,
        nonce: m.encryption?.nonce,
      });
      out.push({ ...m, plaintext });
    } catch {
      out.push({ ...m, plaintext: null, decryptError: true });
    }
  }
  return out;
}
