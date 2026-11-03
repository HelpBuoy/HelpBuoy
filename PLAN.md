# HelpBuoy: self-hosted remote support over a custom Go SSH relay

A self-hosted alternative to TeamViewer QuickSupport. The relay is **a single Go binary
(`helpbuoy-relay`) that speaks SSH on a single port** (default 5959). Everybody, agents and clients,
connects to that same `hostname:port`. **The public key a connection authenticates with is its identity**; the SSH
user name is ignored. Keys are long-lived: they are generated once on first start and reused
for every session.

The relay is not OpenSSH. It has no shell, no filesystem access, no file transfer and no TCP
forwarding to real hosts. It only authenticates keys, tells agents and clients about each
other, and splices their channels together.

- **relay/**: `helpbuoy-relay`, a plain executable that ships next to the desk app and runs
  locally on the agent's machine as a normal user process, on one port (default `:5959`). It
  keeps agents and registered clients in a small state directory. No service, no container.
- **desk/**: the cross-platform raylib app the *support agent* runs (`raylib-go`, no cgo). Many agents can be online at
  once, and each agent can run sessions with many clients at once.
- **client-windows/**: the windigo app the person *receiving* help runs. On first start it asks
  for the relay's `hostname:port` and shows its own application-specific SSH public key
  (generated once on first start, kept in AppData) with a **Copy** button, so the user can
  send it to the agent over a chat such as WhatsApp. After the first successful connection
  the address is stored and every later start connects to it by itself; the address can only
  be changed when connecting fails or after **Disconnect** on the waiting screen. Once an agent has registered that key, the client shows up as online for all
  agents. Agents ask to help: an agent presses **Connect** on an online client, and the
  client asks its user whether that agent (name and key fingerprint) may take
  control. Nothing happens without that approval. Talking about the problem happens over
  WhatsApp or the phone, not in the apps.
  A client is helped by **one agent at a time**; an agent can help many clients at once.

What a session can do, and nothing more:
- **client → agent**: GDI-captured screen updates and the cursor shape.
- **agent → client**: mouse and keyboard input.
- No file transfer, no clipboard, no shell, no audio.

Performance target: **as fast as RDP** for normal desktop work (section 5).

---

## 0. Deployment model and constraints

- **Relay**: the `helpbuoy-relay` executable (`helpbuoy-relay.exe` on Windows) in the same folder as the
  desk app, on the agent's own Linux, macOS or Windows machine. It is started from a
  terminal, or runs inside the desk app (7.1), as the logged-in user. Clients reach it on that
  machine's address, which usually means one port forward on the router (TCP 5959) and
  allowing the OS firewall prompt on first start. An existing OpenSSH on port 22 is unrelated
  and untouched.
- **One port for everyone**: the operator picks it once (`helpbuoy-relay serve --listen :5959`, the
  default). Agents and clients all use the same `hostname:port`, and an address typed
  without a port means 5959, so with the default users only type the host name. There are
  no per-agent or per-session ports. The default is above 1024, so the relay needs no root
  and no capabilities.
- **Runs unprivileged**: no root, no admin, no service registration, no Docker image. The
  relay runs as long as its process runs; when the machine or the process stops, clients and
  other agents simply see the relay as unreachable until it is started again.
- **Identity = key**. The relay maps each public key to exactly one of: agent, registered
  client, or unknown. User names in the SSH handshake are ignored (clients send `sv`).
- **One group per relay**: every agent on a relay sees every registered client and can ask
  to connect to it. Separate groups of agents use separate relays.

### Stored client configuration

The client is one generic exe; nothing is embedded or preconfigured. The only thing the user
types is the relay's `hostname:port`. Everything else is learned on the first successful
connection and stored in `%LOCALAPPDATA%\helpbuoy\config.json`:

| Field | Example | Learned |
|---|---|---|
| relay address | `helpme.example.com` (port 5959 unless given) | typed by the user; the relay this PC got registered on |
| relay host key | `ssh-ed25519 AAAA...` (`SHA256:3q2+7w...`) | first successful connection, pinned from then on |
| last address | `helpme.example.com` | the last address that reached a relay, to prefill the field |
| known agents | `alice` → `ssh-ed25519 AAAA...` | each agent's key, pinned on the first session with that agent (3.5) |

The key is stored next to it, as its own application-specific key pair (6.1). Adding or removing an agent never touches
the client: it trusts the agents the relay announces. Connecting to another address pins
that relay's key on first use; only once this PC is registered there does it replace the
stored relay, and the agent pins of the old relay are forgotten (agent names are only
unique within one relay).

---

## 1. Architecture

```
  Agents (many)                  helpbuoy-relay host:port                       Clients (many)
┌──────────────┐               ┌────────────────────────────────┐               ┌──────────────────┐
│ raylib desk  │── key: alice ▶│ auth: key -> agent / client /  │◀ key: anna-pc │ client (windigo) │
│ own app key, │               │       unknown                  │               │ persistent key   │
│ added by     │◀── presence ──│ registry: online agents,       │── connect ──▶ │ (own app key)    │
│ relay admin  │── register ──▶│   online clients               │               │ sv-control       │
│              │── connect  ──▶│ pending connects               │◀─ allow/deny ─│ asks its user    │
│ accepts      │◀─ sv-session ─│ splice: one channel pair per   │◀─ sv-session ─│ opens a session  │
│ sv-session   │               │   agent<->client session       │               │ once the user    │
│ (one per     │               │                                │               │ allowed it       │
│ client)      │               └────────────────────────────────┘               │                  │
│ inner SSH    │ ═════════════ inner SSH through each spliced channel ═══════════│ inner SSH server │
│ client, pins │     channels: screen (client→agent), input (agent→client)       │ host key = its   │
│ client key   │                                                                 │ key; checks the  │
└──────────────┘                                                                 │ agent key        │
┌──────────────┐                                                                 └──────────────────┘
│ agent (bob)  │── ... same, any number of agents and sessions in parallel ...
└──────────────┘
```

### First contact (once per client key)

1. The client starts for the first time and asks for the relay's `hostname:port` (the agent
   tells the user what to type, e.g. `helpme.example.com`, with `:port` only when the relay
   doesn't use the default 5959). On this first start
   it generates its own Ed25519 key pair in
   `%LOCALAPPDATA%\helpbuoy\` (section 6.1), with the comment `user@machine`, e.g.
   `anna@ANNA-LAPTOP`, so the public key line reads `ssh-ed25519 AAAAC3Nz... anna@ANNA-LAPTOP`.
2. The client connects to that address with the key. It shows the relay's host key
   fingerprint and pins it (trust on first use). The key is unknown to the relay, so the
   connection is **pending**: it can't open anything, it only waits for `Registered`. If the
   connection fails (wrong address, nothing listening, not an helpbuoy-relay), the client says so and
   lets the user correct the address; nothing is stored yet.
3. Once connected, the client shows the public key line (the contents of the `.pub` file)
   with a **Copy key** button and its fingerprint. The user pastes it into a chat with the
   agent (WhatsApp, Signal, mail). The key is public, so the chat needs no secrecy,
   only the assurance that the message really comes from the user.
4. The agent pastes the key line into the desk app (7.2) and, if in doubt, compares the
   fingerprint with the user by phone. The client is registered under the key's comment
   (`user@machine`); there is no separate label to type. The relay stores it permanently
   as a registered client and sends the client `Registered`. The client now stores the
   relay address and relay host key (the stored configuration above) and switches to its normal window.
5. From now on this key **is** `anna@ANNA-LAPTOP` on this relay. It stays registered until a
   agent or admin removes it: no expiry, and restarts, reboots and network changes don't
   matter. Later starts connect to the stored address by themselves and wait for an agent,
   using the pinned relay key. The key is never regenerated automatically; it is
   only removed when the user explicitly does so in the app (6.1).

### Session (every time)

The apps carry no text between user and agent. Explaining the problem, and saying it's
fixed, happens over WhatsApp or the phone. The apps also show nothing about sessions starting
or ending.

1. Client and agents are connected to the relay with their persistent keys. Agents see
   `anna@ANNA-LAPTOP` in their list of connected clients.
2. **The agent presses Connect** on the client's row (7.2), usually while on the phone or
   in a WhatsApp chat with the user. The relay creates a request taken by that agent and
   sends the client `SessionReady`. Other agents see the client as busy; if two agents
   connect at the same moment, the first one wins.
3. **The client asks the user** whether that agent may take control, showing the agent's
   name and key fingerprint, with a separate warning if the key changed (3.5).
   If the user refuses, the client cancels the request and the agent sees "Not allowed".
   The relay ends a request whose session hasn't started within 1 minute, so the user has
   that long to answer; the client then closes the question by itself.
4. Once the user allows it, the client opens a `helpbuoy-session@helpbuoy` channel for that
   agent. The relay opens a matching channel to the agent's connection and splices the
   two (3.6).
5. **Inner SSH** runs over the spliced channel (section 4). The client is the SSH server with
   its persistent key as host key, the agent is the SSH client with its own key, which the relay
   announced in `SessionReady`. Both check the other end's key (3.5).
6. **The client window minimizes**, without any banner or status text, so the agent sees
   the user's desktop and can do the work.
7. **The agent ends the session** when done, with **End session** in the viewer or by
   closing the session's tab. The client window comes back, waiting for the next agent.
8. If more is needed later, the agent presses **Connect** again and the user is asked
   again: every session needs the user's approval.
9. **One agent per client.** While an agent is connecting or a session runs, no other
   agent can connect to that client.
10. **Other ways a session ends**: Esc on the client's own keyboard (the user takes back
   control), exiting the client application (which also takes the client offline), or
   revoking the client key. There is no Disconnect button on the client. The relay
   connections stay up for the next session.

The relay only passes the inner SSH stream through. Nobody on the relay, root included, can
read or alter screen data or keystrokes.

---

## 2. Repository layout

```
helpbuoy/
├── go.work                      # use ./proto ./relay ./desk ./client-windows
├── PLAN.md
├── proto/                       # shared, pure Go, no GUI deps
│   ├── go.mod
│   ├── relayaddr/               # parse and normalize hostname:port (default port 5959)
│   ├── identity/                # Ed25519 key gen, OpenSSH format, fingerprints,
│   │                            # parsing of pasted keys
│   ├── appkey/                  # create-once, load-only app key in OpenSSH format
│   ├── control/                 # sv-control messages (both sides <-> relay)
│   ├── inner/                   # inner SSH server/client, pinning, channel plumbing
│   ├── screen/                  # screen stream messages, tile codecs, tile cache
│   ├── input/                   # input messages, W3C key codes
│   └── wire/                    # length-prefixed framing
├── relay/                       # helpbuoy-relay
│   ├── go.mod
│   ├── cmd/helpbuoy-relay/             # "serve" plus admin subcommands (3.2)
│   ├── hostkey/
│   ├── store/                   # state.json: agents, clients; atomic writes
│   ├── auth/                    # key -> role lookup
│   ├── registry/                # presence, pending keys, session requests
│   ├── splice/                  # session channel pairing, large-buffer copy
│   ├── limits/                  # per-key then per-IP limits, caps, timeouts
├── desk/                      # raylib desk app (Linux / macOS / Windows)
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
    ├── identity/                # app key in %LOCALAPPDATA%\helpbuoy (via proto/appkey)
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
| **agent** | listed as an agent key in `state.json` (plain Ed25519 public key, no certificates) | one `sv-control` channel; incoming `sv-session` channels; control messages |
| **client** | registered | one `sv-control` channel; one agent connecting or in session at a time; outgoing `sv-session` channels, only toward the agent whose `Connect` its user allowed, and only one session at a time |
| **pending** | unknown | nothing but receiving `Registered`; not shown to any agent (agents register keys from the pasted key line, never from a list); idle timeout 24 h |

Not implemented at all: `session`, `shell`, `exec`, `subsystem`, `pty`, `direct-tcpip`,
`tcpip-forward`, `streamlocal`, agent and X11 forwarding. The attack surface is the SSH
transport in `x/crypto/ssh`, the auth callback, the control parser and the splice.

A key can't be both agent and client.

### 3.2 State and admin

State directory in the user's data directory, next to the desk app's own data:
`~/.local/share/helpbuoy-relay` on Linux, `~/Library/Application Support/helpbuoy-relay` on macOS,
`%LOCALAPPDATA%\helpbuoy-relay` on Windows (override with `--state`). It holds `hostkey_ed25519` (0600) and `state.json` with agents
(name, public key) and registered clients (key, `user@machine` name,
registered by, registered at, last seen). Client registrations never expire; they are removed
only by `UnregisterClient` or `helpbuoy-relay client remove`. Writes are temp file + `fsync` + `rename`.

| Command | What it does |
|---|---|
| `helpbuoy-relay serve [--listen :5959]` | run the relay on its single port |
| `helpbuoy-relay agent add alice 'ssh-ed25519 ...'` | add an agent with the public key copied from their desk app |
| `helpbuoy-relay agent remove alice` | remove an agent; takes effect immediately, open sessions are closed |
| `helpbuoy-relay agent list` / `client list` / `client remove SHA256:...` | inspect and revoke |
| `helpbuoy-relay hostkey` | print the host key fingerprint, for agents to compare with what clients show |

**Agents are plain keys**: there are no certificates. A new agent sends
their public key line to the relay admin, who adds it with `helpbuoy-relay agent add`. Removing it
with `helpbuoy-relay agent remove` is the whole revocation story. The relay watches `state.json` and
reloads it when the CLI changes it (no signals, so it works the same on Windows).

### 3.3 Control channel

`helpbuoy-control@helpbuoy`, framed messages (`proto/control`): each is a `proto/wire` frame
whose type byte names the message and whose payload is JSON. Control traffic is small and
rare, so JSON keeps it easy to debug; the screen and input streams use binary payloads.
Agent commands carry a sequence number and the relay answers each with a `Reply`.

| Message | Direction | Content |
|---|---|---|
| `Hello` | relay → any | role, relay time, limits |
| `RelayInfo` | relay → any | title and instruction line (e.g. "Example Inc", "Call +1 234 567 890 for support"), right after `Hello` and again whenever they change |
| `ClientHello` | client → relay | app version, current `user@machine` (the registered name comes from the key comment) |
| `Presence` | relay → agents | full snapshot after every change: registered clients (`user@machine`, fingerprint, online, open or taken request with id and time, in session with whom) and agents |
| `RegisterClient` / `UnregisterClient` | agent → relay | public key line or fingerprint (name taken from the key comment, or given when the comment is empty) |
| `RegisterAgent` | agent → relay | public key line and name: adds another agent, like `helpbuoy-relay agent add` |
| `SetRelayInfo` | agent → relay | new title (up to 64 characters) and instruction line (up to 160), one line each, empty to clear; stored in `state.json` |
| `Reply` | relay → agent | answer to an agent command: sequence number and error text if it failed |
| `Registered` | relay → client | the key is now registered (no name: that is only for agents) |
| `RequestHelp` | client → relay | (no content) |
| `CancelRequest` | client → relay | request id |
| `RequestStateMsg` | relay → client | request id and state (open / taken / cancelled / expired / ended) |
| `OpenRequest` | agent → relay | request id; the relay lets only the first agent win |
| `SessionReady` | relay → client | request id, agent name and public key: an agent wants to connect |
| `Error` | relay → any | reason, sent just before the relay closes a connection on purpose |

Session channels carry small SSH-encoded extra data: the client's open names the request id;
the relay's open towards the agent adds the client's name, key line and current
`user@machine`, so the desk app can pin the client key before the inner handshake.

### 3.4 Abuse limits

There are no bans and no long counting windows. Every limit is either a rate of one attempt
per second or a cap on what is open at the same time, so nothing stays blocked after the
abuse stops and legitimate clients behind one NAT or company proxy never lock each other
out. A key counts as proven only after its signature has been verified in the SSH handshake.

1. **One connection attempt per key per second**, for every role (agents, registered
   clients and unknown keys). A faster attempt is refused and nothing else happens: the key's
   existing connection is not touched. The apps reconnect with a backoff of at least a
   second anyway.
2. **One connection per key.** When a key connects again, the new connection replaces the
   old one, so a client whose network dropped (laptop sleep, Wi-Fi switch) gets back in at
   once instead of waiting for the dead connection to time out.
3. **Before any key is proven**, per source IP: at most 10 handshakes at the same time;
   further connections wait for a slot (handshake timeout 10 s). Failed handshakes have no
   further consequences.
4. **Pending connections** (unknown keys): at most 10 per source IP and 200 for the whole
   relay at the same time, so someone generating fresh keys from one place can't take every
   pending slot. Closing a pending connection, or registering its key, frees its slot at once.

Role caps and timeouts:
- Per agent: max 20 concurrent sessions. Per client: exactly one session at a time, enforced by the relay and again by the client.
- Handshake timeout 10 s, keepalive 15 s, dead connections dropped after 3 missed.
- Control frames are length-checked before they are read: 16 KiB from clients and pending
  connections, 256 KiB from agents, so an unauthenticated peer can't make the relay buffer
  large frames.
- JSON log to stdout and to `relay.log` in the state directory (rotated at 10 MB): auth result, role, fingerprints, source IP, session start/end,
  byte counts. Never payload.

### 3.5 Trust model

- **The relay is trust on first use (TOFU)**, for clients and agents alike, like `ssh` with
  `known_hosts`. Its host key is pinned on the first successful connection. Someone who
  controls the network at exactly that moment could substitute their own relay. Mitigation:
  the client shows the relay fingerprint, and the desk app shows it next to the key it just
  registered, so it can be compared on the same call. After that any change stops the
  client ("relay identity changed") and shows a warning with the old and new fingerprints,
  with Cancel as the default. Only if the user explicitly trusts the new key (after checking
  it with the agent by phone) is it pinned; never a silent update.
- **Agents are vouched for by the relay.** Without certificates the client has no
  independent way to know the agents, so it accepts the agent key the (pinned) relay
  announces in `SessionReady`. On top of that, each agent's key is pinned per agent name
  the first time (TOFU again). Before every session the client asks the user whether the
  agent may take control, showing its name and key fingerprint; if `alice` later
  shows up with a different key, that question also carries a warning.
- **Client** is trusted because an agent registered its key after receiving it (or its
  fingerprint) over a trusted channel. The desk app pins registered client keys in the inner
  handshake, independent of what the relay says.
- **The user stays in charge**: agents can only ask; every session needs the user's
  approval at the PC for that agent, and Esc or exiting the client application always ends the
  session locally, even if the relay misbehaves.
- **Pending keys** are never shown to anyone.
- **Relay compromise**: sees metadata (who, when, how much) and can deny service. Because the
  relay vouches for agents, a compromised relay *could* present an agent key of its own to a
  client that files a request. It can't do so silently for an agent the client has already
  met (key change warning), it can't impersonate a client to an agent (the desk app pins
  the registered client key), and it still needs the user to file a request first. A
  passive or curious relay operator can't read sessions: the inner SSH is end-to-end.
- **Client private key** is an unencrypted, application-specific key protected by a
  user-only file ACL. It is used for nothing else, so stealing it gives no other SSH access;
  it only lets an attacker
  *pose as* that PC to agents (showing a fake screen), never control the real PC.
  The registered `user@machine` is fixed at registration; the inner `Hello` reports the
  current user and machine, and the desk app warns if they differ (key copied to another
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
  from the agent key announced in `SessionReady` for this session, and checks it against the
  pinned key for that agent name if there is one.
- Inner SSH client = desk app. `HostKeyCallback` accepts only the registered key of the
  client it asked for.
- Cipher preference `aes128-gcm@openssh.com` (AES-NI, cheap even with two SSH layers), then
  `chacha20-poly1305@openssh.com`. No compression.
- The inner server accepts exactly two channel types and rejects everything else:

| Channel | Direction | Content |
|---|---|---|
| `screen` | client → agent | `Hello` (hostname, OS user, monitors, DPI), `Frame`, `Cursor`, `CursorPos`; agent sends `Ack`, `SelectMonitor`, `Refresh`, `Quality` |
| `input` | agent → client | `MouseMove` (absolute, normalized 0..65535), `MouseButton`, `Wheel`, `Key` (W3C `code` + scan code), `Text`, `ReleaseAll` |

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
- **Tile cache**: the agent keeps 8192 numbered slots (≈ 128 MiB for 64×64 tiles); the
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
  a list of rect ops, so the agent never shows half a frame. A wire frame is at most 16 MiB,
  so a full raw frame above about 4 megapixels (a 4K screen is 33 MB raw) does not fit; with
  the codecs above that is never close, but a full refresh of a noisy 4K screen may still be
  split into parts with a "last part" flag, applied by the agent only when the last part
  arrives. Phase 2 sends raw tiles and caps the test pattern at 1920x1080.
- Later option, only if video-heavy work needs it: H.264 through Media Foundation for large,
  continuously changing regions. Capture stays GDI either way.

With one agent per client there is exactly one viewer, so capture, damage tracking and the
ack window are a single pipeline with no per-viewer state.

### 5.5 Flow control and the agent side

- At most 2 unacked frames in flight. When blocked, damage accumulates and the next
  frame carries the newest pixels, so latency never builds up in SSH or TCP buffers.
- **Refresh without a cascade**: the first frame after an encoder reset (session start,
  Refresh, new Hello) is marked Full. If the agent can't apply a frame it resets its decoder,
  asks for a Refresh, and then acks but skips frames until the Full frame arrives, because
  frames already in flight were encoded against the old cache. The client resends the current
  cursor shape after every Hello and Refresh.
- **Alpha**: GDI leaves the alpha byte undefined, so the encoder ignores it in damage
  detection and hashing and sends every pixel with A = 255.
- The rate controller measures ack RTT and throughput and adjusts JPEG quality and frame rate.
- **Cursor** is not in the captured image. Shape changes are sent as `Cursor` (cached by
  handle), position as tiny `CursorPos` messages, and the agent draws it locally. While the
  user allows input, the agent's own pointer is used directly, so pointer movement has zero network
  latency, exactly like RDP.
- **Agent rendering**: decode tiles on a worker pool into an `*image.RGBA`, then upload only
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

The client has its own key, used for nothing but helpbuoy. It is not the user's
`~/.ssh` key: no agent, no passphrase, no discovery, and no risk that stealing it opens other
SSH logins. Shared code in `proto/appkey`, also used by the desk app.

- **Location**: `%LOCALAPPDATA%\helpbuoy\id_ed25519` and `id_ed25519.pub`. Local, not
  roaming, so the key never follows the user to another PC.
- **Algorithm**: Ed25519 only (`ssh-ed25519`), the modern default of OpenSSH. It is fast,
  has small keys (the public line fits in one chat message) and has no parameter choices to
  get wrong. Relay, inner SSH and desk app accept only Ed25519 keys.
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
- **Removed only from the app**: **Remove key** on the error screen of a damaged key, with a
  confirmation that says agents will have to register this PC again. It deletes
  `id_ed25519`, `id_ed25519.pub` and `config.json`, and the client continues with a new key. The old
  registration can be removed in the desk app. Deleting the folder by hand has the same
  effect.
- The key is per Windows user, so two users on one PC are two separate registered clients
  (`anna@PC1`, `bob@PC1`).
- The registered name is the key's comment, `user@machine`.

### 6.2 Address and key screens
- "Relay address" field (`hostname:port`, port defaults to 5959 if left out) and
  **Connect**. With a stored address the client connects to it at start by itself, with
  the field locked while it does; the field can only be edited when that fails or after
  **Disconnect**. Errors are shown inline: name not found, connection refused, timeout, not a
  helpbuoy relay. A changed relay key brings the user back here with the warning (3.5).
- If the relay doesn't know this PC's key yet: relay fingerprint, the public key line in a
  read-only box, the key fingerprint, a big **Copy key** button, and "Paste this key into a
  chat with an agent (for example WhatsApp)." A link goes back to the address field.
- Waits for `Registered`, then saves the configuration and moves to the waiting screen.

### 6.3 Waiting screen
- The comment of this PC's own key (`anna@ANNA-LAPTOP`) and "Waiting for an agent", with one line
  saying the user will be asked first and that Esc takes back control. The agent connects,
  and the client asks the question from section 1 (Session, step 3) in a modal dialog.
- **Disconnect** is always available while connected: it leaves the relay and goes back to
  the address screen. During a session the same button reads **End session** and ends only
  the session (like Esc); afterwards it is Disconnect again.
- The client never shows the name agents registered or renamed it under: the relay does
  not send it to the client (neither `Hello` nor `Registered` carry it). That name is only
  for agents.
- There is no settings window.
- **Relay title and instruction**: when the relay has a title or instruction line (set by
  an agent on the desk app's Relay tab), the window shows them in a footer on every
  screen, and the title also goes in the window caption ("Example Inc - HelpBuoy"). The
  client stores the values of the relay it is registered on, so the footer, typically a
  phone number to call, still shows when that relay can't be reached.
- **During a session** the window is minimized (`SW_MINIMIZE`), nothing else is shown.
- **When the session ends** (the agent ended it, or Esc), the window is restored and brought
  to the front, waiting for the next agent.
- No tray icon and no automatic start in the first version. The client runs while its window
  is open (minimizing is fine). Exiting the application (closing the window, also from the
  taskbar while minimized) ends any session and disconnects from the relay. The user starts the exe when they need help.

### 6.4 Input (`injector/`)
- During a session all input from the connected agent is injected. There is no view-only
  mode and no side panel or on-screen control; the user ends the session with Esc or by
  exiting the application.
- `SendInput` with `MOUSEEVENTF_ABSOLUTE | MOUSEEVENTF_VIRTUALDESK`, keys as scan codes
  (`KEYEVENTF_SCANCODE`, extended flag where needed), `Text` via `KEYEVENTF_UNICODE`.
- All held keys and buttons are released when the session ends.
- **Esc stops the session**: during an active session the client installs a low-level
  keyboard hook (`WH_KEYBOARD_LL`) on its own thread. A local Esc press (no `LLKHF_INJECTED`
  flag) is swallowed and ends the session; an injected Esc from the agent (flag set) passes
  through to applications as normal, so the agent can still use Esc remotely. The hook
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
- One generic exe for everybody, nothing embedded, never needs admin. It ships only as a
  per-user MSI (`installer/helpbuoy.wxs`, no UAC prompt) that installs it into
  `%LOCALAPPDATA%\Programs\helpbuoy` with a Start menu shortcut. Because the exe is never
  patched, it can be Authenticode-signed (`osslsigncode`) once per release.
- **Updates**, like otp4win: a release build asks the GitHub API for the latest release at
  start-up, and if its MSI is newer, offers "Download and install it now?". Yes downloads
  the MSI to the temp folder, checks its size, starts `msiexec` and closes the app. No prompt
  while an agent connects or during a session. The check can't be turned off;
  development builds and offline starts skip it silently. It needs the GitHub repository to
  be public, since the release API doesn't show private releases to anonymous callers.
- `release.sh [major|minor|build]` bumps the version in both `winres.json` files, runs
  `make vet test msi deb`, commits, tags `v<version>`, pushes and creates the GitHub release
  with everything in `dist/`: the client MSI, the agent archives (with `helpbuoy-relay`) and the
  `helpbuoy-relay` Debian package. For now only Linux and Windows on amd64 are built (`OSES` and
  `ARCHES` in the Makefile).
- The relay also ships as a Debian 13 package (`make deb`, `packaging/debian/`): `helpbuoy-relay`
  as a hardened systemd service under its own system user with its state in
  `/var/lib/helpbuoy-relay`, settings in `/etc/default/helpbuoy-relay`, and `helpbuoy-relayctl` to run
  admin commands as the service user.
- All state lives in `%LOCALAPPDATA%\helpbuoy`, not next to the exe, so a newer exe
  picks up the same key and registration.

---

## 7. Desk app (`desk/`, raylib)

### 7.0 Toolkit: raylib through raylib-go, without cgo

- `github.com/gen2brain/raylib-go/raylib` built with `CGO_ENABLED=0`. In that mode it calls
  raylib through `purego` and embeds the raylib 6 shared library for Linux, macOS and Windows
  on amd64 and arm64. So the desk app cross-compiles from Linux with plain `go build` for
  every target, like `helpbuoy-relay` and the client: no C toolchain, no Docker.
- On first start the embedded library is written once to the user cache directory
  (`~/.cache/github.com/gen2brain/raylib-go/6.0/` on Linux, the equivalent under
  `%LOCALAPPDATA%` and `~/Library/Caches` elsewhere) and loaded from there. Linux needs the
  usual desktop OpenGL and X11/Wayland libraries at run time, which every desktop has.
- **One window.** raylib has a single window per process, so the desk app is one window
  with a tab bar that is always visible: the fixed tabs **Clients**, **Agents** and **Relay**,
  then one tab per session, marked with the lifebuoy app icon and a close button
  that ends the session. Session tabs are wider than the fixed tabs. A session tab shows
  only the session, without the client list. Fullscreen applies to the active session.
- **No raygui** (it needs cgo). `desk/ui` is a small immediate-mode widget set written for
  this app: label, button, list row, single-line text field with paste and selection, tabs,
  scroll area, tooltip. That is all the app needs.
- **Text**: an embedded TTF (the Go fonts from `golang.org/x/image/font/gofont`) loaded with
  `rl.LoadFontFromMemory` at the monitor's scale; the window uses `FLAG_WINDOW_HIGHDPI` and
  `FLAG_WINDOW_RESIZABLE`.
- **Idle CPU**: with no session open the loop runs at a low frame rate (about 10 fps), enough
  to show presence changes promptly, since raylib can't be woken from another goroutine; during a
  session or while the user is interacting it runs at the monitor's refresh rate.
- **Threading**: raylib calls stay on the main goroutine (`runtime.LockOSThread`); network and
  decoding goroutines hand results over through channels drained once per frame.

### 7.1 Identity and relay connection
- Same key handling as the client (6.1, `proto/appkey`): its own unencrypted Ed25519 key in
  the app's data directory (`%LOCALAPPDATA%\helpbuoy` on Windows,
  `~/.local/share/helpbuoy` on Linux, `~/Library/Application Support/helpbuoy` on
  macOS), generated once on first start, never regenerated automatically. Like the client,
  **Remove key** is only offered on the damaged key screen (a centered box with the error and
  the key folder). The public key, with **Copy public key**, shows on the connect flow when a
  relay does not know this agent yet. No certificates.
- **Connect screen**, like the client's: every start shows a centered box on the grey window
  with "Connect to your client", "Type the relay address you told the client to use, for
  example helpme.example.com.", the address field (prefilled with the last address) and
  **Connect**, with the hint "Leave empty to run a local relay on this computer, with you as
  its agent." Errors show in the box. The relay host key is pinned on the first
  connection per address, like the client; a changed key brings the user back here with a
  warning and an explicit "Trust the new key". If the relay does not know this key as a
  agent, the box shows the public key with **Copy public key** to send to an agent on it
  (**Add agent** on the Agents tab) or its admin (`helpbuoy-relay agent add`) and keeps retrying, so it moves on by itself once the key is added.
- Once online the tabs appear. The **Relay** tab shows the connection status, the address,
  the relay fingerprint to read out to users, the relay's **title** and **instruction line**
  with **Save** (any agent can change them; every client and agent gets the new values
  right away), and **Disconnect** (not while sessions are open; back to the connect screen,
  stopping the local relay). There is no settings tab: nothing else is configurable.
- One relay connection with the control channel, keepalive, reconnect with backoff. All of
  this agent's sessions run as channels over that one connection.
- **Local relay**: the relay is compiled into the desk app (`relay/server`, the same code
  as `helpbuoy-relay`). Connecting with an empty address (or `localhost`, or `localhost:<port>`)
  serves the relay in-process on that port until Disconnect or exit (unless something already listens on the
  port). There is no separate executable
  to find, so `go run .` works too. It uses the same state directory as `helpbuoy-relay serve`, so
  the standalone relay and its admin commands share the agents, clients and host key. The
  app connects to `localhost:<port>` itself, while clients and other agents use the
  machine's public `hostname:port`.
- **This agent on the local relay**: if this agent's key is not an agent there yet, the app
  adds it to the relay state directly (with a number after the name if that name is taken),
  so nobody has to copy keys around. Other agents are added with **Add agent** on the Agents tab
  or with `helpbuoy-relay agent add` by whoever runs the relay.
- **Distribution**: one archive per OS containing the desk app and `helpbuoy-relay` side by side
  (`helpbuoy-desk` + `helpbuoy-relay`, or `.exe` on Windows, or inside the macOS `.app`
  bundle's `Contents/MacOS`). All built on Linux with `CGO_ENABLED=0`; the macOS `.app` is
  assembled by a script (Info.plist, `.icns` written in Go from the PNG icon). No installer,
  no Docker image.

### 7.2 Client list and connecting
- The **Clients** tab shows one row per online client
  (`user@machine`). Registered clients that are offline are in a collapsed "Offline" section
  below.
- The **Agents** tab lists every agent on the relay, online first, with an online dot,
  "(you)" for this agent, and whom they are helping (otherwise the key fingerprint).
- **Connect**: every online client that nobody is helping has a **Connect** button. It
  sends `Connect` with a fresh request id; the row then says "Waiting for the user to allow
  the session…" until the session channel arrives (a tab opens) or the request goes away,
  which shows "Not allowed: the user refused or did not answer". Rows of clients another
  agent is connecting to or helping show that instead of the button.
- **Register client** and **Register agent** are tabs that only show after a click on
  **Add client** (top right of the Clients tab, next to the title) or **Add agent** (same
  place on the Agents tab). At most one of them is open, always in the same place, between
  Agents and Relay, with a close button. Add on an already open one just switches to it;
  the other one replaces it.
- **Register client**: a text field for the key line the user sent in the chat. It tolerates
  what chat apps do to pasted text: line breaks, leading/trailing spaces, quotes, non-breaking
  spaces, and a preceding message ("here is my key: ssh-ed25519 ..."). Accepts only
  `ssh-ed25519`; an RSA or ECDSA key gets a clear "this is not a helpbuoy key, ask the
  user to copy the key shown in the app" message. The app
  shows the name and the fingerprint large for comparing on the phone, plus the relay fingerprint
  the client should be showing. The name field is
  prefilled with the key's comment (`user@machine`) and always editable ("Anna laptop");
  it is required when the comment is empty. Registrations are permanent until removed.
- **Register agent** works like Register client for another agent's key line (their
  app shows it when they connect to this relay before being added): the name is one word, prefilled from the user part of the key comment,
  and the fingerprint is shown large for the phone. Any agent on a relay can add agents;
  removing one is still `helpbuoy-relay agent remove`.
- **Forms reset** when their tab is closed (or replaced): the Register form and its result
  are cleared. Leaving the Clients tab drops open renames and confirmations.
- **Rename**: hovering a client row shows Rename and Remove; Rename (or a double-click) turns
  the name into an inline field (Enter saves, Esc cancels) and sends `RenameClient`. The
  relay stores the name for everyone, so every agent's list shows it. When the
  `user@machine` the client reports differs from the name, it shows as a muted second line.
- **Remove** a client (unregister) from the list.
- Each opened session becomes a tab after the fixed ones; any number at once. When a
  session ends its tab goes away and the Clients tab is shown. Closing the window ends all
  open sessions.

### 7.3 Viewer
View options follow Remmina's connection window.

- **Fit / 1:1** per session tab, default Fit. Fit scales the remote screen into the view with
  its aspect ratio kept. 1:1 shows it pixel for pixel (one remote pixel is one physical
  screen pixel, also on HiDPI), centered when smaller than the view; when larger, thin
  draggable scrollbars appear and the view scrolls by itself when the pointer comes within
  16 px of an edge. The mouse wheel always goes to the remote side.
- **Resize** sizes the window so the view shows the remote screen at 1:1 exactly, clamped to
  the current monitor (then 1:1 with scrolling).
- **Fullscreen** shows only the session; a floating toolbar slides in when the pointer
  touches the top edge and hides a second after it leaves. Leaving fullscreen restores the
  window.
- **Host key**: Right Ctrl + F (fullscreen), S (Fit/1:1), R (resize). These
  combinations are never sent to the client; Right Ctrl with any other key, or with a mouse
  button, acts as a normal Ctrl on the remote side, and tapped alone it is sent as a press
  and release.
- **Cursor**: while the pointer is over the view, a pointer is always drawn at the local
  mouse position (the remote shape when known, else an arrow) and the OS cursor is hidden,
  so moving has no network latency and the pointer never disappears, even when the client
  reports its cursor as hidden. Elsewhere the remote cursor is drawn where the client
  reports it.
- Input while the mouse is over the view or the tab has keyboard focus: mouse position and
  buttons, wheel, keys (mapped in `desk/keymap` to W3C codes, checked against
  `input.Codes`), and characters without a physical key as `Text`.
- **Toolbar**: End session, Win, Alt+Tab, monitor selector, quality (Auto / Sharp / Fast),
  Fit / 1:1, Resize, Fullscreen, and an fps, RTT and Mbit/s readout.
- **Ending**: **End session** in the toolbar, or the close button on the session's tab,
  closes the inner SSH connection; no confirmation dialog. When the user ends the session (Esc or
  exiting the client application), the tab just closes. Esc pressed in the viewer is sent to
  the client as a normal key.

---

## 8. Phased delivery

**Phase 0 - skeleton (1 day)** (done)
`go.work`, four modules, CI: tests for `proto` and `relay`, cross-build of the Windows client
from Linux, `CGO_ENABLED=0` cross-builds of the desk app (raylib-go purego) for Linux,
macOS and Windows, release archives per OS with the desk app and `helpbuoy-relay` side by side,
macOS `.app` assembled on Linux.

**Phase 1 - relay core (3-4 days)** (done)
`helpbuoy-relay serve`, host key, state store, key-to-role auth, agents, pending
state, control channel, presence, session requests, splice, limits, admin CLI. Headless
`cmd/fakeagent` and `cmd/fakeclient` in Go. Tests for every role × every channel and request
type, agent and client removal, many agents and many clients at once.

**Phase 2 - apps on the relay (3-4 days)** (done)
`proto/identity`, `proto/relayaddr`, `proto/appkey`, `proto/inner` with mutual pinning, fuzz
tests. Desk app: settings, clients list, paste registration, help requests.
Windows client: app key creation, loading and removal, first-run
address entry and key window, stored configuration,
Request help button, agent client list with request notifications, sessions with a test pattern instead of the
screen.

**Phase 3 - fast view only (5-7 days)** (done; `helpbuoy-bench` passes the 5.1 targets on synthetic scenes)
GDI capture into DIB sections, tile damage, solid/palette/JPEG codecs, tile cache, flow control,
agent renderer benchmark and GPU upload path. Benchmark harness
that replays recorded screen sequences (typing, scrolling, video) and reports fps, bytes and
latency per scenario. Milestone: meets the 5.1 targets for typing and menus.

**Phase 4 - control and scrolling (4-6 days)** (implemented and checked under Wine; the side-by-side RDP comparison needs real Windows)
Mouse, wheel, keys, release of held keys, minimize on start, agent ends the session, Esc stops
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
  open sessions for a `Connect` it was sent, toward that agent, an agent can't cause a
  session without the client opening one, only one agent wins when several connect at once, a second
  agent can never open a session to a client that is already in one, removed agents and clients are refused at once, 100 agents and 1000 clients connected at once,
  relay restart keeps registrations, one attempt per key per second (a refused attempt
  leaves the existing connection alone), 500 registered clients behind one IP all connect
  while that IP's pending slots are full, a flood of fresh keys from one IP fills only that
  IP's pending slots, and nothing stays blocked after failed handshakes.
- Malicious relay test: a patched relay that splices an agent to the wrong client (the agent
  app must refuse the inner handshake), changes its host key (both apps must refuse; the client warns and only reconnects if the
  user trusts the new key), or
  announces a different key for an agent the client already knows (the client must warn
  and not connect without the user's consent).
- Performance: the replay harness from phase 3 in CI (client encoder against agent decoder,
  no GUI), with regression thresholds; `tc netem` runs at 20 ms / 80 ms RTT and 10 / 50 Mbit/s.
- Session end tests (`fakeagent`): End session on the agent closes the session and
  restores the client window; local Esc ends the session and no agent input is injected
  after it; injected Esc does not end the session; the next Connect asks the user again
  and gives a new session; refusing a Connect starts nothing; closing the client window ends the session and takes the client
  offline.
- Windows: capture and key-map tests on a `windows-latest` runner; manual matrix of Windows 11
  at 100/150/200% scaling, multiple monitors, RDP session, elevated window (including Esc while an elevated window has focus), and a side-by-side
  comparison with `mstsc` for the 5.1 scenarios.

## 10. Open questions

- Default port: 5959, clear of TeamViewer's 5938, so it doesn't clash with a TeamViewer
  installed on the same machine. Above 1024, so no privileges are needed; 443 would pass
  more corporate firewalls but needs privileges on Linux and may still meet TLS inspection.
- Does a relay on the agent's own machine behind a home or office router need a dynamic DNS
  name in the docs, since clients store `hostname:port` once and never change it?
- Is TOFU for the relay plus relay-vouched agents enough, or should the client also be able
  to check a short code (e.g. the first 8 characters of the relay fingerprint) that the agent
  reads out before anything is stored?
- macOS: an `.app` built on Linux is only ad-hoc signed, so Gatekeeper asks users to allow
  it the first time (right-click, Open). Is that acceptable for agents, or is a Developer ID
  signature and notarization (needs a Mac or a signing service) required?
- Is JPEG enough for video-heavy work, or is the Media Foundation H.264 path needed in v1?
