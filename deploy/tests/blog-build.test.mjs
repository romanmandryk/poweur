import test from "node:test";
import assert from "node:assert/strict";
import { markdown, inline, parseFrontMatter, renderPost, renderIndex } from "../../apps/site/scripts/build-blog.mjs";

test("front matter is parsed and the body returned", () => {
  const { meta, body } = parseFrontMatter('---\ntitle: "A: b"\ndraft: true\n---\n\n# A\n\ntext\n');
  assert.equal(meta.title, "A: b");
  assert.equal(meta.draft, true);
  assert.match(body, /text/);
});

test("inline markdown escapes HTML and keeps code literal", () => {
  assert.equal(inline("a <b> **c** `<d>`"), "a &lt;b&gt; <strong>c</strong> <code>&lt;d&gt;</code>");
  assert.equal(inline("[x](https://e.org)"), '<a href="https://e.org" rel="noopener">x</a>');
});

test("block markdown: headings drop to h2, lists, quotes and fences", () => {
  const html = markdown("# T\n\n- a\n- b\n\n1. one\n\n> q\n\n```\n<x>\n```\n");
  assert.match(html, /<h2 id="t">T<\/h2>/);
  assert.match(html, /<ul><li>a<\/li><li>b<\/li><\/ul>/);
  assert.match(html, /<ol><li>one<\/li><\/ol>/);
  assert.match(html, /<blockquote><p>q<\/p><\/blockquote>/);
  assert.match(html, /<pre><code>&lt;x&gt;<\/code><\/pre>/);
});

test("a draft page says so and is canonical on www.poweur.org", () => {
  const post = { slug: "s", meta: { title: "T", description: "D", date: "2026-10-13", draft: true }, html: "<p>x</p>" };
  const page = renderPost(post);
  assert.match(page, /Draft:/);
  assert.match(page, /rel="canonical" href="https:\/\/www\.poweur\.org\/blog\/s\/"/);
  assert.match(renderIndex([post]), /href="s\/"/);
});

import { coverSvg, readingMinutes, renderFeed, renderSitemap } from "../../apps/site/scripts/build-blog.mjs";
import { readFileSync } from "node:fs";

test("covers are deterministic and differ per post", () => {
  assert.equal(coverSvg("a"), coverSvg("a"));
  assert.notEqual(coverSvg("a"), coverSvg("b"));
  assert.match(coverSvg("a"), /^<svg /);
});

test("reading time is at least a minute", () => {
  assert.equal(readingMinutes("<p>short</p>"), 1);
  assert.equal(readingMinutes(`<p>${"word ".repeat(650)}</p>`), 3);
});

test("the feed and the sitemap list published posts, and an empty list clears the sitemap block", () => {
  const post = { slug: "s", meta: { title: "T & U", description: "D", date: "2026-10-13" }, html: "" };
  assert.match(renderFeed([post]), /<title>T &amp; U<\/title>/);
  const xml = "<urlset>\n  <!-- blog --><!-- /blog -->\n</urlset>";
  assert.match(renderSitemap(xml, [post]), /<loc>https:\/\/www\.poweur\.org\/blog\/s\/<\/loc>/);
  assert.equal(renderSitemap(renderSitemap(xml, [post]), []), xml);
});

test("the deploy workflow builds the blog without drafts", () => {
  const wf = readFileSync(new URL("../../.github/workflows/deploy-web.yml", import.meta.url), "utf8");
  assert.match(wf, /node apps\/site\/scripts\/build-blog\.mjs\n/);
  assert.doesNotMatch(wf, /build-blog\.mjs --drafts/);
});
