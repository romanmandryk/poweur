import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";
import { join } from "node:path";
import { pagePath, prerender } from "../../apps/site/scripts/prerender-chrome.mjs";

await import(pathToFileURL(join(import.meta.dirname, "../../apps/site/assets/chrome.js")).href);
const chrome = globalThis.PoweurChrome;

test("page paths", () => {
  assert.equal(pagePath("index.html"), "/");
  assert.equal(pagePath("faq/index.html"), "/faq/");
  assert.equal(pagePath("architecture.html"), "/architecture.html");
  assert.equal(pagePath("legal/privacy/index.html"), "/legal/privacy/");
});

test("the header lists the pages in order, marks the current one and prefixes the root", () => {
  const html = chrome.navHtml("../", "/faq/");
  const labels = [...html.matchAll(/<li><a [^>]*>([^<]+)<\/a><\/li>/g)].map((m) => m[1]);
  assert.deepEqual(labels, ["Product", "Use cases", "Architecture", "Blog", "FAQ", "Docs", "GitHub"]);
  assert.match(html, /<a href="\.\.\/faq\/" aria-current="page" >FAQ<\/a>/);
  assert.doesNotMatch(html, /aria-current="page" >Blog/);
  assert.match(html, /href="\.\.\/blog\/"/);
});

test("prerender fills an empty header and footer once, from the page's data-root", () => {
  const page = '<!doctype html><html lang="en" data-root="../"><body><header id="nav"></header><main></main><footer id="footer"></footer></body></html>';
  const once = prerender(page, "faq/index.html", chrome, 2026);
  assert.match(once, /<header id="nav" class="nav"><div class="wrap">/);
  assert.match(once, /href="\.\.\/faq\/" aria-current="page"/);
  assert.match(once, /© 2026 Poweur contributors/);
  assert.equal(prerender(once, "faq/index.html", chrome, 2026), once, "a second run changes nothing");
});

test("every page loads chrome.js before site.js, and the deploy prerenders it", () => {
  for (const f of ["index.html", "architecture.html", "faq/index.html", "legal/index.html"]) {
    const html = readFileSync(join(import.meta.dirname, "../../apps/site", f), "utf8");
    assert.ok(html.indexOf("assets/chrome.js") > 0 && html.indexOf("assets/chrome.js") < html.indexOf("assets/site.js"), f);
  }
  const wf = readFileSync(join(import.meta.dirname, "../../.github/workflows/deploy-web.yml"), "utf8");
  assert.match(wf, /node apps\/site\/scripts\/prerender-chrome\.mjs/);
  assert.match(wf, /chrome\\\.js/);
});
