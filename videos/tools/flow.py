"""SVG frame writer for flow scenes (stdlib only).

A flow scene shows nodes from diagrams.yaml and, step by step, arrows that
draw themselves, carry a label beside them, pulse their ends, and turn grey once the
next step starts. Looping arrows keep dots moving until the scene ends.

render() writes one SVG per frame; FFmpeg turns them into a clip with
librsvg. Times are in seconds from the start of the scene.
"""
import math
import os
import re
from xml.sax.saxutils import escape

import common

NODE_R = 58          # radius of the white disc behind every node icon
ARROW_GAP = 8        # space between the disc and an arrow's start or end
DEFAULT_OFFSET = 11  # arrows run this far right of the line between nodes
GREY_FADE = 0.5      # seconds a finished arrow takes to turn grey
PULSE = 0.9          # seconds a node pulse lasts
DOT_SPACING = 70     # pixels between the dots of a looping arrow
DOT_SPEED = 170      # pixels per second
LABEL_GAP = 12       # space between an arrow's line and its label


def ease(p):
    p = max(0.0, min(1.0, p))
    return 4 * p ** 3 if p < 0.5 else 1 - (-2 * p + 2) ** 3 / 2


def ease_out(p):
    p = max(0.0, min(1.0, p))
    return 1 - (1 - p) ** 3


def hex_mix(a, b, f):
    """Mix colour a toward b by f (0..1)."""
    f = max(0.0, min(1.0, f))
    ca = [int(a[i:i + 2], 16) for i in (1, 3, 5)]
    cb = [int(b[i:i + 2], 16) for i in (1, 3, 5)]
    return "#" + "".join(f"{round(x + (y - x) * f):02X}" for x, y in zip(ca, cb))


_defs_cache = {}


def style_defs(cfg):
    """The <symbol> elements of the documentation diagram."""
    p = common.path(cfg["sources"]["diagram_style"])
    if p not in _defs_cache:
        with open(p, encoding="utf-8") as f:
            svg = f.read()
        syms = re.findall(r"<symbol\b.*?</symbol>", svg, re.S)
        _defs_cache[p] = "\n".join(syms)
    return _defs_cache[p]


def symbol_ids(cfg):
    return set(re.findall(r'<symbol id="([^"]+)"', style_defs(cfg)))


def text_width(s, size, bold=False):
    """A rough width estimate for Noto Sans, enough to place labels."""
    w = 0.0
    for ch in s:
        if ch in "il.,:;'|!":
            w += 0.27
        elif ch in "mwMW":
            w += 0.85
        elif ch.isupper():
            w += 0.66
        elif ch == " ":
            w += 0.26
        else:
            w += 0.55
    return w * size * (1.06 if bold else 1.0)


class Geometry:
    """An arrow as a polyline with its length and point lookup."""

    def __init__(self, pts):
        self.pts = pts
        self.seg = []
        total = 0.0
        for a, b in zip(pts, pts[1:]):
            d = math.dist(a, b)
            self.seg.append((total, d, a, b))
            total += d
        self.length = total

    def at(self, s):
        """Point and unit direction at distance s along the line."""
        s = max(0.0, min(self.length, s))
        for start, d, a, b in self.seg:
            if s <= start + d or (start, d, a, b) == self.seg[-1]:
                f = 0 if d == 0 else (s - start) / d
                dx, dy = (b[0] - a[0]) / (d or 1), (b[1] - a[1]) / (d or 1)
                return (a[0] + (b[0] - a[0]) * f, a[1] + (b[1] - a[1]) * f), (dx, dy)
        return self.pts[-1], (1, 0)

    def path(self):
        return "M" + " L".join(f"{x:.1f} {y:.1f}" for x, y in self.pts)


def arrow_geometry(arrow, nodes):
    """Polyline from the edge of 'from' to the edge of 'to', through the
    centre of 'through' if given, shifted right of the travel direction."""
    names = [arrow["from"]] + ([arrow["through"]] if arrow.get("through") else []) + [arrow["to"]]
    centres = [(nodes[n]["x"], nodes[n]["y"]) for n in names]
    off = arrow.get("offset", DEFAULT_OFFSET)
    shifted = []
    for i, c in enumerate(centres):
        normals = []
        if i > 0:
            normals.append(_normal(centres[i - 1], c))
        if i < len(centres) - 1:
            normals.append(_normal(c, centres[i + 1]))
        nx = sum(n[0] for n in normals) / len(normals)
        ny = sum(n[1] for n in normals) / len(normals)
        shifted.append((c[0] + nx * off, c[1] + ny * off))
    # Trim the ends to the node discs.
    r = NODE_R + ARROW_GAP
    a, b = shifted[0], shifted[1]
    d = math.dist(a, b)
    start = (a[0] + (b[0] - a[0]) / d * r, a[1] + (b[1] - a[1]) / d * r)
    a, b = shifted[-1], shifted[-2]
    d = math.dist(a, b)
    end = (a[0] + (b[0] - a[0]) / d * (r + 4), a[1] + (b[1] - a[1]) / d * (r + 4))
    return Geometry([start] + shifted[1:-1] + [end])


def _normal(a, b):
    dx, dy = b[0] - a[0], b[1] - a[1]
    d = math.hypot(dx, dy) or 1
    return (-dy / d, dx / d)


class Flow:
    """Everything needed to draw one flow scene at any time t."""

    def __init__(self, scene, timeline, total, lang, cfg, diagrams):
        self.cfg = cfg
        self.lang = lang
        self.total = total
        self.colors = cfg["colors"]
        self.fonts = cfg["fonts"]
        self.W, self.H = cfg["output"]["width"], cfg["output"]["height"]
        self.title = scene.get("title")
        master = cfg["master"]
        self.nodes = {}
        for name in scene.get("show") or []:
            n = diagrams["nodes"][name]
            self.nodes[name] = dict(n, label=common.localized(n.get("label"), lang, master) or "",
                                    sub=common.localized(n.get("sub"), lang, master) or "")
        t = cfg["timing"]
        outside = self.colors[diagrams.get("outside_color", "amber")]
        self.steps = []
        steps = common.steps_of(scene)
        for i, (st, (start, narr_end)) in enumerate(zip(steps, timeline)):
            next_start = timeline[i + 1][0] if i + 1 < len(timeline) else None
            ds = start + 0.15
            de = min(ds + t["arrow_max"], max(ds + t["arrow_min"], narr_end - t["arrow_end_before"]))
            arrows = []
            for a in st["arrows"]:
                color = self.colors[a["color"]] if a.get("color") else (
                    outside if a.get("via") else self.colors[diagrams["nodes"][a["from"]].get("color", "navy")])
                default_at = 0.25 if a.get("through") else 0.5
                arrows.append(dict(a, geom=arrow_geometry(a, diagrams["nodes"]), rgb=color,
                                   dashed=bool(a.get("via")), label_at=a.get("label_at", default_at)))
            self.steps.append(dict(start=start, narr_end=narr_end, next=next_start, ds=ds, de=de,
                                   arrows=arrows, highlight=st.get("highlight") or [],
                                   clear=bool(st.get("clear"))))
        self._place_labels()
        # A step with clear: true fades out everything drawn before it.
        self.cleared_at = {}
        for i, st in enumerate(self.steps):
            if st["clear"]:
                for j in range(i):
                    self.cleared_at.setdefault(j, st["start"])

    # -- drawing -------------------------------------------------------

    def frame(self, t):
        out = [f'<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" '
               f'width="{self.W}" height="{self.H}" viewBox="0 0 {self.W} {self.H}">',
               "<defs>", style_defs(self.cfg), "</defs>",
               f'<rect width="100%" height="100%" fill="{self.colors["background"]}"/>']
        if self.title:
            out.append(self._title())
        appear = {name: ease_out((t - 0.1 - 0.08 * i) / 0.45) for i, name in enumerate(self.nodes)}
        # Arrows below the nodes, labels above them.
        labels = []
        for i, st in enumerate(self.steps):
            for a in st["arrows"]:
                svg, label = self._arrow(i, st, a, t)
                out.append(svg)
                labels.append(label)
        for name, n in self.nodes.items():
            out.append(self._pulses(name, n, t))
            out.append(self._node(n, appear[name]))
        out.extend(labels)
        out.append("</svg>")
        return "\n".join(x for x in out if x)

    def _title(self):
        c, f = self.colors, self.fonts
        return (f'<use href="#buoy" xlink:href="#buoy" transform="translate(64 66) scale(1.5)"/>'
                f'<text x="100" y="78" font-family="{f["serif"]}" font-size="32" font-weight="600" '
                f'fill="{c["navy"]}">{escape(self.title)}</text>')

    def _node(self, n, a):
        if a <= 0:
            return ""
        c, f = self.colors, self.fonts
        s = 0.85 + 0.15 * a
        x, y = n["x"], n["y"]
        return (f'<g opacity="{a:.3f}" transform="translate({x} {y}) scale({s:.3f})">'
                f'<circle r="{NODE_R}" fill="{c["white"]}" stroke="{c["line"]}" stroke-width="2"/>'
                f'<use href="#{n["icon"]}" xlink:href="#{n["icon"]}" transform="scale({n.get("scale", 1)})"/>'
                f'<text y="{NODE_R + 30}" font-family="{f["sans"]}" font-size="21" font-weight="700" '
                f'fill="{c["navy"]}" text-anchor="middle">{escape(n["label"])}</text>'
                f'<text y="{NODE_R + 54}" font-family="{f["sans"]}" font-size="17" '
                f'fill="{c["grey_text"]}" text-anchor="middle">{escape(n["sub"])}</text></g>')

    def _pulses(self, name, n, t):
        rings = []
        for st in self.steps:
            for a in st["arrows"]:
                if a["from"] == name:
                    rings.append((st["start"], a["rgb"]))
                if a["to"] == name:
                    rings.append((st["de"] - 0.1, a["rgb"]))
            if name in st["highlight"]:
                rgb = self.colors[n.get("color", "navy")]
                rings.append((st["start"], rgb))
                rings.append((st["start"] + 0.6, rgb))
        out = []
        for t0, rgb in rings:
            p = (t - t0) / PULSE
            if 0 <= p <= 1:
                r = NODE_R + 4 + 34 * ease_out(p)
                out.append(f'<circle cx="{n["x"]}" cy="{n["y"]}" r="{r:.1f}" fill="none" stroke="{rgb}" '
                           f'stroke-width="{5 * (1 - p) + 1:.1f}" opacity="{0.7 * (1 - p):.3f}"/>')
        return "".join(out)

    def _arrow(self, i, st, a, t):
        if t < st["ds"]:
            return "", ""
        geom = a["geom"]
        p = ease((t - st["ds"]) / max(0.01, st["de"] - st["ds"]))
        grey = 0.0
        if st["next"] is not None and not a.get("loop"):
            grey = (t - st["next"]) / GREY_FADE
        grey = max(0.0, min(1.0, grey))
        fade = 1.0
        if i in self.cleared_at:
            fade = 1 - max(0.0, min(1.0, (t - self.cleared_at[i]) / 0.4))
        if fade <= 0:
            return "", ""
        rgb = hex_mix(a["rgb"], self.colors["grey"], grey)
        L = geom.length
        drawn = L * p
        dash = ' stroke-dasharray="10 9"' if a["dashed"] else ""
        out = [f'<g opacity="{fade:.3f}">']
        if a["dashed"]:
            # Dashed: clip the visible length with a mask-free trick, by
            # drawing only the polyline up to the tip.
            pts = self._partial(geom, drawn)
            out.append(f'<path d="{pts}" fill="none" stroke="{rgb}" stroke-width="4" '
                       f'stroke-linecap="round" stroke-linejoin="round"{dash}/>')
        else:
            out.append(f'<path d="{geom.path()}" fill="none" stroke="{rgb}" stroke-width="4" '
                       f'stroke-linecap="round" stroke-linejoin="round" '
                       f'stroke-dasharray="{L:.1f} {L + 10:.1f}" stroke-dashoffset="{L - drawn:.1f}"/>')
        tip, (dx, dy) = geom.at(drawn)
        if drawn > 4:
            out.append(self._head(tip, dx, dy, rgb))
        if a.get("loop") and p >= 1:
            out.append(self._dots(geom, t - st["de"], rgb))
        out.append("</g>")
        label = self._label(a, geom, p, grey, fade) if a.get("label") else ""
        return "".join(out), label

    @staticmethod
    def _partial(geom, drawn):
        pts = [geom.pts[0]]
        for start, d, a, b in geom.seg:
            if start + d <= drawn:
                pts.append(b)
            else:
                pts.append(geom.at(drawn)[0])
                break
        return "M" + " L".join(f"{x:.1f} {y:.1f}" for x, y in pts)

    @staticmethod
    def _head(tip, dx, dy, rgb):
        size = 17
        bx, by = tip[0] - dx * size, tip[1] - dy * size
        nx, ny = -dy * size * 0.55, dx * size * 0.55
        pts = f"{tip[0] + dx * 3:.1f},{tip[1] + dy * 3:.1f} {bx + nx:.1f},{by + ny:.1f} {bx - nx:.1f},{by - ny:.1f}"
        return f'<polygon points="{pts}" fill="{rgb}"/>'

    @staticmethod
    def _dots(geom, t, rgb):
        out = []
        shift = (t * DOT_SPEED) % DOT_SPACING
        s = shift
        while s < geom.length - 20:
            (x, y), _ = geom.at(s)
            out.append(f'<circle cx="{x:.1f}" cy="{y:.1f}" r="6.5" fill="{rgb}" stroke="#FFFFFF" stroke-width="2"/>')
            s += DOT_SPACING
        return "".join(out)

    # -- labels --------------------------------------------------------

    LABEL_SIZE, VIA_SIZE, LINE_SPACING = 19, 15, 3

    def _label_box(self, a):
        w = max(text_width(a["label"], self.LABEL_SIZE, True),
                text_width(a["via"], self.VIA_SIZE) if a.get("via") else 0)
        h = self.LABEL_SIZE + (self.LINE_SPACING + self.VIA_SIZE if a.get("via") else 0)
        return w, h

    def _obstacles(self):
        """Boxes (x0, y0, x1, y1) a label should not cover: node discs and
        their text, the title, and every arrow line of the scene."""
        boxes = []
        for n in self.nodes.values():
            x, y, r = n["x"], n["y"], NODE_R + 4
            boxes.append((x - r, y - r, x + r, y + r))
            tw = max(text_width(n["label"], 21, True), text_width(n["sub"], 17)) / 2 + 4
            boxes.append((x - tw, y + NODE_R + 10, x + tw, y + NODE_R + 60))
        if self.title:
            boxes.append((40, 30, 120 + text_width(self.title, 32, True), 100))
        for st in self.steps:
            for a in st["arrows"]:
                g = a["geom"]
                s = 0.0
                while s <= g.length:
                    (x, y), _ = g.at(s)
                    boxes.append((x - 4, y - 4, x + 4, y + 4))
                    s += 6
        return boxes

    def _place_labels(self):
        """Put every label beside its arrow where it covers the least: on
        the right of the direction of travel (the side the arrow is offset
        to) unless the left is clearer, moved a little along the arrow if
        that helps. Placed labels become obstacles for the next ones."""
        boxes = self._obstacles()

        def overlap(b):
            area = 0.0
            for o in boxes:
                w = min(b[2], o[2]) - max(b[0], o[0])
                h = min(b[3], o[3]) - max(b[1], o[1])
                if w > 0 and h > 0:
                    area += w * h
            if b[0] < 8 or b[2] > self.W - 8 or b[1] < 8 or b[3] > self.H - 8:
                area += 1e6
            return area

        for st in self.steps:
            for a in st["arrows"]:
                if not a.get("label"):
                    continue
                w, h = self._label_box(a)
                g = a["geom"]
                best = None
                for side_i, side in enumerate((1, -1)):
                    for shift_i, shift in enumerate((0, -0.08, 0.08, -0.16, 0.16, -0.24, 0.24)):
                        at = min(0.85, max(0.15, a["label_at"] + shift))
                        (px, py), (dx, dy) = g.at(g.length * at)
                        nx, ny = -dy * side, dx * side
                        dist = LABEL_GAP + abs(nx) * w / 2 + abs(ny) * h / 2
                        cx, cy = px + nx * dist, py + ny * dist
                        box = (cx - w / 2 - 3, cy - h / 2 - 2, cx + w / 2 + 3, cy + h / 2 + 2)
                        score = overlap(box) + 40 * side_i + 25 * shift_i
                        if best is None or score < best[0]:
                            best = (score, cx, cy, nx, ny, dist, box, at)
                _, cx, cy, nx, ny, dist, box, at = best
                a["label_pos"] = (cx + a.get("label_dx", 0), cy + a.get("label_dy", 0), nx, ny, dist, at)
                boxes.append(box)

    def _label(self, a, geom, p, grey, fade):
        """The label beside the arrow, never on top of the line. No box: a
        halo in the background colour keeps it readable where it comes close
        to another line."""
        c, f = self.colors, self.fonts
        cx, cy, nx, ny, dist, at = a["label_pos"]
        appear = ease_out((p - 0.85 * at) * 4)
        if appear <= 0:
            return ""
        w, h = self._label_box(a)
        size, vsize = self.LABEL_SIZE, self.VIA_SIZE
        back = dist * 0.3 * (1 - appear)          # slides out from the line
        x, y = cx - nx * back, cy - ny * back
        top = y - h / 2
        op = appear * fade * (1 - 0.35 * grey)
        text_rgb = hex_mix(c["red"] if a["rgb"] == c["red"] else c["navy"], c["grey_text"], grey)
        halo = (f'stroke="{c["background"]}" stroke-width="7" stroke-linejoin="round" '
                f'paint-order="stroke"')
        out = [f'<g opacity="{op:.3f}">',
               f'<text x="{x:.1f}" y="{top + size * 0.78:.1f}" font-family="{f["sans"]}" font-size="{size}" '
               f'font-weight="700" fill="{text_rgb}" {halo} text-anchor="middle">{escape(a["label"])}</text>']
        if a.get("via"):
            out.append(f'<text x="{x:.1f}" y="{top + size + self.LINE_SPACING + vsize * 0.8:.1f}" '
                       f'font-family="{f["sans"]}" font-size="{vsize}" font-style="italic" '
                       f'fill="{c["grey_text"]}" {halo} text-anchor="middle">{escape(a["via"])}</text>')
        out.append("</g>")
        return "".join(out)


def render(scene, timeline, total, lang, cfg, diagrams, outdir):
    """Write frame_00000.svg ... into outdir; returns the number of frames."""
    fps = cfg["output"]["fps"]
    flow = Flow(scene, timeline, total, lang, cfg, diagrams)
    n = int(round(total * fps))
    os.makedirs(outdir, exist_ok=True)
    for i in range(n):
        with open(os.path.join(outdir, f"frame_{i:05d}.svg"), "w", encoding="utf-8") as fh:
            fh.write(flow.frame(i / fps))
    return n
