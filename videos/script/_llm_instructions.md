# Instructions for an LLM editing the video scripts

Give this file to the LLM together with `_schema.md`, the script files it
should change, and the output of:

```bash
python3 videos/tools/build.py --list-assets
```

## The task

You write or change the narration and structure of the HelpBuoy instruction
video. The video explains how HelpBuoy works for two people: the **user**
who needs help (the HelpBuoy client on their computer) and the **techie**
who helps (the HelpBuoy Desk app). Everything goes through **the server**,
which is managed for them. The video itself never names the brand (see
Style).

Your output is one or more complete YAML files that pass
`python3 videos/tools/validate.py`. Return whole files, not fragments.

## What the screenshots show

From `msstore/README.md`; all are 1920x1080, in every language.

| screenshot | shows |
|---|---|
| `helpbuoy/1-connect` | first start: the server address field and Connect |
| `helpbuoy/2-warning` | first connection: "Only continue if someone you know personally gave you this address", Continue |
| `helpbuoy/3-key` | the key of this computer, Copy key, fingerprints, waiting for a techie to add it |
| `helpbuoy/4-lock` | after registration: "Get help only through Example Helpdesk?", Lock and Not now |
| `helpbuoy/5-waiting` | waiting for a techie: name, helpdesk name, phone number, Disconnect |
| `helpbuoy/6-allow` | "Allow "Sam" to take control of this computer?", Allow and Cancel |
| `helpbuoy-desk/1-computers` | the techie's list of computers per customer, with Connect |
| `helpbuoy-desk/2-session` | a session: the user's desktop inside HelpBuoy Desk, End session |
| `helpbuoy-desk/4-register` | Register computer: paste the key, Register |
| `helpbuoy-desk/3-server` | **out of scope, never use** |

Demo data: the computer is anna@ANNA-PC, the helpdesk "Example Helpdesk",
the techie "Sam".

## The true flows

Only these flows may appear in diagrams (from `PLAN.md`, "First contact" and
"Session"):

1. The client connects to the server with its own key. The server does not
   know the key yet, so the computer waits (pending).
2. The user copies the public key and sends it to the techie outside
   HelpBuoy, by email or chat. The key is not secret.
3. The techie pastes the key in HelpBuoy Desk (Register computer). The desk
   registers it on the server. The server tells the computer "registered";
   the computer then stores the server address and server key.
4. The client may ask to lock to this helpdesk (Lock or Not now).
5. Every time: the user asks for help by phone or email. While the client is
   open, the computer is online in the techie's list.
6. The techie presses Connect. The request goes through the server to the
   computer. The user chooses Allow or Cancel. Only after Allow does the
   session start.
7. During a session only the screen image goes to the techie, and only mouse
   and keyboard input comes back. No files, no other access. Esc on the
   user's keyboard takes back control; either side can end the session.

Do not invent other flows, features or guarantees.

## Scope

- No server setup or administration: no commands, no installation, no
  `helpbuoy-server`, no Desk built-in server. The server is "the HelpBuoy
  server", managed for you.
- How a techie gets access to the server is not covered.

## Style

The narration is read by a speech synthesizer. Write it the way a
friendly presenter speaks in a typical instruction video: familiar phrases
("Welcome. This video shows you...", "First, let's...", "Next, ...",
"Finally, ...", "To sum up, ...", "Thanks for watching."), natural word order,
and sentences that flow into each other.

- No brand names in any text of a scene: not HelpBuoy, not HelpBuoy Desk.
  Say what a thing is: the app (on the user's computer), the desk app
  (the techie's app), the server. Only the outro note shows the website
  address. `validate.py` refuses the words in `config.yaml` `brand_words`.
- Common, everyday words, no jargon: "your key", not "public key";
  "mouse and keyboard actions", not "input".
- Natural sentences of usual length, about 8 to 20 words. Join short ideas
  with "and", "but", "so" or a comma, as people do when they talk. Avoid
  choppy fragments ("Part one. Register your computer.") and avoid long
  chains of clauses. A comma is a short pause: put it where a speaker
  breathes.
- Write it as one spoken story, not as separate captions. The voice reads
  a whole part of the video in one go, so each line should pick up where
  the previous one stopped: "Then", "In the meantime", "After that",
  "That brings us to". Read a part aloud from start to end to check it.
- No one or two word sentences ("Welcome.", "Done."), the voice stresses
  them wrongly. A short sentence is fine when it leads into the next one
  ("Let's start with who is who.").
- Name what a thing is when you mention it: "click the Connect button",
  "press the Escape key", never "click Connect" or "press Escape".
- Do not squeeze two ideas into one sentence with "which", "where" or
  "by doing". Two plain sentences sound better than one contorted one.
- Contractions are fine and sound natural ("that's", "doesn't", "it's").
- End every `say` with a full stop or a question mark, never a comma: each
  step is spoken on its own, so a sentence must not run over into the next
  step.
- No colons, semicolons, brackets, quotes or slashes in `say`; the voice
  reads them badly.
- Describe both people in the third person: "the user" (who needs help)
  and "the techie". Never address the viewer as the person who gets help
  ("you", "your computer"); the viewer may be either of them. Say "the
  computer", "the key", "the user's screen".
- At most about 30 words per `say`. A narration lasts 2 to 20 seconds.
- Write numbers and keys the way they are spoken ("Escape", not "Esc").
  Use `subtitle` if the text on screen must differ.
- Arrow labels are two or three words: what flows. `via` says how.

## Terminology

- Use the words of the apps' own translations in `proto/i18n/locales/<lang>.json`
  and `msstore/*/listing/<lang>/`. Button names exactly as in that language's
  screenshots: e.g. Connect is Verbinden (nl, de), Se connecter (fr), Conectar
  (es), Connetti (it).
- techie: techie (en, nl), Techniker (de), technicien (fr), técnico (es),
  tecnico (it).
- app, desk app and server as in `diagrams.yaml` (the node labels).
- Register like the apps: formal Sie (de) and vous (fr); informal je (nl),
  tú (es) and tu (it).

## Translating

- Copy the scene ids and the order of steps and arrows from `en-us.yaml`.
- Translate only `say`, `subtitle`, `text`, `note`, `title`, `label` and `via`.
- Never copy visual fields (`type`, `screenshot`, `highlight`, `show`, `from`,
  `to`, ...) into a translation; the build takes them from the master.
- If a screenshot's layout differs in a language so the highlight box misses,
  add `highlight_override: { <lang>: {...} }` to the master scene. Check boxes
  with `python3 videos/tools/build.py --highlights --lang <lang>`, which
  writes `videos/out/highlights-<lang>.png`.

## Loop

1. Change the YAML.
2. `python3 videos/tools/validate.py` and fix every error it prints.
3. `python3 videos/tools/build.py --lang <lang> --scene <id>` to preview one
   scene, or `--lang <lang>` for the whole video.
