#!/usr/bin/env node
// Fails when a pull request changes a package's shipped code without bumping its version
// (AGENTS.md → Version bumps). Usage: check-version-bumps.mjs <base-ref> <head-ref>.
// Set SKIP_VERSION_CHECK=1 (the workflow does this for the `no-version-bump` label) to pass.
import { execFileSync } from "node:child_process";
import { PACKAGES, parseVersion, shippedFiles } from "./versions.mjs";

const [base, head] = process.argv.slice(2);
if (!base || !head) {
  console.error("usage: check-version-bumps.mjs <base> <head>");
  process.exit(2);
}
if (process.env.SKIP_VERSION_CHECK === "1") {
  console.log("skipped (no-version-bump label)");
  process.exit(0);
}
const git = (...a) => execFileSync("git", a, { encoding: "utf8" });
const changed = git("diff", "--name-only", `${base}...${head}`).split("\n").filter(Boolean);
const versionAt = (ref, pkg) => {
  try {
    return parseVersion(git("show", `${ref}:${pkg.file}`), pkg.pattern);
  } catch {
    return null;
  }
};

let bad = 0;
for (const [name, pkg] of Object.entries(PACKAGES)) {
  const shipped = shippedFiles(name, changed);
  if (!shipped.length) continue;
  const [before, after] = [versionAt(base, pkg), versionAt(head, pkg)];
  if (before !== after) {
    console.log(`ok   ${pkg.label}: ${before} -> ${after}`);
    continue;
  }
  bad++;
  console.error(`FAIL ${pkg.label} is still ${after} but shipped code changed:\n  ${shipped.slice(0, 5).join("\n  ")}`);
  console.error(`     bump it: node scripts/release.mjs bump ${name}   (or add the no-version-bump label if behaviour is unchanged)`);
}
process.exit(bad ? 1 : 0);
