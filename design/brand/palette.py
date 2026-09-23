"""Brand palette in OKLCH: even-lightness tonal scales, sRGB gamut-mapped by chroma reduction, WCAG contrast checked."""
import math, json

def oklch_to_srgb(L, C, h):
    a, b = C * math.cos(math.radians(h)), C * math.sin(math.radians(h))
    l_ = L + 0.3963377774 * a + 0.2158037573 * b
    m_ = L - 0.1055613458 * a - 0.0638541728 * b
    s_ = L - 0.0894841775 * a - 1.2914855480 * b
    l, m, s = l_ ** 3, m_ ** 3, s_ ** 3
    r = 4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s
    g = -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s
    bb = -0.0041960863 * l - 0.7034186147 * m + 1.7076147010 * s
    return r, g, bb

def in_gamut(rgb): return all(-1e-4 <= c <= 1 + 1e-4 for c in rgb)
def enc(c):
    c = min(max(c, 0), 1)
    return 12.92 * c if c <= 0.0031308 else 1.055 * c ** (1 / 2.4) - 0.055

def to_hex(L, C, h):
    while not in_gamut(oklch_to_srgb(L, C, h)) and C > 0: C -= 0.001   # gamut map: keep L and h, drop chroma
    return "#" + "".join(f"{round(enc(c) * 255):02X}" for c in oklch_to_srgb(L, C, h)), round(C, 3)

def lum(hexc):
    rgb = [int(hexc[i:i + 2], 16) / 255 for i in (1, 3, 5)]
    lin = [c / 12.92 if c <= 0.04045 else ((c + 0.055) / 1.055) ** 2.4 for c in rgb]
    return 0.2126 * lin[0] + 0.7152 * lin[1] + 0.0722 * lin[2]
def contrast(a, b):
    la, lb = sorted([lum(a), lum(b)], reverse=True)
    return (la + 0.05) / (lb + 0.05)

STEPS = [50, 100, 200, 300, 400, 500, 600, 700, 800, 900, 950]
LIGHT = [0.975, 0.945, 0.890, 0.815, 0.720, 0.620, 0.530, 0.450, 0.370, 0.290, 0.220]
def scale(h, peak, shape=lambda L: 1 - abs(L - 0.56) * 1.35):
    return {s: to_hex(L, max(peak * shape(L), 0.012), h)[0] for s, L in zip(STEPS, LIGHT)}

HUES = {
 "violet": (292, 0.20),   # brand primary — the violet of the glow and the current accent-2
 "indigo": (272, 0.17),   # secondary — deep night backgrounds, gradient partner (analogous, -20°)
 "amber":  (72, 0.16),    # accent — split-complementary warm highlight (~180°+140° from violet)
 "teal":   (190, 0.12),   # support — info / links on dark, charts (triadic-ish)
}
out = {n: scale(h, c) for n, (h, c) in HUES.items()}
out["ink"] = {s: to_hex(L, 0.012 if L < 0.5 else 0.006, 290)[0] for s, L in zip(STEPS, LIGHT)}   # violet-tinted neutrals
sem = {"success": to_hex(0.53, 0.14, 150)[0], "danger": to_hex(0.58, 0.20, 25)[0], "warning": to_hex(0.75, 0.16, 72)[0]}
sem_dark = {"success": to_hex(0.76, 0.16, 150)[0], "danger": to_hex(0.70, 0.18, 25)[0], "warning": to_hex(0.82, 0.15, 72)[0]}
avatars = [to_hex(0.60, 0.14, h)[0] for h in (292, 337, 22, 67, 112, 157, 202, 247)]   # 8 hues, 45° apart, equal L and C

if __name__ == "__main__":
    for n, sc in out.items(): print(n.ljust(7), " ".join(f"{k}:{v}" for k, v in sc.items()))
    print("sem", sem, "dark", sem_dark); print("avatars", avatars)
    W, B = "#FFFFFF", "#000000"
    v = out["violet"]; i = out["indigo"]; k = out["ink"]
    checks = [
      ("white on violet-600 (button)", W, v[600]), ("violet-600 on white (link)", v[600], W),
      ("violet-700 on violet-50", v[700], v[50]), ("violet-300 on black (dark link)", v[300], B),
      ("violet-400 on ink-900 (dark accent)", v[400], k[900]), ("white on indigo-900 (night)", W, i[900]),
      ("ink-950 on amber-400 (badge)", k[950], out["amber"][400]), ("ink-600 on white (muted)", k[600], W),
      ("ink-400 on ink-950 (dark muted)", k[400], k[950]),
      ("white on success", W, sem["success"]), ("white on danger", W, sem["danger"]), ("ink-950 on warning", k[950], sem["warning"]),
    ] + [(f"white on avatar {a}", W, a) for a in avatars]
    for label, a, b in checks: print(f"{contrast(a, b):5.2f}  {label}")
    json.dump({"scales": out, "semantic": sem, "semanticDark": sem_dark, "avatars": avatars}, open("palette.json", "w"), indent=1)
