#!/usr/bin/env python3
"""Build the HelpBuoy instruction videos from the text files in videos/.

    python3 videos/tools/build.py                                  # all languages
    python3 videos/tools/build.py --lang nl-nl                     # one language
    python3 videos/tools/build.py --lang en-us --scene flow-send-key   # preview one scene
    python3 videos/tools/build.py --list-assets                    # for the LLM

Steps: validate, narrate (Edge voices), time, subtitles, render scenes, assemble
(concat, loudnorm, a soft subtitle track) and verify.
Every expensive step is cached in videos/build/ by a hash of its inputs.
"""
import argparse
import concurrent.futures as cf
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import threading
import time
import wave

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import cards  # noqa: E402
import common  # noqa: E402
import flow  # noqa: E402
import subs  # noqa: E402
import validate  # noqa: E402

AUDIO_RATE = 48000
CLIP_CRF = 14


def log(msg):
    print(msg, flush=True)


def run(cmd, **kw):
    r = subprocess.run(cmd, capture_output=True, text=True, **kw)
    if r.returncode != 0:
        sys.stderr.write(r.stderr[-4000:])
        raise SystemExit(f"command failed: {' '.join(cmd[:6])} ...")
    return r


def sha(*parts):
    h = hashlib.sha256()
    for p in parts:
        h.update(json.dumps(p, sort_keys=True, ensure_ascii=False).encode() if not isinstance(p, bytes) else p)
        h.update(b"\0")
    return h.hexdigest()[:20]


def file_hash(p):
    h = hashlib.sha256()
    with open(p, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


_code_hash = None


def code_hash():
    """Changes to the renderer invalidate every cached clip."""
    global _code_hash
    if _code_hash is None:
        h = hashlib.sha256()
        for name in ("build.py", "flow.py", "cards.py"):
            with open(os.path.join(common.TOOLS, name), "rb") as f:
                h.update(f.read())
        _code_hash = h.hexdigest()
    return _code_hash


# -- narration ----------------------------------------------------------------

def narrate(text, lang, cfg):
    """A cached WAV of text spoken by the voice of lang."""
    v = cfg["voices"][lang]
    out = os.path.join(common.BUILD, "audio", f"{sha('edge', v, text)}.wav")
    if os.path.exists(out):
        return out
    os.makedirs(os.path.dirname(out), exist_ok=True)
    tmp = out + ".tmp.wav"
    mp3 = tmp + ".mp3"
    edge_request(text, v, mp3)
    edge_cut(mp3, None, None, tmp)
    os.remove(mp3)
    os.replace(tmp, out)
    return out


_edge_slots = threading.Semaphore(2)  # be gentle with the online service
EDGE_PAD_BEFORE, EDGE_PAD_AFTER = 0.06, 0.12  # kept around a text cut from a group
EDGE_MAX_PAUSE = 0.6  # longest pause inside one text, seconds
EDGE_TRIM = "silenceremove=start_periods=1:start_threshold=-50dB:start_silence=0.05"


def edge_request(text, v, mp3, srt=None):
    """Speak text with an online Edge voice into an MP3, with the times of
    its sentences in srt. The service sometimes answers without audio, so
    try a few times."""
    src = mp3 + ".txt"
    with open(src, "w", encoding="utf-8") as f:
        f.write(text)
    cmd = [common.edge_binary(), "--voice", v["voice"], f"--rate={v.get('rate', '+0%')}",
           "--file", src, "--write-media", mp3, "--write-subtitles", srt or os.devnull]
    try:
        for attempt in range(6):
            with _edge_slots:
                r = subprocess.run(cmd, capture_output=True, text=True)
            if r.returncode == 0 and os.path.exists(mp3) and os.path.getsize(mp3) > 0:
                return
            time.sleep(2 + 3 * attempt)
        sys.stderr.write(r.stderr[-3000:])
        raise SystemExit(f"edge-tts failed for {v['voice']}: {text[:80]!r}")
    finally:
        os.remove(src)


def edge_cut(mp3, start, end, wav):
    """An MP3 as a WAV without the silence around the speech (the scene
    timing adds its own), or exactly the piece from start to end."""
    cmd = ["ffmpeg", "-y", "-v", "error", "-i", mp3]
    if start is None:
        cmd += ["-af", f"{EDGE_TRIM},areverse,{EDGE_TRIM},areverse"]
    else:
        cmd += ["-ss", f"{start:.3f}", "-to", f"{end:.3f}"]
    run(cmd + ["-ac", "1", "-c:a", "pcm_s16le", wav])


def silences(path):
    """The (start, end) of every pause in an audio file."""
    err = subprocess.run(["ffmpeg", "-hide_banner", "-i", path, "-af",
                          "silencedetect=noise=-40dB:d=0.2", "-f", "null", "-"],
                         capture_output=True, text=True).stderr
    starts = [float(x) for x in re.findall(r"silence_start: (-?[\d.]+)", err)]
    ends = [float(x) for x in re.findall(r"silence_end: ([\d.]+)", err)]
    m = re.search(r"Duration: (\d+):(\d+):([\d.]+)", err)
    total = int(m.group(1)) * 3600 + int(m.group(2)) * 60 + float(m.group(3))
    ends += [total] * (len(starts) - len(ends))  # silence up to the end of the file
    return [(max(0.0, a), b) for a, b in zip(starts, ends)], total


def cap_pauses(wav):
    """Shorten every pause inside a WAV to EDGE_MAX_PAUSE; some voices wait
    a full second between two sentences."""
    cuts = [(a, b) for a, b in silences(wav)[0] if b - a > EDGE_MAX_PAUSE]
    if not cuts:
        return
    with wave.open(wav, "rb") as f:
        params, rate, size = f.getparams(), f.getframerate(), f.getsampwidth() * f.getnchannels()
        data = f.readframes(f.getnframes())
    keep, pos = [], 0
    for a, b in cuts:  # drop the middle of the pause
        keep.append(data[pos:int((a + EDGE_MAX_PAUSE / 2) * rate) * size])
        pos = int((b - EDGE_MAX_PAUSE / 2) * rate) * size
    keep.append(data[pos:])
    with wave.open(wav, "wb") as f:
        f.setparams(params)
        f.writeframes(b"".join(keep))


def _letters(text):
    return re.sub(r"[\W_]+", "", text.lower())


def _srt_sentences(path):
    """(end time, text) of every sentence in an SRT of edge-tts."""
    out = []
    with open(path, encoding="utf-8") as f:
        for block in f.read().strip().split("\n\n"):
            lines = block.strip().split("\n")
            m = len(lines) >= 3 and re.search(r"--> (\d+):(\d+):(\d+)[,.](\d+)", lines[1])
            if m:
                h, mi, sec, ms = (int(x) for x in m.groups())
                out.append((h * 3600 + mi * 60 + sec + ms / 1000, " ".join(lines[2:])))
    return out


def narrate_group(texts, lang, cfg):
    """Cached WAVs of texts, spoken by an Edge voice in one request, so the
    voice knows what came before each text and what follows. The audio is
    cut at the sentence times the service reports. Returns None when those
    do not line up with the texts."""
    v = cfg["voices"][lang]
    key = sha("edge-group-4", v, texts)
    outs = [os.path.join(common.BUILD, "audio", f"{key}-{i:02d}.wav") for i in range(len(texts))]
    if all(os.path.exists(o) for o in outs):
        return outs
    os.makedirs(os.path.dirname(outs[0]), exist_ok=True)
    mp3, srt = outs[0] + ".group.mp3", outs[0] + ".group.srt"
    edge_request("\n\n".join(texts), v, mp3, srt)
    try:
        # The sentence that completes a text ends that text.
        ends, seen, want, i = [], 0, len(_letters(texts[0])), 0
        for end, sentence in _srt_sentences(srt):
            seen += len(_letters(sentence))
            if seen > want:
                return None
            if seen == want:
                ends.append(end)
                i += 1
                if i == len(texts):
                    break
                want += len(_letters(texts[i]))
        if len(ends) != len(texts):
            return None
        # The reported times are only near the pauses between the texts,
        # so cut in the real pauses: a text runs from the end of the pause
        # before it to the start of the pause after it.
        pauses, total = silences(mp3)
        start = pauses[0][1] if pauses and pauses[0][0] < 0.05 else 0.0
        pieces = []
        for i, end in enumerate(ends):
            if i == len(ends) - 1:
                last = pauses[-1] if pauses and pauses[-1][1] > total - 0.05 else (total, total)
                pause = (max(last[0], start), total)
            else:
                pause = min(pauses, key=lambda p: max(p[0] - end, end - p[1], 0), default=None)
                if pause is None or max(pause[0] - end, end - pause[1]) > 0.3 or pause[0] <= start:
                    return None
            pieces.append((max(0.0, start - EDGE_PAD_BEFORE), min(total, pause[0] + EDGE_PAD_AFTER)))
            start = pause[1]
        for (a, b), out in zip(pieces, outs):
            edge_cut(mp3, a, b, out + ".tmp.wav")
            cap_pauses(out + ".tmp.wav")
            os.replace(out + ".tmp.wav", out)
        return outs
    finally:
        for p in (mp3, srt):
            if os.path.exists(p):
                os.remove(p)


def narrate_all(scenes, lang, cfg, jobs):
    """The narration WAVs of all segments of scenes, in order. The voice
    speaks a whole part of the video at once: a group starts at every part
    scene."""
    texts = [[seg[0] for seg in segments_of(s)] for s in scenes]
    with cf.ThreadPoolExecutor(max_workers=jobs) as ex:
        groups = []
        for s, ts in zip(scenes, texts):
            if s["type"] == "part" or not groups:
                groups.append([])
            groups[-1].extend(ts)

        def one(group):
            wavs = narrate_group(group, lang, cfg)
            if wavs is None:
                log(f"[{lang}] sentence times do not match the text, narrating each text on its own")
                wavs = [narrate(t, lang, cfg) for t in group]
            return wavs
        return [w for wavs in ex.map(one, groups) for w in wavs]


def wav_info(p):
    with wave.open(p, "rb") as w:
        return w.getframerate(), w.getnchannels(), w.getsampwidth(), w.getnframes()


def segments_of(scene):
    """The narrated parts of a scene: (say, subtitle text)."""
    if scene["type"] == "flow":
        return [(st["say"], st.get("subtitle") or st["say"]) for st in scene["steps"]]
    return [(scene["say"], scene.get("subtitle") or scene["say"])]


# -- timing -------------------------------------------------------------------

def plan_scene(scene, wavs, cfg):
    """Times of the narration segments in a scene and its total length,
    rounded up to a whole frame so audio and video stay in step."""
    t = cfg["timing"]
    fps = cfg["output"]["fps"]
    lead = t.get("flow_lead", 1.0) if scene["type"] == "flow" else t["lead"]
    if scene["type"] == "title":
        lead = t.get("title_lead", 1.0)
    pos, timeline = lead, []
    for w in wavs:
        rate, _, _, n = wav_info(w)
        d = n / rate
        timeline.append((pos, pos + d))
        pos += d + t["gap"]
    total = pos + t["tail"]
    if scene["type"] == "title":
        total += 0.6
    frames = int(-(-total * fps // 1))
    return timeline, frames / fps


def scene_audio(wavs, timeline, total, path):
    """One WAV for the scene: silence with each narration at its time."""
    rate, ch, sw, _ = wav_info(wavs[0])
    n_total = int(round(total * rate))
    buf = bytearray(n_total * ch * sw)
    for w, (start, _) in zip(wavs, timeline):
        with wave.open(w, "rb") as f:
            if (f.getframerate(), f.getnchannels(), f.getsampwidth()) != (rate, ch, sw):
                raise SystemExit(f"{w}: sample format differs within one voice")
            data = f.readframes(f.getnframes())
        off = int(round(start * rate)) * ch * sw
        buf[off:off + len(data)] = data[:max(0, len(buf) - off)]
    with wave.open(path, "wb") as f:
        f.setnchannels(ch)
        f.setsampwidth(sw)
        f.setframerate(rate)
        f.writeframes(bytes(buf))


# -- rendering ----------------------------------------------------------------

def svg_to_png(svg, png):
    tmp = png + ".svg"
    with open(tmp, "w", encoding="utf-8") as f:
        f.write(svg)
    run(["ffmpeg", "-v", "error", "-y", "-c:v", "librsvg", "-i", tmp, "-frames:v", "1", png])
    os.remove(tmp)


def fade_filter(total, cfg):
    d = cfg["timing"]["fade"]
    col = cfg["colors"]["background"].replace("#", "0x")
    return (f"fade=t=in:st=0:d={d}:color={col},"
            f"fade=t=out:st={max(0, total - d):.3f}:d={d}:color={col}")


def encode_frames(frame_dir, audio, total, out, cfg):
    fps = cfg["output"]["fps"]
    run(["ffmpeg", "-v", "error", "-y",
         "-framerate", str(fps), "-f", "image2", "-c:v", "librsvg", "-i", os.path.join(frame_dir, "frame_%05d.svg"),
         "-i", audio,
         "-vf", f"format=yuv420p,{fade_filter(total, cfg)}",
         "-af", f"aresample={AUDIO_RATE},apad", "-t", f"{total:.3f}",
         "-c:v", "libx264", "-preset", "veryfast", "-crf", str(CLIP_CRF), "-pix_fmt", "yuv420p",
         "-c:a", "pcm_s16le", "-ac", "2", out])


def zoom_plan(scene, box, total, cfg):
    """zoompan expressions on the supersampled canvas."""
    sc = cfg["screen"]
    sw, sh = cfg["sources"]["screenshot_size"]
    W, H = cfg["output"]["width"], cfg["output"]["height"]
    ss = sc["supersample"]
    CW, CH = W * ss, H * ss
    k = CW / sw
    fps = cfg["output"]["fps"]
    mode = scene.get("zoom", "none")
    if mode == "none":
        return CW, CH, "1", "0", "0"
    if box:
        cx, cy = box["x"] + box["w"] / 2, box["y"] + box["h"] / 2
    else:
        cx, cy = sw / 2, sh / 2
    if mode == "highlight":
        zt = min(sc["max_zoom"], sw / (box["w"] * 1.25 + 80), sh / (box["h"] * 1.25 + 80))
        zt = max(1.0, zt)
        p = f"clip((on/{fps}-{sc['zoom_start']})/{sc['zoom_duration']},0,1)"
    else:  # slow
        zt = sc["slow_zoom"]
        p = f"clip((on/{fps}-0.2)/{max(1.0, total - 0.4):.3f},0,1)"
    e = f"if(lt({p},0.5),4*pow({p},3),1-pow(-2*{p}+2,3)/2)"
    vw, vh = sw / zt, sh / zt
    # Aim a little below the box, so it sits above the player's subtitles.
    cy += vh * 0.05
    x0 = min(max(cx - vw / 2, 0), sw - vw) * k
    y0 = min(max(cy - vh / 2, 0), sh - vh) * k
    z = f"1+{zt - 1:.5f}*{e}"
    return CW, CH, z, f"{x0:.3f}*{e}", f"{y0:.3f}*{e}"


def render_screen(scene, lang, audio, total, out, cfg, diagrams, work):
    W, H = cfg["output"]["width"], cfg["output"]["height"]
    fps = cfg["output"]["fps"]
    shot = validate.screenshot_file(cfg, scene["screenshot"]["app"], scene["screenshot"]["name"], lang)
    box = (scene.get("highlight_override") or {}).get(lang) or scene.get("highlight")
    CW, CH, z, x, y = zoom_plan(scene, box, total, cfg)
    badge = os.path.join(work, "badge.png")
    svg_to_png(cards.badge_svg(scene["actor"], lang, cfg, diagrams), badge)
    inputs = ["-loop", "1", "-framerate", str(fps), "-t", f"{total:.3f}", "-i", shot,
              "-loop", "1", "-framerate", str(fps), "-t", f"{total:.3f}", "-i", badge]
    graph = [f"[0:v]scale={CW}:{CH}:flags=lanczos,format=rgba[base]"]
    if box:
        mask = os.path.join(work, "highlight.png")
        svg_to_png(cards.highlight_svg(box, cfg, CW, CH), mask)
        inputs += ["-loop", "1", "-framerate", str(fps), "-t", f"{total:.3f}", "-i", mask]
        graph.append("[2:v]format=rgba,fade=t=in:st=0.35:d=0.5:alpha=1[hl]")
        graph.append("[base][hl]overlay=0:0:format=auto[lit]")
    else:
        graph.append("[base]null[lit]")
    graph.append(f"[lit]zoompan=z='{z}':x='{x}':y='{y}':d=1:s={W}x{H}:fps={fps},setsar=1[zp]")
    graph.append("[1:v]format=rgba,fade=t=in:st=0.2:d=0.4:alpha=1[bd]")
    graph.append(f"[zp][bd]overlay=0:0:format=auto,format=yuv420p,{fade_filter(total, cfg)}[v]")
    run(["ffmpeg", "-v", "error", "-y", *inputs, "-i", audio,
         "-filter_complex", ";".join(graph), "-map", "[v]", "-map", f"{3 if box else 2}:a",
         "-af", f"aresample={AUDIO_RATE},apad", "-t", f"{total:.3f}", "-r", str(fps),
         "-c:v", "libx264", "-preset", "veryfast", "-crf", str(CLIP_CRF), "-pix_fmt", "yuv420p",
         "-c:a", "pcm_s16le", "-ac", "2", out])


def render_scene(scene, lang, wavs, timeline, total, cfg, diagrams):
    """The cached clip of one scene."""
    inputs = [scene, lang, timeline, total, code_hash(),
              {k: cfg[k] for k in ("output", "fonts", "colors", "timing", "screen", "sources")},
              [file_hash(w) for w in wavs]]
    if scene["type"] == "screen":
        inputs.append(file_hash(validate.screenshot_file(
            cfg, scene["screenshot"]["app"], scene["screenshot"]["name"], lang)))
    if scene["type"] == "flow":
        inputs.append(diagrams)
        inputs.append(file_hash(common.path(cfg["sources"]["diagram_style"])))
    if scene["type"] == "title":
        inputs.append(file_hash(common.path(cfg["sources"]["logo"])))
    key = sha(*inputs)
    out = os.path.join(common.BUILD, "clips", f"{lang}-{scene['id']}-{key}.mkv")
    if os.path.exists(out):
        return out, False
    os.makedirs(os.path.dirname(out), exist_ok=True)
    work = os.path.join(common.BUILD, "work", f"{lang}-{scene['id']}")
    shutil.rmtree(work, ignore_errors=True)
    os.makedirs(work)
    audio = os.path.join(work, "audio.wav")
    scene_audio(wavs, timeline, total, audio)
    tmp = out + ".tmp.mkv"
    fps = cfg["output"]["fps"]
    if scene["type"] == "flow":
        flow.render(scene, timeline, total, lang, cfg, diagrams, work)
        encode_frames(work, audio, total, tmp, cfg)
    elif scene["type"] in ("title", "part"):
        card = cards.TitleCard(scene, cfg) if scene["type"] == "title" else cards.PartCard(scene, cfg)
        cards.write_frames(card, total, fps, work)
        encode_frames(work, audio, total, tmp, cfg)
    else:
        render_screen(scene, lang, audio, total, tmp, cfg, diagrams, work)
    os.replace(tmp, out)
    shutil.rmtree(work, ignore_errors=True)
    # Old clips of this scene are no longer needed.
    for f in os.listdir(os.path.dirname(out)):
        if f.startswith(f"{lang}-{scene['id']}-") and f != os.path.basename(out) \
                and re.fullmatch(rf"{re.escape(lang)}-{re.escape(scene['id'])}-[0-9a-f]{{20}}\.mkv", f):
            os.remove(os.path.join(os.path.dirname(out), f))
    return out, True


# -- assembly -----------------------------------------------------------------

def measure_loudness(concat_list):
    r = run(["ffmpeg", "-hide_banner", "-f", "concat", "-safe", "0", "-i", concat_list, "-vn",
             "-af", "loudnorm=print_format=json", "-f", "null", "-"])
    m = re.search(r"\{[^{}]*\"input_i\"[^{}]*\}", r.stderr, re.S)
    return json.loads(m.group(0))


ISO639_2 = {"en": "eng", "nl": "nld", "de": "deu", "fr": "fra", "es": "spa", "it": "ita"}


def assemble(clips, srt_path, lang, out, cfg):
    """Join the clips, normalize the audio, and add the subtitles as a soft
    track (mov_text) that players can switch on and off."""
    o = cfg["output"]
    lst = out + ".concat.txt"
    with open(lst, "w", encoding="utf-8") as f:
        for c in clips:
            f.write(f"file '{c}'\n")
    m = measure_loudness(lst)
    target = o["loudness"]
    loud = (f"loudnorm=I={target}:TP=-1.5:LRA=11:measured_I={m['input_i']}:measured_TP={m['input_tp']}:"
            f"measured_LRA={m['input_lra']}:measured_thresh={m['input_thresh']}:offset={m['target_offset']}:"
            f"linear=true,aresample={AUDIO_RATE}")
    tmp = out + ".tmp.mp4"
    iso = ISO639_2.get(lang.split("-")[0], "und")
    run(["ffmpeg", "-v", "error", "-y", "-f", "concat", "-safe", "0", "-i", lst, "-i", srt_path,
         "-map", "0:v", "-map", "0:a", "-map", "1:s",
         "-af", loud,
         "-c:v", "libx264", "-preset", o["preset"], "-crf", str(o["crf"]), "-pix_fmt", "yuv420p",
         "-profile:v", "high", "-r", str(o["fps"]),
         "-c:a", "aac", "-b:a", o["audio_bitrate"], "-ac", "2",
         "-c:s", "mov_text", "-metadata:s:s:0", f"language={iso}",
         "-metadata:s:a:0", f"language={iso}",
         "-movflags", "+faststart", "-metadata", f"title={os.path.basename(out)}", tmp])
    os.replace(tmp, out)
    os.remove(lst)


def probe(path):
    r = run(["ffprobe", "-v", "error", "-show_entries", "format=duration:stream=codec_type,width,height",
             "-of", "json", path])
    return json.loads(r.stdout)


def verify(path, expected, cfg, problems):
    info = probe(path)
    dur = float(info["format"]["duration"])
    v = [s for s in info["streams"] if s["codec_type"] == "video"]
    a = [s for s in info["streams"] if s["codec_type"] == "audio"]
    W, H = cfg["output"]["width"], cfg["output"]["height"]
    name = os.path.basename(path)
    if not v or (v[0]["width"], v[0]["height"]) != (W, H):
        problems.append(f"{name}: video is not {W}x{H}")
    if not a:
        problems.append(f"{name}: no audio stream")
    if abs(dur - expected) > 0.25:
        problems.append(f"{name}: {dur:.2f} s long, the scenes add up to {expected:.2f} s")
    if a:
        r = run(["ffmpeg", "-hide_banner", "-i", path, "-vn", "-af", "ebur128", "-f", "null", "-"])
        m = re.findall(r"I:\s+(-?[\d.]+) LUFS", r.stderr)
        if m and abs(float(m[-1]) - cfg["output"]["loudness"]) > 1.5:
            problems.append(f"{name}: loudness {m[-1]} LUFS, target {cfg['output']['loudness']}")
    return dur


# -- per language ---------------------------------------------------------------

def build_language(lang, cfg, diagrams, only_scene=None, jobs=4):
    errors = []
    meta, scenes = common.load_script(lang, cfg, errors)
    if errors:
        raise SystemExit("\n".join(errors))
    if only_scene:
        scenes = [s for s in scenes if s["id"] == only_scene]
        if not scenes:
            raise SystemExit(f"no scene '{only_scene}' in {lang}")
    log(f"[{lang}] narrating {sum(len(segments_of(s)) for s in scenes)} texts")
    wav_list = narrate_all(scenes, lang, cfg, jobs)
    problems, warnings = [], []
    t = cfg["timing"]
    plans, i = [], 0
    for s in scenes:
        n = len(segments_of(s))
        wavs = wav_list[i:i + n]
        i += n
        timeline, total = plan_scene(s, wavs, cfg)
        for (a, b), (say, _) in zip(timeline, segments_of(s)):
            if not t["min_narration"] <= b - a <= t["max_narration"]:
                problems.append(f"{lang}:{s['id']}: narration is {b - a:.1f} s, allowed "
                                f"{t['min_narration']}-{t['max_narration']} s: {say!r}")
        plans.append((s, wavs, timeline, total))
    # Subtitles.
    cues, offset = [], 0.0
    for s, wavs, timeline, total in plans:
        for (a, b), (_, text) in zip(timeline, segments_of(s)):
            # The last cue stays up during most of the pause after the sentence.
            for cue in subs.timed_cues(text, offset + a, offset + b + 0.8 * t["gap"], cfg["subtitles"]["max_chars"]):
                cues.append(cue)
                chars = sum(len(x) for x in cue[2])
                cps = chars / max(0.01, cue[1] - cue[0])
                if cps > cfg["subtitles"]["max_cps"]:
                    warnings.append(f"{lang}:{s['id']}: subtitle at {cps:.1f} characters per second: "
                                    f"{' '.join(cue[2])!r}")
        offset += total
    for w in warnings:
        log(f"warning: {w}")
    if problems:
        raise SystemExit("\n".join(problems))
    # Render, a few scenes at a time.
    log(f"[{lang}] rendering {len(plans)} scenes")
    clips = [None] * len(plans)

    def job(idx):
        s, wavs, timeline, total = plans[idx]
        t0 = time.time()
        clip, made = render_scene(s, lang, wavs, timeline, total, cfg, diagrams)
        log(f"[{lang}]   {s['id']:<22} {total:5.1f} s  {'rendered in %.0f s' % (time.time() - t0) if made else 'cached'}")
        return idx, clip
    with cf.ThreadPoolExecutor(max_workers=max(1, jobs // 2)) as ex:
        for idx, clip in ex.map(job, range(len(plans))):
            clips[idx] = clip
    os.makedirs(common.OUT, exist_ok=True)
    expected = sum(p[3] for p in plans)
    base = f"helpbuoy-{lang}" if not only_scene else f"preview-{lang}-{only_scene}"
    srt_path = os.path.join(common.OUT, f"{base}.srt")
    with open(srt_path, "w", encoding="utf-8") as f:
        f.write(subs.srt(cues))
    with open(os.path.join(common.OUT, f"{base}.vtt"), "w", encoding="utf-8") as f:
        f.write(subs.vtt(cues))
    out = os.path.join(common.OUT, f"{base}.mp4")
    log(f"[{lang}] assembling {os.path.relpath(out, common.ROOT)}")
    assemble(clips, srt_path, lang, out, cfg)
    vproblems = []
    dur = verify(out, expected, cfg, vproblems)
    for p in vproblems:
        log(f"error: {p}")
    if vproblems:
        raise SystemExit(1)
    log(f"[{lang}] ok: {dur:.1f} s, {os.path.getsize(out) / 1e6:.1f} MB")
    return dict(lang=lang, title=meta.get("title", ""), file=os.path.basename(out), duration=round(dur, 2),
                srt=f"{base}.srt", vtt=f"{base}.vtt")


# -- highlight check ------------------------------------------------------------

def highlight_sheet(lang, cfg):
    """videos/out/highlights-<lang>.png: every screen scene of lang with its
    highlight box, to check the boxes against translated screenshots."""
    # Boxes are visual fields, so the master script has them all; this works
    # before a translation exists.
    errors = []
    _, scenes = common.load_script(cfg["master"], cfg, errors)
    if errors:
        raise SystemExit("\n".join(errors))
    work = os.path.join(common.BUILD, "work", f"highlights-{lang}")
    shutil.rmtree(work, ignore_errors=True)
    os.makedirs(work)
    W, H = 640, 360
    tiles = []
    for s in scenes:
        if s["type"] != "screen":
            continue
        shot = validate.screenshot_file(cfg, s["screenshot"]["app"], s["screenshot"]["name"], lang)
        box = (s.get("highlight_override") or {}).get(lang) or s.get("highlight")
        tile = os.path.join(work, f"{len(tiles):02d}.png")
        if box:
            mask = os.path.join(work, f"{len(tiles):02d}-mask.png")
            svg_to_png(cards.highlight_svg(box, cfg, W, H), mask)
            run(["ffmpeg", "-v", "error", "-y", "-i", shot, "-i", mask, "-filter_complex",
                 f"[0]scale={W}:{H}[a];[a][1]overlay,drawtext=text='{s['id']}':x=8:y=8:fontsize=18:"
                 f"fontcolor=white:box=1:boxcolor=black@0.6", "-pix_fmt", "rgb24", "-frames:v", "1", tile])
        else:
            run(["ffmpeg", "-v", "error", "-y", "-i", shot, "-vf",
                 f"scale={W}:{H},drawtext=text='{s['id']}':x=8:y=8:fontsize=18:fontcolor=white:"
                 f"box=1:boxcolor=black@0.6", "-pix_fmt", "rgb24", "-frames:v", "1", tile])
        tiles.append(tile)
    cols = 3
    rows = -(-len(tiles) // cols)
    out = os.path.join(common.OUT, f"highlights-{lang}.png")
    os.makedirs(common.OUT, exist_ok=True)
    run(["ffmpeg", "-v", "error", "-y", "-i", os.path.join(work, "%02d.png"),
         "-vf", f"tile={cols}x{rows}", "-fps_mode", "passthrough", "-frames:v", "1", out])
    shutil.rmtree(work, ignore_errors=True)
    log(f"{os.path.relpath(out, common.ROOT)}")


# -- assets listing -------------------------------------------------------------

def list_assets(cfg):
    diagrams = common.load_diagrams()
    pattern = cfg["sources"]["screenshot"]
    root = pattern.split("{app}")[0]
    print("Screenshots (app/name), with the languages they exist in:")
    for app in sorted(os.listdir(common.path(root))):
        base = common.path(pattern.split("{lang}")[0].format(app=app))
        if not os.path.isdir(base):
            continue
        names = {}
        for lang in sorted(os.listdir(base)):
            for f in sorted(os.listdir(os.path.join(base, lang))):
                if f.endswith(".png"):
                    names.setdefault(f[:-4], []).append(lang)
        for name, langs in sorted(names.items()):
            flag = "  (out of scope, do not use)" if f"{app}/{name}" in cfg.get("forbidden_screenshots", []) else ""
            print(f"  {app}/{name}: {', '.join(langs)}{flag}")
    print("\nDiagram nodes (name: icon, label):")
    for name, n in diagrams["nodes"].items():
        print(f"  {name}: {n['icon']}, {common.localized(n['label'], cfg['master'], cfg['master'])}"
              + (f" (screen actor, badge app: {n['badge_app']})" if n.get("badge_app") else ""))
    print(f"\nIcons: {', '.join(sorted(flow.symbol_ids(cfg)))}")
    print(f"Colours: {', '.join(cfg['colors'])}")
    print(f"Languages: {', '.join(cfg['languages'])} (master: {cfg['master']})")
    print("Scene types: title, part, screen, flow. Zoom: none, highlight, slow.")


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--lang", action="append", help="build only this language (repeatable)")
    ap.add_argument("--scene", help="render only this scene id, to videos/out/preview-<lang>-<id>.mp4")
    ap.add_argument("--list-assets", action="store_true", help="print screenshots and diagram nodes")
    ap.add_argument("--highlights", action="store_true",
                    help="write videos/out/highlights-<lang>.png to check the highlight boxes")
    ap.add_argument("--jobs", type=int, default=max(2, min(8, (os.cpu_count() or 4) // 2)),
                    help="parallel narration and render jobs")
    args = ap.parse_args()
    cfg = common.load_config()
    if args.list_assets:
        list_assets(cfg)
        return
    langs = args.lang or cfg["languages"]
    if args.highlights:
        for lang in langs:
            highlight_sheet(lang, cfg)
        return
    errors, warnings = validate.validate(cfg, langs)
    for w in warnings:
        log(f"warning: {w}")
    if errors:
        for e in errors:
            log(f"error: {e}")
        raise SystemExit(f"{len(errors)} error(s); fix the script and build again")
    diagrams = common.load_diagrams()
    for lang in langs:
        build_language(lang, cfg, diagrams, args.scene, args.jobs)


if __name__ == "__main__":
    main()
