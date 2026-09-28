import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { fromBase64, utf8 } from "../src/encoding.js";
import { generateEncryptionKeypair, sealWithDomain } from "../src/crypto/index.js";
import { driveContext } from "../src/drive/crypto.js";
import { nameHash, normalizeName, openName, sealName } from "../src/drive/names.js";

const vectors = JSON.parse(readFileSync(new URL("../../identity/testdata/vectors/drive-names.json", import.meta.url), "utf8")) as Array<{
  name: string; normalized: string; private_key: string; hash: string;
}>;

describe("drive names", () => {
  for (const v of vectors) it(`matches Go normalization and hash for ${v.name}`, () => {
    expect(normalizeName(v.name)).toBe(v.normalized);
    expect(nameHash(fromBase64(v.private_key), v.name)).toBe(v.hash);
  });
  it("seals NFC names and rejects substitutions", () => {
    const { publicKey, privateKey } = generateEncryptionKeypair();
    const context = driveContext("alice.example", "node", "name", 1);
    const sealed = sealName(publicKey, "cafe\u0301", context);
    expect(openName(privateKey, sealed, context)).toBe("café");
    expect(() => openName(privateKey, sealed, driveContext("alice.example", "other", "name", 1))).toThrow();
    const noncanonical = sealWithDomain(publicKey, utf8("cafe\u0301"), "poweur/drive/name/v1", context);
    expect(() => openName(privateKey, noncanonical, context)).toThrow();
    expect(nameHash(privateKey, "café")).not.toBe(nameHash(privateKey, "Café"));
    expect(nameHash(privateKey, "café")).not.toBe(nameHash(generateEncryptionKeypair().privateKey, "café"));
  });
  it("rejects invalid names and keys", () => {
    for (const name of ["", ".", "..", "a/b", "a\0b", "x".repeat(256), "\ud800"]) expect(() => normalizeName(name)).toThrow();
    expect(() => nameHash(new Uint8Array(31), "name")).toThrow();
  });
});
