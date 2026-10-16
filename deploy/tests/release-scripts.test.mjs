import test from "node:test";
import { cpSync, mkdirSync, mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import assert from "node:assert/strict";
import {
  PACKAGES, bumpVersion, onlyVersionChanged, parseVersion, replaceVersion, shippedFiles, readVersion, versionTargets, writeVersion,
} from "../../scripts/versions.mjs";

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

test("every package's version files agree, build-info included", () => {
  for (const name of Object.keys(PACKAGES)) {
    const vs = versionTargets(name).map((t) => parseVersion(readFileSync(t.file, "utf8"), t.pattern));
    assert.equal(new Set(vs).size, 1, `${name}: ${vs.join(" vs ")}`);
  }
});

test("bump writes every version file and stamps the build time", () => {
  for (const name of ["web", "sdk"]) {
    const root = mkdtempSync(join(tmpdir(), "release-"));
    for (const t of versionTargets(name)) {
      mkdirSync(dirname(join(root, t.file)), { recursive: true });
      cpSync(t.file, join(root, t.file));
    }
    writeVersion(name, "9.8.7", root, new Date("2031-02-03T04:05:00Z"));
    for (const t of versionTargets(name)) {
      const text = readFileSync(join(root, t.file), "utf8");
      assert.equal(parseVersion(text, t.pattern), "9.8.7", `${name} ${t.file}`);
      if (t.stamp) assert.equal(parseVersion(text, t.stamp), "2031-02-03 04:05", `${name} ${t.file} stamp`);
    }
  }
});

test("a version file that only changed its version ships nothing", () => {
  const [, info] = versionTargets("web");
  const before = 'export const APP_VERSION = "0.2.30";\nexport const APP_BUILD_TIME = "2026-10-16 14:42";\n';
  const bumped = 'export const APP_VERSION = "0.2.31";\nexport const APP_BUILD_TIME = "2026-10-16 20:45";\n';
  assert.equal(onlyVersionChanged(info, before, bumped), true);
  assert.equal(onlyVersionChanged(info, before, bumped + "export const BETA = true;\n"), false);
  assert.equal(onlyVersionChanged(info, null, bumped), false);
  const [, alias] = versionTargets("sdk");
  assert.equal(onlyVersionChanged(alias, '{ "version": "0.2.16", "bin": "x" }', '{ "version": "0.2.17", "bin": "x" }'), true);
  assert.equal(onlyVersionChanged(alias, '{ "version": "0.2.16", "bin": "x" }', '{ "version": "0.2.17", "bin": "y" }'), false);
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
