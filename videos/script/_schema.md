# Video script format

One YAML file per language in `videos/script/`. `en-us.yaml` is the **master**:
it holds the order of the scenes and every visual field. The other languages
hold **only text fields**, keyed by scene id, in the same shape as the master.
`python3 videos/tools/validate.py` checks all of this and says exactly what is
wrong.

## Master file

```yaml
meta:
  title: "How HelpBuoy works"   # title of the video
  lang: en-us

scenes:
  - id: intro                   # lowercase letters, digits and hyphens; unique
    type: title
    ...
```

Every scene lasts as long as its narration plus the pauses in
`config.yaml` (`timing`). Nobody types a duration.

### `title`

Logo and large text on the navy background. Used for the intro and the outro.

| field | | |
|---|---|---|
| `text` | text, required | the large line |
| `note` | text | a smaller amber line under it |
| `say` | text, required | narration, also the subtitle |
| `subtitle` | text | subtitle instead of `say` |

### `part`

A numbered card between parts.

| field | | |
|---|---|---|
| `number` | number | shown in the red circle |
| `text` | text, required | the heading |
| `note` | text | amber pill under the heading, e.g. "first time only" |
| `say`, `subtitle` | text | as above |

### `screen`

A screenshot, scaled to 720p, with a badge saying whose screen it is, an
optional highlight box and an optional zoom.

| field | | |
|---|---|---|
| `actor` | `user` or `techie` | the badge: node label and its app (diagrams.yaml `badge_app`) |
| `screenshot` | `{ app, name }` | `msstore/<app>/screenshots/<lang>/<name>.png`, in every language |
| `highlight` | `{ x, y, w, h }` | box in pixels of the 1920x1080 screenshot; the rest is dimmed |
| `highlight_override` | `{ <lang>: { x, y, w, h } }` | another box for one language, when its layout differs |
| `zoom` | `none`, `highlight`, `slow` | `highlight` zooms in on the box after about a second (up to 1.5x, which is 1:1 with the source); `slow` zooms slowly toward the box or the centre |
| `say`, `subtitle` | text | as above |

`helpbuoy-desk/3-server` is out of scope and refused.

### `flow`

An animated diagram. The nodes listed in `show` appear, then every step draws
its arrows while its narration plays.

| field | | |
|---|---|---|
| `title` | text | heading in the top left corner |
| `show` | list of node names | nodes from `diagrams.yaml`: `user`, `client`, `server`, `desk`, `techie` |
| `steps` | list, required | one narration each, in order |

A step:

| field | | |
|---|---|---|
| `say` | text, required | narration and subtitle of this step |
| `subtitle` | text | subtitle instead of `say` |
| `from`, `to` | node names | shorthand for one arrow; the other arrow fields go next to them |
| `arrows` | list of arrows | several arrows drawn at the same time |
| `highlight` | list of node names | nodes that pulse, for a step without arrows |
| `clear` | `true` | fade out everything drawn by earlier steps |

An arrow:

| field | | |
|---|---|---|
| `from`, `to` | node names, required | both in `show` |
| `through` | node name | route via this node, e.g. `server` |
| `label` | text, required | the text beside the arrow: what flows |
| `via` | text | second line under the label: how it travels; makes the arrow dashed amber (outside the apps, e.g. email or phone) |
| `loop` | `true` | dots keep moving along the arrow until the scene ends |
| `color` | colour name | instead of the sender's colour (config.yaml `colors`) |
| `offset` | pixels | distance right of the centre line (default 11); opposite arrows never overlap |
| `label_at` | 0..1 | where the label sits along the arrow (default 0.5, or 0.25 with `through`); it is always on the right of the direction of travel, next to the line |
| `label_dx`, `label_dy` | pixels | nudge the label |

An arrow draws in during its sentence and is complete shortly before the
sentence ends. When the next step starts, finished arrows turn grey;
looping arrows keep their colour.

## Translation files

Same scene ids, same order of steps and arrows, only these text fields:
`say`, `subtitle`, `text`, `note`, `title`, `label`, `via`. A scene with steps
has a `steps` list of the same length, a step with `arrows` an `arrows` list
of the same length.

```yaml
meta:
  title: "Zo werkt HelpBuoy"
  lang: nl-nl

scenes:
  intro:
    text: "Zo werkt HelpBuoy"
    say: "Welkom. ..."
  flow-send-key:
    title: "De sleutel versturen"
    steps:
      - label: "publieke sleutel"
        via: "per e-mail of chat"
        say: "De gebruiker stuurt deze sleutel naar de techie, ..."
  flow-summary:
    steps:
      - arrows:
          - { label: "publieke sleutel", via: "één keer" }
          - { label: "registreren" }
        say: "..."
```

## Checks

`validate.py` (also run by `build.py`) fails on:

- YAML errors, unknown fields, missing or extra scenes, steps or arrows
- visual fields in a translation, missing text fields
- screenshots that do not exist in a language, or are not 1920x1080
- highlight boxes outside 1920x1080
- arrows or highlights with nodes that are not in `show`
- out of scope screenshots or words (`config.yaml` `forbidden_*`)
- subtitle lines over 42 characters, cues over 2 lines

`build.py` then fails when a narration is shorter than 2 or longer than 20
seconds, warns when a subtitle needs more than 17 characters per second, and
checks the length, size, audio and loudness of the result.
