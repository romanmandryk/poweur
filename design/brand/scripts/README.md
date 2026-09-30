# Logo generators

These scripts produce the logo files. `lockup.py` writes to `design/brand/out/` (not tracked); the
logos in use are copied into `apps/site/assets/brand/`, `apps/web/src/assets/brand/` and
`apps/docs/static/img/`.

- `pgeo3.py`: the geometry of the P, traced from the reference render. It unions and splits shapes
  with shapely.
- `blackglass.py`: the black glass SVG, built with SVG lighting filters. The `black_glass(px, uid)`
  function returns the whole `<svg>`.
- `lockup.py`: the P with the Sora wordmark, shaped with HarfBuzz and converted to outlines with
  fontTools. It writes `design/brand/out/lockups/`.
- `icons.py`: every icon, splash screen and social image. It writes into `apps/web`, `apps/mobile`
  and `design/brand/assets/` (see the brand README). Set `SORA_TTF` to include the social images,
  which carry the wordmark.

```sh
python3 -m venv .venv && .venv/bin/pip install shapely uharfbuzz fonttools
curl -L -o 'Sora[wght].ttf' 'https://github.com/google/fonts/raw/main/ofl/sora/Sora%5Bwght%5D.ttf'
.venv/bin/python lockup.py
```

`rsvg-convert` and ImageMagick (`magick`) are needed only for the preview renders. Sora is licensed
under the SIL Open Font License 1.1. It is not committed, because the outlined files do not need it.
