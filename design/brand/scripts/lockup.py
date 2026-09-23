"""Outlined Sora lockups: the P (glass or flat) + 'Poweur ID', text converted to paths."""
import json, os, re, subprocess
import uharfbuzz as hb
from fontTools.ttLib import TTFont
from fontTools.varLib.instancer import instantiateVariableFont
from fontTools.pens.svgPathPen import SVGPathPen
from fontTools.pens.transformPen import TransformPen
from blackglass import black_glass
from pgeo3 import paths, C, RD

D = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(D, "..", "..", "claude", "lockups")
FONT = os.environ.get("SORA_TTF", os.path.join(D, "Sora[wght].ttf"))  # google/fonts ofl/sora, not committed
WEIGHT = 600
S = json.load(open(os.path.join(D, "..", "tokens.json")))["scales"]
INK, V600, V300 = S["ink"]["950"], S["violet"]["600"], S["violet"]["300"]

font_bytes = open(FONT, "rb").read()
face = hb.Face(font_bytes); hbfont = hb.Font(face); hbfont.set_variations({"wght": WEIGHT})
tt = instantiateVariableFont(TTFont(FONT), {"wght": WEIGHT})
UPM = tt["head"].unitsPerEm; CAP = tt["OS/2"].sCapHeight
gs = tt.getGlyphSet(); order = tt.getGlyphOrder()

def shape(text, size, tracking_em):
    """-> (svg path d, advance width) with baseline at y=0, x from 0."""
    buf = hb.Buffer(); buf.add_str(text); buf.guess_segment_properties()
    hb.shape(hbfont, buf, {"kern": True, "liga": True})
    k = size / UPM; x = 0; ds = []
    for info, pos in zip(buf.glyph_infos, buf.glyph_positions):
        name = order[info.codepoint]
        pen = SVGPathPen(gs)
        gs[name].draw(TransformPen(pen, (k, 0, 0, -k, x + pos.x_offset * k, -pos.y_offset * k)))
        if pen.getCommands(): ds.append(pen.getCommands())
        x += pos.x_advance * k + tracking_em * size
    return " ".join(ds), x - tracking_em * size

TOP, BOT, LEFT, RIGHT = 130, 765, 375, 828      # the P's inked box (pgeo3 units)
H = BOT - TOP
P = paths()
FLAT = "".join(f'<path d="{P[k]}"/>' for k in ("stem", "upper", "lower")) + f'<circle cx="{C[0]}" cy="{C[1]}" r="{RD}"/>'
def glass_inner(uid):
    s = black_glass(600, uid)
    return s[s.index(">") + 1:s.rindex("</svg>")]

def words(size, x, baseline, word_col, id_col, show_id):
    d1, w1 = shape("Poweur", size, -0.02)
    out = f'<path transform="translate({x:.1f} {baseline:.1f})" fill="{word_col}" d="{d1}"/>'
    w = w1
    if show_id:
        gap = 0.24 * size
        d2, w2 = shape("ID", size, -0.02)
        out += f'<path transform="translate({x + w1 + gap:.1f} {baseline:.1f})" fill="{id_col}" d="{d2}"/>'
        w = w1 + gap + w2
    return out, w

def lockup(kind, mark, word_col, id_col, show_id=True, bg=None, uid="l"):
    capH = H / 1.45
    if kind == "h":
        size = capH * UPM / CAP
        x = RIGHT + 0.34 * H
        base = (TOP + BOT) / 2 + capH / 2
        txt, w = words(size, x, base, word_col, id_col, show_id)
        pad = 0.18 * H
        vb = (LEFT - pad, TOP - pad, (x + w + pad) - (LEFT - pad), H + 2 * pad)
        mark_tx = ""
    else:
        size = 0.62 * capH * UPM / CAP
        _, w = words(size, 0, 0, word_col, id_col, show_id)
        cx = (LEFT + RIGHT) / 2
        x = cx - w / 2
        base = BOT + 0.30 * H + CAP * size / UPM
        txt, w = words(size, x, base, word_col, id_col, show_id)
        pad = 0.18 * H
        left = min(LEFT, x) - pad; right = max(RIGHT, x + w) + pad
        vb = (left, TOP - pad, right - left, (base + 0.05 * H + pad) - (TOP - pad))
    if mark == "glass": m = glass_inner(uid)
    else: m = f'<g fill="{mark}">{FLAT}</g>'
    rect = f'<rect x="{vb[0]:.1f}" y="{vb[1]:.1f}" width="{vb[2]:.1f}" height="{vb[3]:.1f}" fill="{bg}"/>' if bg else ""
    label = "Poweur ID" if show_id else "Poweur"
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="{vb[0]:.1f} {vb[1]:.1f} {vb[2]:.1f} {vb[3]:.1f}" role="img" aria-label="{label}">'
            f'<title>{label}</title>{rect}{m}{txt}</svg>\n')

if __name__ == "__main__":
    os.makedirs(OUT, exist_ok=True)
    files = {}
    for kind, kn in (("h", "horizontal"), ("s", "stacked")):
        for show_id, sfx in ((True, ""), (False, "-no-id")):
            files[f"poweur-{kn}{sfx}-glass.svg"] = lockup(kind, "glass", INK, V600, show_id, uid=kind)
            files[f"poweur-{kn}{sfx}-on-dark.svg"] = lockup(kind, "#FFFFFF", "#FFFFFF", V300, show_id)
            files[f"poweur-{kn}{sfx}-black.svg"] = lockup(kind, "#000000", "#000000", "#000000", show_id)
            files[f"poweur-{kn}{sfx}-white.svg"] = lockup(kind, "#FFFFFF", "#FFFFFF", "#FFFFFF", show_id)
    for n, s in files.items(): open(f"{OUT}/{n}", "w").write(s)
    import tempfile
    D = tempfile.mkdtemp()
    # preview renders
    prev = {"h-glass": lockup("h", "glass", INK, V600, True, "#FFFFFF", "p1"),
            "h-dark": lockup("h", "#FFFFFF", "#FFFFFF", V300, True, S["indigo"]["950"]),
            "s-glass": lockup("s", "glass", INK, V600, True, "#FFFFFF", "p2"),
            "s-dark": lockup("s", "#FFFFFF", "#FFFFFF", V300, True, S["indigo"]["950"])}
    for n, s in prev.items():
        open(f"{D}/prev-{n}.svg", "w").write(s)
        subprocess.run(["rsvg-convert", "-h", "220" if n.startswith("h") else "300", f"{D}/prev-{n}.svg", "-o", f"{D}/prev-{n}.png"], check=True)
    subprocess.run(["magick", f"{D}/prev-h-glass.png", f"{D}/prev-h-dark.png", "-append", f"{D}/prev-h.png"], check=True)
    subprocess.run(["magick", f"{D}/prev-s-glass.png", f"{D}/prev-s-dark.png", "+append", f"{D}/prev-s.png"], check=True)
    print(len(files), "files", "UPM", UPM, "cap", CAP)
