# Geometry traced from the reference render (image pixel units).
from shapely.geometry import box, Point
from shapely.ops import unary_union
C = (597, 364); R = 231; RC = 118; RD = 72; GAP = 42
def smooth(p, r=9):
    return p.buffer(-r, join_style="round", quad_segs=12).buffer(r, join_style="round", quad_segs=12)
def pieces():
    stem = box(375, 130, 485, 765)
    bowl = unary_union([box(527, C[1] - R, C[0], C[1] + R), Point(C).buffer(R, quad_segs=96)])
    bowl = bowl.intersection(box(527, 0, 2000, 2000)).difference(Point(C).buffer(RC, quad_segs=96))
    seam = box(C[0], C[1] - GAP / 2, 2000, C[1] + GAP / 2)
    upper, lower = sorted(bowl.difference(seam).geoms, key=lambda g: g.centroid.y)
    return {"stem": smooth(stem, 10), "upper": smooth(upper), "lower": smooth(lower)}
def d(poly):
    def ring(cs): return "M" + " L".join(f"{x:.1f} {y:.1f}" for x, y in list(cs)[:-1]) + " Z"
    poly = poly.simplify(0.25, preserve_topology=True)
    return " ".join([ring(poly.exterior.coords)] + [ring(i.coords) for i in poly.interiors])
def paths(): return {k: d(v) for k, v in pieces().items()}
