/**
 * Identity lifecycle: create, register (hosted or DNS-token), publish and
 * rotate encryption keys, export, rotate signing keys, look up.
 *
 * One function per relay endpoint that exists today; the signing always goes
 * through the `Signer` seam so a passkey-held key and an agent's file key are
 * interchangeable here.
 */
import { canonicalEncryptionKeyUpdate, canonicalIdentityExport, canonicalIdentityRegistration, canonicalIdentityRotation, } from "./canonical.js";
import { generateIdentityKeys, signerFor } from "./crypto/keys.js";
import { x25519PublicKey, ed25519PublicKey } from "./crypto/index.js";
import { newDocument, signDocument } from "./document.js";
import { rfc3339, stripKeyPrefix, toBase64url } from "./encoding.js";
import { PoweurError, RelayError } from "./errors.js";
import { newNonce } from "./ids.js";
import { RelayClient } from "./http.js";
import { solvePow } from "./pow.js";
export class IdentityApi {
    client;
    constructor(relayUrl, options = {}) {
        this.client =
            relayUrl instanceof RelayClient ? relayUrl : new RelayClient(relayUrl, options);
    }
    health() {
        return this.client.request({ method: "GET", path: "/health" });
    }
    /**
     * Can this handle be claimed here (EPIC-018 E18-T2)?
     *
     * Called before the passkey ceremony, not after: the relay's answer carries
     * a `reason` and the policy it applied, so a client can say *why* a name is
     * unavailable and validate the next attempt inline.
     */
    availability(handle, domain) {
        const query = new URLSearchParams({ handle });
        if (domain)
            query.set("domain", domain);
        return this.client.request({
            method: "GET",
            path: `/hosted/availability?${query.toString()}`,
        });
    }
    /**
     * The relay's service banner (EPIC-015 E15-T7).
     *
     * It carries the launcher hosts and hosted domains a client needs to work
     * out which front door it is serving, so this is the one unauthenticated
     * read that happens before anything else — including before an identity
     * exists.
     */
    root() {
        return this.client.request({ method: "GET", path: "/" });
    }
    /**
     * The relay's canonical address (host[:port]). Registration signatures bind
     * to it, so a captured signature cannot be replayed at another relay.
     */
    async relayAddress() {
        const root = await this.root();
        if (!root.relay_address) {
            throw new PoweurError("relay_error", "relay did not return its address");
        }
        return root.relay_address;
    }
    register(request) {
        return this.client.request({
            method: "POST",
            path: "/identities",
            body: request,
        });
    }
    get(identity) {
        return this.client.request({
            method: "GET",
            path: `/identities/${encodeURIComponent(identity)}`,
        });
    }
    /**
     * Fetch and solve the relay's registration challenge (REGISTRATION_GATE=pow).
     *
     * The callbacks exist because this runs in front of a person waiting to sign
     * up: at the difficulties an operator actually sets, a silent solve is a
     * signup screen that looks broken.
     */
    async solveRegistrationChallenge(options = {}) {
        const challenge = await this.client.request({
            method: "GET",
            path: "/auth/pow?purpose=registration",
        });
        if (!challenge.token) {
            throw new PoweurError("relay_error", "malformed registration challenge");
        }
        options.onChallenge?.({ bits: challenge.bits });
        const solution = await solvePow(challenge.token, challenge.bits, {
            ...(options.onSolveProgress ? { onProgress: options.onSolveProgress } : {}),
            ...(options.signal ? { signal: options.signal } : {}),
        });
        return { token: challenge.token, solution };
    }
    /** Publish or rotate an identity's X25519 encryption key. */
    async publishEncryptionKey(signer, encryptionPublicKey, options = {}) {
        const issuedAt = rfc3339();
        const nonce = newNonce();
        const key = stripKeyPrefix(encryptionPublicKey);
        const signature = await signer.sign(canonicalEncryptionKeyUpdate(signer.identity, key, issuedAt, nonce), "base64std");
        return this.client.request({
            method: "POST",
            path: `/identities/${encodeURIComponent(signer.identity)}/encryption-key`,
            body: {
                encryption_public_key: key,
                ...(options.dnsProvider ? { dns_provider: options.dnsProvider } : {}),
                ...(options.dnsToken ? { dns_token: options.dnsToken } : {}),
                issued_at: issuedAt,
                nonce,
                identity_signature: signature,
            },
        });
    }
    /** Owner-authorized full export; returns the raw .tar.gz bytes. */
    async export(signer) {
        const issuedAt = rfc3339();
        const nonce = newNonce();
        const signature = await signer.sign(canonicalIdentityExport(signer.identity, issuedAt, nonce), "base64std");
        const response = await this.client.raw({
            method: "POST",
            path: `/identities/${encodeURIComponent(signer.identity)}/export`,
            body: { issued_at: issuedAt, nonce, identity_signature: signature },
        });
        if (!response.ok) {
            throw new PoweurError("relay_error", `export failed: HTTP ${response.status}`, {
                status: response.status,
                detail: await response.text(),
            });
        }
        return new Uint8Array(await response.arrayBuffer());
    }
    /**
     * Rotate the signing key. The *old* key signs the rotation so a resolver can
     * chain trust across the change; the new document must already list the old
     * key under `previous_keys` for in-flight verifiers.
     */
    async rotate(oldSigner, newKeys) {
        const issuedAt = rfc3339();
        const nonce = newNonce();
        const signature = await oldSigner.sign(canonicalIdentityRotation(oldSigner.identity, stripKeyPrefix(oldSigner.publicKey), stripKeyPrefix(newKeys.newPublicKey), issuedAt, nonce), "base64std");
        return this.client.request({
            method: "POST",
            path: `/identities/${encodeURIComponent(oldSigner.identity)}/rotate`,
            body: {
                identity_document: newKeys.document,
                new_public_key: stripKeyPrefix(newKeys.newPublicKey),
                ...(newKeys.encryptionPublicKey
                    ? { encryption_public_key: stripKeyPrefix(newKeys.encryptionPublicKey) }
                    : {}),
                issued_at: issuedAt,
                nonce,
                rotation_signature: signature,
            },
        });
    }
}
/**
 * Generate keys, sign the registration envelope, and register with the relay.
 * The equivalent of `poweur identity create`.
 */
export async function createIdentity(api, identity, options = {}) {
    const keys = options.keys ?? generateIdentityKeys(identity);
    const { signer } = signerFor(keys);
    const publicKey = toBase64url(ed25519PublicKey(keys.signingPrivateKey));
    const encryptionPublicKey = keys.encryptionPrivateKey
        ? toBase64url(x25519PublicKey(keys.encryptionPrivateKey))
        : "";
    const relayAddress = await api.relayAddress();
    const issuedAt = rfc3339();
    const nonce = newNonce();
    const identitySignature = await signer.sign(canonicalIdentityRegistration(identity, publicKey, encryptionPublicKey, relayAddress, issuedAt, nonce), "base64std");
    const document = await signDocument(newDocument({
        identity,
        publicKey,
        ...(encryptionPublicKey ? { encryptionPublicKey } : {}),
        relay: relayAddress,
        updatedAt: issuedAt,
    }), signer);
    const request = {
        identity,
        public_key: publicKey,
        ...(encryptionPublicKey ? { encryption_public_key: encryptionPublicKey } : {}),
        ...(options.inviteCode ? { invite_code: options.inviteCode } : {}),
        identity_document: document,
        issued_at: issuedAt,
        nonce,
        identity_signature: identitySignature,
    };
    if (!options.hosted) {
        if (!options.dnsToken) {
            throw new PoweurError("invalid_argument", "a DNS token is required for non-hosted registration (or pass hosted: true)");
        }
        request.dns_provider = options.dnsProvider ?? "cloudflare";
        request.dns_token = options.dnsToken;
    }
    let response;
    try {
        response = await api.register(request);
    }
    catch (error) {
        // Branch on the relay's *code*, not its prose: the message a relay sends
        // is human-facing ("hosted registration requires a solved proof-of-work
        // challenge"), so matching on "pow_required" in it never fired against a
        // real relay and the auto-solve silently did nothing.
        const relayCode = error instanceof RelayError ? error.relayCode : undefined;
        const message = error instanceof Error ? error.message : String(error);
        const gated = relayCode === "pow_required" || message.includes("pow_required");
        if (!gated || options.solveRegistrationPow === false)
            throw error;
        const { token, solution } = await api.solveRegistrationChallenge({
            ...(options.onRegistrationChallenge ? { onChallenge: options.onRegistrationChallenge } : {}),
            ...(options.onRegistrationProgress ? { onSolveProgress: options.onRegistrationProgress } : {}),
        });
        response = await api.register({ ...request, pow_token: token, pow_solution: solution });
    }
    return { identity, keys, publicKey, encryptionPublicKey, document, response };
}
