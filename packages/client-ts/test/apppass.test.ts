/** App passwords: the argon2id parameters must match Go's exactly. */

import { describe, expect, it } from "vitest";

import {
  generateAppPassword,
  hashAppPassword,
  parseAppPasswordsFile,
  verifyAppPassword,
} from "../src/apppass.js";

describe("app passwords", () => {
  it("generates a password in the poweur-ap- form", () => {
    const password = generateAppPassword();
    expect(password).toMatch(/^poweur-ap-[A-Za-z0-9_-]{32}$/);
    expect(generateAppPassword()).not.toBe(password);
  });

  it("produces a PHC hash with Go's parameters", async () => {
    const hash = await hashAppPassword("hunter2");
    // A different m/t/p here would silently stop the relay verifying.
    expect(hash.startsWith("$argon2id$v=19$m=65536,t=1,p=4$")).toBe(true);
    expect(hash.split("$")).toHaveLength(6);
  });

  it("verifies the right password and rejects the wrong one", async () => {
    const hash = await hashAppPassword("hunter2");
    expect(await verifyAppPassword("hunter2", hash)).toBe(true);
    expect(await verifyAppPassword("hunter3", hash)).toBe(false);
  });

  it("salts each hash so two hashes of one password differ", async () => {
    expect(await hashAppPassword("same")).not.toBe(await hashAppPassword("same"));
  });

  it("rejects a malformed hash rather than throwing", async () => {
    for (const bad of ["", "$argon2i$v=19$m=1,t=1,p=1$AA$BB", "not-a-hash", "$argon2id$v=18$m=1,t=1,p=1$AA$BB"]) {
      expect(await verifyAppPassword("x", bad)).toBe(false);
    }
  });

  it("parses a file with no passwords array", () => {
    expect(parseAppPasswordsFile("{}").passwords).toEqual([]);
  });
});
