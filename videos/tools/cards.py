"""SVG for everything that is not a flow: title and part cards (one SVG per
frame, with a small entrance animation), the badge on screen scenes and the
highlight mask over a screenshot (both still images)."""
import base64
import os
from xml.sax.saxutils import escape

import common
import flow

_logo_cache = {}


def logo_uri(cfg):
    p = common.path(cfg["sources"]["logo"])
    if p not in _logo_cache:
        with open(p, "rb") as f:
            _logo_cache[p] = "data:image/png;base64," + base64.b64encode(f.read()).decode()
    return _logo_cache[p]


def _head(cfg, w=None, h=None):
    W = w or cfg["output"]["width"]
    H = h or cfg["output"]["height"]
    return (f'<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" '
            f'width="{W}" height="{H}" viewBox="0 0 {W} {H}">')


def _rise(t, t0, d=0.6):
    """Opacity and a y offset for text that fades in while moving up."""
    p = flow.ease_out((t - t0) / d)
    return p, 24 * (1 - p)


def wrap_title(text, max_chars):
    words, lines, cur = text.split(), [], ""
    for w in words:
        if cur and len(cur) + 1 + len(w) > max_chars:
            lines.append(cur)
            cur = w
        else:
            cur = f"{cur} {w}".strip()
    if cur:
        lines.append(cur)
    return lines


class TitleCard:
    """Logo, large text and an optional note, on the navy brand background."""

    def __init__(self, scene, cfg):
        self.cfg, self.scene = cfg, scene
        self.lines = wrap_title(scene.get("text", ""), 30)

    def frame(self, t):
        c, f = self.cfg["colors"], self.cfg["fonts"]
        W, H = self.cfg["output"]["width"], self.cfg["output"]["height"]
        out = [_head(self.cfg), "<defs>", flow.style_defs(self.cfg), "</defs>",
               f'<rect width="100%" height="100%" fill="{c["navy"]}"/>']
        # A quiet wave line at the bottom, in the brand colours.
        out.append(f'<path d="M0 {H - 70} Q {W * 0.25} {H - 100} {W * 0.5} {H - 70} T {W} {H - 70} '
                   f'L {W} {H} L 0 {H} Z" fill="#1E3D5A"/>')
        p = flow.ease_out(t / 0.8)
        size = 190 * (0.8 + 0.2 * p)
        top = 200 - 30 * len(self.lines)
        out.append(f'<image x="{W / 2 - size / 2:.1f}" y="{top + 95 - size / 2:.1f}" width="{size:.1f}" '
                   f'height="{size:.1f}" opacity="{p:.3f}" href="{logo_uri(self.cfg)}" '
                   f'xlink:href="{logo_uri(self.cfg)}"/>')
        y = top + 270
        for i, line in enumerate(self.lines):
            op, dy = _rise(t, 0.35 + 0.12 * i)
            out.append(f'<text x="{W / 2}" y="{y + dy:.1f}" opacity="{op:.3f}" font-family="{f["serif"]}" '
                       f'font-size="60" font-weight="600" fill="{c["white"]}" text-anchor="middle">'
                       f'{escape(line)}</text>')
            y += 74
        if self.scene.get("note"):
            op, dy = _rise(t, 0.7)
            out.append(f'<text x="{W / 2}" y="{y + 10 + dy:.1f}" opacity="{op:.3f}" font-family="{f["sans"]}" '
                       f'font-size="28" fill="{c["amber"]}" text-anchor="middle">'
                       f'{escape(self.scene["note"])}</text>')
        out.append("</svg>")
        return "\n".join(out)


class PartCard:
    """A numbered part heading with an optional note pill, on the light background."""

    def __init__(self, scene, cfg):
        self.cfg, self.scene = cfg, scene

    def frame(self, t):
        c, f = self.cfg["colors"], self.cfg["fonts"]
        W, H = self.cfg["output"]["width"], self.cfg["output"]["height"]
        out = [_head(self.cfg), "<defs>", flow.style_defs(self.cfg), "</defs>",
               f'<rect width="100%" height="100%" fill="{c["background"]}"/>',
               f'<rect x="80" y="110" width="{W - 160}" height="{H - 260}" rx="28" fill="{c["white"]}" '
               f'stroke="{c["line"]}" stroke-width="2"/>']
        p = flow.ease_out(t / 0.5)
        r = 52 * (0.6 + 0.4 * p)
        out.append(f'<g opacity="{p:.3f}"><circle cx="{W / 2}" cy="230" r="{r:.1f}" fill="{c["red"]}"/>'
                   f'<text x="{W / 2}" y="252" font-family="{f["sans"]}" font-size="60" font-weight="700" '
                   f'fill="{c["white"]}" text-anchor="middle">{escape(str(self.scene.get("number", "")))}</text></g>')
        op, dy = _rise(t, 0.2)
        out.append(f'<text x="{W / 2}" y="{375 + dy:.1f}" opacity="{op:.3f}" font-family="{f["serif"]}" '
                   f'font-size="54" font-weight="600" fill="{c["navy"]}" text-anchor="middle">'
                   f'{escape(self.scene.get("text", ""))}</text>')
        note = self.scene.get("note")
        if note:
            op, dy = _rise(t, 0.45)
            w = flow.text_width(note, 22, True) + 44
            out.append(f'<g opacity="{op:.3f}" transform="translate({W / 2} {440 + dy:.1f})">'
                       f'<rect x="{-w / 2:.1f}" y="-22" width="{w:.1f}" height="44" rx="22" fill="{c["amber_light"]}"/>'
                       f'<text y="8" font-family="{f["sans"]}" font-size="22" font-weight="700" '
                       f'fill="{c["navy"]}" text-anchor="middle">{escape(note)}</text></g>')
        out.append("</svg>")
        return "\n".join(out)


def badge_svg(actor, lang, cfg, diagrams):
    """A pill in the top left corner that says whose screen this is."""
    c, f = cfg["colors"], cfg["fonts"]
    master = cfg["master"]
    node = diagrams["nodes"][actor]
    who = common.localized(node["label"], lang, master)
    app_node = diagrams["nodes"].get(node.get("badge_app", ""), {})
    app = common.localized(app_node.get("label"), lang, master) if app_node else ""
    text_w = flow.text_width(who, 22, True) + (flow.text_width("  ·  " + app, 20) if app else 0)
    w = 74 + text_w + 26
    return (_head(cfg) + "<defs>" + flow.style_defs(cfg) + "</defs>"
            f'<rect x="26" y="26" width="{w:.1f}" height="60" rx="30" fill="#000000" opacity="0.12" '
            f'transform="translate(0 3)"/>'
            f'<rect x="24" y="24" width="{w:.1f}" height="60" rx="30" fill="{c["white"]}" '
            f'stroke="{c["line"]}" stroke-width="1.5"/>'
            f'<use href="#{node["icon"]}" xlink:href="#{node["icon"]}" transform="translate(54 54) scale(0.72)"/>'
            f'<text x="96" y="62" font-family="{f["sans"]}" font-size="22" font-weight="700" fill="{c["navy"]}">'
            f'{escape(who)}'
            + (f'<tspan dx="10" font-weight="400" font-size="20" fill="{c["grey_text"]}">·</tspan>'
               f'<tspan dx="10" font-weight="400" font-size="20" fill="{c["grey_text"]}">{escape(app)}</tspan>'
               if app else "")
            + "</text></svg>")


def highlight_svg(box, cfg, canvas_w, canvas_h):
    """A dark veil with a rounded hole and an amber frame around box, which
    is in screenshot pixels; the SVG is canvas sized."""
    c = cfg["colors"]
    sw, sh = cfg["sources"]["screenshot_size"]
    k = canvas_w / sw
    pad = 10
    x, y = (box["x"] - pad) * k, (box["y"] - pad) * k
    w, h = (box["w"] + 2 * pad) * k, (box["h"] + 2 * pad) * k
    r = 14 * k
    hole = (f"M{x + r:.1f} {y:.1f} H{x + w - r:.1f} A{r:.1f} {r:.1f} 0 0 1 {x + w:.1f} {y + r:.1f} "
            f"V{y + h - r:.1f} A{r:.1f} {r:.1f} 0 0 1 {x + w - r:.1f} {y + h:.1f} H{x + r:.1f} "
            f"A{r:.1f} {r:.1f} 0 0 1 {x:.1f} {y + h - r:.1f} V{y + r:.1f} A{r:.1f} {r:.1f} 0 0 1 {x + r:.1f} {y:.1f} Z")
    dim = cfg["screen"]["dim"]
    return (_head(cfg, canvas_w, canvas_h)
            + f'<path fill-rule="evenodd" fill="{c["navy"]}" opacity="{dim}" '
              f'd="M0 0 H{canvas_w} V{canvas_h} H0 Z {hole}"/>'
            + f'<path fill="none" stroke="{c["amber"]}" stroke-width="{4 * k:.1f}" d="{hole}"/>'
            + "</svg>")


def write_frames(card, total, fps, outdir):
    n = int(round(total * fps))
    os.makedirs(outdir, exist_ok=True)
    for i in range(n):
        with open(os.path.join(outdir, f"frame_{i:05d}.svg"), "w", encoding="utf-8") as fh:
            fh.write(card.frame(i / fps))
    return n
