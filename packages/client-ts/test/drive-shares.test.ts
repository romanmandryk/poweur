import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { fromBase64 } from "../src/encoding.js";
import { canonicalShare, roleGrants, shareHash, signShare, validateShare, verifierHash, verifyShare, type Share } from "../src/drive/share.js";

const vectors = JSON.parse(readFileSync(new URL("../../identity/testdata/vectors/drive-shares.json", import.meta.url), "utf8")) as {
  shares: Array<{ name: string; share: Share; seed: string; public_key: string; canonical: string; hash: string }>;
};
const hex = (bytes: Uint8Array) => Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");

describe("signed shares match Go", () => {
  for (const v of vectors.shares) it(v.name, () => {
    expect(hex(canonicalShare(v.share))).toBe(v.canonical);
    expect(signShare(v.share, fromBase64(v.seed)).signature).toBe(v.share.signature);
    expect(shareHash(v.share)).toBe(v.hash);
    expect(() => verifyShare(v.share, fromBase64(v.public_key))).not.toThrow();
    for (const changes of [{ role: "admin" as const }, { node: "f".repeat(32) }, { generation: 3 }, { issuer: "carol.example" }]) {
      expect(() => verifyShare({ ...v.share, ...changes }, fromBase64(v.public_key))).toThrow();
    }
  });

  it("refuses shapes Go refuses", () => {
    const member = vectors.shares[0]!.share;
    for (const changes of [
      { link: "c3".repeat(16) }, { member: "" }, { role: "owner" as never }, { role: "append" as const },
      { expires: "2027-01-01T00:00:00+00:00" }, { pow: 33 }, { kdf: "argon2id-m65536-t3-p1" },
    ]) {
      expect(() => validateShare({ ...member, ...changes })).toThrow();
    }
  });

  it("grants roles like Go", () => {
    expect(roleGrants("write", "append")).toBe(true);
    expect(roleGrants("append", "read")).toBe(false);
    expect(roleGrants("create", "read")).toBe(false);
    expect(roleGrants("admin", "write")).toBe(true);
    expect(verifierHash(new TextEncoder().encode("verifier"))).toBe(vectors.shares[1]!.share.verifier_hash);
  });
});
