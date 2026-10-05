# poweur.org — marketing site

Static HTML/CSS/JS, no build step. In production the docs (`apps/docs`) are built into this
tree at `/docs` by `.github/workflows/deploy-web.yml` (see `deploy/README.md`); locally, `/docs`
links 404 unless you copy `apps/docs/build` to `apps/site/docs` (gitignore it).

```bash
python3 -m http.server 4321 --directory apps/site
```

| File | What |
|------|------|
| `index.html` | Landing page: hero, protocols strip, three primitives, pyramid, comparison, use cases, apps, agents, hosting, CTA |
| `architecture.html` | Topology diagram + four flows (hello, pizza order, device enrollment, folder sharing) |
| `assets/site.js` | Shared nav + footer (edit links in `LINKS`), scroll reveals |
| `assets/seq.js` | Renders sequence diagrams from inline JSON (`<figure data-seq>`) |
| `assets/brand/` | Copied from `design/` — regenerate there, not here |
| `product-shot.html` | 1920×1080 canvas of the hero composition, for social/press |
| `assets/shots/` | Real web-app screenshots (WebP for the site, PNG sources) + exported `product-shot-1920x1080.png` |
| `404.html`, `robots.txt`, `sitemap.xml` | Served as-is; `404.html` is what Caddy returns for unknown paths |

## Performance and SEO

- Screenshots are served as WebP (`assets/shots/*.webp`, made from the PNG sources with
  `magick in.png -resize 1600x -strip -quality 80 -define webp:method=6 out.webp`); the PNGs stay in
  the repo as sources and are left out of the deploy. Give every `<img>` its `width` and `height`.
- Fonts are self-hosted and cached for a year; the HTML for five minutes (see the `website`
  snippet in `deploy/infra/caddy/Caddyfile`).
- Every page carries a canonical URL, Open Graph and Twitter tags; `robots.txt` and `sitemap.xml`
  list the pages (the docs ship their own sitemap). Add a new page to `sitemap.xml`.

## Regenerating the screenshots

The screens are the real web app, driven by Playwright against a local relay with
`alice.poweur.net`, `bob.poweur.net` and `carl.example.com` (see
`apps/web/test/shots/product-shots.spec.js`). DNS-over-HTTPS is blocked in that browser so the
real poweur.net records don't interfere.

```bash
pnpm web
cd apps/web && SHOTS_DIR=../site/assets/shots npx playwright test -c playwright.shots.config.js
python3 ../site/scripts/crop-cards.py   # use-case card crops
# with the site served on :4321:
npx playwright screenshot --viewport-size=1920,1080 --wait-for-timeout=1500 \
  http://localhost:4321/product-shot.html ../site/assets/shots/product-shot-1920x1080.png
```

Use-case cards: messaging, sharing and devices are real crops (`card-*-crop.png`); the rest are small
HTML scenes (`.art-*` in `site.css`) styled like the app. The terminal window is HTML text shaped like the `poweur` CLI's output, not a capture.
- Hero video: drop an original or licensed loop at `assets/hero.mp4` and uncomment the `<video>` in `index.html`.
- X / LinkedIn links in `assets/site.js` (`#` until the accounts exist).
- The SDK snippet in the Apps section shows the intended API shape, not the current `@poweur/client` surface.
- Fonts are self-hosted in `assets/fonts/` (Sora, Inter, JetBrains Mono, Latin subsets from Fontsource, SIL OFL 1.1; licences alongside). The site makes no third-party requests.
