import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { ed25519PublicKey, generateEncryptionKeypair, signCanonical } from "../src/crypto/index.js";
import { fromBase64, toBase64url } from "../src/encoding.js";
import {
  groupKeyringCanonical, groupPublicKeyCanonical, newGroupKeyring, openGroupKeyring,
  verifyGroupKeyring, verifyGroupPublicKey, type GroupKeyring, type GroupPublicKey,
} from "../src/groupkeys.js";

const vectors = JSON.parse(readFileSync(new URL("../../identity/testdata/vectors/group-keys.json", import.meta.url), "utf8")) as Array<{
  name: string; keyring: GroupKeyring; canonical: string; public: GroupPublicKey; public_canonical: string;
  member: string; member_private: string; opened: Array<{ epoch: number; private: string }>;
}>;
const seed = new Uint8Array(Array.from({ length: 32 }, (_, i) => i + 1));
const groupPublic = ed25519PublicKey(seed);

describe("group keys match Go", () => {
  for (const v of vectors) it(v.name, () => {
    expect(groupKeyringCanonical(v.keyring)).toBe(v.canonical);
    expect(groupPublicKeyCanonical(v.public)).toBe(v.public_canonical);
    expect(verifyGroupKeyring(v.keyring, groupPublic)).toBe(true);
    expect(verifyGroupPublicKey(v.public, groupPublic)).toBe(true);
    expect(verifyGroupKeyring({ ...v.keyring, epoch: v.keyring.epoch + 1 }, groupPublic)).toBe(false);
    const opened = openGroupKeyring(v.keyring, v.member, fromBase64(v.member_private));
    expect(opened.map((k) => ({ epoch: k.epoch, private: toBase64url(k.private) }))).toEqual(v.opened);
  });
});

it("issues a keyring members open, carrying earlier epochs, and refuses outsiders", () => {
  const bob = generateEncryptionKeypair(), carol = generateEncryptionKeypair();
  const first = newGroupKeyring("Atlas.poweur.net", 1, { "bob.poweur.net": bob.publicKey, "carol.poweur.net": carol.publicKey }, [], "2026-09-30T00:00:00Z");
  const second = newGroupKeyring("atlas.poweur.net", 2, { "bob.poweur.net": bob.publicKey }, [first.key], "2026-09-30T01:00:00Z");
  const ring = { ...second.keyring, signature: signCanonical(seed, groupKeyringCanonical(second.keyring)) };
  expect(verifyGroupKeyring(ring, groupPublic)).toBe(true);
  const keys = openGroupKeyring(ring, "bob.poweur.net", bob.privateKey);
  expect(keys.map((k) => k.epoch)).toEqual([2, 1]);
  expect(keys[1]!.private).toEqual(first.key.private);
  expect(() => openGroupKeyring(ring, "carol.poweur.net", carol.privateKey)).toThrow();
});
