#!/usr/bin/env python3
"""Check the video scripts, assets and tools before rendering.

    python3 videos/tools/validate.py              # all languages
    python3 videos/tools/validate.py --lang nl-nl

Exit status 1 with one line per problem, so a person or an LLM knows exactly
what to fix. Warnings do not fail the check.
"""
import argparse
import os
import re
import struct
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import common  # noqa: E402
import flow  # noqa: E402
import subs  # noqa: E402

SCENE_TYPES = {"title", "part", "screen", "flow"}
ZOOMS = {"none", "highlight", "slow"}
SCENE_FIELDS = {
    "title": {"id", "type", "text", "note", "say", "subtitle"},
    "part": {"id", "type", "number", "text", "note", "say", "subtitle"},
    "screen": {"id", "type", "actor", "screenshot", "highlight", "highlight_override", "zoom", "say", "subtitle"},
    "flow": {"id", "type", "title", "show", "steps"},
}
STEP_FIELDS = {"from", "to", "through", "label", "via", "loop", "color", "offset", "label_at", "label_dx",
               "label_dy", "arrows", "highlight", "clear", "say", "subtitle"}
ARROW_FIELDS = {"from", "to", "through", "label", "via", "loop", "color", "offset", "label_at", "label_dx",
                "label_dy"}


def png_size(p):
    with open(p, "rb") as f:
        head = f.read(24)
    if head[:8] != b"\x89PNG\r\n\x1a\n":
        return None
    return struct.unpack(">II", head[16:24])


def screenshot_file(cfg, app, name, lang):
    return common.path(cfg["sources"]["screenshot"].format(app=app, name=name, lang=lang))


def texts_of(scene):
    """Every (field, text) of a scene, steps and arrows included."""
    out = []

    def walk(node):
        if isinstance(node, dict):
            for k, v in node.items():
                if k in common.TEXT_FIELDS and isinstance(v, str):
                    out.append((k, v))
                else:
                    walk(v)
        elif isinstance(node, list):
            for v in node:
                walk(v)
    walk(scene)
    return out


def check_tools(cfg, errors, warnings):
    try:
        conf = subprocess.run(["ffmpeg", "-hide_banner", "-buildconf"], capture_output=True, text=True).stdout
    except FileNotFoundError:
        errors.append("ffmpeg is not installed: sudo apt install ffmpeg")
        return
    for lib in ("librsvg", "libx264"):
        if f"--enable-{lib}" not in conf:
            errors.append(f"ffmpeg is built without {lib}; use the distro build (sudo apt install ffmpeg)")
    filters = subprocess.run(["ffmpeg", "-hide_banner", "-filters"], capture_output=True, text=True).stdout
    for flt in ("zoompan", "loudnorm", "overlay", "fade"):
        if not re.search(rf"^\s*\S+\s+{flt}\s", filters, re.M):
            errors.append(f"ffmpeg has no {flt} filter")
    voices = [cfg.get("voices", {}).get(lang) for lang in cfg["languages"]]
    for lang, v in zip(cfg["languages"], voices):
        if not v:
            errors.append(f"config.yaml: no voice for {lang}")
        elif not v.get("voice"):
            errors.append(f"config.yaml: the voice for {lang} needs a 'voice' name")
    if not common.edge_binary():
        errors.append("edge-tts is not installed: pip install edge-tts (or a venv in videos/.venv)")


def check_scene(s, lang, cfg, diagrams, symbols, errors, warnings, where):
    t = s.get("type")
    if t not in SCENE_TYPES:
        errors.append(f"{where}: unknown type '{t}' (one of {', '.join(sorted(SCENE_TYPES))})")
        return
    for k in s:
        if k not in SCENE_FIELDS[t]:
            errors.append(f"{where}: unknown field '{k}' for a {t} scene")
    if t != "flow" and not s.get("say"):
        errors.append(f"{where}: 'say' is missing")
    if t in ("title", "part") and not s.get("text"):
        errors.append(f"{where}: 'text' is missing")
    if t == "screen":
        actor = s.get("actor")
        if actor not in diagrams["nodes"] or "badge_app" not in diagrams["nodes"][actor]:
            errors.append(f"{where}: actor must be a node with a badge_app (user or techie), not '{actor}'")
        shot = s.get("screenshot") or {}
        app, name = shot.get("app"), shot.get("name")
        if not app or not name:
            errors.append(f"{where}: screenshot needs app and name")
        else:
            if f"{app}/{name}" in cfg.get("forbidden_screenshots", []):
                errors.append(f"{where}: screenshot {app}/{name} is out of scope (server setup)")
            p = screenshot_file(cfg, app, name, lang)
            if not os.path.exists(p):
                errors.append(f"{where}: screenshot {os.path.relpath(p, common.ROOT)} does not exist")
            elif png_size(p) != tuple(cfg["sources"]["screenshot_size"]):
                errors.append(f"{where}: {os.path.relpath(p, common.ROOT)} is {png_size(p)}, "
                              f"expected {tuple(cfg['sources']['screenshot_size'])}")
        zoom = s.get("zoom", "none")
        if zoom not in ZOOMS:
            errors.append(f"{where}: zoom must be one of {', '.join(sorted(ZOOMS))}")
        boxes = [("highlight", s.get("highlight"))] + [
            (f"highlight_override.{k}", v) for k, v in (s.get("highlight_override") or {}).items()]
        sw, sh = cfg["sources"]["screenshot_size"]
        for label, box in boxes:
            if box is None:
                continue
            if not all(isinstance(box.get(k), (int, float)) for k in ("x", "y", "w", "h")):
                errors.append(f"{where}: {label} needs numbers x, y, w and h")
                continue
            if box["x"] < 0 or box["y"] < 0 or box["w"] <= 0 or box["h"] <= 0 \
                    or box["x"] + box["w"] > sw or box["y"] + box["h"] > sh:
                errors.append(f"{where}: {label} {box} is not inside {sw}x{sh}")
        for k in (s.get("highlight_override") or {}):
            if k not in cfg["languages"]:
                errors.append(f"{where}: highlight_override for unknown language '{k}'")
        if zoom == "highlight" and not s.get("highlight"):
            errors.append(f"{where}: zoom: highlight needs a highlight box")
    if t == "flow":
        show = s.get("show") or []
        if not show:
            errors.append(f"{where}: 'show' lists no nodes")
        for n in show:
            if n not in diagrams["nodes"]:
                errors.append(f"{where}: show: unknown node '{n}' (known: {', '.join(diagrams['nodes'])})")
            elif diagrams["nodes"][n].get("icon") not in symbols:
                errors.append(f"diagrams.yaml: node '{n}' has unknown icon '{diagrams['nodes'][n].get('icon')}'")
        steps = s.get("steps") or []
        if not steps:
            errors.append(f"{where}: a flow needs at least one step")
        for i, st in enumerate(steps):
            sw_ = f"{where}.steps[{i}]"
            for k in st:
                if k not in STEP_FIELDS:
                    errors.append(f"{sw_}: unknown field '{k}'")
            if not st.get("say"):
                errors.append(f"{sw_}: 'say' is missing")
            for a in st.get("arrows") or []:
                for k in a:
                    if k not in ARROW_FIELDS:
                        errors.append(f"{sw_}.arrows: unknown field '{k}'")
            for n in st.get("highlight") or []:
                if n not in show:
                    errors.append(f"{sw_}: highlight node '{n}' is not in show")
            for a in common.steps_of({"steps": [st]})[0]["arrows"]:
                for k in ("from", "to", "through"):
                    n = a.get(k)
                    if n is None and k == "through":
                        continue
                    if n not in show:
                        errors.append(f"{sw_}: arrow {k} '{n}' is not a node in show")
                if a.get("from") == a.get("to"):
                    errors.append(f"{sw_}: arrow goes from '{a.get('from')}' to itself")
                if not a.get("label"):
                    errors.append(f"{sw_}: arrow {a.get('from')} -> {a.get('to')} has no label")
                if a.get("color") and a["color"] not in cfg["colors"]:
                    errors.append(f"{sw_}: unknown color '{a['color']}'")
    # Text checks for every language.
    # Case-sensitive: "helpbuoy-server" is the server binary, while
    # "HelpBuoy-Server" is the German and Dutch for the HelpBuoy server.
    forbidden = cfg.get("forbidden_words", [])
    for field, text in texts_of(s):
        for w in forbidden:
            if w in text:
                errors.append(f"{where}: {field} contains '{w}', which is out of scope: {text!r}")
        for w in cfg.get("brand_words", []):
            if w in text:
                errors.append(f"{where}: {field} contains the brand name '{w}'; say what it is instead "
                              f"(the app, the desk app, the server): {text!r}")
        if field == "say":
            if re.search(r"[:;()\[\]/]", text):
                errors.append(f"{where}: say contains a colon, semicolon, bracket or slash, "
                              f"which the voice stumbles over: {text!r}")
            if text.count('"') % 2:
                errors.append(f"{where}: say has a quote that is not closed: {text!r}")
            if not re.search(r"[.?!]$", text.strip()):
                errors.append(f"{where}: say must end with a full stop, question mark or exclamation mark: {text!r}")
            for sentence in re.split(r"(?<=[.?!])\s+", text):
                n = len(sentence.split())
                if n > 24:
                    warnings.append(f"{where}: a sentence of {n} words; the voice sounds better with "
                                    f"sentences of about 8 to 20 words: {sentence!r}")
        if field in ("say", "subtitle"):
            words = len(text.split())
            if words > 30:
                warnings.append(f"{where}: {field} has {words} words; keep it under about 25")
            for cue in subs.split_cues(text, cfg["subtitles"]["max_chars"]):
                lines = subs.wrap(cue, cfg["subtitles"]["max_chars"])
                for line in lines:
                    if len(line) > cfg["subtitles"]["max_chars"]:
                        errors.append(f"{where}: subtitle line is longer than "
                                      f"{cfg['subtitles']['max_chars']} characters: {line!r}")
                if len(lines) > cfg["subtitles"]["max_lines"]:
                    errors.append(f"{where}: subtitle cue needs {len(lines)} lines: {cue!r}")


def validate(cfg, langs, tools=True):
    errors, warnings = [], []
    try:
        diagrams = common.load_diagrams()
    except Exception as e:  # noqa: BLE001
        return [f"diagrams.yaml: {e}"], warnings
    symbols = flow.symbol_ids(cfg)
    for name, n in diagrams.get("nodes", {}).items():
        for k in ("x", "y", "icon", "label"):
            if k not in n:
                errors.append(f"diagrams.yaml: node '{name}' has no {k}")
        for lang in cfg["languages"]:
            for k in ("label", "sub"):
                if isinstance(n.get(k), dict) and lang not in n[k]:
                    errors.append(f"diagrams.yaml: node '{name}' has no {k} for {lang}")
    if tools:
        check_tools(cfg, errors, warnings)
    if not os.path.exists(common.path(cfg["sources"]["logo"])):
        errors.append(f"logo {cfg['sources']['logo']} does not exist")
    for lang in langs:
        if lang not in cfg["languages"]:
            errors.append(f"unknown language '{lang}' (config.yaml languages: {', '.join(cfg['languages'])})")
            continue
        meta, scenes = common.load_script(lang, cfg, errors)
        if meta is None:
            continue
        if meta.get("lang") != lang:
            errors.append(f"{lang}.yaml: meta.lang is '{meta.get('lang')}', expected '{lang}'")
        if not meta.get("title"):
            errors.append(f"{lang}.yaml: meta.title is missing")
        seen = set()
        for i, s in enumerate(scenes):
            sid = s.get("id")
            where = f"{lang}.yaml:{sid or f'scene {i + 1}'}"
            if not sid or not re.fullmatch(r"[a-z0-9][a-z0-9-]*", str(sid)):
                errors.append(f"{where}: id must be lowercase letters, digits and hyphens")
            if sid in seen:
                errors.append(f"{where}: duplicate id")
            seen.add(sid)
            check_scene(s, lang, cfg, diagrams, symbols, errors, warnings, where)
    return errors, warnings


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--lang", action="append", help="check only this language (repeatable)")
    ap.add_argument("--no-tools", action="store_true", help="skip the ffmpeg and voice checks")
    args = ap.parse_args()
    cfg = common.load_config()
    langs = args.lang or cfg["languages"]
    errors, warnings = validate(cfg, langs, tools=not args.no_tools)
    for w in warnings:
        print(f"warning: {w}")
    for e in errors:
        print(f"error: {e}")
    if errors:
        print(f"{len(errors)} error(s)")
        sys.exit(1)
    print(f"ok: {', '.join(langs)}")


if __name__ == "__main__":
    main()
