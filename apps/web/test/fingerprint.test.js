/**
 * Safety numbers in the browser (EPIC-007 E07-T4).
 *
 * The web app shows a fingerprint in two places that matter — the contact
 * panel and the key-mismatch dialog — and a user is expected to read one of
 * them aloud to the person on the other end, who is looking at a CLI. So the
 * assertion here is not "the function exists": it is that the copy of the
 * derivation the *browser* actually loads (`vendor/`, no bundler) produces
 * byte-for-byte what Go produced into the conformance fixtures.
 *
 * A stale vendor tree is exactly how the two sides would silently disagree.
 */
import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const WEB_DIR = join(dirname(fileURLToPath(import.meta.url)), "..");
const VECTORS = join(
  WEB_DIR,
  "../../packages/identity/testdata/vectors/fingerprints.json",
);

const vectors = JSON.parse(readFileSync(VECTORS, "utf8"));

describe("safety numbers as the browser loads them", () => {
  it("matches the Go fixtures through the vendored client", async () => {
    const client = await import(join(WEB_DIR, "vendor/poweur-client/index.js"));
    expect(typeof client.keyFingerprint).toBe("function");
    expect(typeof client.fingerprintOrKey).toBe("function");

    for (const vector of vectors) {
      const derive = () =>
        vector.kind === "encryption"
          ? client.encryptionKeyFingerprint(vector.key)
          : client.keyFingerprint(vector.key);
      if (!vector.valid) {
        expect(derive, vector.name).toThrow();
        continue;
      }
      expect(derive(), vector.name).toBe(vector.fingerprint);
    }
  });

  it("is short enough to read aloud and safe to render", async () => {
    const { keyFingerprint, fingerprintOrKey } = await import(
      join(WEB_DIR, "vendor/poweur-client/index.js")
    );
    const signing = vectors.find((v) => v.valid && v.kind === "signing");
    expect(keyFingerprint(signing.key)).toMatch(/^\d{5} \d{5} \d{5} \d{5}$/);
    // A pin the app cannot parse must still be shown, not blanked: the UI
    // renders whatever this returns straight into the contact panel.
    expect(fingerprintOrKey("not-a-key")).toBe("not-a-key");
    expect(fingerprintOrKey("")).toBe("");
  });
});
