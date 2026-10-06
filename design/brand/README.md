# Poweur brand

The brand in one line: **a black glass P on violet night**. The mark is black
(or white on dark), and the app icon is the white P on violet. Colour comes from the ground it stands on, not from the mark itself.

- Tokens: [`tokens.css`](tokens.css) (CSS custom properties) and [`tokens.json`](tokens.json)
- Generator: [`palette.py`](palette.py) (`python3 palette.py` prints every scale and the contrast checks)
- Logo files: only the ones in use are kept, next to where they are used: the website in
  [`apps/site/assets/brand/`](../../apps/site/assets/brand/) (glass P, white P, horizontal lockups
  for dark grounds), the web app in [`apps/web/src/assets/brand/`](../../apps/web/src/assets/brand/)
  (flat and glass P, black and white lockups) and the docs in
  [`apps/docs/static/img/`](../../apps/docs/static/img/) (logo, favicon, social card). Icons, splash
  screens and social images are in [`assets/`](assets/). Regenerate any of them with
  [`scripts/`](scripts/)

## The mark

A **P** built from three pieces and a dot. Each piece is one pillar of Poweur:

| Piece | Pillar |
|-------|--------|
| Stem | **ID**: who you are |
| Upper bowl | **Messaging** |
| Lower bowl | **File sharing** |
| Dot | the separator of a domain name: `alice.poweur.net` |

| Variant | File | Use |
|---------|------|-----|
| **App icon** | `assets/icons/app-icon.svg` (full bleed) and `favicon.svg` (rounded) | The white flat P on violet-600. The icon everywhere something small and square is needed: web install icons, favicons, store listings, native launchers, avatars |
| Black glass | `black-glass/p-black-glass.svg` | Hero, splash, marketing: anywhere at 64 px or more |
| Black glass on glow | `black-glass/p-black-glass-on-glow.svg` | App icon tile, social cards |
| Flat black | `flat/p-black.svg` | UI at small sizes (favicon, nav, 16–48 px), print, embossing |
| Flat white | `flat/p-white.svg` | The same, on dark or brand-violet grounds |

Rules:

- The glass version uses SVG lighting filters. Below about 48 px the lighting turns to mush, so use flat
  there. Some design tools drop the filters on import; use the PNGs there.
- Never recolour the pieces separately. The three pillars are told apart by their shape, not by hue.
- Clear space around the mark is at least the width of the gap between the pieces (about 7% of the
  mark's height).
- Put the mark on white, `ink-50`, `violet-600`, `night` or the glow. Never put it on amber or teal.

## Colour, and why these colours

All scales are built in **OKLCH**, a perceptual colour space. Its lightness (L) is what the eye sees,
so every step of every scale has the same L across hues. That gives two properties:

- **Equal steps look equal.** `violet-600` and `indigo-600` read as the same weight.
- **Contrast is predictable.** Each step clears the same WCAG thresholds in every hue.

Each step's L is fixed: 0.975, 0.945, 0.89, 0.815, 0.72, 0.62, 0.53, 0.45, 0.37, 0.29, 0.22 for steps
50 to 950. Chroma (colourfulness) peaks at mid-lightness and tapers towards white and black. Colours
the screen cannot show (outside sRGB) are brought back into range by lowering chroma only, so
lightness and hue never drift.

### Roles

| Role | Hue (OKLCH h) | Why |
|------|---------------|-----|
| **Violet**: primary | 292° | The colour of the glow behind the glass P. The current app accents (`#5856D6` at 277°, `#8B5CF6` at 293°) already sit here, so the change is a refinement, not a rebrand |
| **Indigo**: secondary | 272° | Analogous, 20° cooler. It is the partner in the brand gradient and the "night" ground for dark heroes and icon tiles |
| **Amber**: accent | 72° | Split-complementary to violet: the one warm colour, for highlights, "new", callouts and marketing emphasis. Use sparingly |
| **Teal**: support | 190° | Info states, charts, links on night backgrounds. Pairs with amber as the second chart colour |
| **Ink**: neutral | 290°, chroma 0.006–0.012 | Greys faintly tinted towards violet, so neutrals sit with the brand instead of against it |

### Scales

| | 50 | 100 | 200 | 300 | 400 | 500 | 600 | 700 | 800 | 900 | 950 |
|---|---|---|---|---|---|---|---|---|---|---|---|
| violet | `#F7F5FE` | `#EDEAFF` | `#DBD5FF` | `#C3B7FF` | `#A890FE` | `#8B6BE9` | **`#724CCF`** | `#5B39A9` | `#452786` | `#301563` | `#1F0647` |
| indigo | `#F4F6FE` | `#E6ECFF` | `#CED9FF` | `#AEBFFE` | `#889EF8` | `#667CE3` | `#4D60C9` | `#3B4AA4` | `#2A3681` | `#1B2360` | **`#0E1344`** |
| amber | `#FFF5EA` | `#FFE9CF` | `#FFD29B` | `#EDB774` | `#D59740` | `#B67703` | `#935F00` | `#764B00` | `#593800` | `#3E2500` | `#281600` |
| teal | `#DDFFFC` | `#C1FAF5` | `#A7E9E4` | `#84D3CE` | `#53B8B2` | `#059A94` | `#027C77` | `#02635F` | `#024A47` | `#003331` | `#00201E` |
| ink | `#F6F6FB` | `#ECECF1` | `#DADADE` | `#C2C2C6` | `#A4A4A8` | `#86868A` | `#6C6B6F` | `#55545C` | `#3F3F46` | `#2B2A31` | `#1A1A20` |

**Poweur Violet** is `violet-600` `#724CCF`. **Night** is `indigo-950` `#0E1344`.

### Brand surfaces

| Token | Value | Use |
|-------|-------|-----|
| `--poweur-brand` | `violet-600` | Primary buttons, links, focus rings, selected states |
| `--poweur-brand-on-dark` | `violet-400` | The same roles in dark mode |
| `--poweur-gradient` | `violet-600` → `indigo-700`, 135° | Primary CTA, welcome tile, onboarding hero. White text passes AA along the whole gradient (5.75:1 or more) |
| `--poweur-glow` | radial `violet-500` → `indigo-800` → `indigo-950` | App icon tile, splash screen, landing hero, social cards. Decorative only: put text on its dark edge, not the centre |
| `--poweur-night` | `indigo-950` | Dark hero sections, the dark app-icon tile |

### Semantic and avatar colours

| | Light | Dark | Contrast (light) |
|---|---|---|---|
| success | `#11813C` | `#58CD78` | white on it 4.97:1 |
| danger | `#D73337` | `#FA6863` | white on it 4.75:1 |
| warning | `#EB9A04` (= amber) | `#FFB346` | `ink-950` on it 7.55:1 |

The **avatar palette** (initials on a coloured circle) is eight hues 45° apart, starting at brand violet,
all at L 0.60 and C 0.14. Every avatar has the same visual weight, and white initials reach at least
3.7:1 on all of them (AA for large text): `#836ECC` `#B35C9E` `#C65959` `#B36E02` `#838703` `#0D985D`
`#01929A` `#2785CE`. They replace the iOS system colours in `apps/web/src/lib/identity.ts`, where three
of the eight (`#FF9500`, `#34C759`, `#00C7BE`) give white initials only 2.1–2.2:1.

### Checked pairings (WCAG 2.x contrast)

| Pairing | Ratio | Passes |
|---------|-------|--------|
| white on `violet-600` (button) | 5.75 | AA |
| `violet-600` on white (link) | 5.75 | AA |
| `violet-700` on `violet-50` | 7.47 | AAA |
| `violet-400` on `ink-900` (dark accent) | 5.47 | AA |
| `violet-400` on black | 8.09 | AAA |
| white on `indigo-700` (gradient end) | 7.77 | AAA |
| white on `indigo-950` (night) | 14.4 | AAA |
| `violet-200` on `indigo-950` | 12.5 | AAA |
| `ink-600` on white (muted text) | 5.29 | AA |
| `ink-400` on `ink-950` (dark muted) | 6.97 | AA |
| `ink-950` on `amber-400` (badge) | 6.87 | AA |
| `violet-500` on white | 3.91 | large text and UI only: not for body links |

## Applying it to the web app

The web app keeps its semantic token names (`accent`, `surface`, …). Only their values change. The
rollout is tracked in [EPIC-015 E15-T14](../../epics/EPIC-015-web-app-ux.md).

| Token (`apps/web/src/index.css`) | Now (light / dark) | Brand (light / dark) |
|---|---|---|
| `--color-accent` | `#5856D6` / `#6E6CF8` | `violet-600` `#724CCF` / `violet-400` `#A890FE` |
| `--color-accent-soft` | accent at 12% / 15% | the same alphas over the new accent |
| `--color-accent-2` | `#8B5CF6` | `indigo-700` `#3B4AA4` (the gradient partner) |
| `--color-bg` | `#F2F2F7` / `#000000` | `ink-50` `#F6F6FB` / `#000000` (kept: true black suits OLED) |
| `--color-surface` | `#FFFFFF` / `#1C1C1E` | `#FFFFFF` / `ink-950` `#1A1A20` |
| `--color-surface-2` / `-3` | iOS greys | `ink-50` / `ink-100`, and `ink-900` / `ink-800` in dark |
| `--color-fg` | `#000000` / `#FFFFFF` | `ink-950` `#1A1A20` / `#FFFFFF` |
| `--color-muted` | `#636366` | `ink-600` `#6C6B6F` (dark: `ink-400`) |
| `--color-faint` | `#AEAEB2` | `ink-400` (dark: `ink-600`) |
| `--color-success` / `danger` / `warning` | iOS system | the semantic row above |
| `--shadow-accent` | indigo at 40% | `violet-600` at 35% |
| `<meta name="theme-color">` | `#FFFFFF` / `#000000` | `#FFFFFF` / `#000000` (unchanged; the chrome stays neutral) |

## Icons, splash and social images

[`scripts/icons.py`](scripts/icons.py) renders all of these from the P masters (`rsvg-convert` and
ImageMagick). Re-run it after any change to the mark or the glow.

| Where | What |
|-------|------|
| `apps/web/public/` | `favicon.svg` (violet tile, white P), `favicon.ico` (16/32/48), `apple-touch-icon.png` (180), `icon-192.png`, `icon-512.png`, `icon-maskable-512.png`, `manifest.webmanifest`, `og-image.png` (1200×630) |
| `apps/web/src/assets/brand/` | `p-glass.svg`, `p-black.svg`, `p-white.svg`, used by `ui/Logo` |
| iOS `AppIcon.appiconset` | 1024 px, no alpha: violet-600 with the flat white P (any and dark), white P on black (tinted) |
| iOS `Splash.imageset` | 2732 px: the glass P on the glow, aspect-filled |
| Android `mipmap-*` | `ic_launcher` (violet rounded square, white P), `ic_launcher_round`, adaptive violet `background` / white-P `foreground` / `monochrome` at 108 dp |
| Android `drawable-{port,land}-*` | splash at every density |
| [`assets/icons/`](assets/icons/) | the app icon and favicon at 16–1024 px, a maskable 512, SVG masters, `favicon.ico` |
| [`assets/social/`](assets/social/) | Open Graph 1200×630, X card 1200×600, X header 1500×500, LinkedIn banner 1584×396, GitHub social preview 1280×640, Facebook cover 1640×624, YouTube banner 2560×1440 (lockup inside the 1546×423 safe area), profile pictures 400 and 1024 (circle-safe) |

Proportions of the P inside each icon:
- Favicon: 70% of the height, flat white on `violet-600`. At 16 px only the silhouette survives, which is expected.
- iOS launcher: 64%, same mark. The squircle mask leaves enough margin at that size.
- Android legacy (rounded square and circle): 60%, shifted right so the ink's centroid — the stem pulls it left of the viewBox centre — sits in the middle of the tile.
- Android adaptive foreground and monochrome: 50%, with the same shift. The launcher masks this down to a 66 dp circle on the 108 dp canvas; 50% keeps the stem inside it. The background is solid `violet-600`.
- Web install icon (glass on the glow) and maskable: 58% and 46%.
- Splash: 14% of the square, 22% of the short side on Android.

## Other surfaces (follow-ups, same tokens)

- **Docs** (`apps/docs/src/css/custom.css`): Infima primary moves from Tailwind blue `#2563EB` to the violet
  scale (600 / 700 / 800 / 900 / 500 / 400 / 300 for primary, dark, darker, darkest, light, lighter,
  lightest). Replace the placeholder "e" logo in `static/img/logo.svg` with `flat/p-black.svg`.
- **OAuth bridge** (`apps/oauth/bridge/static/bridge.css`): its accent `#2F5BD3` / `#7D9BFF` becomes
  `violet-600` / `violet-400`, and its warm greys become ink.
- **Mobile** (EPIC-019): done, see "Icons, splash and social images" above.

## Type

- UI: the system stack already in the app (`-apple-system, BlinkMacSystemFont, "Segoe UI", …`). Keep it:
  it is fast and native in the Capacitor shell.
- Display and wordmark: **Sora** SemiBold (600), under the SIL Open Font License (self-hosted on the site in `apps/site/assets/fonts/`).
  Use it for headings on marketing pages and for the wordmark. Self-host it (no CDN, per E15-T6).

## Lockups

The files (`scripts/lockup.py` writes them to `design/brand/out/lockups/`; the ones in use are
copied into the apps, see the top of this page). The wordmark is Sora 600, kerned by the
font, with tracking −0.02 em, and **converted to outlines**. No font is needed to render these files.

- **Proportions:** the P is 1.45 times the height of the capital letters. The horizontal gap between
  the P and the text is 0.34 of the P's height.
- **"ID":** it sits 0.24 em after "Poweur", in `violet-600` on light grounds and `violet-300` on dark.
- **Stacked:** the text is 0.62 of the horizontal size, set 0.30 of the P's height below it.

| File pattern | Mark | Text |
|--------------|------|------|
| `poweur-{horizontal,stacked}-glass.svg` | black glass | ink + violet "ID" (light grounds) |
| `poweur-{horizontal,stacked}-on-dark.svg` | flat white | white + `violet-300` "ID" |
| `poweur-{horizontal,stacked}-black.svg` | flat black | all black (one colour, print) |
| `poweur-{horizontal,stacked}-white.svg` | flat white | all white (one colour) |

Each has a `-no-id` twin that reads just "Poweur", for the company or brand rather than the product.
All of them have transparent backgrounds.

To regenerate them, run [`scripts/lockup.py`](scripts/README.md). It shapes the text with HarfBuzz at
weight 600 and outlines it with fontTools. To use a different weight, change `WEIGHT`.
