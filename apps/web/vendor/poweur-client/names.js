/**
 * Identity name policy — the twin of `packages/identity/names.go`.
 * Pinned by conformance vectors: a name Go accepts must be accepted here.
 */
export const MIN_LABEL_LEN = 3;
export const MAX_LABEL_LEN = 63;
export const MAX_NAME_LEN = 253;
/** Labels that cannot be the leftmost label of a hosted identity. */
export const RESERVED_LABELS = new Set([
    "www", "admin", "relay", "mail", "ftp",
    "api", "app", "static", "cdn", "ns", "ns1", "ns2",
    "mx", "smtp", "imap", "pop", "root", "localhost",
    "poweur", "well-known", "dav", "sync",
]);
function looksLikeIpLiteral(value) {
    if (value.startsWith("["))
        return true;
    const parts = value.split(".");
    if (parts.length !== 4)
        return false;
    return parts.every((p) => p.length > 0 && /^[0-9]+$/.test(p));
}
function validateLabel(label) {
    if (label.length < 1 || label.length > MAX_LABEL_LEN) {
        throw new Error(`label "${label}": invalid length`);
    }
    for (let i = 0; i < label.length; i++) {
        const ch = label[i];
        if (/[\p{L}\p{Nd}]/u.test(ch))
            continue;
        if (ch === "-" && i > 0 && i < label.length - 1)
            continue;
        throw new Error(`label "${label}": invalid character`);
    }
    if (label.startsWith("-") || label.endsWith("-")) {
        throw new Error(`label "${label}": cannot start or end with hyphen`);
    }
}
/** Throws when the identity is not a usable FQDN. */
export function validateIdentityName(identity) {
    const value = identity.trim().toLowerCase();
    if (value === "")
        throw new Error("identity is empty");
    if (value.length > MAX_NAME_LEN)
        throw new Error("identity too long");
    if (looksLikeIpLiteral(value))
        throw new Error("IP-literal identities are not allowed");
    const labels = value.split(".");
    if (labels.length < 2)
        throw new Error("identity must be a FQDN with at least two labels");
    labels.forEach((label, index) => {
        validateLabel(label);
        if (index === 0 && RESERVED_LABELS.has(label)) {
            throw new Error(`label "${label}" is reserved`);
        }
    });
}
export function isValidIdentityName(identity) {
    try {
        validateIdentityName(identity);
        return true;
    }
    catch {
        return false;
    }
}
/** Hosted handles additionally require a leftmost label of >= 3 characters. */
export function validateHostedHandle(identity) {
    validateIdentityName(identity);
    const label = identity.trim().toLowerCase().split(".")[0];
    if (label.length < MIN_LABEL_LEN) {
        throw new Error(`hosted handle must be at least ${MIN_LABEL_LEN} characters`);
    }
}
/** True when `identity` is `parent` or a subdomain of it. */
export function isUnderDomain(identity, parent) {
    const id = identity.toLowerCase().replace(/\.$/, "");
    const dom = parent.toLowerCase().replace(/\.$/, "");
    if (dom === "")
        return false;
    return id === dom || id.endsWith(`.${dom}`);
}
/** Map an identity FQDN to one safe directory name (dots become `__`). */
export function sanitizeIdentityDirName(identity) {
    validateIdentityName(identity);
    const id = identity.toLowerCase().replace(/\.$/, "");
    if (id.includes("/") || id.includes("\\") || id.includes("..")) {
        throw new Error("identity contains path separators");
    }
    return id.replace(/\./g, "__");
}
