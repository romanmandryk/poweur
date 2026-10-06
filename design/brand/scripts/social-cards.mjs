#!/usr/bin/env node
// Link-preview and share cards that carry the website's own title and description:
//   og-image 1200x630 (also the website, web app and docs social card), x-card 1200x600, github-social 1280x640.
// Rendered in Chromium from the site's fonts and the brand SVGs; no other tooling.
//
//   node design/brand/scripts/social-cards.mjs
// Playwright is resolved from apps/web (run `pnpm install` first); set PLAYWRIGHT_FROM to another
// package.json to use a different install.
import { copyFileSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { execFileSync } from "node:child_process";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const repo = join(here, "..", "..", "..");
const require = createRequire(process.env.PLAYWRIGHT_FROM || join(repo, "apps/web/package.json"));
const { chromium } = require("@playwright/test");

// The words come from the website's title and hero, so a change there is a change here.
const HEADLINE = "One open ID for<br>sign-in, messaging<br>and files.";
const SUB = "Keys stay on your devices. Open source, self-hostable.";
const URL_TEXT = "poweur.org";

const fonts = pathToFileURL(join(repo, "apps/site/assets/fonts")).href;
const brand = pathToFileURL(join(repo, "apps/site/assets/brand")).href;
const html = (w, h) => `<!doctype html><meta charset="utf-8"><style>
@font-face{font-family:Sora;font-weight:600;src:url("${fonts}/sora-latin-600-normal.woff2")}
@font-face{font-family:Sora;font-weight:500;src:url("${fonts}/sora-latin-500-normal.woff2")}
@font-face{font-family:Inter;font-weight:500;src:url("${fonts}/inter-latin-500-normal.woff2")}
*{box-sizing:border-box;margin:0}
body{width:${w}px;height:${h}px;overflow:hidden;position:relative;color:#fff;
  background:radial-gradient(circle at 70% 18%,#8B6BE9 0,#3B4AA4 38%,#2A3681 58%,#0E1344 100%)}
.p{position:absolute;right:${w * 0.045}px;bottom:${-h * 0.12}px;height:${h * 1.05}px;opacity:.08}
.logo{position:absolute;left:${w * 0.055}px;top:${h * 0.09}px;height:${h * 0.085}px}
h1{position:absolute;left:${w * 0.055}px;top:${h * 0.27}px;width:${w * 0.9}px;font:600 ${h * 0.125}px/1.08 Sora,sans-serif;letter-spacing:-.01em}
.sub{position:absolute;left:${w * 0.055}px;top:${h * 0.72}px;width:${w * 0.9}px;white-space:nowrap;font:500 ${h * 0.05}px/1.35 Inter,sans-serif;color:#DBD5FF}
.url{position:absolute;left:${w * 0.055}px;bottom:${h * 0.085}px;font:500 ${h * 0.046}px Sora,sans-serif;color:#C3B7FF}
</style><img class="p" src="${brand}/p-white.svg"><img class="logo" src="${brand}/poweur-horizontal-no-id-on-dark.svg">
<h1>${HEADLINE}</h1><div class="sub">${SUB}</div><div class="url">${URL_TEXT}</div>`;

const jobs = [
  ["og-image-1200x630.png", 1200, 630],
  ["x-card-1200x600.png", 1200, 600],
  ["github-social-1280x640.png", 1280, 640],
];

const tmp = mkdtempSync(join(tmpdir(), "social-"));
const browser = await chromium.launch();
const out = join(repo, "design/brand/assets/social");
for (const [name, w, h] of jobs) {
  const page = await browser.newPage({ viewport: { width: w, height: h } });
  const file = join(tmp, name + ".html");
  writeFileSync(file, html(w, h));
  await page.goto(pathToFileURL(file).href);
  await page.evaluate(() => document.fonts.ready);
  await page.screenshot({ path: join(out, name) });
  await page.close();
}
await browser.close();

// The one link-preview card, in every place that serves it.
const og = join(out, "og-image-1200x630.png");
copyFileSync(og, join(repo, "apps/site/assets/brand/og-image.png"));
copyFileSync(og, join(repo, "apps/web/public/og-image.png"));
copyFileSync(og, join(repo, "apps/docs/static/img/social-card.png"));
execFileSync("magick", [og, "-quality", "88", join(repo, "apps/site/assets/brand/og-image.jpg")]);
console.log("social cards written");
