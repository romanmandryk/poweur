/**
 * Short authentication strings — the twin of `packages/identity/fingerprint.go`.
 *
 * A fingerprint is the human-comparable projection of a public key: what two
 * people actually read to each other when they verify a pin out of band. It
 * is therefore **numeric**, four groups of five digits, in the Signal
 * safety-number tradition — digits survive every terminal, font and spoken
 * language, and can be typed back in. (The reasoning, including why not
 * emoji, lives in the Go file and in `apps/docs/docs/trust/contacts.md`.)
 *
 *   canonical   = "poweur-fingerprint-v1" LF <normalized key string>
 *   digest      = SHA-256(canonical)
 *   group i     = uint40(digest[5i..5i+5]) mod 100000, zero-padded to 5
 *   fingerprint = groups 0..3 joined with a single space
 *
 * Go is canonical; `test/conformance.test.ts` pins this against fixtures Go
 * produced. A split here is worse than a failed request: it is two people on
 * a phone call concluding they are under attack when they are not.
 */
import { parseEd25519PublicKey, parseX25519PublicKey, sha256Bytes, } from "./crypto/index.js";
import { toBase64url, utf8 } from "./encoding.js";
/** Domain separator; versioned, because changing it changes every fingerprint. */
export const FINGERPRINT_DOMAIN = "poweur-fingerprint-v1";
/** Digit groups in a fingerprint. */
export const FINGERPRINT_GROUPS = 4;
/** Digits per group. */
export const FINGERPRINT_GROUP_DIGITS = 5;
const CHUNK_BYTES = 5;
function fingerprintOf(normalizedKey) {
    const digest = sha256Bytes(utf8(`${FINGERPRINT_DOMAIN}\n${normalizedKey}`));
    const groups = [];
    for (let i = 0; i < FINGERPRINT_GROUPS; i += 1) {
        // 40 bits fits exactly in a JS double, so no BigInt is needed.
        let n = 0;
        for (let j = 0; j < CHUNK_BYTES; j += 1) {
            n = n * 256 + (digest[i * CHUNK_BYTES + j] ?? 0);
        }
        groups.push(String(n % 100000).padStart(FINGERPRINT_GROUP_DIGITS, "0"));
    }
    return groups.join(" ");
}
/**
 * Short auth string for an Ed25519 signing key. Accepts `ed25519:<base64url>`
 * or bare base64 in any padding variant — all of them fingerprint alike.
 * Throws (like the Go twin returns an error) when the key does not parse.
 */
export function keyFingerprint(key) {
    const raw = parseEd25519PublicKey(key);
    return fingerprintOf(`ed25519:${toBase64url(raw)}`);
}
/**
 * Short auth string for an X25519 encryption key. Distinct from
 * {@link keyFingerprint} even for identical bytes: the algorithm prefix is
 * inside the hash.
 */
export function encryptionKeyFingerprint(key) {
    const raw = parseX25519PublicKey(key);
    return fingerprintOf(`x25519:${toBase64url(raw)}`);
}
/**
 * Display helper: the fingerprint when the key parses, the raw string when
 * it does not. A malformed pin still has to reach the user's eyes rather
 * than vanish from the UI.
 */
export function fingerprintOrKey(key) {
    try {
        return keyFingerprint(key);
    }
    catch {
        return key;
    }
}
