/**
 * Web-first identity resolution — the twin of `packages/identity/resolve.go`.
 *
 * Order is: HTTPS `/.well-known/poweur/id.json`, then DNS TXT. When both
 * answer and the keys disagree the resolve *fails* rather than picking one —
 * a key mismatch is the signature of a compromised relay or registrar, and
 * failing closed is the whole point of publishing in two places.
 *
 * DNS is injected (`TxtResolver`) because runtimes differ: Node uses
 * `node:dns`, browsers use DNS-over-HTTPS. See `./node` and `./browser`.
 */
import { parseDocument } from "./document.js";
import { stripKeyPrefix, withEd25519Prefix, withX25519Prefix, rfc3339 } from "./encoding.js";
import { PoweurError } from "./errors.js";
import { defaultFetch } from "./http.js";
import { validateIdentityName } from "./names.js";
export const WELL_KNOWN_PATH = "/.well-known/poweur/id.json";
export const MAX_DOCUMENT_BYTES = 16 * 1024;
export const DEFAULT_TIMEOUT_MS = 5_000;
/** Small TTL cache mirroring the well-known `Cache-Control: max-age=300`. */
export class ResolveCache {
    #entries = new Map();
    get(identity) {
        const entry = this.#entries.get(identity);
        if (!entry)
            return null;
        if (Date.now() > entry.expiresAt) {
            this.#entries.delete(identity);
            return null;
        }
        return entry.result;
    }
    put(identity, result, ttlMs) {
        this.#entries.set(identity, { result, expiresAt: Date.now() + ttlMs });
    }
    clear() {
        this.#entries.clear();
    }
}
/**
 * Names that must never be fetched: loopback and private-range literals, and
 * the suffixes that resolve inside a LAN.
 *
 * Go checks the *resolved* IPs, which a browser cannot do — so this is a
 * name-shaped approximation. It is the conservative half of the check: it can
 * refuse something Go would allow, never the reverse, and `allowPrivate`
 * (test and dev only) turns it off entirely.
 */
const PRIVATE_LITERAL = /^(localhost|0\.0\.0\.0|\[?::1\]?|127\.\d{1,3}\.\d{1,3}\.\d{1,3}|10\.\d{1,3}\.\d{1,3}\.\d{1,3}|192\.168\.\d{1,3}\.\d{1,3}|169\.254\.\d{1,3}\.\d{1,3})$/i;
const PRIVATE_SUFFIX = [".localhost", ".local", ".internal", ".home.arpa"];
function isPrivateHost(identity) {
    const host = identity.toLowerCase().replace(/\.$/, "");
    if (PRIVATE_LITERAL.test(host))
        return true;
    if (PRIVATE_SUFFIX.some((suffix) => host.endsWith(suffix)))
        return true;
    // 172.16.0.0/12
    const match = /^172\.(\d{1,3})\.\d{1,3}\.\d{1,3}$/.exec(host);
    if (match) {
        const octet = Number(match[1]);
        return octet >= 16 && octet <= 31;
    }
    return false;
}
function assertPublicHost(identity, allowPrivate) {
    if (allowPrivate)
        return;
    if (isPrivateHost(identity)) {
        throw new PoweurError("resolve_failed", "refusing to fetch identity document from a private/loopback address");
    }
}
async function fetchWellKnown(url, opts) {
    const doFetch = opts.fetch ?? defaultFetch();
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), opts.timeoutMs ?? DEFAULT_TIMEOUT_MS);
    let response;
    try {
        response = await doFetch(url, {
            method: "GET",
            // Redirects are refused: a redirect could walk the fetch to an
            // attacker-chosen origin after the SSRF check already passed.
            redirect: "error",
            headers: { Accept: "application/json" },
            signal: controller.signal,
        });
    }
    finally {
        clearTimeout(timer);
    }
    if (!response.ok) {
        throw new PoweurError("resolve_failed", `well-known returned ${response.status}`, {
            status: response.status,
        });
    }
    const body = await response.text();
    if (body.length > MAX_DOCUMENT_BYTES) {
        throw new PoweurError("resolve_failed", "identity document exceeds 16KB");
    }
    const parsed = JSON.parse(body);
    // GET /identities/{id} wraps the document; the well-known path serves it bare.
    const nested = parsed["identity_document"];
    if (nested) {
        return parseDocument(typeof nested === "string" ? nested : JSON.stringify(nested), true);
    }
    return parseDocument(body, true);
}
async function resolveWeb(identity, opts) {
    assertPublicHost(identity, opts.allowPrivate === true);
    const scheme = opts.scheme ?? "https";
    const candidates = [`${scheme}://${identity}${WELL_KNOWN_PATH}`];
    if (opts.relayUrl) {
        const base = opts.relayUrl.replace(/\/$/, "");
        candidates.push(`${base}/identities/${encodeURIComponent(identity)}`);
    }
    let lastError;
    for (const url of candidates) {
        try {
            const doc = await fetchWellKnown(url, opts);
            if (doc.identity.toLowerCase() !== identity.toLowerCase()) {
                throw new PoweurError("key_mismatch", "document identity does not match requested identity");
            }
            return doc;
        }
        catch (error) {
            // A key/identity mismatch is an attack signal, not a "try the next
            // candidate" condition — surface it immediately.
            if (error instanceof PoweurError && error.code === "key_mismatch")
                throw error;
            lastError = error;
        }
    }
    throw lastError instanceof Error
        ? lastError
        : new PoweurError("resolve_failed", "identity document not found");
}
async function resolveDns(identity, txt) {
    const records = await txt.lookupTxt(`_poweur.${identity}`);
    let publicKey = "";
    for (const record of records) {
        const value = record.trim();
        if (value.startsWith("poweur-pubkey=")) {
            publicKey = withEd25519Prefix(value.slice("poweur-pubkey=".length));
            break;
        }
    }
    if (!publicKey) {
        throw new PoweurError("resolve_failed", "no poweur-pubkey TXT record");
    }
    let encryptionPublicKey = "";
    try {
        for (const record of await txt.lookupTxt(`_poweur-enc.${identity}`)) {
            const value = record.trim();
            if (value.startsWith("poweur-enckey=")) {
                encryptionPublicKey = withX25519Prefix(value.slice("poweur-enckey=".length));
                break;
            }
        }
    }
    catch {
        // An absent encryption record is normal; only the signing key is required.
    }
    const doc = {
        version: 1,
        identity,
        public_key: publicKey,
        relay: identity, // DNS path: the A/CNAME on the identity is the relay
        updated_at: rfc3339(),
    };
    if (encryptionPublicKey)
        doc.encryption_public_key = encryptionPublicKey;
    return doc;
}
/**
 * Resolve an identity to a verified document.
 *
 * @throws PoweurError `key_mismatch` when web and DNS disagree, `resolve_failed`
 *   when neither source answers.
 */
export async function resolveIdentity(identity, options = {}) {
    const name = identity.trim().toLowerCase();
    validateIdentityName(name);
    const opts = { scheme: "https", timeoutMs: DEFAULT_TIMEOUT_MS, ...options };
    const ttl = opts.cacheTtlMs ?? 5 * 60_000;
    const cached = opts.cache?.get(name);
    if (cached)
        return cached;
    let webDoc = null;
    let webError;
    if (!opts.skipWeb) {
        try {
            webDoc = await resolveWeb(name, opts);
        }
        catch (error) {
            if (error instanceof PoweurError && error.code === "key_mismatch")
                throw error;
            webError = error;
        }
    }
    let dnsDoc = null;
    let dnsError;
    if (!opts.skipDns && opts.txt) {
        try {
            dnsDoc = await resolveDns(name, opts.txt);
        }
        catch (error) {
            dnsError = error;
        }
    }
    let result;
    if (webDoc && dnsDoc) {
        if (stripKeyPrefix(webDoc.public_key) !== stripKeyPrefix(dnsDoc.public_key)) {
            throw new PoweurError("key_mismatch", "identity key mismatch between web and DNS sources");
        }
        result = { document: webDoc, source: "both" };
    }
    else if (webDoc) {
        result = { document: webDoc, source: "web" };
    }
    else if (dnsDoc) {
        result = { document: dnsDoc, source: "dns" };
    }
    else {
        // Error phrasing mirrors Go so operators reading either client's logs
        // see the same sentence for the same failure.
        if (webError && dnsError) {
            throw new PoweurError("resolve_failed", `identity not found: web: ${webError.message}; dns: ${dnsError.message}`);
        }
        if (webError) {
            throw new PoweurError("resolve_failed", `identity not found via web: ${webError.message}`);
        }
        if (dnsError) {
            throw new PoweurError("resolve_failed", `identity not found via dns: ${dnsError.message}`);
        }
        throw new PoweurError("resolve_failed", "identity not found");
    }
    // Follow a hosted-migration tombstone exactly once — never a chain.
    if (!opts.skipMovedTo && result.document.moved_to) {
        const target = result.document.moved_to.trim().toLowerCase();
        if (target && target !== name) {
            try {
                result = await resolveIdentity(target, { ...opts, skipMovedTo: true });
            }
            catch {
                // A dangling tombstone leaves the original document in play.
            }
        }
    }
    opts.cache?.put(name, result, ttl);
    return result;
}
/** The recipient's signing key, bare base64url, or null when unresolvable. */
export async function resolveSigningKey(identity, options = {}) {
    try {
        const { document } = await resolveIdentity(identity, options);
        return document.public_key ? stripKeyPrefix(document.public_key) : null;
    }
    catch {
        return null;
    }
}
/** The recipient's X25519 key, bare base64url, or null when unresolvable. */
export async function resolveEncryptionKey(identity, options = {}) {
    try {
        const { document } = await resolveIdentity(identity, options);
        return document.encryption_public_key
            ? stripKeyPrefix(document.encryption_public_key)
            : null;
    }
    catch {
        return null;
    }
}
