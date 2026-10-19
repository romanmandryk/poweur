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
      { file: "packages/client-ts/src/index.ts", pattern: /(export const SDK_VERSION = ")([^"]+)(")/, stamp: /(export const SDK_BUILD_TIME = ")([^"]+)(")/ },
    ],
  },
  web: {
    label: "Web app",
    file: "apps/web/package.json",
    pattern: jsonVersion,
    paths: ["apps/web/"],
    // Settings → About; a Vitest test fails when it disagrees with package.json.
    also: [
      { file: "apps/web/src/build-info.ts", pattern: /(export const APP_VERSION = ")([^"]+)(")/, stamp: /(export const APP_BUILD_TIME = ")([^"]+)(")/ },
    ],
  },
  oauth: { label: "OAuth bridge", file: "apps/oauth/bridge/doc.go", pattern: goConst, paths: ["apps/oauth/"] },
  email: { label: "Email bridge", file: "apps/email/bridge/doc.go", pattern: goConst, paths: ["apps/email/"] },
  openclaw: { label: "OpenClaw channel", file: "packages/openclaw-poweur/package.json", pattern: jsonVersion, paths: ["packages/openclaw-poweur/"], tagPrefix: "openclaw-v" },
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

/** Every file a package's version lives in, with its version pattern and optional build stamp. */
export function versionTargets(name) {
  const pkg = PACKAGES[name];
  return [{ file: pkg.file, pattern: pkg.pattern }, ...(pkg.also ?? [])];
}

/**
 * Whether two copies of a version file differ only in the version and build
 * stamp: aligning a version (the npm alias, build-info) changes nothing that
 * ships. A file that is missing on either side counts as a real change.
 */
export function onlyVersionChanged(target, before, after) {
  if (before == null || after == null) return false;
  const blank = (text) => {
    let out = text.replace(target.pattern, "$1$3");
    if (target.stamp) out = out.replace(target.stamp, "$1$3");
    return out;
  };
  return blank(before) === blank(after);
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

export function writeVersion(name, version, root = ".", now = new Date()) {
  for (const t of versionTargets(name)) {
    const path = join(root, t.file);
    let text = replaceVersion(readFileSync(path, "utf8"), t.pattern, version);
    if (t.stamp) text = text.replace(t.stamp, `$1${utcStamp(now)}$3`);
    writeFileSync(path, text);
  }
}
