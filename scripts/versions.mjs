// Where each shippable package keeps its version, and which files count as shipped
// behaviour. Shared by scripts/release.mjs and scripts/check-version-bumps.mjs.
// The table mirrors "Version bumps" in AGENTS.md.
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const goVar = /(var Version = ")([^"]+)(")/;
const goConst = /(const Version = ")([^"]+)(")/;
const jsonVersion = /("version":\s*")([^"]+)(")/;

export const PACKAGES = {
  relay: { label: "Relay", file: "apps/api/internal/buildinfo/buildinfo.go", pattern: goVar, paths: ["apps/api/"] },
  cli: { label: "Go CLI", file: "apps/cli/internal/buildinfo/buildinfo.go", pattern: goVar, paths: ["apps/cli/"], tagPrefix: "cli-v" },
  sdk: {
    label: "@poweur/client",
    file: "packages/client-ts/package.json",
    pattern: jsonVersion,
    paths: ["packages/client-ts/", "packages/poweur/"],
    tagPrefix: "sdk-v",
    // Carry the same version; the release workflow refuses a mismatch.
    also: [
      { file: "packages/poweur/package.json", pattern: jsonVersion },
      { file: "packages/client-ts/src/index.ts", pattern: /(export const SDK_VERSION = ")([^"]+)(")/ },
    ],
  },
  web: { label: "Web app", file: "apps/web/package.json", pattern: jsonVersion, paths: ["apps/web/"] },
  oauth: { label: "OAuth bridge", file: "apps/oauth/bridge/doc.go", pattern: goConst, paths: ["apps/oauth/"] },
};

// Files that change without changing what ships: tests, fixtures, docs, lockfiles, dependency manifests.
const notShipped = [
  /_test\.go$/,
  /(^|\/)(test|tests|testdata|e2e)\//,
  /\.(test|spec)\.[cm]?[jt]sx?$/,
  /\.md$/,
  /(^|\/)(go\.sum|go\.mod|pnpm-lock\.yaml)$/,
];

export function shippedFiles(name, files) {
  const pkg = PACKAGES[name];
  return files.filter((f) => pkg.paths.some((p) => f.startsWith(p)) && !notShipped.some((re) => re.test(f)));
}

export function parseVersion(text, pattern) {
  const m = pattern.exec(text);
  return m ? m[2] : null;
}

export function readVersion(name, root = ".") {
  const pkg = PACKAGES[name];
  return parseVersion(readFileSync(join(root, pkg.file), "utf8"), pkg.pattern);
}

export function bumpVersion(current, how) {
  if (/^\d+\.\d+\.\d+$/.test(how)) return how;
  const m = /^(\d+)\.(\d+)\.(\d+)$/.exec(current);
  if (!m) throw new Error(`cannot bump "${current}"`);
  const [maj, min, pat] = m.slice(1).map(Number);
  if (how === "major") return `${maj + 1}.0.0`;
  if (how === "minor") return `${maj}.${min + 1}.0`;
  if (how === "patch") return `${maj}.${min}.${pat + 1}`;
  throw new Error(`bump must be patch, minor, major or x.y.z, got "${how}"`);
}

export function replaceVersion(text, pattern, version) {
  if (!pattern.test(text)) throw new Error("version not found");
  return text.replace(pattern, `$1${version}$3`);
}

export function utcStamp(date = new Date()) {
  return date.toISOString().slice(0, 16).replace("T", " ");
}

export function writeVersion(name, version, root = ".") {
  const pkg = PACKAGES[name];
  for (const t of [{ file: pkg.file, pattern: pkg.pattern }, ...(pkg.also ?? [])]) {
    const path = join(root, t.file);
    let text = replaceVersion(readFileSync(path, "utf8"), t.pattern, version);
    if (name === "sdk" && t.file.endsWith("index.ts")) {
      text = text.replace(/(export const SDK_BUILD_TIME = ")([^"]+)(")/, `$1${utcStamp()}$3`);
    }
    writeFileSync(path, text);
  }
}
