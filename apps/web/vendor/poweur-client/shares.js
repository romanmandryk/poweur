/**
 * Share grants and groups — the twin of `poweur share`.
 *
 * A grant is a document the owner signs and stores in their *own* tree at
 * `poweur-sys/relay/shares/<share-id>.json`. The relay enforces grants but
 * cannot forge them, and revocation is simply deleting the file. Groups live
 * alongside at `poweur-sys/relay/groups/<name>.json`.
 */
import { hashAppPassword } from "./apppass.js";
import { canonicalShareGrant, canonicalShareGroup, normalizeGrantPath } from "./canonical.js";
import { parseEd25519PublicKey, verifyCanonical } from "./crypto/index.js";
import { randomBytes, rfc3339 } from "./encoding.js";
import { PoweurError } from "./errors.js";
import { newShareId } from "./ids.js";
import { PERM_READ, PERM_WRITE, } from "./types.js";
export { normalizeGrantPath };
export const SHARES_DIR = "poweur-sys/relay/shares";
export const GROUPS_DIR = "poweur-sys/relay/groups";
export const MAX_GRANT_AUDIENCE = 100;
export const MAX_GROUP_MEMBERS = 1000;
/**
 * Link-share tokens (E05-T4) — 16 random bytes as lowercase unpadded
 * base32: 26 characters of [a-z2-7], 128 bits of entropy, URL-path safe and
 * free of look-alike characters. Mirrors identity.GenerateLinkToken.
 */
export const LINK_TOKEN_BYTES = 16;
export const LINK_TOKEN_LEN = 26;
export const MAX_LINK_DOWNLOADS = 1_000_000;
const LINK_ALPHABET = "abcdefghijklmnopqrstuvwxyz234567";
/** A fresh capability-URL token. */
export function generateLinkToken() {
    // 16 bytes → 26 base32 characters, emitted five bits at a time so the
    // result matches Go's encoder byte for byte.
    const bytes = randomBytes(LINK_TOKEN_BYTES);
    let out = "";
    let buffer = 0;
    let bits = 0;
    for (const byte of bytes) {
        buffer = (buffer << 8) | byte;
        bits += 8;
        while (bits >= 5) {
            out += LINK_ALPHABET[(buffer >>> (bits - 5)) & 31];
            bits -= 5;
        }
    }
    if (bits > 0)
        out += LINK_ALPHABET[(buffer << (5 - bits)) & 31];
    return out;
}
/** Throws unless `token` has the exact shape a link grant may carry. */
export function validateLinkToken(token) {
    if (token.length !== LINK_TOKEN_LEN) {
        throw new PoweurError("invalid_argument", `link token must be ${LINK_TOKEN_LEN} characters`);
    }
    for (const character of token) {
        if (!LINK_ALPHABET.includes(character)) {
            throw new PoweurError("invalid_argument", `link token contains an invalid character "${character}"`);
        }
    }
}
/** The grant's capability token, or undefined when it is not a link grant. */
export function linkToken(grant) {
    for (const entry of grant.audience ?? []) {
        const token = entry.link?.trim();
        if (token)
            return token.toLowerCase();
    }
    return undefined;
}
export function grantIsLink(grant) {
    return linkToken(grant) !== undefined;
}
/** Structural validation — everything except the signature. */
export function validateGrant(grant) {
    if (!grant.share_id?.trim())
        throw new PoweurError("invalid_argument", "share_id is required");
    if (!grant.owner?.trim())
        throw new PoweurError("invalid_argument", "owner is required");
    normalizeGrantPath(grant.path);
    if (!grant.audience?.length)
        throw new PoweurError("invalid_argument", "audience is empty");
    if (grant.audience.length > MAX_GRANT_AUDIENCE) {
        throw new PoweurError("invalid_argument", `audience exceeds ${MAX_GRANT_AUDIENCE} entries`);
    }
    let links = 0;
    for (const entry of grant.audience) {
        const set = [entry.id, entry.group, entry.link].filter((v) => Boolean(v?.trim())).length;
        if (set !== 1) {
            throw new PoweurError("invalid_argument", "each audience entry needs exactly one of id, group or link");
        }
        const token = entry.link?.trim();
        if (token) {
            links += 1;
            validateLinkToken(token);
        }
    }
    validateGrantLink(grant, links);
    if (!grant.permissions?.length) {
        throw new PoweurError("invalid_argument", "permissions is empty");
    }
    for (const permission of grant.permissions) {
        if (permission !== PERM_READ && permission !== PERM_WRITE) {
            throw new PoweurError("invalid_argument", `unknown permission "${permission}" (v1 vocabulary: read, write)`);
        }
    }
}
/**
 * The link-share rules (identity.validateLink). A link grant is a
 * *capability*: whoever holds the URL is the audience. v1 therefore keeps it
 * narrow — one token per grant, never mixed with identity or group entries,
 * and read-only, so a leaked URL can never mutate the owner's tree.
 */
function validateGrantLink(grant, links) {
    if (links > 1) {
        throw new PoweurError("invalid_argument", "a grant carries at most one link token");
    }
    if (links === 1) {
        if (grant.audience.length !== 1) {
            throw new PoweurError("invalid_argument", "a link grant's audience is the link alone (no ids or groups)");
        }
        if (grant.permissions?.includes(PERM_WRITE)) {
            throw new PoweurError("invalid_argument", "link shares are read-only in v1");
        }
    }
    else if (grant.link) {
        throw new PoweurError("invalid_argument", "link options require a link audience entry");
    }
    if (!grant.link)
        return;
    if (grant.link.password && !grant.link.password.startsWith("$argon2id$")) {
        throw new PoweurError("invalid_argument", "link password must be a PHC argon2id hash, never a plaintext password");
    }
    const max = grant.link.max_downloads ?? 0;
    if (max < 0 || max > MAX_LINK_DOWNLOADS) {
        throw new PoweurError("invalid_argument", `max_downloads must be between 0 (unlimited) and ${MAX_LINK_DOWNLOADS}`);
    }
}
export function grantExpired(grant, now = new Date()) {
    if (!grant.expires_at)
        return false;
    const expiry = Date.parse(grant.expires_at);
    // An unparseable expiry fails closed.
    return Number.isNaN(expiry) || now.getTime() > expiry;
}
export function grantAllowsWrite(grant) {
    return grant.permissions.includes(PERM_WRITE);
}
export function verifyGrantSignature(grant, ownerPublicKey) {
    return verifyCanonical(parseEd25519PublicKey(ownerPublicKey), canonicalShareGrant(grant), grant.signature);
}
export function verifyGroupSignature(group, ownerPublicKey) {
    return verifyCanonical(parseEd25519PublicKey(ownerPublicKey), canonicalShareGroup(group), group.signature);
}
/** Build and sign a grant without writing it — useful for offline flows. */
export async function buildGrant(signer, path, options = {}) {
    const audience = [
        ...(options.with ?? []).map((id) => ({ id })),
        ...(options.withGroups ?? []).map((group) => ({ group })),
    ];
    if (audience.length === 0) {
        throw new PoweurError("invalid_argument", "at least one recipient or group is required");
    }
    const permissions = options.permissions === "rw" ? [PERM_READ, PERM_WRITE] : [PERM_READ];
    const draft = {
        share_id: options.shareId ?? newShareId(),
        owner: signer.identity,
        path: normalizeGrantPath(path),
        audience,
        permissions,
        created_at: rfc3339(),
        ...(options.expiresAt ? { expires_at: options.expiresAt } : {}),
    };
    validateGrant(draft);
    const signature = await signer.sign(canonicalShareGrant(draft), "base64url");
    return { ...draft, signature };
}
/**
 * Build and sign a public-link grant (E05-T4) without writing it. The
 * returned token is the whole credential: it appears nowhere else, so a
 * caller that loses it has to issue a new link.
 */
export async function buildLinkGrant(signer, path, options = {}) {
    const token = (options.token ?? generateLinkToken()).toLowerCase();
    validateLinkToken(token);
    const link = {};
    if (options.password)
        link.password = await hashAppPassword(options.password);
    if (options.maxDownloads)
        link.max_downloads = options.maxDownloads;
    const draft = {
        share_id: options.shareId ?? newShareId(),
        owner: signer.identity,
        path: normalizeGrantPath(path),
        audience: [{ link: token }],
        permissions: [PERM_READ],
        created_at: rfc3339(),
        ...(options.expiresAt ? { expires_at: options.expiresAt } : {}),
        ...(link.password || link.max_downloads ? { link } : {}),
    };
    validateGrant(draft);
    const signature = await signer.sign(canonicalShareGrant(draft), "base64url");
    return { grant: { ...draft, signature }, token };
}
/** CRUD over an owner's grant and group documents. */
export class Shares {
    #dav;
    #sync;
    constructor(dav, sync) {
        this.#dav = dav;
        this.#sync = sync;
    }
    /** Create, sign and store a grant (`poweur share add`). */
    async add(signer, path, options = {}) {
        const grant = await buildGrant(signer, path, options);
        await this.#dav.writeJson(`${SHARES_DIR}/${grant.share_id}.json`, grant);
        return grant;
    }
    /**
     * Create, sign and store a public-link grant, returning the grant and the
     * capability token to put in `https://<owner>/s/<token>` (E05-T4).
     */
    async addLink(signer, path, options = {}) {
        const built = await buildLinkGrant(signer, path, options);
        await this.#dav.writeJson(`${SHARES_DIR}/${built.grant.share_id}.json`, built.grant);
        return built;
    }
    /**
     * List grants. The manifest endpoint gives the file names — a PROPFIND on
     * poweur-sys would work too, but the manifest is one request for the whole
     * prefix and is what the CLI uses.
     */
    async list() {
        return this.#loadAll(SHARES_DIR);
    }
    /** Revoke by deleting the grant file. Returns false if it wasn't there. */
    async revoke(shareId) {
        if (/[/\\]/.test(shareId)) {
            throw new PoweurError("invalid_argument", "invalid share id");
        }
        return this.#dav.remove(`${SHARES_DIR}/${shareId}.json`);
    }
    /** Create or replace a group (`poweur share group set`). */
    async setGroup(signer, name, members) {
        if (/[/\\]/.test(name)) {
            throw new PoweurError("invalid_argument", "group name must not contain slashes");
        }
        if (members.length > MAX_GROUP_MEMBERS) {
            throw new PoweurError("invalid_argument", `group exceeds ${MAX_GROUP_MEMBERS} members`);
        }
        const draft = {
            group: name,
            owner: signer.identity,
            members,
            updated_at: rfc3339(),
        };
        const signature = await signer.sign(canonicalShareGroup(draft), "base64url");
        const group = { ...draft, signature };
        await this.#dav.writeJson(`${GROUPS_DIR}/${name}.json`, group);
        return group;
    }
    async listGroups() {
        return this.#loadAll(GROUPS_DIR);
    }
    async removeGroup(name) {
        if (/[/\\]/.test(name)) {
            throw new PoweurError("invalid_argument", "invalid group name");
        }
        return this.#dav.remove(`${GROUPS_DIR}/${name}.json`);
    }
    async #loadAll(directory) {
        let names;
        try {
            const { entries } = await this.#sync.manifest([directory]);
            names = entries
                .filter((e) => !e.dir && e.path.startsWith(`${directory}/`) && e.path.endsWith(".json"))
                .map((e) => e.path.slice(directory.length + 1));
        }
        catch {
            // A relay without the manifest endpoint (or an empty tree) still lists
            // via PROPFIND; an absent directory just means no documents.
            try {
                names = (await this.#dav.list(directory))
                    .filter((e) => !e.dir && e.name.endsWith(".json"))
                    .map((e) => e.name);
            }
            catch {
                return [];
            }
        }
        const out = [];
        for (const name of names) {
            const raw = await this.#dav.readOptional(`${directory}/${name}`);
            if (!raw)
                continue;
            try {
                out.push(JSON.parse(raw));
            }
            catch {
                // A malformed document is skipped rather than failing the whole list.
            }
        }
        return out;
    }
}
