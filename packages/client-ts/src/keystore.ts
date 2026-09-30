/**
 * Keystore client — wrapped master-seed copies held by the relay
 * (EPIC-011 E11-T1).
 *
 * Two asymmetric halves, and the asymmetry is the point:
 *
 * - **Writes** are signed by the identity key. You are unlocked when you
 *   enroll a device, so there is nothing to bootstrap.
 * - **The read** is authenticated by a WebAuthn assertion instead, because its
 *   purpose is recovering an identity whose key you no longer hold. Reading
 *   anything under `poweur-sys/` would need a DAV token signed by that very
 *   key — the circularity this endpoint exists to break.
 *
 * The relay stores ciphertext it cannot open; wrapping secrets never leave the
 * authenticator or device.
 */

import {
  canonicalKeystoreEnroll,
  canonicalKeystoreList,
  canonicalKeystoreRemove,
} from "./canonical.js";
import { sha256Bytes } from "./crypto/index.js";
import type { Signer } from "./crypto/keys.js";
import { rfc3339, toBase64url, utf8 } from "./encoding.js";
import { PoweurError } from "./errors.js";
import type { RelayClient } from "./http.js";
import { newNonce } from "./ids.js";

/** How the seed copy is protected. */
export type KeystoreWrap = "prf" | "passphrase" | "native";

/** What kind of enrollment holds it. */
export type KeystoreKind =
  | "passkey"
  | "hardware-key"
  | "cli-passphrase"
  | "recovery-kit"
  | "native";

/** Opaque ciphertext. The relay never interprets this. */
export interface WrappedBlob {
  iv: string;
  ciphertext: string;
  /** Present for passphrase wraps (PBKDF2 salt). */
  salt?: string;
}

export interface KeystoreEntry {
  enrollment_id: string;
  kind: KeystoreKind;
  wrap: KeystoreWrap;
  credential_id?: string;
  credential_public_key?: string;
  credential_alg?: number;
  wrapped: WrappedBlob;
  label?: string;
  role?: "device" | "recovery-master";
  created_at: string;
  last_used_at?: string;
}

/**
 * One row of the inventory: everything needed to show a device, and no
 * ciphertext. `has_passkey` stands in for the credential itself, which the
 * listing deliberately withholds — an attacker who could enumerate credential
 * ids would learn which authenticators to phish.
 */
export interface KeystoreSummary {
  enrollment_id: string;
  kind: KeystoreKind;
  wrap: KeystoreWrap;
  label?: string;
  role?: "device" | "recovery-master";
  has_passkey: boolean;
  created_at: string;
  last_used_at?: string;
}

/** A WebAuthn assertion, base64url-encoded as the browser produces it. */
export interface WebAuthnAssertion {
  credential_id: string;
  client_data_json: string;
  authenticator_data: string;
  signature: string;
}

export interface EnrollOptions {
  enrollmentId: string;
  kind: KeystoreKind;
  wrap: KeystoreWrap;
  wrapped: WrappedBlob;
  /** Required together: a passkey enrollment that cannot be verified is useless. */
  credentialId?: string;
  /** SPKI DER (base64url) from WebAuthn's `getPublicKey()`. */
  credentialPublicKey?: string;
  /** COSE alg id: -7 ES256, -8 EdDSA, -257 RS256. */
  credentialAlg?: number;
  label?: string;
  role?: "device" | "recovery-master";
}

/**
 * Digest bound into the enrollment signature so the stored ciphertext cannot
 * be swapped under an otherwise valid authorization. Must serialize exactly as
 * the request body does — the relay hashes the bytes it received.
 */
export function wrappedDigest(wrapped: WrappedBlob): string {
  return toBase64url(sha256Bytes(utf8(JSON.stringify(wrapped))));
}

export interface RemoveOptions {
  /**
   * Assertion from a `recovery-master` authenticator. Required once the
   * identity has one — see `remove`.
   */
  actorAssertion?: WebAuthnAssertion;
  rpId?: string;
  /**
   * Permit removing the final enrollment. Refused by default: silently
   * stranding recovery is worse than an error the caller acknowledges.
   */
  allowLast?: boolean;
  /** Also end the removed device's live sessions — the kill-my-phone path. */
  revokeSessions?: boolean;
}

export class KeystoreApi {
  readonly #relay: RelayClient;

  constructor(relay: RelayClient) {
    this.#relay = relay;
  }

  /** Store or replace one authenticator's wrapped copy of the seed. */
  async enroll(
    signer: Signer,
    identity: string,
    options: EnrollOptions,
  ): Promise<{ enrollment_id: string; created_at: string }> {
    const hasCredential = Boolean(options.credentialId);
    if (hasCredential !== Boolean(options.credentialPublicKey)) {
      throw new PoweurError(
        "invalid_argument",
        "credentialId and credentialPublicKey must be provided together",
      );
    }
    const issuedAt = rfc3339();
    const nonce = newNonce();
    const canonical = canonicalKeystoreEnroll(
      identity.toLowerCase(),
      options.enrollmentId,
      options.kind,
      options.credentialId ?? "",
      wrappedDigest(options.wrapped),
      issuedAt,
      nonce,
    );
    return this.#relay.request({
      method: "PUT",
      path: `/identities/${encodeURIComponent(identity)}/keystore`,
      body: {
        enrollment_id: options.enrollmentId,
        kind: options.kind,
        wrap: options.wrap,
        ...(options.credentialId ? { credential_id: options.credentialId } : {}),
        ...(options.credentialPublicKey
          ? { credential_public_key: options.credentialPublicKey }
          : {}),
        ...(options.credentialAlg ? { credential_alg: options.credentialAlg } : {}),
        wrapped: options.wrapped,
        ...(options.label ? { label: options.label } : {}),
        ...(options.role ? { role: options.role } : {}),
        issued_at: issuedAt,
        nonce,
        identity_signature: await signer.sign(canonical),
      },
    });
  }

  /**
   * The device inventory, identity-signed — the "Keys & devices" list.
   *
   * Metadata only, so it needs no authenticator prompt: you are already
   * unlocked when you manage devices. `fetch` is the one that needs an
   * assertion, because it runs when you are not.
   */
  async list(
    signer: Signer,
    identity: string = signer.identity,
  ): Promise<{ identity: string; enrollments: KeystoreSummary[] }> {
    const issuedAt = rfc3339();
    const nonce = newNonce();
    const canonical = canonicalKeystoreList(identity.toLowerCase(), issuedAt, nonce);
    return this.#relay.request({
      method: "POST",
      path: `/identities/${encodeURIComponent(identity)}/keystore/list`,
      body: {
        issued_at: issuedAt,
        nonce,
        identity_signature: await signer.sign(canonical),
      },
    });
  }

  /**
   * A fresh challenge for the authenticator to sign.
   *
   * Lives here because `fetch` is the one call in this package that cannot be
   * made with a signer, so its caller has no other way to reach the relay.
   * Single-use and consumed whatever the outcome: a failed attempt needs a new
   * one.
   */
  async challenge(identity: string): Promise<string> {
    const { challenge } = await this.#relay.request<{ challenge: string }>({
      method: "GET",
      path: `/auth/challenge?identity=${encodeURIComponent(identity)}`,
    });
    return challenge;
  }

  /**
   * Bootstrap read: fetch the wrapped copies with a WebAuthn assertion and no
   * identity key. Get the challenge from `challenge()` and sign it with the
   * enrolled authenticator.
   */
  async fetch(
    identity: string,
    assertion: WebAuthnAssertion,
    rpId?: string,
  ): Promise<{ identity: string; entries: KeystoreEntry[] }> {
    return this.#relay.request({
      method: "POST",
      path: `/identities/${encodeURIComponent(identity)}/keystore/fetch`,
      body: { assertion, ...(rpId ? { rp_id: rpId } : {}) },
    });
  }

  /**
   * Remove an enrollment. This denies that authenticator the bootstrap read;
   * it does **not** protect against an attacker who already extracted the
   * seed — that is what rotation is for.
   *
   * Once an identity has a `recovery-master` enrollment, removals additionally
   * require `actorAssertion` from that authenticator. The identity key is
   * shared by every device, so without it the relay cannot tell which device
   * is asking — and a stolen phone could evict the very security key meant to
   * revoke it.
   */
  async remove(
    signer: Signer,
    identity: string,
    enrollmentId: string,
    options: RemoveOptions = {},
  ): Promise<void> {
    const issuedAt = rfc3339();
    const nonce = newNonce();
    const canonical = canonicalKeystoreRemove(
      identity.toLowerCase(),
      enrollmentId,
      issuedAt,
      nonce,
    );
    await this.#relay.request({
      method: "DELETE",
      path: `/identities/${encodeURIComponent(identity)}/keystore/${encodeURIComponent(enrollmentId)}`,
      body: {
        issued_at: issuedAt,
        nonce,
        identity_signature: await signer.sign(canonical),
        ...(options.actorAssertion ? { actor_assertion: options.actorAssertion } : {}),
        ...(options.rpId ? { rp_id: options.rpId } : {}),
        ...(options.allowLast ? { allow_last: true } : {}),
        ...(options.revokeSessions ? { revoke_sessions: true } : {}),
      },
      allowStatus: [204],
    });
  }
}
