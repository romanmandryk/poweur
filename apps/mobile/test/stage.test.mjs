/**
 * The staging step is the whole of the "no fork of the UI" rule in code: the
 * shell ships the same tree the relay serves. These tests exist because the
 * ways that quietly stops being true — an absolute `/app/` path, a missing
 * vendor directory — produce a bundle that works in the browser and 404s on a
 * device, which is the worst place to find out.
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

test("stages the web client into www/", () => {
  execFileSync("node", [join(mobile, "scripts/stage-web.mjs")], { stdio: "pipe" });

  for (const required of ["index.html", "js", "css", "vendor"]) {
    assert.ok(existsSync(join(www, required)), `missing ${required}`);
  }
  // The vendored SDK has to come along: the app imports it through an import
  // map, so a missing vendor tree is a blank screen rather than a build error.
  assert.ok(readdirSync(join(www, "vendor")).length > 0, "vendor/ is empty");
});

test("ships no test or tooling files to a device", () => {
  const staged = readdirSync(www);
  for (const unwanted of ["test", "node_modules", "package.json", "playwright.config.js"]) {
    assert.ok(!staged.includes(unwanted), `${unwanted} was staged`);
  }
});

test("the entry point resolves everything relatively", () => {
  const html = readFileSync(join(www, "index.html"), "utf8");
  // `capacitor://localhost/` has no /app/ prefix; an absolute path here loads
  // in the browser and fails only once it is on a phone.
  const absolute = [...html.matchAll(/(?:src|href)\s*=\s*"(\/[^"]*)"/g)].map((m) => m[1]);
  assert.deepEqual(absolute, [], `absolute paths in index.html: ${absolute.join(", ")}`);

  const importMap = html.match(/<script type="importmap">([\s\S]*?)<\/script>/);
  assert.ok(importMap, "no import map");
  const imports = JSON.parse(importMap[1]).imports ?? {};
  for (const [specifier, target] of Object.entries(imports)) {
    assert.ok(target.startsWith("./") || target.startsWith("../"),
      `import map entry ${specifier} → ${target} is not relative`);
  }
});

test("the Capacitor config points at the staged directory", () => {
  const config = JSON.parse(readFileSync(join(mobile, "capacitor.config.json"), "utf8"));
  assert.equal(config.webDir, "www");
  assert.ok(config.appId.includes("."), "appId must be a reverse-DNS identifier");
});

// EPIC-021 E21-T13: the React build stages the same way, behind WEB_SOURCE=next.
const nextDist = join(mobile, "../web-next/dist/index.html");

test("WEB_SOURCE=next stages the React build, relative and without source maps", { skip: !existsSync(nextDist) && "apps/web-next is not built" }, () => {
  try {
    execFileSync("node", [join(mobile, "scripts/stage-web.mjs")], { stdio: "pipe", env: { ...process.env, WEB_SOURCE: "next" } });

    assert.ok(existsSync(join(www, "index.html")), "missing index.html");
    assert.ok(readFileSync(join(www, ".staged"), "utf8").startsWith("source: next"));
    const assets = readdirSync(join(www, "assets"));
    assert.ok(assets.some((name) => name.endsWith(".js")), "no bundle in assets/");
    assert.ok(!assets.some((name) => name.endsWith(".map")), "source maps were staged");
    // No legacy tree mixed in.
    assert.ok(!existsSync(join(www, "vendor")), "legacy vendor/ was staged");

    const html = readFileSync(join(www, "index.html"), "utf8");
    const absolute = [...html.matchAll(/(?:src|href)\s*=\s*"(\/[^"]*)"/g)].map((m) => m[1]);
    assert.deepEqual(absolute, [], `absolute paths in index.html: ${absolute.join(", ")}`);
  } finally {
    // Leave www/ as the default (legacy) staging, as the other tests expect.
    execFileSync("node", [join(mobile, "scripts/stage-web.mjs")], { stdio: "pipe", env: { ...process.env, WEB_SOURCE: "" } });
  }
});
