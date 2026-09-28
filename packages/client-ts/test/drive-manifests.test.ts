import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { fromBase64, toBase64url } from "../src/encoding.js";
import {
  canonicalChunkPage, canonicalManifest, chunkPageHash, manifestHash, signManifest, splitPages,
  validateManifest, verifyManifest, verifyManifestPages, type ChunkPage, type Manifest,
} from "../src/drive/manifest.js";

const vectors = JSON.parse(readFileSync(new URL("../../identity/testdata/vectors/drive-manifests.json", import.meta.url), "utf8")) as {
  pages: Array<{ page: ChunkPage; canonical: string; hash: string }>;
  manifests: Array<{ name: string; manifest: Manifest; pages: ChunkPage[] | null; seed: string; public_key: string; canonical: string; hash: string }>;
};
const hex = (bytes: Uint8Array) => Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");

describe("chunk-list pages match Go", () => {
  for (const v of vectors.pages) it(`page of ${v.page.chunks.length}`, () => {
    expect(hex(canonicalChunkPage(v.page))).toBe(v.canonical);
    expect(chunkPageHash(v.page)).toBe(v.hash);
  });
  it("splits at 1024 references the same way", () => {
    const refs = vectors.pages.flatMap((v) => v.page.chunks);
    const { hashes } = splitPages(vectors.pages[0]!.page.drive, vectors.pages[0]!.page.node, refs);
    expect(hashes).toEqual(vectors.pages.map((v) => v.hash));
  });
});

describe("signed manifests match Go", () => {
  for (const v of vectors.manifests) it(v.name, () => {
    const m = { ...v.manifest, name_hash: v.manifest.name_hash ?? "" };
    expect(hex(canonicalManifest(m))).toBe(v.canonical);
    expect(signManifest(m, fromBase64(v.seed)).signature).toBe(m.signature);
    expect(manifestHash(m)).toBe(v.hash);
    expect(() => verifyManifest(m, fromBase64(v.public_key))).not.toThrow();
    expect(verifyManifestPages(m, v.pages ?? [])).toHaveLength(m.count);
    for (const changes of [{ drive: "carol.example" }, { version: "3".repeat(32) }, { generation: 2 }, { signature: "" }, { mode: "append" as const }]) {
      expect(() => verifyManifest({ ...m, ...changes }, fromBase64(v.public_key))).toThrow();
    }
  });

  it("rejects reordered, foreign and missing pages", () => {
    const v = vectors.manifests[0]!;
    const pages = v.pages!;
    expect(() => verifyManifestPages(v.manifest, [pages[1]!, pages[0]!])).toThrow();
    expect(() => verifyManifestPages(v.manifest, [{ ...pages[0]!, node: "f".repeat(32) }, pages[1]!])).toThrow();
    expect(() => verifyManifestPages(v.manifest, pages.slice(0, 1))).toThrow();
  });

  it("refuses shapes Go refuses", () => {
    const create = vectors.manifests[0]!.manifest;
    for (const changes of [
      { parent: "2".repeat(32) }, { node_key: null }, { content_key: null }, { name_hash: "" }, { operation: "append" },
      { count: create.count + 1024 }, { folder: create.node }, { kind: "folder" as const }, { generation: 0 }, { author: "Alice.example" },
    ]) {
      expect(() => validateManifest({ ...create, ...changes } as Manifest)).toThrow();
    }
    // A removal names nothing; the one encoded here would otherwise be valid.
    expect(() => validateManifest({ ...create, operation: "remove", parent: "1".repeat(32), version: "2".repeat(32), folder: "", name: null, name_hash: "", node_key: null, content_key: null, count: 0, pages: [] })).not.toThrow();
  });
});

it("keeps the base64url encoding strict", () => {
  const m = vectors.manifests[0]!.manifest;
  expect(() => validateManifest({ ...m, node_key: { ...m.node_key!, nonce: toBase64url(new Uint8Array(12)) + "=" } })).toThrow();
});
