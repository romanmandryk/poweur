#!/usr/bin/env node
// Writes the shared header and footer (assets/chrome.js) into every page's HTML, so the first paint
// already has its navigation instead of an empty bar that JavaScript fills in. Run by the website
// deploy on a copy of the checkout, after build-blog.mjs, and idempotent: a page that already has
// its header is left alone. Without it (a local preview) assets/site.js builds the same markup.
//
//   node apps/site/scripts/prerender-chrome.mjs
import { readdirSync, readFileSync, realpathSync, statSync, writeFileSync } from "node:fs";
import { dirname, join, relative, sep } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const site = join(dirname(fileURLToPath(import.meta.url)), "..");

async function loadChrome() {
  await import(pathToFileURL(join(site, "assets", "chrome.js")).href);
  return globalThis.PoweurChrome;
}

/** "faq/index.html" -> "/faq/", "architecture.html" -> "/architecture.html". */
export function pagePath(rel) {
  const p = "/" + rel.split(sep).join("/");
  return p.replace(/index\.html$/, "");
}

export function prerender(html, rel, chrome, year = new Date().getFullYear()) {
  const root = /<html[^>]*\sdata-root="([^"]*)"/.exec(html)?.[1] ?? "";
  let out = html;
  out = out.replace(/<header id="nav"><\/header>/, () => `<header id="nav" class="nav">${chrome.navHtml(root, pagePath(rel))}</header>`);
  out = out.replace(/<footer id="footer"><\/footer>/, () => `<footer id="footer">${chrome.footerHtml(root, year)}</footer>`);
  return out;
}

function* htmlFiles(dir) {
  for (const name of readdirSync(dir)) {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) {
      if (name === "node_modules" || name === "content" || name === "scripts") continue;
      yield* htmlFiles(path);
    } else if (name.endsWith(".html")) yield path;
  }
}

if (realpathSync(process.argv[1]) === realpathSync(fileURLToPath(import.meta.url))) {
  const chrome = await loadChrome();
  let n = 0;
  for (const file of htmlFiles(site)) {
    const before = readFileSync(file, "utf8");
    const after = prerender(before, relative(site, file), chrome);
    if (after !== before) {
      writeFileSync(file, after);
      n++;
    }
  }
  console.log(`prerendered the header and footer into ${n} page(s)`);
}
