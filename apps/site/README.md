# poweur.org — marketing site

Static HTML/CSS/JS, no build step. Deploy the folder as-is to any static host.

```bash
python3 -m http.server 4321 --directory apps/site
```

(or the `site` entry in `.claude/launch.json`).

| File | What |
|------|------|
| `index.html` | Landing page: hero, protocols strip, three primitives, pyramid, comparison, use cases, apps, agents, hosting, status, blog teaser, CTA |
| `architecture.html` | Topology diagram + four flows (hello, pizza order, device enrollment, folder sharing) |
| `blog/index.html` | Blog index (placeholder posts) |
| `assets/site.js` | Shared nav + footer (edit links in `LINKS`), scroll reveals |
| `assets/seq.js` | Renders sequence diagrams from inline JSON (`<figure data-seq>`) |
| `assets/brand/` | Copied from `design/` — regenerate there, not here |
| `social/memes.md` | Social copy and meme drafts (not linked from the site) |

## Placeholders to replace before launch

- Anything with class `ph` (striped box with a label): product shots, icons, illustrations, blog covers.
- Hero video: drop an original or licensed loop at `assets/hero.mp4` and uncomment the `<video>` in `index.html`.
- X / LinkedIn links in `assets/site.js` (`#` until the accounts exist).
- The SDK snippet in the Apps section shows the intended API shape, not the current `@poweur/client` surface.
- Fonts load from Google Fonts; self-host Sora/Inter per `design/brand/README.md`.
