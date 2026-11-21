"""Shared helpers: paths, config and script loading (stdlib and PyYAML only)."""
import copy
import os
import shutil
import sys

try:
    import yaml
except ImportError:  # pragma: no cover
    sys.exit("PyYAML is missing: sudo apt install python3-yaml")

TOOLS = os.path.dirname(os.path.abspath(__file__))
VIDEOS = os.path.dirname(TOOLS)
ROOT = os.path.dirname(VIDEOS)
BUILD = os.path.join(VIDEOS, "build")
OUT = os.path.join(VIDEOS, "out")

# Fields a translation may (and must) carry; everything else comes from the
# master script.
TEXT_FIELDS = ("say", "subtitle", "text", "title", "label", "via", "note")


def path(p):
    return p if os.path.isabs(p) else os.path.join(ROOT, p)


def load_yaml(p):
    with open(p, encoding="utf-8") as f:
        return yaml.safe_load(f)


def load_config():
    return load_yaml(os.path.join(VIDEOS, "config.yaml"))


def load_diagrams():
    return load_yaml(os.path.join(VIDEOS, "diagrams.yaml"))


def script_path(lang):
    return os.path.join(VIDEOS, "script", f"{lang}.yaml")


def edge_binary():
    found = shutil.which("edge-tts")
    if found:
        return found
    venv = os.path.join(VIDEOS, ".venv", "bin", "edge-tts")
    return venv if os.path.exists(venv) else None


def localized(value, lang, master):
    """A {lang: text} map or a plain string, resolved for lang."""
    if isinstance(value, dict):
        return value.get(lang, value.get(master))
    return value


def merge_text(master_node, trans_node, where, errors):
    """Copy the text fields of trans_node over a deep copy of master_node.

    Both have the same shape: dicts keyed alike, lists of the same length.
    Text fields present in the master must be present in the translation,
    and the translation may hold nothing else."""
    if isinstance(master_node, dict):
        out = {}
        trans_node = trans_node if isinstance(trans_node, dict) else {}
        for k, v in master_node.items():
            if k in TEXT_FIELDS and isinstance(v, str):
                t = trans_node.get(k)
                if not isinstance(t, str) or not t.strip():
                    errors.append(f"{where}: missing text field '{k}'")
                    out[k] = v
                else:
                    out[k] = t
            elif isinstance(v, (dict, list)) and _has_text(v):
                out[k] = merge_text(v, trans_node.get(k), f"{where}.{k}", errors)
            else:
                out[k] = copy.deepcopy(v)
        for k in trans_node:
            if k not in master_node:
                errors.append(f"{where}: '{k}' is not in the master script")
            elif k not in TEXT_FIELDS and not isinstance(master_node[k], (dict, list)):
                errors.append(f"{where}: '{k}' is a visual field; it belongs in the master script only")
        return out
    if isinstance(master_node, list):
        trans_node = trans_node if isinstance(trans_node, list) else []
        if len(trans_node) != len(master_node):
            errors.append(f"{where}: {len(trans_node)} items, the master script has {len(master_node)}")
        return [merge_text(m, trans_node[i] if i < len(trans_node) else None, f"{where}[{i}]", errors)
                for i, m in enumerate(master_node)]
    return copy.deepcopy(master_node)


def _has_text(node):
    if isinstance(node, dict):
        return any((k in TEXT_FIELDS and isinstance(v, str)) or _has_text(v) for k, v in node.items())
    if isinstance(node, list):
        return any(_has_text(v) for v in node)
    return False


def load_script(lang, cfg, errors):
    """The script for lang as a list of full scenes (visual fields from the
    master, text from lang), plus its meta. Problems go to errors."""
    master_lang = cfg["master"]
    try:
        master = load_yaml(script_path(master_lang))
    except (OSError, yaml.YAMLError) as e:
        errors.append(f"{master_lang}.yaml: {e}")
        return None, []
    if lang == master_lang:
        return master.get("meta", {}), master.get("scenes", [])
    try:
        trans = load_yaml(script_path(lang))
    except (OSError, yaml.YAMLError) as e:
        errors.append(f"{lang}.yaml: {e}")
        return None, []
    trans = trans or {}
    tscenes = trans.get("scenes") or {}
    if not isinstance(tscenes, dict):
        errors.append(f"{lang}.yaml: 'scenes' must be a map from scene id to its text fields")
        tscenes = {}
    scenes = []
    for s in master.get("scenes", []):
        sid = s.get("id")
        if sid not in tscenes:
            errors.append(f"{lang}.yaml: scene '{sid}' is missing")
        scenes.append(merge_text(s, tscenes.get(sid), f"{lang}.yaml:{sid}", errors))
    master_ids = {s.get("id") for s in master.get("scenes", [])}
    for sid in tscenes:
        if sid not in master_ids:
            errors.append(f"{lang}.yaml: scene '{sid}' is not in the master script")
    meta = dict(master.get("meta", {}))
    meta.update(trans.get("meta") or {})
    return meta, scenes


def steps_of(scene):
    """The steps of a flow scene, each with a list of arrows."""
    out = []
    for st in scene.get("steps") or []:
        st = dict(st)
        arrows = list(st.get("arrows") or [])
        if "from" in st:
            arrows.insert(0, {k: st[k] for k in ("from", "to", "through", "label", "via", "loop", "color")
                              if k in st})
        st["arrows"] = arrows
        out.append(st)
    return out
