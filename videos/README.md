# HelpBuoy instruction videos

One video per language, about three and a half minutes, built from plain
text: the store screenshots, animated flow diagrams, spoken narration and
subtitles that the viewer can switch off. `plan.md` explains the design.

## Setup (once)

```bash
sudo apt install ffmpeg python3-yaml fonts-noto-core
pip install edge-tts                   # or: python3 -m venv videos/.venv && videos/.venv/bin/pip install edge-tts
```

The narration uses the online Microsoft Edge voices through `edge-tts`
(from PATH, else `videos/.venv/bin/edge-tts`), so the first build of a text
needs a network connection. The voices are listed in `config.yaml`.

## Build

```bash
make video                                                  # everything
python3 videos/tools/build.py --lang nl-nl                  # one language
python3 videos/tools/build.py --lang en-us --scene flow-register   # preview one scene
python3 videos/tools/build.py --highlights --lang de-de     # check highlight boxes
python3 videos/tools/validate.py                            # check the scripts only
```

Output in `videos/out/`: `helpbuoy-<lang>.mp4` (with a soft subtitle stream),
`.srt` and `.vtt`. Narration and clips are cached in `videos/build/`, so a
rebuild only renders what changed.

## Files

| file | what |
|---|---|
| `config.yaml` | size, fonts, colours, timing, voices, scope rules |
| `diagrams.yaml` | diagram nodes, positions and labels per language |
| `script/en-us.yaml` | master script: scenes, visuals and English text |
| `script/<lang>.yaml` | translations, text fields only |
| `script/_schema.md` | the script format |
| `script/_llm_instructions.md` | what to give an LLM that edits or translates the script |
| `tools/build.py` | the pipeline |
| `tools/flow.py` | SVG frames of the flow diagrams |
| `tools/cards.py` | title and part cards, badge, highlight mask |
| `tools/subs.py` | subtitle cues, WebVTT and SRT |
| `tools/validate.py` | script, asset and tool checks |
