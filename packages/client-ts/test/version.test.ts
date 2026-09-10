import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

import { SDK_BUILD_TIME, SDK_VERSION } from "../src/index.js";
import { run } from "../src/cli/index.js";

const pkg = JSON.parse(
  readFileSync(join(dirname(fileURLToPath(import.meta.url)), "../package.json"), "utf8"),
) as { version: string };

describe("SDK_VERSION", () => {
  it("matches package.json", () => {
    expect(SDK_VERSION).toBe(pkg.version);
  });

  it("is a stamped UTC minute", () => {
    expect(SDK_BUILD_TIME).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/);
  });
});

describe("poweur version", () => {
  it("prints the SDK semver and build stamp", async () => {
    for (const arg of ["version", "--version", "-v"]) {
      let out = "";
      const code = await run([arg], {
        stdout: (s) => {
          out += s;
        },
        stderr: () => {},
      });
      expect(code, arg).toBe(0);
      expect(out, arg).toContain(SDK_VERSION);
      expect(out, arg).toContain(SDK_BUILD_TIME);
    }
  });
});
