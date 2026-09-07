/**
 * New-device enrollment ceremony (EPIC-011 E11-T3).
 *
 * Moving a seed to a new device needs an **authentic** channel, not a secret
 * one. The new device generates an ephemeral X25519 keypair and displays a
 * six-digit code derived from its public key; the user types that code on a
 * device that already holds the identity, which seals the seed to the
 * ephemeral key.
 *
 * **Why there is no PAKE here.** If the code protected the payload it would be
 * a six-digit password, brute-forceable offline by anyone holding the
 * ciphertext — hence the usual SPAKE2 machinery. Having the *new* device
 * generate the keypair removes the requirement: the code authenticates a
 * public key and encrypts nothing, so there is no offline target. Forging it
 * means finding a colliding code on the first and only try. This is the
 * numeric-comparison model used by Bluetooth pairing and Signal safety
 * numbers, and it needs no exotic primitive — which matters, because no
 * reviewed browser PAKE implementation exists.
 *
 * The relay is a blind letterbox: it sees an ephemeral public key and a sealed
 * blob, and can open neither.
 */

import { canonicalEnrollAction } from "./canonical.js";
import {
  generateEncryptionKeypair,
  open as openSealed,
  seal,
  sha256Bytes,
  x25519PublicKey,
  type SealedPayload,
} from "./crypto/index.js";
import type { Signer } from "./crypto/keys.js";
import { fromBase64, rfc3339, toBase64url, utf8 } from "./encoding.js";
import { PoweurError } from "./errors.js";
import type { RelayClient } from "./http.js";
import { newNonce } from "./ids.js";

/** Digits in the short authentication string shown on both screens. */
export const SAS_DIGITS = 6;

/**
 * Derive the code both devices display. Computed independently on each side
 * from the ephemeral public key, so the relay is never trusted to report it —
 * the value it echoes is a convenience, not an authority.
 */
export function computeSas(ephemeralPublicKey: string): string {
  const digest = sha256Bytes(utf8(`poweur/v1/enroll-sas\n${ephemeralPublicKey}`));
  const n =
    ((digest[0]! << 24) | (digest[1]! << 16) | (digest[2]! << 8) | digest[3]!) >>> 0;
  return String(n % 1_000_000).padStart(SAS_DIGITS, "0");
}

export interface EnrollOffer {
  rendezvous_id: string;
  sas: string;
  expires_at: string;
}

export interface PendingEnrollment {
  rendezvous_id: string;
  ephemeral_public_key: string;
  sas: string;
  label?: string;
  expires_at: string;
}

/** The new device's half: an ephemeral keypair plus the offer it opened. */
export interface EnrollSession {
  rendezvousId: string;
  /** Show this to the user; they type it on the device that has the identity. */
  sas: string;
  expiresAt: string;
  /** Never leaves this device. */
  ephemeralPrivateKey: Uint8Array;
  ephemeralPublicKey: string;
}

export class EnrollApi {
  readonly #relay: RelayClient;

  constructor(relay: RelayClient) {
    this.#relay = relay;
  }

  /**
   * Called on the **new** device. Generates the ephemeral keypair and opens a
   * rendezvous. Unauthenticated by necessity — this device has no key yet —
   * so the relay caps concurrent offers per identity.
   */
  async offer(identity: string, label?: string): Promise<EnrollSession> {
    const keypair = generateEncryptionKeypair();
    const ephemeralPublicKey = toBase64url(keypair.publicKey);
    const offer = await this.#relay.request<EnrollOffer>({
      method: "POST",
      path: `/identities/${encodeURIComponent(identity)}/enroll/offer`,
      body: { ephemeral_public_key: ephemeralPublicKey, ...(label ? { label } : {}) },
      allowStatus: [201],
    });
    // Verify the relay's code rather than displaying what it sent us.
    const sas = computeSas(ephemeralPublicKey);
    if (offer.sas && offer.sas !== sas) {
      throw new PoweurError("key_mismatch", "relay reported a SAS that does not match the key");
    }
    return {
      rendezvousId: offer.rendezvous_id,
      sas,
      expiresAt: offer.expires_at,
      ephemeralPrivateKey: keypair.privateKey,
      ephemeralPublicKey,
    };
  }

  /**
   * Called on the **approving** device once the user types the code. Returns
   * what is being approved; the caller must show `sas` and have the user
   * confirm it matches the new device's screen before calling `approve`.
   */
  async pending(
    signer: Signer,
    identity: string,
    rendezvousId: string,
  ): Promise<PendingEnrollment> {
    const body = await this.#signed(signer, "enroll-fetch", identity, rendezvousId);
    const pending = await this.#relay.request<PendingEnrollment>({
      method: "POST",
      path: `/identities/${encodeURIComponent(identity)}/enroll/${encodeURIComponent(rendezvousId)}/fetch`,
      body,
    });
    const sas = computeSas(pending.ephemeral_public_key);
    if (pending.sas && pending.sas !== sas) {
      throw new PoweurError("key_mismatch", "relay reported a SAS that does not match the key");
    }
    return { ...pending, sas };
  }

  /**
   * Seal the seed to the new device's ephemeral key and deliver it.
   *
   * Call only after the user has confirmed the codes match: that comparison is
   * the whole authentication step, and skipping it hands the seed to whoever
   * opened the rendezvous.
   */
  async approve(
    signer: Signer,
    identity: string,
    pending: PendingEnrollment,
    seed: Uint8Array,
  ): Promise<void> {
    const sealed = seal(pending.ephemeral_public_key, seed);
    const body = {
      ...(await this.#signed(signer, "enroll-deliver", identity, pending.rendezvous_id)),
      sealed: JSON.stringify(sealed),
    };
    await this.#relay.request({
      method: "POST",
      path: `/identities/${encodeURIComponent(identity)}/enroll/${encodeURIComponent(pending.rendezvous_id)}/deliver`,
      body,
      allowStatus: [204],
    });
  }

  /**
   * Called on the **new** device to collect the seed. Returns null while the
   * other side has not approved yet, so callers can poll. The rendezvous is
   * consumed on success — a second call fails, which is what stops a captured
   * id being replayed.
   */
  async claim(identity: string, session: EnrollSession): Promise<Uint8Array | null> {
    const result = await this.#relay.request<{ ready: boolean; sealed?: string }>({
      method: "GET",
      path: `/identities/${encodeURIComponent(identity)}/enroll/${encodeURIComponent(session.rendezvousId)}`,
    });
    if (!result.ready || !result.sealed) return null;
    let payload: SealedPayload;
    try {
      payload = JSON.parse(result.sealed) as SealedPayload;
    } catch {
      throw new PoweurError("decrypt_failed", "sealed payload is not valid JSON");
    }
    const seed = openSealed(session.ephemeralPrivateKey, payload);
    // A payload sealed to a different key would not have decrypted, but check
    // the shape before handing 32 bytes to the key derivation.
    if (seed.length !== 32) {
      throw new PoweurError("decrypt_failed", `expected a 32-byte seed, got ${seed.length}`);
    }
    return seed;
  }

  /**
   * Abandon an offer, freeing its slot. Callers should do this when the user
   * backs out: the relay caps concurrent offers, so a litter of abandoned
   * ceremonies would lock the identity out until they expired.
   */
  async cancel(identity: string, session: Pick<EnrollSession, "rendezvousId">): Promise<void> {
    await this.#relay.request({
      method: "DELETE",
      path: `/identities/${encodeURIComponent(identity)}/enroll/${encodeURIComponent(session.rendezvousId)}`,
      allowStatus: [204],
    });
  }

  async #signed(
    signer: Signer,
    action: string,
    identity: string,
    rendezvousId: string,
  ): Promise<Record<string, string>> {
    const issuedAt = rfc3339();
    const nonce = newNonce();
    const canonical = canonicalEnrollAction(
      action,
      identity.toLowerCase(),
      rendezvousId,
      issuedAt,
      nonce,
    );
    return {
      issued_at: issuedAt,
      nonce,
      identity_signature: await signer.sign(canonical),
    };
  }
}

/** Public key for an ephemeral private key — for callers rebuilding a session. */
export function ephemeralPublicKeyOf(privateKey: Uint8Array): string {
  return toBase64url(x25519PublicKey(privateKey));
}

/** Decode a base64url ephemeral key, for tests and cross-device resumption. */
export function decodeEphemeralKey(value: string): Uint8Array {
  return fromBase64(value);
}
