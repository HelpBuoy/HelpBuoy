"""Subtitle cues: split narration into cues of at most two balanced lines,
and write them as WebVTT (the player's subtitle track) and SRT (muxed into
the MP4 as a soft subtitle stream, and published for other players)."""
import re

PUNCT = re.compile(r"[,.;:!?]$")


def wrap(text, max_chars):
    """One line if it fits, else two lines broken near the middle, else
    greedy lines (which the validator reports as too many)."""
    text = " ".join(text.split())
    if len(text) <= max_chars:
        return [text]
    words = text.split(" ")
    best = None
    for i in range(1, len(words)):
        a, b = " ".join(words[:i]), " ".join(words[i:])
        if len(a) <= max_chars and len(b) <= max_chars:
            # Prefer balanced lines, and a break after punctuation.
            score = abs(len(a) - len(b)) - (6 if PUNCT.search(a) else 0)
            if best is None or score < best[0]:
                best = (score, [a, b])
    if best:
        return best[1]
    lines, cur = [], ""
    for w in words:
        if cur and len(cur) + 1 + len(w) > max_chars:
            lines.append(cur)
            cur = w
        else:
            cur = f"{cur} {w}".strip()
    lines.append(cur)
    return lines


def split_cues(text, max_chars, max_lines=2):
    """Split text into cues that each fit max_lines lines, preferring to end a
    cue at the end of a sentence or clause."""
    words = " ".join(text.split()).split(" ")
    cues, cur = [], []
    limit = max_chars * max_lines - 4
    for w in words:
        cand = " ".join(cur + [w])
        if cur and (len(cand) > limit or len(wrap(cand, max_chars)) > max_lines):
            # Move back to the last punctuation in the cue, if it is not too early.
            cut = None
            for j in range(len(cur) - 1, 0, -1):
                if PUNCT.search(cur[j - 1]) and len(" ".join(cur[:j])) >= max_chars * 0.6:
                    cut = j
                    break
            if cut:
                cues.append(" ".join(cur[:cut]))
                cur = cur[cut:] + [w]
            else:
                cues.append(" ".join(cur))
                cur = [w]
        else:
            cur.append(w)
    if cur:
        cues.append(" ".join(cur))
    return cues


def timed_cues(text, start, end, max_chars):
    """Cues for one narration, its time shared by character count."""
    cues = split_cues(text, max_chars)
    total = sum(len(c) for c in cues) or 1
    out, t = [], start
    for c in cues:
        d = (end - start) * len(c) / total
        out.append((t, t + d, wrap(c, max_chars)))
        t += d
    return out


def _ts(t, sep=","):
    ms = int(round(t * 1000))
    h, ms = divmod(ms, 3600000)
    m, ms = divmod(ms, 60000)
    s, ms = divmod(ms, 1000)
    return f"{h:02d}:{m:02d}:{s:02d}{sep}{ms:03d}"


def srt(cues):
    out = []
    for i, (a, b, lines) in enumerate(cues, 1):
        out.append(f"{i}\n{_ts(a)} --> {_ts(b)}\n" + "\n".join(lines) + "\n")
    return "\n".join(out)


def vtt(cues):
    out = ["WEBVTT", ""]
    for a, b, lines in cues:
        out.append(f"{_ts(a, '.')} --> {_ts(b, '.')}")
        out.extend(line.replace("-->", "->") for line in lines)
        out.append("")
    return "\n".join(out)
