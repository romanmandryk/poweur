import { generateEncryptionKeypair } from "../src/crypto/index.js";
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { fromBase64, toBase64url, utf8 } from "../src/encoding.js";
import { openRecordContent, sealRecordContent, appendRecordHash, canonicalAppendRecord, signAppendRecord, verifyAppendRecord, verifyNextRecord, type AppendRecord } from "../src/drive/records.js";

const vectors = JSON.parse(readFileSync(new URL("../../identity/testdata/vectors/drive-records.json", import.meta.url), "utf8")) as Array<{
  record: AppendRecord; private_key: string; public_key: string; canonical: string; hash: string; node_private?: string;
}>;

describe("signed drive records", () => {
  for (const v of vectors) it(`matches Go author sequence ${v.record.sequence}`, () => {
    expect(toBase64url(canonicalAppendRecord(v.record))).toBe(v.canonical);
    expect(signAppendRecord(v.record, fromBase64(v.private_key)).signature).toBe(v.record.signature);
    expect(appendRecordHash(v.record)).toBe(v.hash);
    if (v.node_private) expect(openRecordContent(v.record, fromBase64(v.node_private))).toEqual(utf8("sealed contribution"));
    expect(() => verifyAppendRecord(v.record, fromBase64(v.public_key))).not.toThrow();
    expect(() => verifyNextRecord(v.record, fromBase64(v.public_key), v.record.sequence - 1, v.record.previous)).not.toThrow();
    for (const changes of [{ drive: "carol.example" }, { author: "carol.example" }, { node: "c".repeat(32) }, { generation: 2 }, { signature: "" }, { chunks: [{ id: "c".repeat(64), size: 4136 }] }]) {
      expect(() => verifyAppendRecord({ ...v.record, ...changes }, fromBase64(v.public_key))).toThrow();
    }
  });
  it("rejects duplicates, reorders, gaps and author-chain forks", () => {
    const first = vectors[0]!, second = vectors[1]!, third = vectors[2]!;
    const pub = fromBase64(first.public_key);
    expect(() => verifyNextRecord(first.record, pub, 1, first.hash)).toThrow(/duplicate/);
    expect(() => verifyNextRecord(first.record, pub, 2, second.hash)).toThrow(/reordered/);
    expect(() => verifyNextRecord(third.record, pub, 1, first.hash)).toThrow(/gap/);
    expect(() => verifyNextRecord(second.record, pub, 1, "0".repeat(64))).toThrow(/mismatch/);
  });
  it("refuses noncanonical and out-of-range fields", () => {
    const record = vectors[0]!.record;
    for (const changes of [{ format: 2 }, { author: "BOB.example" }, { drive: " alice.example" }, { node: "../escape" }, { generation: 0 }, { sequence: 0.5 }, { sequence: Number.MAX_SAFE_INTEGER + 1 }, { previous: "a".repeat(64) }, { chunks: [] }, { chunks: [{ id: "b".repeat(64), size: 4137 }] }]) {
      expect(() => canonicalAppendRecord({ ...record, ...changes })).toThrow();
    }
  });
});

it("seals write-only records and binds their author chain", () => {
  const { publicKey, privateKey } = generateEncryptionKeypair();
  const record = sealRecordContent(vectors[0]!.record, publicKey, utf8("drop box secret"));
  expect(openRecordContent(record, privateKey)).toEqual(utf8("drop box secret"));
  expect(() => openRecordContent({ ...record, author: "carol.example" }, privateKey)).toThrow();
  expect(() => openRecordContent({ ...record, sequence: 2, previous: "a".repeat(64) }, privateKey)).toThrow();
  expect(() => canonicalAppendRecord({ ...record, chunks: vectors[0]!.record.chunks })).toThrow();
  expect(() => openRecordContent(vectors[0]!.record, privateKey)).toThrow();
});
