/**
 * Signed shares (E20-T7); Go packages/identity/drive/share.go is
 * authoritative and drive-shares.json pins both implementations.
 *
 * A share grants a member identity, or the holder of a link, a role on a node
 * and everything below it. `append` and `create` do not read; `read` opens
 * content; `write` also replaces, moves and removes; `admin` also shares,
 * trims and rotates. Effective access is the union of shares on the node and
 * its ancestors.
 */
import { sha256 } from "@noble/hashes/sha2.js";
import { signBytes, verifyBytes } from "../crypto/index.js";
import { concatBytes, toBase64url, utf8 } from "../encoding.js";
import { MAX_COUNTER, hex, lengthPrefixed, sealedFields, strictBase64, validHex, validIdentity, validatePayload } from "./internal.js";
import type { DriveSealedPayload } from "./records.js";

export const SHARE_ROLES = ["read", "write", "append", "create", "admin"] as const;
export type ShareRole = (typeof SHARE_ROLES)[number];

/** argon2id, 64 MiB, 3 passes, 1 lane → 64 bytes: key half, then verifier half. */
export const SHARE_KDF = "argon2id-m65536-t3-p1";

export interface ShareCaps {
  bytes?: number;
  files?: number;
  records?: number;
  downloads?: number;
  per_hour?: number;
}

export interface Share {
  format: number;
  drive: string;
  id: string;
  node: string;
  member?: string;
  link?: string;
  role: ShareRole;
  generation: number;
  node_key?: DriveSealedPayload | null;
  node_public: string;
  expires?: string;
  caps: ShareCaps;
  kdf?: string;
  salt?: string;
  verifier_hash?: string;
  pow?: number;
  issuer: string;
  issued: string;
  signature: string;
}

/** Whether holding role satisfies need. */
export function roleGrants(role: ShareRole, need: ShareRole): boolean {
  switch (need) {
    case "read": return role === "read" || role === "write" || role === "admin";
    case "write": return role === "write" || role === "admin";
    case "append": return role === "append" || role === "write" || role === "admin";
    case "create": return role === "create" || role === "write" || role === "admin";
    case "admin": return role === "admin";
  }
}

/** Whether a role receives the node's private key. */
export function keyBearing(role: ShareRole): boolean {
  return role === "read" || role === "write" || role === "admin";
}

function validTime(value: string): boolean {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/.test(value)) return false;
  const parsed = new Date(value);
  return !Number.isNaN(parsed.getTime()) && parsed.toISOString().replace(".000Z", "Z") === value;
}

function counter(value: number | undefined): string {
  const v = value ?? 0;
  if (!Number.isSafeInteger(v) || v < 0 || v > MAX_COUNTER) throw new Error("share limit out of range");
  return String(v);
}

export function validateShare(s: Share): void {
  if (s.format !== 1) throw new Error("unsupported share format");
  validIdentity(s.drive);
  validIdentity(s.issuer);
  if (!validHex(s.id, 16) || !validHex(s.node, 16)) throw new Error("invalid share or node ID");
  const member = s.member ?? "", link = s.link ?? "";
  if ((member === "") === (link === "")) throw new Error("a share names a member or a link, exclusively");
  if (member !== "") validIdentity(member);
  if (link !== "" && !validHex(link, 16)) throw new Error("invalid member or link");
  if (!SHARE_ROLES.includes(s.role)) throw new Error("invalid role");
  if (link !== "" && s.role === "admin") throw new Error("links cannot administer");
  if (!Number.isSafeInteger(s.generation) || s.generation < 1) throw new Error("invalid generation");
  if (keyBearing(s.role) !== Boolean(s.node_key)) throw new Error("read, write and admin shares carry the node key; append and create do not");
  if (s.node_key) validatePayload(s.node_key);
  if (strictBase64(s.node_public).length !== 32) throw new Error("invalid node public key");
  if ((s.expires && !validTime(s.expires)) || !validTime(s.issued)) throw new Error("invalid share times");
  for (const v of [s.caps.bytes, s.caps.files, s.caps.records, s.caps.downloads, s.caps.per_hour, s.pow]) counter(v);
  if ((s.pow ?? 0) > 32) throw new Error("proof-of-work difficulty above 32 bits");
  if (s.kdf || s.salt || s.verifier_hash) {
    if (link === "" || s.kdf !== SHARE_KDF || strictBase64(s.salt ?? "").length !== 16 || !validHex(s.verifier_hash ?? "", 32)) {
      throw new Error("invalid link password parameters");
    }
  }
}

export function canonicalShare(s: Share): Uint8Array {
  validateShare(s);
  const c = s.caps;
  const fields = ["1", s.drive, s.id, s.node, s.member ?? "", s.link ?? "", s.role, String(s.generation),
    ...sealedFields(s.node_key), s.node_public, s.expires ?? "", counter(c.bytes), counter(c.files), counter(c.records),
    counter(c.downloads), counter(c.per_hour), s.kdf ?? "", s.salt ?? "", s.verifier_hash ?? "", counter(s.pow), s.issuer, s.issued];
  return lengthPrefixed("poweur/drive/share/v1\n", fields);
}

export function signShare(s: Share, privateKey: Uint8Array): Share {
  return { ...s, signature: toBase64url(signBytes(privateKey, canonicalShare(s))) };
}

export function verifyShare(s: Share, publicKey: Uint8Array): void {
  const raw = canonicalShare(s), signature = strictBase64(s.signature);
  if (signature.length !== 64 || !verifyBytes(publicKey, raw, signature)) throw new Error("share signature verification failed");
}

export function shareHash(s: Share): string {
  const raw = canonicalShare(s), signature = strictBase64(s.signature);
  if (signature.length !== 64) throw new Error("invalid signature encoding");
  return hex(sha256(concatBytes(utf8("poweur/drive/share-hash/v1\n"), raw, signature)));
}

/** SHA-256 of a link-password verifier, lowercase hex (what the relay stores). */
export function verifierHash(verifier: Uint8Array): string {
  return hex(sha256(verifier));
}
