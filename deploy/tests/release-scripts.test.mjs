import test from "node:test";
import { readFileSync } from "node:fs";
import assert from "node:assert/strict";
import { PACKAGES, bumpVersion, parseVersion, replaceVersion, shippedFiles, readVersion } from "../../scripts/versions.mjs";

test("bumpVersion", () => {
  assert.equal(bumpVersion("0.2.12", "patch"), "0.2.13");
  assert.equal(bumpVersion("0.2.12", "minor"), "0.3.0");
  assert.equal(bumpVersion("0.2.12", "major"), "1.0.0");
  assert.equal(bumpVersion("0.2.12", "1.4.0"), "1.4.0");
  assert.throws(() => bumpVersion("0.2.12", "huge"));
});

test("every package's version file is readable with its pattern", () => {
  for (const name of Object.keys(PACKAGES)) assert.match(readVersion(name), /^\d+\.\d+\.\d+$/, name);
});

test("the SDK and the poweur alias carry one version", () => {
  const sdk = PACKAGES.sdk;
  const vs = [readVersion("sdk"), ...sdk.also.map((t) => parseVersion(readFileSync(t.file), t.pattern))];
  assert.equal(new Set(vs).size, 1, vs.join(" vs "));
});

test("replaceVersion keeps the surrounding text", () => {
  assert.equal(replaceVersion('var Version = "0.2.10"\n', /(var Version = ")([^"]+)(")/, "0.2.11"), 'var Version = "0.2.11"\n');
  assert.throws(() => replaceVersion("nothing", /(var Version = ")([^"]+)(")/, "1.0.0"));
});

test("shippedFiles ignores tests, fixtures, docs and dependency manifests", () => {
  const files = [
    "apps/cli/internal/cli/run.go",
    "apps/cli/internal/cli/run_test.go",
    "apps/cli/go.sum",
    "apps/api/internal/relay/devices.go",
    "packages/client-ts/src/index.ts",
    "packages/client-ts/test/conformance.test.ts",
    "packages/client-ts/README.md",
    "apps/web/e2e/x.spec.js",
  ];
  assert.deepEqual(shippedFiles("cli", files), ["apps/cli/internal/cli/run.go"]);
  assert.deepEqual(shippedFiles("relay", files), ["apps/api/internal/relay/devices.go"]);
  assert.deepEqual(shippedFiles("sdk", files), ["packages/client-ts/src/index.ts"]);
  assert.deepEqual(shippedFiles("web", files), []);
});
