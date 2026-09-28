import { x25519 } from "@noble/curves/ed25519.js";
import { hkdf } from "@noble/hashes/hkdf.js";
import { hmac } from "@noble/hashes/hmac.js";
import { sha256 } from "@noble/hashes/sha2.js";
import { utf8 } from "../encoding.js";
import { sealWithDomain, openWithDomain, type SealedPayload } from "../crypto/index.js";

export function normalizeName(input: string): string {
  if (new TextDecoder().decode(utf8(input)) !== input) throw new Error("name is not UTF-8");
  const name = input.normalize("NFC");
  if (!name || utf8(name).length > 255 || name === "." || name === ".." || name.includes("/") || name.includes("\0")) {
    throw new Error("invalid drive name");
  }
  return name;
}

export function nameHash(parentPrivate: Uint8Array, name: string): string {
  const normalized = normalizeName(name);
  if (parentPrivate.length !== 32) throw new Error("parent key must be 32 bytes");
  const pub = x25519.getPublicKey(parentPrivate);
  const key = hkdf(sha256, parentPrivate, pub, utf8("poweur/drive/name-index/v1"), 32);
  return Array.from(hmac(sha256, key, utf8(normalized)), byte => byte.toString(16).padStart(2, "0")).join("");
}

export function sealName(parentPublic: Uint8Array, name: string, context: Uint8Array): SealedPayload {
  return sealWithDomain(parentPublic, utf8(normalizeName(name)), "poweur/drive/name/v1", context);
}

export function openName(parentPrivate: Uint8Array, payload: SealedPayload, context: Uint8Array): string {
  const raw = new TextDecoder("utf-8", { fatal: true }).decode(openWithDomain(parentPrivate, payload, "poweur/drive/name/v1", context));
  const name = normalizeName(raw);
  if (name !== raw) throw new Error("encrypted name is not NFC");
  return name;
}
