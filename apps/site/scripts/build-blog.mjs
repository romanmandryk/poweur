#!/usr/bin/env node
// Renders apps/site/content/blog/*.md into apps/site/blog/ (an index and one page per post).
// No dependencies. Posts with `draft: true` in their front matter are skipped unless you pass
// --drafts, so a production build never publishes one by accident.
//
//   node apps/site/scripts/build-blog.mjs --drafts     # then serve apps/site, open /blog/
//
// Output: blog/index.html, blog/<slug>/index.html and cover.svg, blog/posts.json, blog/feed.xml,
// and, for published posts only, the blog URLs in sitemap.xml. A build with no published post
// writes no blog at all. The site's nav and footer link to /blog/ unconditionally (assets/site.js),
// so keep at least one published post, or take those two links out (assets/chrome.js).
import { mkdirSync, readdirSync, readFileSync, realpathSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const site = join(dirname(fileURLToPath(import.meta.url)), "..");
const SITE_URL = "https://www.poweur.org";

export function escapeHtml(s) {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

export function parseFrontMatter(text) {
  const m = /^---\n([\s\S]*?)\n---\n?/.exec(text);
  if (!m) return { meta: {}, body: text };
  const meta = {};
  for (const line of m[1].split("\n")) {
    const kv = /^([A-Za-z_-]+):\s*(.*)$/.exec(line);
    if (!kv) continue;
    let v = kv[2].trim();
    if (/^".*"$/.test(v)) v = v.slice(1, -1);
    meta[kv[1]] = v === "true" ? true : v === "false" ? false : v;
  }
  return { meta, body: text.slice(m[0].length) };
}

// Inline markdown: code, links, bold, italic. Everything else is escaped.
export function inline(text) {
  const codes = [];
  let s = text.replace(/`([^`]+)`/g, (_, c) => `\u0000${codes.push(`<code>${escapeHtml(c)}</code>`) - 1}\u0000`);
  s = escapeHtml(s);
  s = s.replace(/\[([^\]]+)\]\(([^)\s]+)\)/g, (_, label, href) => {
    const ext = /^https?:/.test(href) ? ' rel="noopener"' : "";
    return `<a href="${href}"${ext}>${label}</a>`;
  });
  s = s.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>").replace(/(^|[^*])\*([^*\s][^*]*)\*/g, "$1<em>$2</em>");
  return s.replace(/\u0000(\d+)\u0000/g, (_, i) => codes[Number(i)]);
}

// Block markdown: headings, paragraphs, lists, block quotes, fenced code, rules.
export function markdown(src) {
  const lines = src.replace(/\r\n/g, "\n").split("\n");
  const out = [];
  let i = 0;
  const blank = (l) => l === undefined || l.trim() === "";
  while (i < lines.length) {
    const line = lines[i];
    if (blank(line)) { i++; continue; }
    const fence = /^```(\w*)/.exec(line);
    if (fence) {
      const code = [];
      for (i++; i < lines.length && !/^```/.test(lines[i]); i++) code.push(lines[i]);
      i++;
      out.push(`<pre><code>${escapeHtml(code.join("\n"))}</code></pre>`);
      continue;
    }
    const h = /^(#{1,4})\s+(.*)$/.exec(line);
    if (h) {
      const level = Math.max(2, h[1].length); // the page title is the only h1
      const text = h[2].trim();
      const id = text.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");
      out.push(`<h${level} id="${id}">${inline(text)}</h${level}>`);
      i++;
      continue;
    }
    if (/^(-{3,}|\*{3,})$/.test(line.trim())) { out.push("<hr>"); i++; continue; }
    if (/^>\s?/.test(line)) {
      const q = [];
      for (; i < lines.length && /^>\s?/.test(lines[i]); i++) q.push(lines[i].replace(/^>\s?/, ""));
      out.push(`<blockquote>${markdown(q.join("\n"))}</blockquote>`);
      continue;
    }
    const list = /^(\s*)([-*]|\d+\.)\s+/.exec(line);
    if (list) {
      const ordered = /\d/.test(list[2]);
      const items = [];
      for (; i < lines.length && !blank(lines[i]); i++) {
        const m = /^(\s*)([-*]|\d+\.)\s+(.*)$/.exec(lines[i]);
        if (m) items.push(m[3]);
        else if (items.length) items[items.length - 1] += " " + lines[i].trim();
      }
      const tag = ordered ? "ol" : "ul";
      out.push(`<${tag}>${items.map((t) => `<li>${inline(t)}</li>`).join("")}</${tag}>`);
      continue;
    }
    const para = [];
    for (; i < lines.length && !blank(lines[i]) && !/^(#{1,4}\s|```|>)/.test(lines[i]); i++) para.push(lines[i].trim());
    out.push(`<p>${inline(para.join(" "))}</p>`);
  }
  return out.join("\n");
}

function formatDate(iso) {
  const d = new Date(`${iso}T00:00:00Z`);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleDateString("en-GB", { day: "numeric", month: "long", year: "numeric", timeZone: "UTC" });
}


const PALETTES = [
  ["#1F0647", "#5B39A9", "#A890FE"], // violet
  ["#0E1344", "#3B4AA4", "#889EF8"], // indigo
  ["#1F0647", "#3B4AA4", "#84D3CE"], // violet to teal
  ["#301563", "#724CCF", "#EDB774"], // violet to amber
];

function hash(text) {
  let h = 2166136261;
  for (const c of text) h = Math.imul(h ^ c.charCodeAt(0), 16777619) >>> 0;
  return h;
}

// A cover for posts that name no image: brand colours and soft shapes, the same every build.
export function coverSvg(slug) {
  const h = hash(slug);
  const [a, b, c] = PALETTES[h % PALETTES.length];
  const circle = (i) => {
    const x = 120 + ((h >>> (i * 5)) % 960);
    const y = 60 + ((h >>> (i * 3 + 1)) % 510);
    const r = 90 + ((h >>> (i * 4)) % 220);
    return `<circle cx="${x}" cy="${y}" r="${r}" fill="${i % 2 ? c : b}" opacity="${i % 2 ? 0.28 : 0.55}"/>`;
  };
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1200 630" role="img" aria-hidden="true">
<defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="${a}"/><stop offset="1" stop-color="#07060B"/></linearGradient>
<filter id="b"><feGaussianBlur stdDeviation="46"/></filter></defs>
<rect width="1200" height="630" fill="url(#g)"/>
<g filter="url(#b)">${[1, 2, 3, 4].map(circle).join("")}</g>
<g fill="none" stroke="${c}" stroke-opacity=".35" stroke-width="2"><circle cx="${900 - (h % 200)}" cy="${300 + (h % 120)}" r="190"/><circle cx="${900 - (h % 200)}" cy="${300 + (h % 120)}" r="130"/><circle cx="${900 - (h % 200)}" cy="${300 + (h % 120)}" r="70"/></g>
</svg>
`;
}

export function readingMinutes(html) {
  const words = html.replace(/<[^>]+>/g, " ").split(/\s+/).filter(Boolean).length;
  return Math.max(1, Math.round(words / 200));
}

const coverOf = (p, rel) => (p.meta.image ? p.meta.image : `${rel}${p.slug}/cover.svg`);

function page({ root, path, title, description, ogType, body, head = "" }) {
  const url = `${SITE_URL}${path}`;
  return `<!doctype html>
<html lang="en" data-root="${root}">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>${escapeHtml(title)}</title>
  <meta name="description" content="${escapeHtml(description)}">
  <meta name="theme-color" content="#07060B">
  <meta name="color-scheme" content="dark">
  <style>html{background:#07060B;color:#F4F3F8}#nav:empty{min-height:65px}</style>
  <link rel="canonical" href="${url}">
  <meta property="og:type" content="${ogType}">
  <meta property="og:site_name" content="Poweur">
  <meta property="og:url" content="${url}">
  <meta property="og:title" content="${escapeHtml(title)}">
  <meta property="og:description" content="${escapeHtml(description)}">
  <meta property="og:image" content="${SITE_URL}/assets/brand/og-image.jpg">
  <meta name="twitter:card" content="summary_large_image">
  <link rel="apple-touch-icon" href="${root}assets/brand/apple-touch-icon.png">
  <link rel="icon" href="${root}assets/brand/favicon.svg" type="image/svg+xml">
  <link rel="alternate" type="application/rss+xml" title="Poweur blog" href="${SITE_URL}/blog/feed.xml">
  <link rel="preload" href="${root}assets/fonts/sora-latin-600-normal.woff2" as="font" type="font/woff2" crossorigin>
  <link rel="preload" href="${root}assets/fonts/inter-latin-400-normal.woff2" as="font" type="font/woff2" crossorigin>
  <link rel="stylesheet" href="${root}assets/site.css">
${head}  <script src="${root}assets/faro.js" defer></script>
</head>
<body>
<header id="nav"></header>

<main>
${body}
</main>

<footer id="footer"></footer>
<script src="${root}assets/chrome.js"></script>
<script src="${root}assets/site.js"></script>
</body>
</html>
`;
}

export function renderPost(post) {
  const { meta, html, slug } = post;
  const draft = meta.draft ? `\n      <p class="legal-meta"><strong>Draft:</strong> not published. Visible only in a local build.</p>` : "";
  const cover = coverOf(post, "../");
  const body = `  <section class="page-hero">
    <div class="wrap legal-wrap">
      <div class="eyebrow"><a href="../">Blog</a></div>
      <h1 class="h2 gradient-text">${escapeHtml(meta.title)}</h1>
      <p class="legal-meta">${escapeHtml(meta.author ?? "Poweur")} · ${formatDate(meta.date)} · ${readingMinutes(html)} min read</p>${draft}
    </div>
  </section>

  <section style="padding-top:0">
    <div class="wrap legal-wrap">
      <img class="post-cover" src="${cover}" alt="" width="1200" height="630">
    </div>
    <div class="wrap legal-wrap prose post-body">
${html}
      <div class="post-end">
        <p><strong>Poweur is open source and pre-1.0.</strong> Claim a name, try it, or read the code.</p>
        <p><a class="btn btn-sm btn-primary" href="../../index.html#claim">Claim your ID</a> <a class="btn btn-sm btn-ghost" href="../">More posts</a></p>
      </div>
    </div>
  </section>`;
  const head = `  <meta property="article:published_time" content="${meta.date}">\n  <meta property="og:image:alt" content="${escapeHtml(meta.title)}">\n`;
  return page({ root: "../../", path: `/blog/${slug}/`, title: `${meta.title} · Poweur`, description: meta.description ?? "", ogType: "article", body, head });
}

function card(p, featured) {
  return `      <a class="post-card${featured ? " featured" : ""}" href="${p.slug}/">
        <img src="${coverOf(p, "")}" alt="" loading="${featured ? "eager" : "lazy"}" width="1200" height="630">
        <div class="post-card-body">
          <p class="post-meta">${formatDate(p.meta.date)} · ${readingMinutes(p.html)} min read${p.meta.draft ? " · draft" : ""}</p>
          <h2>${escapeHtml(p.meta.title)}</h2>
          <p>${escapeHtml(p.meta.description ?? "")}</p>
          <span class="post-more">Read the post →</span>
        </div>
      </a>`;
}

export function renderIndex(posts) {
  const [first, ...rest] = posts;
  const body = `  <section class="page-hero">
    <div class="wrap">
      <div class="eyebrow">Blog</div>
      <h1 class="h2 gradient-text">Notes from building Poweur</h1>
      <p class="legal-meta">Identity, messaging and files that people and agents share. <a href="feed.xml">RSS</a></p>
    </div>
  </section>

  <section style="padding-top:0">
    <div class="wrap">
${first ? card(first, true) : "      <p>No posts yet.</p>"}
${rest.length ? `      <div class="post-grid">\n${rest.map((p) => card(p, false)).join("\n")}\n      </div>` : ""}
    </div>
  </section>`;
  return page({ root: "../", path: "/blog/", title: "Blog · Poweur", description: "Notes from building Poweur: identity, messaging and files that people and agents share.", ogType: "website", body });
}

export function renderFeed(posts) {
  const items = posts
    .map((p) => `  <item><title>${escapeHtml(p.meta.title)}</title><link>${SITE_URL}/blog/${p.slug}/</link><guid>${SITE_URL}/blog/${p.slug}/</guid><pubDate>${new Date(`${p.meta.date}T08:00:00Z`).toUTCString()}</pubDate><description>${escapeHtml(p.meta.description ?? "")}</description></item>`)
    .join("\n");
  return `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel>
  <title>Poweur blog</title><link>${SITE_URL}/blog/</link><description>Notes from building Poweur.</description>
${items}
</channel></rss>
`;
}

export function renderSitemap(xml, posts) {
  const urls = posts.length ? [`${SITE_URL}/blog/`, ...posts.map((p) => `${SITE_URL}/blog/${p.slug}/`)] : [];
  const block = `<!-- blog -->${urls.map((u) => `\n  <url><loc>${u}</loc></url>`).join("")}${urls.length ? "\n  " : ""}<!-- /blog -->`;
  return xml.replace(/<!-- blog -->[\s\S]*?<!-- \/blog -->/, block);
}

export function loadPosts(dir, { drafts = false } = {}) {
  return readdirSync(dir)
    .filter((f) => f.endsWith(".md"))
    .map((f) => {
      const { meta, body } = parseFrontMatter(readFileSync(join(dir, f), "utf8"));
      // The title comes from the front matter, so a leading "# Title" in the body is dropped.
      const html = markdown(body.replace(/^\s*#\s+.*\n/, ""));
      return { slug: f.replace(/\.md$/, ""), meta, html };
    })
    .filter((p) => drafts || !p.meta.draft)
    .sort((a, b) => String(b.meta.date).localeCompare(String(a.meta.date)));
}

if (realpathSync(process.argv[1]) === realpathSync(fileURLToPath(import.meta.url))) {
  const drafts = process.argv.includes("--drafts");
  const posts = loadPosts(join(site, "content", "blog"), { drafts });
  const outDir = join(site, "blog");
  rmSync(outDir, { recursive: true, force: true });
  if (posts.length) {
    mkdirSync(outDir, { recursive: true });
    writeFileSync(join(outDir, "index.html"), renderIndex(posts));
    writeFileSync(join(outDir, "feed.xml"), renderFeed(posts));
    writeFileSync(join(outDir, "posts.json"), JSON.stringify(posts.map((p) => ({ slug: p.slug, title: p.meta.title, date: p.meta.date })), null, 2) + "\n");
    for (const p of posts) {
      mkdirSync(join(outDir, p.slug), { recursive: true });
      writeFileSync(join(outDir, p.slug, "index.html"), renderPost(p));
      if (!p.meta.image) writeFileSync(join(outDir, p.slug, "cover.svg"), coverSvg(p.slug));
    }
  }
  if (!drafts) {
    const map = join(site, "sitemap.xml");
    writeFileSync(map, renderSitemap(readFileSync(map, "utf8"), posts));
  }
  console.log(`blog: ${posts.length} post(s)${drafts ? " (drafts included)" : ""}${posts.length ? " → apps/site/blog/" : ", nothing to publish"}`);
}
