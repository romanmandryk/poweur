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
import { canonicalKeystoreEnroll, canonicalKeystoreList, canonicalKeystoreRemove, } from "./canonical.js";
import { sha256Bytes } from "./crypto/index.js";
import { rfc3339, toBase64url, utf8 } from "./encoding.js";
import { PoweurError } from "./errors.js";
import { newNonce } from "./ids.js";
/**
 * Digest bound into the enrollment signature so the stored ciphertext cannot
 * be swapped under an otherwise valid authorization. Must serialize exactly as
 * the request body does — the relay hashes the bytes it received.
 */
export function wrappedDigest(wrapped) {
    return toBase64url(sha256Bytes(utf8(JSON.stringify(wrapped))));
}
export class KeystoreApi {
    #relay;
    constructor(relay) {
        this.#relay = relay;
    }
    /** Store or replace one authenticator's wrapped copy of the seed. */
    async enroll(signer, identity, options) {
        const hasCredential = Boolean(options.credentialId);
        if (hasCredential !== Boolean(options.credentialPublicKey)) {
            throw new PoweurError("invalid_argument", "credentialId and credentialPublicKey must be provided together");
        }
        const issuedAt = rfc3339();
        const nonce = newNonce();
        const canonical = canonicalKeystoreEnroll(identity.toLowerCase(), options.enrollmentId, options.kind, options.credentialId ?? "", wrappedDigest(options.wrapped), issuedAt, nonce);
        return this.#relay.request({
            method: "PUT",
            path: `/identities/${encodeURIComponent(identity)}/keystore`,
            body: {
                enrollment_id: options.enrollmentId,
                kind: options.kind,
                wrap: options.wrap,
                payload: options.payload ?? "seed",
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
    async list(signer, identity = signer.identity) {
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
    async challenge(identity) {
        const { challenge } = await this.#relay.request({
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
    async fetch(identity, assertion, rpId) {
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
    async remove(signer, identity, enrollmentId, options = {}) {
        const issuedAt = rfc3339();
        const nonce = newNonce();
        const canonical = canonicalKeystoreRemove(identity.toLowerCase(), enrollmentId, issuedAt, nonce);
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
