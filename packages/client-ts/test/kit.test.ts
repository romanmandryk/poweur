/**
 * Recovery kit: TypeScript ↔ Go conformance (EPIC-011 E11-T1).
 *
 * Go and TypeScript use different BIP39 libraries, so the wordlists and
 * checksum handling could silently diverge. These vectors are what stops that:
 * a kit written down from one client must restore in the other.
 */

import { describe, expect, it } from "vitest";

import { toBase64url } from "../src/encoding.js";
import { deriveSigningKey, newSeed } from "../src/crypto/seed.js";
import {
  MNEMONIC_WORDS,
  mnemonicToSeed,
  newRecoveryKit,
  normalizeMnemonic,
  parseSeedOrMnemonic,
  seedToMnemonic,
  validMnemonic,
} from "../src/kit.js";
import { loadVectors } from "./vectors.js";

interface KitVectorFile {
  words: number;
  vectors: { name: string; seed: string; mnemonic: string }[];
}

describe("recovery kit — Go conformance", () => {
  const file = loadVectors<KitVectorFile>("recovery-kit");

  it("agrees on the word count", () => {
    expect(file.words).toBe(MNEMONIC_WORDS);
  });

  for (const v of file.vectors) {
    it(`produces Go's mnemonic for ${v.name}`, () => {
      const seed = parseSeedOrMnemonic(v.seed);
      expect(seedToMnemonic(seed)).toBe(v.mnemonic);
    });

    it(`decodes Go's mnemonic for ${v.name}`, () => {
      expect(toBase64url(mnemonicToSeed(v.mnemonic))).toBe(v.seed);
    });

    // The point of a kit is the identity it restores, not the bytes.
    it(`restores the same signing key for ${v.name}`, () => {
      const fromWords = deriveSigningKey(mnemonicToSeed(v.mnemonic));
      const fromSeed = deriveSigningKey(parseSeedOrMnemonic(v.seed));
      expect(toBase64url(fromWords.publicKey)).toBe(toBase64url(fromSeed.publicKey));
    });
  }
});

describe("recovery kit — behaviour", () => {
  it("round-trips random seeds", () => {
    for (let i = 0; i < 5; i += 1) {
      const seed = newSeed();
      expect(toBase64url(mnemonicToSeed(seedToMnemonic(seed)))).toBe(toBase64url(seed));
    }
  });

  it("normalizes case and whitespace from a hand-copied kit", () => {
    const seed = newSeed();
    const words = seedToMnemonic(seed).split(" ");
    const messy = `  ${words.slice(0, 4).join("  ").toUpperCase()}\n${words.slice(4).join("\t")}  `;
    expect(toBase64url(mnemonicToSeed(messy))).toBe(toBase64url(seed));
    expect(normalizeMnemonic(messy)).toBe(words.join(" "));
  });

  it("rejects typos via the checksum", () => {
    // Fixed vector: a random valid-word substitution has a 1/256 chance of
    // accidentally producing another valid checksum, which made this test
    // flaky even though the validator was correct.
    const words = "absurd avoid scissors anxiety gather lottery category door army half long cage bachelor another expect people blade school educate curtain scrub monitor lady beyond".split(" ");

    const swapped = [...words];
    swapped[0] = swapped[0] === "zoo" ? "abandon" : "zoo";
    expect(validMnemonic(swapped.join(" "))).toBe(false);

    const transposed = [...words];
    [transposed[0], transposed[1]] = [transposed[1]!, transposed[0]!];
    if (transposed[0] !== transposed[1]) {
      expect(validMnemonic(transposed.join(" "))).toBe(false);
    }

    expect(validMnemonic(words.slice(0, 23).join(" "))).toBe(false);
    expect(validMnemonic([...words, words[0]!].join(" "))).toBe(false);
    expect(validMnemonic("")).toBe(false);
    expect(validMnemonic(Array(MNEMONIC_WORDS).fill("notaword").join(" "))).toBe(false);
  });

  it("rejects a seed that is not 32 bytes", () => {
    expect(() => seedToMnemonic(new Uint8Array(16))).toThrow();
  });

  it("accepts either encoding through one entry point", () => {
    const seed = newSeed();
    expect(toBase64url(parseSeedOrMnemonic(seedToMnemonic(seed)))).toBe(toBase64url(seed));
    expect(toBase64url(parseSeedOrMnemonic(toBase64url(seed)))).toBe(toBase64url(seed));
    expect(() => parseSeedOrMnemonic("nonsense")).toThrow();
  });

  it("builds a kit whose two encodings name the same secret", () => {
    const seed = newSeed();
    const kit = newRecoveryKit("alice.poweur.net", "relay.poweur.net", seed);
    expect(kit.identity).toBe("alice.poweur.net");
    expect(toBase64url(mnemonicToSeed(kit.mnemonic))).toBe(kit.seed);
  });
});
