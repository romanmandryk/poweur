#!/usr/bin/env node
// Release helper. See RELEASING.md.
//
//   node scripts/release.mjs status
//   node scripts/release.mjs bump <sdk|cli|relay|web|oauth> [patch|minor|major|x.y.z]
//   node scripts/release.mjs tag <sdk|cli> [--dry-run]
//
// `bump` edits the version files (open a pull request with the result).
// `tag` runs from an up-to-date, clean master and pushes sdk-v<version> or cli-v<version>,
// which starts the release workflow.
import { execFileSync } from "node:child_process";
import { PACKAGES, bumpVersion, readVersion, writeVersion } from "./versions.mjs";

const git = (...args) => execFileSync("git", args, { encoding: "utf8" }).trim();
const fail = (msg) => {
  console.error(msg);
  process.exit(1);
};
const [cmd, name, arg] = process.argv.slice(2);
const dry = process.argv.includes("--dry-run");

function latestTag(prefix) {
  const tags = git("tag", "--list", `${prefix}*`).split("\n").filter(Boolean);
  const num = (t) => t.slice(prefix.length).split(".").map(Number);
  tags.sort((a, b) => {
    const [x, y] = [num(a), num(b)];
    return x[0] - y[0] || x[1] - y[1] || x[2] - y[2];
  });
  return tags.at(-1) ?? "none";
}

if (cmd === "status") {
  git("fetch", "--tags", "--quiet");
  for (const [key, pkg] of Object.entries(PACKAGES)) {
    const tag = pkg.tagPrefix ? latestTag(pkg.tagPrefix) : "deployed from master";
    console.log(`${key.padEnd(6)} ${pkg.label.padEnd(15)} files ${readVersion(key).padEnd(8)} latest tag ${tag}`);
  }
} else if (cmd === "bump") {
  if (!PACKAGES[name]) fail(`unknown package "${name}" (${Object.keys(PACKAGES).join(", ")})`);
  const from = readVersion(name);
  const to = bumpVersion(from, arg ?? "patch");
  writeVersion(name, to);
  console.log(`${name}: ${from} -> ${to}. Commit it in a pull request, then run: node scripts/release.mjs tag ${name}`);
} else if (cmd === "tag") {
  const pkg = PACKAGES[name];
  if (!pkg?.tagPrefix) fail("tag needs sdk or cli (relay, web and oauth deploy from master)");
  const version = readVersion(name);
  const tag = `${pkg.tagPrefix}${version}`;
  if (git("rev-parse", "--abbrev-ref", "HEAD") !== "master") fail("run this from master");
  if (git("status", "--porcelain")) fail("the working tree is not clean");
  git("fetch", "--tags", "--quiet");
  if (git("rev-parse", "HEAD") !== git("rev-parse", "origin/master")) fail("master is not at origin/master (git pull first)");
  if (git("tag", "--list", tag)) fail(`${tag} already exists: bump the version first`);
  if (name === "sdk" && readVersion(name) !== readVersion("sdk")) fail("version files disagree");
  console.log(`${dry ? "would tag" : "tagging"} ${tag} at ${git("rev-parse", "--short", "HEAD")}`);
  if (!dry) {
    git("tag", "-a", tag, "-m", `${pkg.label} ${version}`);
    git("push", "origin", tag);
    console.log(`pushed ${tag}; watch: gh run list --workflow release-${name === "sdk" ? "npm" : "cli"}.yml`);
  }
} else {
  fail("usage: release.mjs status | bump <package> [patch|minor|major|x.y.z] | tag <sdk|cli> [--dry-run]");
}
