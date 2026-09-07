/**
 * Identity documents — the twin of `packages/identity/doc.go`.
 *
 * The document is what makes an identity self-describing and verifiable
 * without trusting the relay that serves it: the owner signs canonical JSON
 * over everything but the signature, so any resolver can re-derive it.
 */
import { marshalCanonicalJSON } from "./canonical.js";
import { parseEd25519PublicKey, signCanonical, verifyCanonical } from "./crypto/index.js";
import { rfc3339, stripKeyPrefix, withEd25519Prefix, withX25519Prefix } from "./encoding.js";
import { PoweurError } from "./errors.js";
/** Build an unsigned v1 document ready for `signDocument`. */
export function newDocument(input) {
    const doc = {
        version: 1,
        identity: input.identity,
        public_key: withEd25519Prefix(input.publicKey),
        relay: input.relay,
        capabilities: input.capabilities ?? ["messaging"],
        updated_at: input.updatedAt ?? rfc3339(),
    };
    if (input.encryptionPublicKey) {
        doc.encryption_public_key = withX25519Prefix(input.encryptionPublicKey);
    }
    return doc;
}
/**
 * Deterministic JSON for signing: sorted keys, no whitespace, signature and
 * empty optional fields omitted. Must match `IdentityDocument.CanonicalBytes`.
 */
export function canonicalDocument(doc) {
    const canonical = {
        version: doc.version,
        identity: doc.identity,
        public_key: doc.public_key,
        relay: doc.relay,
        updated_at: doc.updated_at,
    };
    if (doc.encryption_public_key)
        canonical["encryption_public_key"] = doc.encryption_public_key;
    if (doc.capabilities && doc.capabilities.length > 0)
        canonical["capabilities"] = doc.capabilities;
    if (doc.previous_keys && doc.previous_keys.length > 0) {
        canonical["previous_keys"] = doc.previous_keys.map((key) => {
            const entry = { public_key: key.public_key };
            if (key.valid_until)
                entry["valid_until"] = key.valid_until;
            return entry;
        });
    }
    if (doc.moved_to)
        canonical["moved_to"] = doc.moved_to;
    return marshalCanonicalJSON(canonical);
}
/** Sign with raw key bytes (used where no Signer is in play, e.g. the CLI). */
export function signDocumentWithKey(doc, privateKey) {
    return { ...doc, signature: signCanonical(privateKey, canonicalDocument(doc)) };
}
/** Sign through the Signer seam — the path browsers and agents use. */
export async function signDocument(doc, signer) {
    const signature = await signer.sign(canonicalDocument(doc), "base64url");
    return { ...doc, signature };
}
/** Throws unless the document is well-formed and self-signed. Fail-closed. */
export function verifyDocument(doc) {
    if (doc.version !== 1) {
        throw new PoweurError("invalid_document", `unsupported identity document version ${doc.version}`);
    }
    if (!doc.identity || !doc.public_key || !doc.relay || !doc.updated_at) {
        throw new PoweurError("invalid_document", "identity document missing required fields");
    }
    if (!doc.signature) {
        throw new PoweurError("invalid_document", "identity document missing signature");
    }
    const publicKey = parseEd25519PublicKey(doc.public_key);
    if (!verifyCanonical(publicKey, canonicalDocument(doc), doc.signature)) {
        throw new PoweurError("invalid_signature", "identity document signature invalid");
    }
}
/** Parse JSON into a document, verifying the signature unless told not to. */
export function parseDocument(raw, verify = true) {
    const text = typeof raw === "string" ? raw : new TextDecoder().decode(raw);
    let doc;
    try {
        doc = JSON.parse(text);
    }
    catch (cause) {
        throw new PoweurError("invalid_document", "identity document is not valid JSON", { cause });
    }
    if (verify)
        verifyDocument(doc);
    return doc;
}
/**
 * Is `publicKey` the document's current key, or a previous key still inside
 * its rotation grace window? A previous key with no `valid_until` is
 * informational only and never valid — same as Go.
 */
export function keyValidAt(doc, publicKey, at = new Date()) {
    const want = stripKeyPrefix(publicKey);
    if (stripKeyPrefix(doc.public_key) === want)
        return true;
    for (const previous of doc.previous_keys ?? []) {
        if (stripKeyPrefix(previous.public_key) !== want)
            continue;
        if (!previous.valid_until)
            return false;
        const until = Date.parse(previous.valid_until);
        if (Number.isNaN(until))
            return false;
        return at.getTime() <= until;
    }
    return false;
}
