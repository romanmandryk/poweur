/**
 * The staging step is the whole of the "no fork of the UI" rule in code: the
 * shell ships the same build the relay serves. These tests exist because the
 * ways that quietly stops being true — an absolute path in index.html, source
 * maps or tooling copied along — produce a bundle that works in the browser and
 * misbehaves on a device, which is the worst place to find out.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readdirSync, readFileSync, existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const mobile = join(here, "..");
const www = join(mobile, "www");
const skip = !existsSync(join(mobile, "../web/dist/index.html")) && "apps/web is not built (`pnpm web`)";

test("stages the built web client into www/, without source maps", { skip }, () => {
  execFileSync("node", [join(mobile, "scripts/stage-web.mjs")], { stdio: "pipe" });

  assert.ok(existsSync(join(www, "index.html")), "missing index.html");
  const assets = readdirSync(join(www, "assets"));
  assert.ok(assets.some((name) => name.endsWith(".js")), "no bundle in assets/");
  assert.ok(!assets.some((name) => name.endsWith(".map")), "source maps were staged");
});

test("ships no source, test or tooling files to a device", { skip }, () => {
  const staged = readdirSync(www);
  for (const unwanted of ["src", "test", "node_modules", "package.json", "playwright.config.js", "vite.config.ts"]) {
    assert.ok(!staged.includes(unwanted), `${unwanted} was staged`);
  }
});

test("the entry point resolves everything relatively", { skip }, () => {
  const html = readFileSync(join(www, "index.html"), "utf8");
  // `capacitor://localhost/` has no /app/ prefix; an absolute path here loads
  // in the browser and fails only once it is on a phone.
  const absolute = [...html.matchAll(/(?:src|href)\s*=\s*"(\/[^"]*)"/g)].map((m) => m[1]);
  assert.deepEqual(absolute, [], `absolute paths in index.html: ${absolute.join(", ")}`);
});

test("the Capacitor config points at the staged directory", () => {
  const config = JSON.parse(readFileSync(join(mobile, "capacitor.config.json"), "utf8"));
  assert.equal(config.webDir, "www");
  assert.ok(config.appId.includes("."), "appId must be a reverse-DNS identifier");
});
