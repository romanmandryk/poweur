import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

import { APP_BUILD_TIME, APP_VERSION } from "../js/build-info.js";

const pkg = JSON.parse(
  readFileSync(join(dirname(fileURLToPath(import.meta.url)), "../package.json"), "utf8"),
);

describe("app build-info", () => {
  it("matches package.json and stamps a UTC minute", () => {
    expect(APP_VERSION).toBe(pkg.version);
    expect(APP_BUILD_TIME).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/);
  });
});
