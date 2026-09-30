/**
 * Group keys (EPIC-024 E24-T3) — twin of packages/identity/groupkeys.go.
 *
 * A group identity has one X25519 key per membership epoch. The keyring
 * seals the epoch's private key to every member and admin and seals every
 * earlier epoch's key to the current public key; a share to a group seals
 * its node key to the current public key.
 */
import { generateEncryptionKeypair, openWithDomain, sealWithDomain, verifyCanonical, x25519PublicKey } from "./crypto/index.js";
import { fromBase64, toBase64url, utf8 } from "./encoding.js";
import { PoweurError } from "./errors.js";

export const GROUP_KEYS_DOC = ".poweur/relay/group-keys.json";
export const GROUP_PUBLIC_KEY_DOC = ".poweur/public/group-key.json";
const DOMAIN = "poweur/group/key/v1" as const;

/** Sealed payload as stored in group documents (snake_case, as Go writes it). */
export interface GroupSealed { ciphertext: string; ephemeral_public_key: string; nonce: string }
export interface GroupPreviousKey { epoch: number; public: string; sealed: GroupSealed }
export interface GroupKeyring {
  version: number; group: string; epoch: number; public: string;
  sealed: Record<string, GroupSealed>; previous?: GroupPreviousKey[];
  updated_at: string; signature: string;
}
export interface GroupPublicKey { version: number; group: string; epoch: number; public: string; updated_at: string; signature: string }
export interface GroupEpochKey { epoch: number; public: Uint8Array; private: Uint8Array }

const memberContext = (group: string, epoch: number, recipient: string) =>
  utf8(`member\n${group.toLowerCase()}\n${epoch}\n${recipient.toLowerCase()}`);
const previousContext = (group: string, epoch: number, current: number) =>
  utf8(`previous\n${group.toLowerCase()}\n${epoch}\n${current}`);

const toWire = (p: { ciphertext: string; ephemeralPublicKey: string; nonce: string }): GroupSealed =>
  ({ ciphertext: p.ciphertext, ephemeral_public_key: p.ephemeralPublicKey, nonce: p.nonce });
const fromWire = (p: GroupSealed) => ({ ciphertext: p.ciphertext, ephemeralPublicKey: p.ephemeral_public_key, nonce: p.nonce });
const line = (p: GroupSealed) => `${p.ciphertext}\t${p.ephemeral_public_key}\t${p.nonce}`;

export function groupKeyringCanonical(k: Omit<GroupKeyring, "signature">): string {
  const recipients = Object.keys(k.sealed).sort();
  const previous = k.previous ?? [];
  return [
    "poweur-group-keys", String(k.version), k.group.toLowerCase(), String(k.epoch), k.public,
    String(recipients.length), ...recipients.map((r) => `${r}\t${line(k.sealed[r]!)}`),
    String(previous.length), ...previous.map((p) => `${p.epoch}\t${p.public}\t${line(p.sealed)}`),
    k.updated_at,
  ].join("\n");
}

export function groupPublicKeyCanonical(k: Omit<GroupPublicKey, "signature">): string {
  return ["poweur-group-key", String(k.version), k.group.toLowerCase(), String(k.epoch), k.public, k.updated_at].join("\n");
}

/** `groupSigningKey` is the group identity's Ed25519 public key. */
export function verifyGroupKeyring(k: GroupKeyring, groupSigningKey: Uint8Array): boolean {
  return verifyCanonical(groupSigningKey, groupKeyringCanonical(k), k.signature);
}

export function verifyGroupPublicKey(k: GroupPublicKey, groupSigningKey: Uint8Array): boolean {
  return verifyCanonical(groupSigningKey, groupPublicKeyCanonical(k), k.signature);
}

function key32(value: string, what: string): Uint8Array {
  const raw = fromBase64(value);
  if (raw.length !== 32) throw new PoweurError("invalid_argument", `${what} must be a 32-byte key`);
  return raw;
}

const same = (a: Uint8Array, b: Uint8Array) => a.length === b.length && a.every((v, i) => v === b[i]);

/** Every epoch key the recipient can reach, newest first. */
export function openGroupKeyring(k: GroupKeyring, recipient: string, encryptionPrivateKey: Uint8Array): GroupEpochKey[] {
  const sealed = k.sealed[recipient.toLowerCase()];
  if (!sealed) throw new PoweurError("not_found", `${recipient} holds no key for group ${k.group} at epoch ${k.epoch}`);
  const priv = openWithDomain(encryptionPrivateKey, fromWire(sealed), DOMAIN, memberContext(k.group, k.epoch, recipient));
  const pub = key32(k.public, "public");
  if (!same(x25519PublicKey(priv), pub)) throw new PoweurError("invalid_argument", "the group key does not match its public key");
  const out: GroupEpochKey[] = [{ epoch: k.epoch, public: pub, private: priv }];
  for (const p of k.previous ?? []) {
    const prev = openWithDomain(priv, fromWire(p.sealed), DOMAIN, previousContext(k.group, p.epoch, k.epoch));
    const prevPub = key32(p.public, "previous public");
    if (!same(x25519PublicKey(prev), prevPub)) throw new PoweurError("invalid_argument", `the key of epoch ${p.epoch} does not match its public key`);
    out.push({ epoch: p.epoch, public: prevPub, private: prev });
  }
  return out;
}

/**
 * Issue the key for `epoch`, sealed to every recipient (identity → X25519
 * public key), with every earlier key re-sealed to it. The caller signs
 * `groupKeyringCanonical(result)` with the group's key.
 */
export function newGroupKeyring(group: string, epoch: number, recipients: Record<string, Uint8Array>, earlier: GroupEpochKey[], updatedAt: string): { keyring: Omit<GroupKeyring, "signature">; key: GroupEpochKey } {
  group = group.trim().toLowerCase();
  const { privateKey: priv, publicKey: pub } = generateEncryptionKeypair();
  const sealed: Record<string, GroupSealed> = {};
  for (const [id, encPub] of Object.entries(recipients)) {
    const who = id.trim().toLowerCase();
    sealed[who] = toWire(sealWithDomain(encPub, priv, DOMAIN, memberContext(group, epoch, who)));
  }
  const previous = [...earlier].filter((e) => e.epoch < epoch).sort((a, b) => b.epoch - a.epoch).slice(0, 128)
    .map((e) => ({ epoch: e.epoch, public: toBase64url(e.public), sealed: toWire(sealWithDomain(pub, e.private, DOMAIN, previousContext(group, e.epoch, epoch))) }));
  return {
    keyring: { version: 1, group, epoch, public: toBase64url(pub), sealed, ...(previous.length ? { previous } : {}), updated_at: updatedAt },
    key: { epoch, public: pub, private: priv },
  };
}
