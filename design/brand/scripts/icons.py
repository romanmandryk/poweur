"""Render every icon, splash and social image from the P masters.

Writes into the repo:
  apps/web/public/                 favicon.svg/.ico, apple-touch-icon, PWA icons, manifest, og-image
  apps/web/src/assets/brand/       the P as SVG for in-app use (glass, flat black, flat white)
  apps/mobile/ios/.../AppIcon      1024 icon (+ dark and tinted variants), Splash 2732
  apps/mobile/android/.../res      launcher (legacy, round, adaptive fg/bg/monochrome), splash
  design/brand/assets/             the same icons at common sizes, social images and banners

Needs rsvg-convert and ImageMagick (`magick`). The social images carry the wordmark and need Sora
(see README.md); everything else needs only the scripts next to this file.
"""
import json, os, re, shutil, subprocess, tempfile

from blackglass import black_glass
from pgeo3 import paths, C, RD

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.normpath(os.path.join(HERE, "..", "..", ".."))
WEB = os.path.join(REPO, "apps/web")
IOS = os.path.join(REPO, "apps/mobile/ios/App/App/Assets.xcassets")
ANDROID = os.path.join(REPO, "apps/mobile/android/app/src/main/res")
ASSETS = os.path.join(REPO, "design/brand/assets")
S = json.load(open(os.path.join(HERE, "..", "tokens.json")))["scales"]
TMP = tempfile.mkdtemp()

VB_X, VB_Y, VB_W, VB_H = 300, 70, 600, 760      # the P's viewBox
INKED = 635                                     # the P's drawn height inside it
P = paths()
FLAT = "".join(f'<path d="{P[k]}"/>' for k in ("stem", "upper", "lower")) + f'<circle cx="{C[0]}" cy="{C[1]}" r="{RD}"/>'


def glow(uid, cx="50%", cy="44%"):
    return (f'<radialGradient id="glow{uid}" cx="{cx}" cy="{cy}" r="75%">'
            f'<stop offset="0" stop-color="{S["violet"]["500"]}"/>'
            f'<stop offset="0.6" stop-color="{S["indigo"]["800"]}"/>'
            f'<stop offset="1" stop-color="{S["indigo"]["950"]}"/></radialGradient>')


def mark(kind, cx, cy, inked_h, uid, fill="#000000"):
    """The P centred on (cx, cy) with its drawn height = inked_h."""
    h = inked_h * VB_H / INKED
    w = h * VB_W / VB_H
    x, y = cx - w / 2, cy - h / 2
    if kind == "glass":
        inner = black_glass(600, uid)
        inner = inner[inner.index(">") + 1:inner.rindex("</svg>")]
    else:
        inner = f'<g fill="{fill}">{FLAT}</g>'
    return f'<svg x="{x:.2f}" y="{y:.2f}" width="{w:.2f}" height="{h:.2f}" viewBox="{VB_X} {VB_Y} {VB_W} {VB_H}">{inner}</svg>'


def doc(w, h, body, defs=""):
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {w} {h}" width="{w}" height="{h}">'
            f'<defs>{defs}</defs>{body}</svg>\n')


def render(svg, out, w=None, h=None, flatten=None):
    src = os.path.join(TMP, "r.svg")
    open(src, "w").write(svg)
    os.makedirs(os.path.dirname(out), exist_ok=True)
    args = ["rsvg-convert", src, "-o", out]
    if w: args += ["-w", str(w)]
    if h: args += ["-h", str(h)]
    subprocess.run(args, check=True)
    if flatten:  # iOS rejects an app icon with an alpha channel
        subprocess.run(["magick", out, "-background", flatten, "-alpha", "remove", "-alpha", "off", out], check=True)


# ---- masters (1024 units; rendered at any size) -------------------------------------------------
N = 1024
def app_icon(scale=0.58, uid="a"):
    """Glass P on the glow. Web install icons and social profiles; native launchers use flat_violet."""
    return doc(N, N, f'<rect width="{N}" height="{N}" fill="url(#glowa)"/>' + mark("glass", N / 2, N / 2, scale * N, uid), glow("a"))

def app_icon_tinted():
    """iOS 18 tinted: a grayscale mark on black; the system applies the tint."""
    return doc(N, N, f'<rect width="{N}" height="{N}" fill="#000000"/>' + mark("flat", N / 2, N / 2, 0.58 * N, "t", "#FFFFFF"))

# The stem is solid and the bowl is open, so the ink's centroid sits 40 viewBox
# units left of the viewBox centre. Centring the viewBox makes the P look
# shifted left, and on a round Android mask the stem then kisses the edge.
OPTICAL_LEFT = 40.27
# iOS masks a squircle, so the P can stay close to the favicon's 70%. Android
# legacy icons are the rounded bitmap itself; adaptive icons are masked again
# down to a 66dp circle on a 108dp canvas, which is what was clipping the stem.
IOS_ICON = 0.64
ANDROID_LEGACY = 0.60
ANDROID_ADAPTIVE = 0.50

def optical_dx(scale):
    """Canvas shift that puts the ink centroid on the centre."""
    return OPTICAL_LEFT * scale * N / INKED

def adaptive_fg():
    """White P on the 108dp canvas, inside Android's 66dp safe circle."""
    return doc(N, N, mark("flat", N / 2 + optical_dx(ANDROID_ADAPTIVE), N / 2, ANDROID_ADAPTIVE * N, "f", "#FFFFFF"))

def adaptive_bg():
    return doc(N, N, f'<rect width="{N}" height="{N}" fill="{S["violet"]["600"]}"/>')

def adaptive_mono():
    return doc(N, N, mark("flat", N / 2 + optical_dx(ANDROID_ADAPTIVE), N / 2, ANDROID_ADAPTIVE * N, "m", "#FFFFFF"))

def maskable():
    """PWA maskable: safe zone is the central 80% circle."""
    return doc(N, N, f'<rect width="{N}" height="{N}" fill="url(#glowa)"/>' + mark("glass", N / 2, N / 2, 0.46 * N, "k"), glow("a"))

def favicon_svg():
    """Violet tile + flat white P: legible at 16 px on light and dark tabs alike."""
    return flat_violet(clip="rounded")

def flat_violet(scale=0.70, uid="v", clip=None, dx=0):
    """The favicon's mark: a violet-600 field and the flat white P.

    Full bleed for iOS, which masks the squircle itself. Legacy Android bakes
    the same rounded tile or a circle, because those bitmaps are the icon.
    """
    inner = (f'<rect width="{N}" height="{N}" fill="{S["violet"]["600"]}"/>'
             + mark("flat", N / 2 + dx, N / 2, scale * N, uid, "#FFFFFF"))
    if clip == "rounded":
        return doc(N, N, f'<clipPath id="rc{uid}"><rect width="{N}" height="{N}" rx="{0.22 * N}"/></clipPath>'
                   f'<g clip-path="url(#rc{uid})">{inner}</g>')
    if clip == "circle":
        return doc(N, N, f'<clipPath id="cc{uid}"><circle cx="{N / 2}" cy="{N / 2}" r="{N / 2}"/></clipPath>'
                   f'<g clip-path="url(#cc{uid})">{inner}</g>')
    return doc(N, N, inner)

def splash(w, h, uid="s", frac=0.16):
    return doc(w, h, f'<rect width="{w}" height="{h}" fill="url(#glow{uid})"/>' + mark("glass", w / 2, h / 2, frac * min(w, h), uid), glow(uid, cy="46%"))


def flat_mark(fill):
    return f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="{VB_X} {VB_Y} {VB_W} {VB_H}"><g fill="{fill}">{FLAT}</g></svg>\n'

def glass_mark():
    s = black_glass(600, "")
    return re.sub(r' width="\d+" height="\d+"', "", s, count=1)


def web():
    pub = os.path.join(WEB, "public")
    os.makedirs(pub, exist_ok=True)
    open(os.path.join(pub, "favicon.svg"), "w").write(favicon_svg().replace(f' width="{N}" height="{N}"', "", 1))
    for s in (16, 32, 48):
        render(favicon_svg(), os.path.join(TMP, f"fav{s}.png"), s, s)
    subprocess.run(["magick", *[os.path.join(TMP, f"fav{s}.png") for s in (16, 32, 48)], os.path.join(pub, "favicon.ico")], check=True)
    render(app_icon(), os.path.join(pub, "apple-touch-icon.png"), 180, 180, flatten=S["indigo"]["950"])
    render(app_icon(), os.path.join(pub, "icon-192.png"), 192, 192)
    render(app_icon(), os.path.join(pub, "icon-512.png"), 512, 512)
    render(maskable(), os.path.join(pub, "icon-maskable-512.png"), 512, 512)
    manifest = {
        "name": "Poweur ID", "short_name": "Poweur", "id": ".", "start_url": ".", "scope": ".",
        "display": "standalone", "background_color": "#FFFFFF", "theme_color": "#FFFFFF",
        "icons": [
            {"src": "icon-192.png", "sizes": "192x192", "type": "image/png", "purpose": "any"},
            {"src": "icon-512.png", "sizes": "512x512", "type": "image/png", "purpose": "any"},
            {"src": "icon-maskable-512.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable"},
            {"src": "favicon.svg", "sizes": "any", "type": "image/svg+xml", "purpose": "any"},
        ],
    }
    json.dump(manifest, open(os.path.join(pub, "manifest.webmanifest"), "w"), indent=2)
    brand = os.path.join(WEB, "src/assets/brand")
    os.makedirs(brand, exist_ok=True)
    open(os.path.join(brand, "p-glass.svg"), "w").write(glass_mark())
    open(os.path.join(brand, "p-black.svg"), "w").write(flat_mark("#000000"))
    open(os.path.join(brand, "p-white.svg"), "w").write(flat_mark("#FFFFFF"))


def ios():
    icon = os.path.join(IOS, "AppIcon.appiconset")
    for f in os.listdir(icon):
        if f.endswith(".png"): os.remove(os.path.join(icon, f))
    violet = S["violet"]["600"]
    render(flat_violet(scale=IOS_ICON), os.path.join(icon, "AppIcon-1024.png"), 1024, 1024, flatten=violet)
    render(flat_violet(scale=IOS_ICON, uid="d"), os.path.join(icon, "AppIcon-1024-dark.png"), 1024, 1024, flatten=violet)
    render(app_icon_tinted(), os.path.join(icon, "AppIcon-1024-tinted.png"), 1024, 1024, flatten="#000000")
    entry = lambda f, appearance=None: {**({"appearances": [{"appearance": "luminosity", "value": appearance}]} if appearance else {}),
                                        "filename": f, "idiom": "universal", "platform": "ios", "size": "1024x1024"}
    json.dump({"images": [entry("AppIcon-1024.png"), entry("AppIcon-1024-dark.png", "dark"), entry("AppIcon-1024-tinted.png", "tinted")],
               "info": {"author": "xcode", "version": 1}}, open(os.path.join(icon, "Contents.json"), "w"), indent=2)
    sp = os.path.join(IOS, "Splash.imageset")
    for f in ("splash-2732x2732.png", "splash-2732x2732-1.png", "splash-2732x2732-2.png"):
        render(splash(2732, 2732, frac=0.14), os.path.join(sp, f), flatten=S["indigo"]["950"])


DENSITY = {"mdpi": 1, "hdpi": 1.5, "xhdpi": 2, "xxhdpi": 3, "xxxhdpi": 4}
def android():
    for d, k in DENSITY.items():
        m = os.path.join(ANDROID, f"mipmap-{d}")
        dx = optical_dx(ANDROID_LEGACY)
        render(flat_violet(scale=ANDROID_LEGACY, clip="rounded", dx=dx, uid=f"r{d}"), os.path.join(m, "ic_launcher.png"), round(48 * k), round(48 * k))
        render(flat_violet(scale=ANDROID_LEGACY, clip="circle", dx=dx, uid=f"o{d}"), os.path.join(m, "ic_launcher_round.png"), round(48 * k), round(48 * k))
        render(adaptive_fg(), os.path.join(m, "ic_launcher_foreground.png"), round(108 * k), round(108 * k))
        render(adaptive_bg(), os.path.join(m, "ic_launcher_background.png"), round(108 * k), round(108 * k))
        render(adaptive_mono(), os.path.join(m, "ic_launcher_monochrome.png"), round(108 * k), round(108 * k))
    xml = ('<?xml version="1.0" encoding="utf-8"?>\n'
           '<adaptive-icon xmlns:android="http://schemas.android.com/apk/res/android">\n'
           '    <background android:drawable="@mipmap/ic_launcher_background"/>\n'
           '    <foreground android:drawable="@mipmap/ic_launcher_foreground"/>\n'
           '    <monochrome android:drawable="@mipmap/ic_launcher_monochrome"/>\n'
           '</adaptive-icon>\n')
    for name in ("ic_launcher.xml", "ic_launcher_round.xml"):
        open(os.path.join(ANDROID, "mipmap-anydpi-v26", name), "w").write(xml)
    open(os.path.join(ANDROID, "values", "ic_launcher_background.xml"), "w").write(
        '<?xml version="1.0" encoding="utf-8"?>\n<resources>\n'
        f'    <color name="ic_launcher_background">{S["violet"]["600"]}</color>\n</resources>\n')
    for folder in os.listdir(ANDROID):
        f = os.path.join(ANDROID, folder, "splash.png")
        if folder.startswith("drawable") and os.path.exists(f):
            w, h = map(int, subprocess.run(["magick", "identify", "-format", "%w %h", f], capture_output=True, text=True, check=True).stdout.split())
            render(splash(w, h, uid="s", frac=0.22), f, flatten=S["indigo"]["950"])


def social():
    """Banners and share images with the wordmark (needs Sora; see README.md)."""
    try:
        from lockup import shape, CAP, UPM
    except Exception as e:  # font missing
        print("skipping social images:", e)
        return
    V300 = S["violet"]["300"]
    def lockup_on_glow(w, h, cap_frac, uid, tagline=None, show_id=True):
        capH = cap_frac * h
        mark_h = capH * 1.45
        size = capH * UPM / CAP
        d1, w1 = shape("Poweur", size, -0.02)
        gap_id = 0.24 * size
        d2, w2 = shape("ID", size, -0.02) if show_id else ("", 0)
        text_w = w1 + (gap_id + w2 if show_id else 0)
        mark_w = mark_h * 453 / 635
        gap = 0.34 * mark_h
        total = mark_w + gap + text_w
        x0 = (w - total) / 2
        cy = h / 2 - (0.06 * h if tagline else 0)
        base = cy + capH / 2
        body = (f'<rect width="{w}" height="{h}" fill="url(#glow{uid})"/>'
                + mark("glass", x0 + mark_w / 2, cy, mark_h, uid)
                + f'<path transform="translate({x0 + mark_w + gap:.1f} {base:.1f})" fill="#FFFFFF" d="{d1}"/>')
        if show_id:
            body += f'<path transform="translate({x0 + mark_w + gap + w1 + gap_id:.1f} {base:.1f})" fill="{V300}" d="{d2}"/>'
        if tagline:
            ts = 0.045 * h
            dt, wt = shape(tagline, ts * UPM / CAP * 0.72, 0)
            body += f'<path transform="translate({(w - wt) / 2:.1f} {base + 0.16 * h:.1f})" fill="{S["violet"]["200"]}" d="{dt}"/>'
        return doc(w, h, body, glow(uid, cy="40%"))

    out = os.path.join(ASSETS, "social")
    tag = "Encrypted, identity-first messaging."
    jobs = {
        "og-image-1200x630.png": (1200, 630, 0.13, tag),
        "x-card-1200x600.png": (1200, 600, 0.13, tag),
        "x-header-1500x500.png": (1500, 500, 0.12, None),
        "linkedin-banner-1584x396.png": (1584, 396, 0.13, None),
        "github-social-1280x640.png": (1280, 640, 0.13, tag),
        "facebook-cover-1640x624.png": (1640, 624, 0.12, None),
        "youtube-banner-2560x1440.png": (2560, 1440, 0.06, None),   # 1546x423 centre is the safe area
    }
    for name, (w, h, cap, tl) in jobs.items():
        render(lockup_on_glow(w, h, cap, "g", tl), os.path.join(out, name), flatten=S["indigo"]["950"])
    shutil.copy(os.path.join(out, "og-image-1200x630.png"), os.path.join(WEB, "public", "og-image.png"))
    for s in (400, 1024):  # profile pictures: circle-safe
        render(app_icon(scale=0.5), os.path.join(out, f"profile-{s}.png"), s, s, flatten=S["indigo"]["950"])


def library():
    """The icons again at common sizes, for anything that is not the web app or the shell."""
    out = os.path.join(ASSETS, "icons")
    for s in (16, 32, 48, 64, 128, 180, 192, 256, 512, 1024):
        render(app_icon(), os.path.join(out, f"app-icon-{s}.png"), s, s)
        render(favicon_svg(), os.path.join(out, f"favicon-{s}.png"), s, s)
    render(maskable(), os.path.join(out, "maskable-512.png"), 512, 512)
    for name, fn in (("app-icon.svg", app_icon), ("favicon.svg", favicon_svg), ("maskable.svg", maskable)):
        open(os.path.join(out, name), "w").write(fn().replace(f' width="{N}" height="{N}"', "", 1))
    shutil.copy(os.path.join(WEB, "public", "favicon.ico"), os.path.join(out, "favicon.ico"))


if __name__ == "__main__":
    web(); ios(); android(); library(); social()
    print("done")
