# help-me-win: self-hosted remote support over a custom Go SSH relay

A self-hosted alternative to TeamViewer QuickSupport. The relay is **a single Go binary
(`hmw-relay`) that speaks SSH on a single port** (default 2222). Everybody, helpers and clients,
connects to that same `hostname:port`. **The public key a connection authenticates with is its identity**; the SSH
user name is ignored. Keys are long-lived: they are generated once on first start and reused
for every session.

The relay is not OpenSSH. It has no shell, no filesystem access, no file transfer and no TCP
forwarding to real hosts. It only authenticates keys, tells helpers and clients about each
other, and splices their channels together.

- **relay/**: `hmw-relay`, a plain executable that ships next to the helper app and runs
  locally on the helper's machine as a normal user process, on one port (default `:2222`). It
  keeps helpers and registered clients in a small state directory. No service, no container.
- **helper/**: the cross-platform raylib app the *helper* runs (`raylib-go`, no cgo). Many helpers can be online at
  once, and each helper can run sessions with many clients at once.
- **client-windows/**: the windigo app the person *receiving* help runs. On first start it asks
  for the relay's `hostname:port` and shows its own application-specific SSH public key
  (generated once on first start, kept in AppData) with a **Copy** button, so the user can
  send it to the helper over a chat such as WhatsApp. After the first successful connection
  the address is stored, and later starts go straight to the relay. Once a helper has registered that key, the client shows up as online for all
  helpers. A session only ever starts from the client: the user presses **Request help**,
  and helpers see the request and can open a session for it. Talking about the problem
  happens over WhatsApp or the phone, not in the apps.
  A client is helped by **one helper at a time**; a helper can help many clients at once.

What a session can do, and nothing more:
- **client → helper**: GDI-captured screen updates and the cursor shape.
- **helper → client**: mouse and keyboard input.
- No file transfer, no clipboard, no shell, no audio.

Performance target: **as fast as RDP** for normal desktop work (section 5).

---

## 0. Deployment model and constraints

- **Relay**: the `hmw-relay` executable (`hmw-relay.exe` on Windows) in the same folder as the
  helper app, on the helper's own Linux, macOS or Windows machine. It is started from a
  terminal or by the helper app (7.1) and runs as the logged-in user. Clients reach it on that
  machine's address, which usually means one port forward on the router (TCP 2222) and
  allowing the OS firewall prompt on first start. An existing OpenSSH on port 22 is unrelated
  and untouched.
- **One port for everyone**: the operator picks it once (`hmw-relay serve --listen :2222`, the
  default). Helpers and clients all use the same `hostname:port`; there are no per-helper or
  per-session ports. The default is above 1024, so the relay needs no root and no
  capabilities.
- **Runs unprivileged**: no root, no admin, no service registration, no Docker image. The
  relay runs as long as its process runs; when the machine or the process stops, clients and
  other helpers simply see the relay as unreachable until it is started again.
- **Identity = key**. The relay maps each public key to exactly one of: helper, registered
  client, or unknown. User names in the SSH handshake are ignored (clients send `sv`).
- **One group per relay**: every helper on a relay sees every registered client and every
  help request. Separate groups of helpers use separate relays.

### Stored client configuration

The client is one generic exe; nothing is embedded or preconfigured. The only thing the user
types is the relay's `hostname:port`. Everything else is learned on the first successful
connection and stored in `%LOCALAPPDATA%\help-me-win\config.json`:

| Field | Example | Learned |
|---|---|---|
| relay address | `relay.example.com:2222` | typed by the user on first start |
| relay host key | `ssh-ed25519 AAAA...` (`SHA256:3q2+7w...`) | first successful connection, pinned from then on |
| known helpers | `alice` → `ssh-ed25519 AAAA...` | each helper's key, pinned on the first session with that helper (3.5) |

The key is stored next to it, as its own application-specific key pair (6.1). Adding or removing a helper never touches
the client: it trusts the helpers the relay announces. "Reset" in the client's
about box forgets this configuration and starts the first-run flow again; it never touches
the key.

---

## 1. Architecture

```
 Helpers (many)                  hmw-relay host:port                       Clients (many)
┌──────────────┐               ┌────────────────────────────────┐               ┌──────────────────┐
│ raylib helper│── key: alice ▶│ auth: key -> helper / client / │◀ key: anna-pc │ client (windigo) │
│ own app key, │               │       unknown                  │               │ persistent key   │
│ added by     │◀── presence ──│ registry: online helpers,      │◀─ requests ── │ (own app key)    │
│ relay admin  │── register ──▶│   online clients               │               │ sv-control       │
│              │── open     ──▶│ help requests                  │               │                  │
│ accepts      │◀─ sv-session ─│ splice: one channel pair per   │◀─ sv-session ─│ opens a session  │
│ sv-session   │               │   helper<->client session      │               │ per opened       │
│ (one per     │               │                                │               │ helper           │
│ client)      │               └────────────────────────────────┘               │                  │
│ inner SSH    │ ═════════════ inner SSH through each spliced channel ═══════════│ inner SSH server │
│ client, pins │     channels: screen (client→helper), input (helper→client)     │ host key = its   │
│ client key   │                                                                 │ key; checks the  │
└──────────────┘                                                                 │ helper key       │
┌──────────────┐                                                                 └──────────────────┘
│ helper (bob) │── ... same, any number of helpers and sessions in parallel ...
└──────────────┘
```

### First contact (once per client key)

1. The client starts for the first time and asks for the relay's `hostname:port` (the helper
   tells the user what to type, e.g. `relay.example.com:2222`). On this first start
   it generates its own Ed25519 key pair in
   `%LOCALAPPDATA%\help-me-win\` (section 6.1), with the comment `user@machine`, e.g.
   `anna@ANNA-LAPTOP`, so the public key line reads `ssh-ed25519 AAAAC3Nz... anna@ANNA-LAPTOP`.
2. The client connects to that address with the key. It shows the relay's host key
   fingerprint and pins it (trust on first use). The key is unknown to the relay, so the
   connection is **pending**: it can't open anything, it only waits for `Registered`. If the
   connection fails (wrong address, nothing listening, not an hmw-relay), the client says so and
   lets the user correct the address; nothing is stored yet.
3. Once connected, the client shows the public key line (the contents of the `.pub` file)
   with a **Copy key** button and its fingerprint. The user pastes it into a chat with the
   helper (WhatsApp, Signal, mail). The key is public, so the chat needs no secrecy,
   only the assurance that the message really comes from the user.
4. The helper pastes the key line into the helper app (7.2) and, if in doubt, compares the
   fingerprint with the user by phone. The client is registered under the key's comment
   (`user@machine`); there is no separate label to type. The relay stores it permanently
   as a registered client and sends the client `Registered`. The client now stores the
   relay address and relay host key (the stored configuration above) and switches to its normal window.
5. From now on this key **is** `anna@ANNA-LAPTOP` on this relay. It stays registered until a
   helper or admin removes it: no expiry, and restarts, reboots and network changes don't
   matter. Later starts skip all of the above: the client reads its configuration, connects,
   and is ready to file a request. The key is never regenerated automatically; it is
   only removed when the user explicitly does so in the app (6.1).

### Session (every time)

The apps carry no text between user and helper. Explaining the problem, and saying it's
fixed, happens over WhatsApp or the phone. The apps also show nothing about sessions starting
or ending.

1. Client and helpers are connected to the relay with their persistent keys. Helpers see
   `anna@ANNA-LAPTOP` in their list of connected clients.
2. **The user presses Request help** in the client, usually while on the phone or in a
   WhatsApp chat with the helper. Helpers can't start a session on their own; there is no
   Connect button on the helper side.
3. **Helpers see the request** on that client's row in the helper app (7.2), with a
   notification if the app isn't focused, and **Open session**. Requests stay visible to
   helpers who come online later. The first helper to open it takes it; for the others the
   row just loses its button. The user can **Cancel** an open request; requests nobody opens
   expire after 1 hour.
4. The client opens a `hmw-session@help-me-win` channel for that helper. The relay opens a
   matching channel to the helper's connection and splices the two (3.6).
5. **Inner SSH** runs over the spliced channel (section 4). The client is the SSH server with
   its persistent key as host key, the helper is the SSH client with its own key, which the relay
   announced in `SessionReady`. Both check the other end's key (3.5).
6. **The client window minimizes**, without any banner or status text, so the helper sees
   the user's desktop and can do the work.
7. **The helper ends the session** when done, with **End session** in the viewer or by
   closing the session's tab. The client window comes back, ready for the next request.
8. If more is needed later, the user presses **Request help** again: a new request and a new
   session.
9. **One helper per client.** While a session runs, no other helper can connect to that
   client, and the client can't file a new request.
10. **Other ways a session ends**: Esc on the client's own keyboard (the user takes back
   control), exiting the client application (which also takes the client offline), or
   revoking the client key. There is no Disconnect button on the client. The relay
   connections stay up for the next session.

The relay only passes the inner SSH stream through. Nobody on the relay, root included, can
read or alter screen data or keystrokes.

---

## 2. Repository layout

```
help-me-win/
├── go.work                      # use ./proto ./relay ./helper ./client-windows
├── PLAN.md
├── proto/                       # shared, pure Go, no GUI deps
│   ├── go.mod
│   ├── relayaddr/               # parse and normalize hostname:port (default port 2222)
│   ├── identity/                # Ed25519 key gen, OpenSSH format, fingerprints,
│   │                            # parsing of pasted keys
│   ├── appkey/                  # create-once, load-only app key in OpenSSH format
│   ├── control/                 # sv-control messages (both sides <-> relay)
│   ├── inner/                   # inner SSH server/client, pinning, channel plumbing
│   ├── screen/                  # screen stream messages, tile codecs, tile cache
│   ├── input/                   # input messages, W3C key codes
│   └── wire/                    # length-prefixed framing
├── relay/                       # hmw-relay
│   ├── go.mod
│   ├── cmd/hmw-relay/             # "serve" plus admin subcommands (3.2)
│   ├── hostkey/
│   ├── store/                   # state.json: helpers, clients; atomic writes
│   ├── auth/                    # key -> role lookup
│   ├── registry/                # presence, pending keys, session requests
│   ├── splice/                  # session channel pairing, large-buffer copy
│   ├── limits/                  # per-key then per-IP limits, caps, timeouts
├── helper/                      # raylib helper app (Linux / macOS / Windows)
│   ├── go.mod
│   ├── identity/                # app key (via proto/appkey)
│   ├── relayconn/               # login, reconnect, control channel
│   ├── session/                 # inner SSH client, decoder, input sender
│   ├── render/                  # GPU texture upload of dirty rects (5.5)
│   ├── ui/                      # minimal immediate-mode widgets, layout, fonts, DPI
│   ├── views/                   # client list, session tabs and viewer, register, settings
│   └── keymap/                  # raylib key codes -> W3C code + scan code
└── client-windows/              # windigo app, Windows 10 2004+ / Windows 11
    ├── go.mod
    ├── resources/               # manifest (PerMonitorV2 DPI, comctl6), icon, .syso
    ├── identity/                # app key in %LOCALAPPDATA%\help-me-win (via proto/appkey)
    ├── config/                  # first-run address entry, stored config.json
    ├── capture/                 # GDI capture, damage detection, scroll detection
    ├── encode/                  # tile classification and encoding, per-viewer state
    ├── injector/                # SendInput, key mapping, Esc hook
    ├── session/                 # relay connection, inner SSH server, the one viewer
    └── ui/                      # main window, request form
```

---

## 3. The relay (`relay/`)

### 3.1 Roles and what they may do

The relay is deny-by-default. The role comes from the key alone.

| Role | Key is | Allowed |
|---|---|---|
| **helper** | listed as a helper key in `state.json` (plain Ed25519 public key, no certificates) | one `sv-control` channel; incoming `sv-session` channels; control messages |
| **client** | registered | one `sv-control` channel; one open help request at a time; outgoing `sv-session` channels, only toward the helper that opened its current request, and only one session at a time |
| **pending** | unknown | nothing but receiving `Registered`; not shown to any helper (helpers register keys from the pasted key line, never from a list); idle timeout 24 h |

Not implemented at all: `session`, `shell`, `exec`, `subsystem`, `pty`, `direct-tcpip`,
`tcpip-forward`, `streamlocal`, agent and X11 forwarding. The attack surface is the SSH
transport in `x/crypto/ssh`, the auth callback, the control parser and the splice.

A key can't be both helper and client.

### 3.2 State and admin

State directory in the user's data directory, next to the helper app's own data:
`~/.local/share/hmw-relay` on Linux, `~/Library/Application Support/hmw-relay` on macOS,
`%LOCALAPPDATA%\hmw-relay` on Windows (override with `--state`). It holds `hostkey_ed25519` (0600) and `state.json` with helpers
(name, display name, public key) and registered clients (key, `user@machine` name,
registered by, registered at, last seen). Client registrations never expire; they are removed
only by `UnregisterClient` or `hmw-relay client remove`. Writes are temp file + `fsync` + `rename`.

| Command | What it does |
|---|---|
| `hmw-relay serve [--listen :2222]` | run the relay on its single port |
| `hmw-relay helper add alice --display "Alice" 'ssh-ed25519 ...'` | add a helper with the public key copied from their helper app |
| `hmw-relay helper remove alice` | remove a helper; takes effect immediately, open sessions are closed |
| `hmw-relay helper list` / `client list` / `client remove SHA256:...` | inspect and revoke |
| `hmw-relay hostkey` | print the host key fingerprint, for helpers to compare with what clients show |

**Helpers are plain keys**: there are no certificates. A new helper sends
their public key line to the relay admin, who adds it with `hmw-relay helper add`. Removing it
with `hmw-relay helper remove` is the whole revocation story. The relay watches `state.json` and
reloads it when the CLI changes it (no signals, so it works the same on Windows).

### 3.3 Control channel

`hmw-control@help-me-win`, framed messages (`proto/control`): each is a `proto/wire` frame
whose type byte names the message and whose payload is JSON. Control traffic is small and
rare, so JSON keeps it easy to debug; the screen and input streams use binary payloads.
Helper commands carry a sequence number and the relay answers each with a `Reply`.

| Message | Direction | Content |
|---|---|---|
| `Hello` | relay → any | role, relay time, limits |
| `ClientHello` | client → relay | app version, current `user@machine` (the registered name comes from the key comment) |
| `Presence` | relay → helpers | full snapshot after every change: registered clients (`user@machine`, fingerprint, online, open or taken request with id and time, in session with whom) and helpers |
| `RegisterClient` / `UnregisterClient` | helper → relay | public key line or fingerprint (name taken from the key comment, or given when the comment is empty) |
| `Reply` | relay → helper | answer to a helper command: sequence number and error text if it failed |
| `Registered` | relay → client | registered `user@machine` |
| `RequestHelp` | client → relay | (no content) |
| `CancelRequest` | client → relay | request id |
| `RequestStateMsg` | relay → client | request id and state (open / taken / cancelled / expired / ended) |
| `OpenRequest` | helper → relay | request id; the relay lets only the first helper win |
| `SessionReady` | relay → client | request id, helper name, display name and public key: open the `sv-session` now |
| `Error` | relay → any | reason, sent just before the relay closes a connection on purpose |

Session channels carry small SSH-encoded extra data: the client's open names the request id;
the relay's open towards the helper adds the client's name, key line and current
`user@machine`, so the helper app can pin the client key before the inner handshake.

### 3.4 Abuse limits

Limits are keyed on the authenticated key first and on the source IP only after that, so
many legitimate clients behind one NAT or company proxy never block each other. A key counts
as proven only after its signature has been verified in the SSH handshake.

1. **Known keys** (helpers and registered clients): no rate limit and no IP limit, only the
   role caps below. A known key that reconnects replaces its previous connection.
2. **Per unknown key** (pending): max 1 connection, max 10 connection attempts per hour,
   24 h idle timeout. Exceeding it blocks that key for 1 hour, not the IP.
3. **Per source IP, for unknown keys only**: since anyone can generate keys, the IP is the
   backstop. Max 5 distinct unknown keys per IP per hour and max 10 pending connections per
   IP; beyond that, new unknown keys from that IP are refused for 1 hour, while known keys from
   the same IP still get in.
4. **Per source IP, before any key is proven** (scanners, failed handshakes, wrong
   signatures): max 10 concurrent handshakes, 30 failed handshakes per hour, then backoff.
5. **Relay-wide**: max 200 pending connections in total.

Role caps and timeouts:
- Per helper: max 20 concurrent sessions. Per client: exactly one session at a time, enforced by the relay and again by the client.
- Handshake timeout 10 s, keepalive 15 s, dead connections dropped after 3 missed.
- Control frames are length-checked before they are read: 16 KiB from clients and pending
  connections, 256 KiB from helpers, so an unauthenticated peer can't make the relay buffer
  large frames.
- After 30 failed handshakes in an hour an IP is blocked for 1 minute, doubling up to 1 hour.
  This also affects known keys from that IP, since no key is proven at that point.
- JSON log to stdout and to `relay.log` in the state directory (rotated at 10 MB): auth result, role, fingerprints, source IP, session start/end,
  byte counts. Never payload.

### 3.5 Trust model

- **The relay is trust on first use (TOFU)**, for clients and helpers alike, like `ssh` with
  `known_hosts`. Its host key is pinned on the first successful connection. Someone who
  controls the network at exactly that moment could substitute their own relay. Mitigation:
  the client shows the relay fingerprint, and the helper app shows it next to the key it just
  registered, so it can be compared on the same call. After that any change is a hard error
  ("relay identity changed"), never a silent update.
- **Helpers are vouched for by the relay.** Without certificates the client has no
  independent way to know the helpers, so it accepts the helper key the (pinned) relay
  announces in `SessionReady`. On top of that, each helper's key is pinned per helper name
  the first time (TOFU again): if `alice` later shows up with a different key, the client
  warns before connecting. The client's settings show the last helper's name and key
  fingerprint; the main window stays free of status text.
- **Client** is trusted because a helper registered its key after receiving it (or its
  fingerprint) over a trusted channel. The helper app pins registered client keys in the inner
  handshake, independent of what the relay says.
- **The user stays in charge**: every session starts from a request the user made
  at the PC, helpers can't initiate anything, and Esc or exiting the client application always ends the
  session locally, even if the relay misbehaves.
- **Pending keys** are never shown to anyone.
- **Relay compromise**: sees metadata (who, when, how much) and can deny service. Because the
  relay vouches for helpers, a compromised relay *could* present a helper key of its own to a
  client that files a request. It can't do so silently for a helper the client has already
  met (key change warning), it can't impersonate a client to a helper (the helper app pins
  the registered client key), and it still needs the user to file a request first. A
  passive or curious relay operator can't read sessions: the inner SSH is end-to-end.
- **Client private key** is an unencrypted, application-specific key protected by a
  user-only file ACL. It is used for nothing else, so stealing it gives no other SSH access;
  it only lets an attacker
  *pose as* that PC to helpers (showing a fake screen), never control the real PC.
  The registered `user@machine` is fixed at registration; the inner `Hello` reports the
  current user and machine, and the helper app warns if they differ (key copied to another
  PC or account, or the PC was renamed).

### 3.6 Splice

Each session is one channel pair copied with 256 KiB buffers in both directions and half-close
on EOF. `x/crypto/ssh` fixes the channel window at 2 MiB, which caps one session at about
2 MiB per round trip (e.g. 160 Mbit/s at 100 ms); phase 3 measures whether that matters.
Per-relay memory is bounded by the session caps. TCP sockets
use `TCP_NODELAY`. No SSH compression on either layer (the screen stream compresses itself).

---

## 4. Inside the tunnel (`proto/`)

The spliced channel carries a complete **inner SSH connection** (`proto/inner`):

- Inner SSH server = client app. Host key = its persistent key. Accepts public key auth only
  from the helper key announced in `SessionReady` for this session, and checks it against the
  pinned key for that helper name if there is one.
- Inner SSH client = helper app. `HostKeyCallback` accepts only the registered key of the
  client it asked for.
- Cipher preference `aes128-gcm@openssh.com` (AES-NI, cheap even with two SSH layers), then
  `chacha20-poly1305@openssh.com`. No compression.
- The inner server accepts exactly two channel types and rejects everything else:

| Channel | Direction | Content |
|---|---|---|
| `screen` | client → helper | `Hello` (hostname, OS user, monitors, DPI), `Frame`, `Cursor`, `CursorPos`; helper sends `Ack`, `SelectMonitor`, `Refresh`, `Quality` |
| `input` | helper → client | `MouseMove` (absolute, normalized 0..65535), `MouseButton`, `Wheel`, `Key` (W3C `code` + scan code), `Text`, `ReleaseAll` |

Separate channels give each stream its own flow-control window, so input never waits behind
screen data. There is deliberately no channel that can carry files or clipboard data.

Framing: `uint32 length | uint8 type | payload`, big-endian.

---

## 5. Performance: "as fast as RDP" with GDI only

RDP feels fast for three reasons: it sends only what changed, it reuses pixels the viewer
already has (bitmap cache, screen-to-screen copies for scrolling), and text stays sharp because
UI content is coded losslessly. The plan copies those ideas on top of plain GDI capture.

### 5.1 Targets (to be measured in CI and manually)

| Metric | Target |
|---|---|
| Input to visible change, LAN | ≤ 30 ms + RTT |
| Typing / menus / dialogs at 1080p | 60 fps, ≤ 1 Mbit/s typical |
| Scrolling a document or web page at 1080p | ≥ 30 fps, mostly copy-rect and cache hits |
| Full-screen video | ≥ 20 fps, lossy, quality degrades before latency does |
| Client CPU while idle screen | < 2 % of one core |
| Text | pixel-exact |

### 5.2 Capture (GDI only)

- `BitBlt` from the screen DC into a top-down 32-bit DIB section (`CreateDIBSection`), so the
  pixels are directly addressable from Go without `GetDIBits` copies. Without `CAPTUREBLT` by
  default (avoids the cursor flicker and layered-window cost).
- Per monitor, on a locked OS thread, double-buffered (current and previous frame).
- Adaptive rate: capture is triggered by a timer that runs at up to 60 Hz while things change
  and backs off to 5 Hz when the screen is static, and is kicked immediately after injected
  input (the next change is likely right after a click or key press).
- Optional "performance mode" during a session, like RDP's experience settings: solid
  wallpaper, no window animations, no font smoothing override is touched (ClearType stays),
  all restored when the session ends, via `SystemParametersInfo`.

### 5.3 Damage, scroll and cache

- **Damage**: compare current and previous frame in 64×64 tiles, row by row as `uint64` words,
  bailing out per tile on the first difference. Unchanged tiles cost one pass of memory reads;
  on a 1080p frame this is well under 2 ms.
- **Scroll / move detection**: per column band, hash each row and look for a vertical shift of
  the previous frame that matches most rows of a damaged region. A hit becomes a `CopyRect`
  (like RDP's ScrBlt), and only the newly exposed strip is encoded.
- **Tile cache**: the helper keeps 8192 numbered slots (≈ 128 MiB for 64×64 tiles); the
  client keeps only 64-bit hashes and owns the LRU. A lossless tile can carry "store in slot
  N", and a damaged tile whose hash matches a cached one is sent as a tiny `CacheRef` to slot
  N. Because the client chooses every slot and frames are applied in order, the two sides
  can never disagree about the cache. A Refresh or a new Hello clears it. This covers
  switching back to a window, undoing a scroll, blinking cursors and hover effects.

### 5.4 Encoding

Each damaged tile that isn't a copy or cache hit is classified by a fast color count:
- **Solid**: one color, 5 bytes.
- **Few colors (≤ 256)**: text, UI, code editors. Palette + per-tile indices compressed with
  `zstd` (`klauspost/compress`, pure Go, fastest level). Lossless, so text stays sharp.
- **Many colors**: photos, video, gradients. JPEG, quality chosen by the rate controller; a
  region that stays static for 500 ms is resent losslessly ("refine", like progressive RDP
  codecs).
- Tiles of one frame are encoded in parallel across cores and sent as one `Frame` message with
  a list of rect ops, so the helper never shows half a frame. A wire frame is at most 16 MiB,
  so a full raw frame above about 4 megapixels (a 4K screen is 33 MB raw) does not fit; with
  the codecs above that is never close, but a full refresh of a noisy 4K screen may still be
  split into parts with a "last part" flag, applied by the helper only when the last part
  arrives. Phase 2 sends raw tiles and caps the test pattern at 1920x1080.
- Later option, only if video-heavy work needs it: H.264 through Media Foundation for large,
  continuously changing regions. Capture stays GDI either way.

With one helper per client there is exactly one viewer, so capture, damage tracking and the
ack window are a single pipeline with no per-viewer state.

### 5.5 Flow control and the helper side

- At most 2 unacked frames in flight. When blocked, damage accumulates and the next
  frame carries the newest pixels, so latency never builds up in SSH or TCP buffers.
- **Refresh without a cascade**: the first frame after an encoder reset (session start,
  Refresh, new Hello) is marked Full. If the helper can't apply a frame it resets its decoder,
  asks for a Refresh, and then acks but skips frames until the Full frame arrives, because
  frames already in flight were encoded against the old cache. The client resends the current
  cursor shape after every Hello and Refresh.
- **Alpha**: GDI leaves the alpha byte undefined, so the encoder ignores it in damage
  detection and hashing and sends every pixel with A = 255.
- The rate controller measures ack RTT and throughput and adjusts JPEG quality and frame rate.
- **Cursor** is not in the captured image. Shape changes are sent as `Cursor` (cached by
  handle), position as tiny `CursorPos` messages, and the helper draws it locally. While the
  user allows input, the helper's own pointer is used directly, so pointer movement has zero network
  latency, exactly like RDP.
- **Helper rendering**: decode tiles on a worker pool into an `*image.RGBA`, then upload only
  the dirty rects of each frame into one GPU texture per session with `rl.UpdateTextureRec`
  (`glTexSubImage2D` underneath) on the render thread, and draw it scaled with
  `rl.DrawTexturePro`. Nothing re-uploads the whole screen, which is one of the reasons for
  raylib.
- **Input** is sent immediately on its own channel, mouse moves coalesced to the latest
  position per 4 ms, keys never coalesced.

---

## 6. Windows client (`client-windows/`, windigo)

`github.com/rodrigocfd/windigo`: pure Go Win32 bindings and UI, no cgo, cross-compiled from
Linux with `GOOS=windows`. Missing calls go through `golang.org/x/sys/windows`.

### 6.1 Identity: an application-specific key

The client has its own key, used for nothing but help-me-win. It is not the user's
`~/.ssh` key: no agent, no passphrase, no discovery, and no risk that stealing it opens other
SSH logins. Shared code in `proto/appkey`, also used by the helper app.

- **Location**: `%LOCALAPPDATA%\help-me-win\id_ed25519` and `id_ed25519.pub`. Local, not
  roaming, so the key never follows the user to another PC.
- **Algorithm**: Ed25519 only (`ssh-ed25519`), the modern default of OpenSSH. It is fast,
  has small keys (the public line fits in one chat message) and has no parameter choices to
  get wrong. Relay, inner SSH and helper app accept only Ed25519 keys.
- **Format**: standard OpenSSH files, exactly as `ssh-keygen -t ed25519 -N ""` would write
  them: private key `-----BEGIN OPENSSH PRIVATE KEY-----`, **not encrypted**, so the app
  starts without any prompt; public key as one `ssh-ed25519 AAAA... user@machine` line with
  comment `%USERNAME%@%COMPUTERNAME%` (lower-cased user). The private key is protected by the
  file ACL only: owner and SYSTEM, inheritance removed (`0600` on Unix).
- **Generated once**: on the first start, when the key files don't exist yet. Files are created with `CREATE_NEW` / `O_EXCL`, so an existing file is never replaced,
  even in a race.
- **Persistent**: `%LOCALAPPDATA%` (`C:\Users\anna\AppData\Local`) survives reboots, app
  updates and replacing or moving the exe, because the exe and the key are unrelated. Deleting
  or re-downloading the exe changes nothing; the key stays where it is.
- **Never regenerated automatically.** No code path rewrites, renames or re-permissions the
  key, and the app never replaces it on its own. If the file exists but can't be read or is
  damaged, the client shows an error with the path and a **Remove key** button; it does not
  make a new key silently.
- **Removed only from the app**: settings → **Remove key**, with a confirmation that says
  helpers will have to register this PC again. It deletes `id_ed25519`, `id_ed25519.pub` and
  `config.json`, and the next start begins the first-run flow with a new key. The old
  registration can be removed in the helper app. Deleting the folder by hand has the same
  effect.
- The key is per Windows user, so two users on one PC are two separate registered clients
  (`anna@PC1`, `bob@PC1`).
- The registered name is the key's comment, `user@machine`.

### 6.2 First-run window
- Step 1: "Relay address" field (`hostname:port`, port defaults to 2222 if left out),
  **Connect**. Errors are shown inline: name not found, connection refused, timeout, not a
  help-me-win relay.
- Step 2, once connected: relay fingerprint, the public key line in a read-only box, the key
  fingerprint, a big **Copy key**
  button, and "Paste this key into a chat with your helper (for example WhatsApp)." 
- Waits for `Registered`, then saves the configuration and moves to the main window. If the
  app is closed before that, nothing but the key is kept and the next start begins at step 1
  with the address prefilled.

### 6.3 Main window
- One big **Request help** button and the client's name (`anna@ANNA-LAPTOP`). After
  pressing it, the button becomes **Cancel request** until a helper opens it; no other status
  text.
- **During a session** the window is minimized (`SW_MINIMIZE`), nothing else is shown.
- **When the session ends** (the helper ended it, or Esc), the window is restored and brought
  to the front with the **Request help** button again.
- No tray icon and no automatic start in the first version. The client runs while its window
  is open (minimizing is fine). Exiting the application (closing the window, also from the
  taskbar while minimized) ends any session and disconnects from the relay. The user starts the exe when they need help.

### 6.4 Input (`injector/`)
- During a session all input from the connected helper is injected. There is no view-only
  mode and no side panel or on-screen control; the user ends the session with Esc or by
  exiting the application.
- `SendInput` with `MOUSEEVENTF_ABSOLUTE | MOUSEEVENTF_VIRTUALDESK`, keys as scan codes
  (`KEYEVENTF_SCANCODE`, extended flag where needed), `Text` via `KEYEVENTF_UNICODE`.
- All held keys and buttons are released when the session ends.
- **Esc stops the session**: during an active session the client installs a low-level
  keyboard hook (`WH_KEYBOARD_LL`) on its own thread. A local Esc press (no `LLKHF_INJECTED`
  flag) is swallowed and ends the session; an injected Esc from the helper (flag set) passes
  through to applications as normal, so the helper can still use Esc remotely. The hook
  exists only during a session, so Esc behaves normally otherwise.
- **Stopping** blocks input injection first, releases held keys, stops capture, then closes
  the inner SSH connection. Restoring the window uses `ShowWindow` +
  `SetForegroundWindow`, which is allowed because the client process handled the last input
  event; if Windows still refuses, the taskbar button flashes.
- While the user is in an elevated window, the hook may not see Esc (UIPI); exiting the
  client application from its taskbar button always works. To be verified in the Windows test matrix.
- Known limits: no input into elevated windows unless the client runs elevated ("Restart as
  administrator"), no Ctrl+Alt+Del or secure desktop.

### 6.5 Distribution
- One generic portable exe for everybody: no installer, no admin, nothing embedded. The user
  downloads it and runs it from anywhere (Downloads, Desktop). Because it is never patched, it
  can be Authenticode-signed (`osslsigncode`) once per release.
- All state lives in `%LOCALAPPDATA%\help-me-win`, not next to the exe, so a newer exe
  picks up the same key and registration.

---

## 7. Helper app (`helper/`, raylib)

### 7.0 Toolkit: raylib through raylib-go, without cgo

- `github.com/gen2brain/raylib-go/raylib` built with `CGO_ENABLED=0`. In that mode it calls
  raylib through `purego` and embeds the raylib 6 shared library for Linux, macOS and Windows
  on amd64 and arm64. So the helper app cross-compiles from Linux with plain `go build` for
  every target, like `hmw-relay` and the client: no C toolchain, no Docker.
- On first start the embedded library is written once to the user cache directory
  (`~/.cache/github.com/gen2brain/raylib-go/6.0/` on Linux, the equivalent under
  `%LOCALAPPDATA%` and `~/Library/Caches` elsewhere) and loaded from there. Linux needs the
  usual desktop OpenGL and X11/Wayland libraries at run time, which every desktop has.
- **One window.** raylib has a single window per process, so the helper app is one window: a
  client list on the left and the work area on the right (session tabs, Register client,
  Settings). Fullscreen applies to the active session.
- **No raygui** (it needs cgo). `helper/ui` is a small immediate-mode widget set written for
  this app: label, button, list row, single-line text field with paste and selection, tabs,
  scroll area, tooltip. That is all the app needs.
- **Text**: an embedded TTF (the Go fonts from `golang.org/x/image/font/gofont`) loaded with
  `rl.LoadFontFromMemory` at the monitor's scale; the window uses `FLAG_WINDOW_HIGHDPI` and
  `FLAG_WINDOW_RESIZABLE`.
- **Idle CPU**: with no session open the loop runs at a low frame rate (about 10 fps), enough
  to show new requests promptly, since raylib can't be woken from another goroutine; during a
  session or while the user is interacting it runs at the monitor's refresh rate.
- **Notifications** use the OS directly through `github.com/gen2brain/beeep` (pure Go), since
  raylib has none.
- **Threading**: raylib calls stay on the main goroutine (`runtime.LockOSThread`); network and
  decoding goroutines hand results over through channels drained once per frame.

### 7.1 Identity and relay connection
- Same key handling as the client (6.1, `proto/appkey`): its own unencrypted Ed25519 key in
  the app's data directory (`%LOCALAPPDATA%\help-me-win` on Windows,
  `~/.local/share/help-me-win` on Linux, `~/Library/Application Support/help-me-win` on
  macOS), generated once on first start, never regenerated automatically, removable from the app's
  settings (**Remove key**) like the client. **Copy public key** in settings, to send to the relay admin
  for `hmw-relay helper add`. No certificates.
- Settings: relay `hostname:port` and relay host key (pinned on first connection, like the
  client). The settings page shows the address and fingerprints to read out to users.
- One relay connection with the control channel, keepalive, reconnect with backoff. All of
  this helper's sessions run as channels over that one connection.
- **Local relay**: the helper app looks for `hmw-relay` next to its own executable. With "Run
  the relay on this machine" enabled in settings, it starts `hmw-relay serve` as a child process
  on startup (unless one is already listening), shows its status and log tail in settings,
  and stops it when the app exits. The app then connects to `localhost:<port>` itself, while
  clients and other helpers use the machine's public `hostname:port`.
- **First start of a local relay**: if the relay has no helpers yet, the helper app adds its
  own key with `hmw-relay helper add` (a local CLI call, no network), so the first helper never
  has to copy keys around. Further helpers are added the same way by whoever runs the relay.
- **Distribution**: one archive per OS containing the helper app and `hmw-relay` side by side
  (`hmw-helper` + `hmw-relay`, or `.exe` on Windows, or inside the macOS `.app`
  bundle's `Contents/MacOS`). All built on Linux with `CGO_ENABLED=0`; the macOS `.app` is
  assembled by a script (Info.plist, `.icns` written in Go from the PNG icon). No installer,
  no Docker image.

### 7.2 Client list and requests
- **Connected clients** is the left column of the window: one row per online client
  (`user@machine`). Registered clients that are offline are in a collapsed "Offline" section
  below.
- **Requests**: when a client presses Request help (its `Presence` entry gets an open request), its row is highlighted
  with the time of the request and an **Open session** button. A notification
  (`beeep`) is shown only for new requests and only when the window isn't focused; clicking it brings the app to the front on that client. When another helper
  opens it, or the user cancels it, the button just disappears.
- A row without an open request has no button: a session can only be opened for a request
  the user made.
- **Register client** (a page in the work area): a text field for the key line the user sent in the chat. It tolerates
  what chat apps do to pasted text: line breaks, leading/trailing spaces, quotes, non-breaking
  spaces, and a preceding message ("here is my key: ssh-ed25519 ..."). Accepts only
  `ssh-ed25519`; an RSA or ECDSA key gets a clear "this is not a help-me-win key, ask the
  user to copy the key shown in the app" message. The app
  shows the name and the fingerprint large for comparing on the phone, plus the relay fingerprint
  the client should be showing. Register uses the key's comment as the name,
  editable if the comment is empty. Registrations are permanent until removed.
- **Remove** a client (unregister) from the list.
- Each opened session becomes a tab in the work area; any number at once. Closing the
  window ends all open sessions.

### 7.3 Viewer
- The session tab draws the remote screen texture (5.5), letterbox aware, fit / 1:1,
  monitor selector.
- Input while the mouse is over the view or the tab has keyboard focus: mouse position and
  buttons (`rl.GetMousePosition`, `IsMouseButtonPressed/Released`), wheel
  (`GetMouseWheelMoveV`), keys (`GetKeyPressed` / `IsKeyReleased` for every key code, mapped
  in `helper/keymap` to W3C `code` + scan code), and characters without a physical key
  (`GetCharPressed`) as `Text`. The local cursor is hidden over the view and the remote
  cursor shape is drawn instead.
- **Ending**: **End session** in the toolbar, or closing the session's tab. Either closes the
  inner SSH connection; no confirmation dialog.
- Toolbar: special keys (Win, Alt+Tab, Ctrl+Esc), quality
  (auto / sharp / fast), fps and latency readout, fullscreen.
- When the user ends the session (Esc or exiting the client application), the tab just
  closes; no dialog. Esc pressed in the viewer is sent to the client as a normal key.

---

## 8. Phased delivery

**Phase 0 - skeleton (1 day)** (done)
`go.work`, four modules, CI: tests for `proto` and `relay`, cross-build of the Windows client
from Linux, `CGO_ENABLED=0` cross-builds of the helper app (raylib-go purego) for Linux,
macOS and Windows, release archives per OS with the helper app and `hmw-relay` side by side,
macOS `.app` assembled on Linux.

**Phase 1 - relay core (3-4 days)** (done)
`hmw-relay serve`, host key, state store, key-to-role auth, helpers, pending
state, control channel, presence, session requests, splice, limits, admin CLI. Headless
`cmd/fakehelper` and `cmd/fakeclient` in Go. Tests for every role × every channel and request
type, helper and client removal, many helpers and many clients at once.

**Phase 2 - apps on the relay (3-4 days)** (done)
`proto/identity`, `proto/relayaddr`, `proto/appkey`, `proto/inner` with mutual pinning, fuzz
tests. Helper app: settings, clients list, paste registration, help requests.
Windows client: app key creation, loading and removal, first-run
address entry and key window, stored configuration,
Request help button, helper client list with request notifications, sessions with a test pattern instead of the
screen.

**Phase 3 - fast view only (5-7 days)**
GDI capture into DIB sections, tile damage, solid/palette/JPEG codecs, tile cache, flow control,
helper renderer benchmark and GPU upload path. Benchmark harness
that replays recorded screen sequences (typing, scrolling, video) and reports fps, bytes and
latency per scenario. Milestone: meets the 5.1 targets for typing and menus.

**Phase 4 - control and scrolling (4-6 days)**
Mouse, wheel, keys, release of held keys, minimize on start, helper ends the session, Esc stops
the session via keyboard hook, window restored for the next request, local cursor rendering, scroll
detection with `CopyRect`, lossless refine, rate controller. Milestone: meets all 5.1 targets
side by side with an RDP session on the same machine and network.

**Phase 5 - polish**
Multi-monitor, performance mode, restart as administrator, code signing,
relay metrics endpoint (localhost only).

**Later**: tray icon and optional "Start with Windows" (HKCU Run entry, no admin), so the
client can stay online in the background; `client-macos/` and `client-linux/` reusing `proto`, the encoder and the `Capturer` /
`Injector` interfaces.

---

## 9. Testing

- `proto`: unit and fuzz tests for framing, control messages, key parsing, parsing of
  pasted key lines as mangled by chat apps, `hostname:port` parsing, `appkey`
  output accepted by `ssh-keygen -y -f` and `ssh -i`, a test that starts the app repeatedly,
  with a damaged key, with a read-only key, and with the exe moved or replaced by a newer
  build, and asserts the key files are byte-for-byte unchanged with the same ACL/mode
  afterwards and never regenerated; **Remove key** deletes exactly the key and config files
  and the next start generates a new key, tile codecs (decode(encode(x)) == x for lossless), cache consistency between encoder
  and decoder.
- `relay`: Go tests with `x/crypto/ssh` clients: pending key can do nothing, client can only
  open sessions for its own current request toward the helper that opened it, a helper
  can't cause a session without a request, only one helper wins when several open the same request, a second
  helper can never open a session to a client that is already in one, removed helpers and clients are refused at once, 100 helpers and 1000 clients connected at once,
  relay restart keeps registrations, 500 registered clients behind one IP all connect
  while that IP is blocked for unknown keys, a flood of fresh keys from one IP is cut off at the
  per-IP limit without affecting other IPs.
- Malicious relay test: a patched relay that splices a helper to the wrong client (the helper
  app must refuse the inner handshake), changes its host key (both apps must refuse), or
  announces a different key for a helper the client already knows (the client must warn
  and not connect without the user's consent).
- Performance: the replay harness from phase 3 in CI (client encoder against helper decoder,
  no GUI), with regression thresholds; `tc netem` runs at 20 ms / 80 ms RTT and 10 / 50 Mbit/s.
- Session end tests (`fakehelper`): End session on the helper closes the session and
  restores the client window; local Esc ends the session and no helper input is injected
  after it; injected Esc does not end the session; the next Request help is a new request
  with a new session; closing the client window ends the session and takes the client
  offline.
- Windows: capture and key-map tests on a `windows-latest` runner; manual matrix of Windows 11
  at 100/150/200% scaling, multiple monitors, RDP session, elevated window (including Esc while an elevated window has focus), and a side-by-side
  comparison with `mstsc` for the 5.1 scenarios.

## 10. Open questions

- Default port 2222 or 443? The port is configurable, so this is only about the default and
  documentation: 2222 needs no privileges anywhere; 443 passes more corporate firewalls on the
  client side but needs privileges on Linux and macOS and may still be blocked by TLS
  inspection.
- Does a relay on the helper's own machine behind a home or office router need a dynamic DNS
  name in the docs, since clients store `hostname:port` once and never change it?
- Is TOFU for the relay plus relay-vouched helpers enough, or should the client also be able
  to check a short code (e.g. the first 8 characters of the relay fingerprint) that the helper
  reads out before anything is stored?
- macOS: an `.app` built on Linux is only ad-hoc signed, so Gatekeeper asks users to allow
  it the first time (right-click, Open). Is that acceptable for helpers, or is a Developer ID
  signature and notarization (needs a Mac or a signing service) required?
- Is JPEG enough for video-heavy work, or is the Media Foundation H.264 path needed in v1?
