/**
 * Transcription of a device-enrollment request code. Lives next to the
 * ceremony rather than the live-relay suite because it must not need a
 * network: a corrupted paste is a local mistake.
 */

import { describe, expect, it } from "vitest";

import { normalizeRendezvousId } from "../src/enroll.js";

describe("normalizeRendezvousId", () => {
  const id = "AbC-_def0123456789xyz";

  it("leaves a clean id alone", () => {
    expect(normalizeRendezvousId(id)).toBe(id);
  });

  it("strips wrapping and wrapping-whitespace that mobile paste injects", () => {
    expect(normalizeRendezvousId(`  ${id} \n`)).toBe(id);
    expect(normalizeRendezvousId(id.split("").join("\u200b"))).toBe(id);
  });

  it("turns unicode dashes back into the ASCII hyphen base64url uses", () => {
    expect(normalizeRendezvousId(id.replace("-", "\u2013"))).toBe(id);
  });

  it("does not case-fold: the token is case-sensitive", () => {
    expect(normalizeRendezvousId(id)).not.toBe(id.toLowerCase());
  });
});
