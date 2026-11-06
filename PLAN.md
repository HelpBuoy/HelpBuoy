# HelpBuoy: self-hosted remote support over a custom Go SSH server

A self-hosted alternative to TeamViewer QuickSupport. The server is **a single Go binary
(`helpbuoy-server`) that speaks SSH on a single port** (default 5959). Everybody, techies and computers,
connects to that same `hostname:port`. **The public key a connection authenticates with is its identity**; the SSH
user name is ignored. Keys are long-lived: they are generated once on first start and reused
for every session.

The server is not OpenSSH. It has no shell, no filesystem access, no file transfer and no TCP
forwarding to real hosts. It only authenticates keys, tells techies and computers about each
other, and splices their channels together.

- **server/**: `helpbuoy-server`, a plain executable (also built into the desk app) that runs
  locally on the techie's machine as a normal user process, on one port (default `:5959`). It
  keeps techies and registered computers in a small state directory. No service, no container.
- **desk/**: the cross-platform raylib app the *support techie* runs (`raylib-go`, no cgo). Many techies can be online at
  once, and each techie can run sessions with many computers at once.
- **client-windows/** and **client-linux/**: the app the person *receiving* help runs,
  for Windows (windigo) and for Linux (X11). The Windows client is described here and in
  section 6; the Linux client works the same way, with two differences: it needs an X11
  session (on Wayland it only explains how to log in with X11), and a local Esc ends the
  session but also reaches the focused application, since X can't take one key away from
  other applications without also taking the injected presses. On first start it asks
  for the server's `hostname:port` and shows its own application-specific SSH public key
  (generated once on first start, kept in AppData) with a **Copy** button, so the user can
  send it to the techie over a chat such as WhatsApp. After the first successful connection
  the address is stored and every later start connects to it by itself; the address can only
  be changed when connecting fails or after **Disconnect** on the waiting screen. Once a techie has registered that key, the computer shows up as online for all
  techies. Techies ask to help: a techie presses **Connect** on an online computer, and the
  computer asks its user whether that techie (name and key fingerprint) may take
  control. Nothing happens without that approval. Talking about the problem happens over
  WhatsApp or the phone, not in the apps.
  A computer is helped by **one techie at a time**; a techie can help many computers at once.

What a session can do, and nothing more:
- **computer → techie**: GDI-captured screen updates and the cursor shape.
- **techie → computer**: mouse and keyboard input.
- No file transfer, no clipboard, no shell, no audio.

Performance target: **as fast as RDP** for normal desktop work (section 5).

---

## 0. Deployment model and constraints

- **Server**: the `helpbuoy-server` executable (`helpbuoy-server.exe` on Windows) in the same folder as the
  desk app, on the techie's own Linux, macOS or Windows machine. It is started from a
  terminal, or runs inside the desk app (7.1), as the logged-in user. Computers reach it on that
  machine's address, which usually means one port forward on the router (TCP 5959) and
  allowing the OS firewall prompt on first start. An existing OpenSSH on port 22 is unrelated
  and untouched.
- **One port for everyone**: the operator picks it once (`helpbuoy-server serve --listen :5959`, the
  default). Techies and computers all use the same `hostname:port`, and an address typed
  without a port means 5959, so with the default users only type the host name. There are
  no per-techie or per-session ports. The default is above 1024, so the server needs no root
  and no capabilities.
- **Runs unprivileged**: no root, no admin, no Docker image. Started by hand or inside the
  desk app it needs no service registration; on a Debian server it can also run as a
  hardened systemd service under its own system user (6.5). The
  server runs as long as its process runs; when the machine or the process stops, computers and
  other techies simply see the server as unreachable until it is started again.
- **Identity = key**. The server maps each public key to exactly one of: techie, registered
  computer, or unknown. User names in the SSH handshake are ignored (computers send `sv`).
- **One group per server**: every techie on a server sees every registered computer and can ask
  to connect to it. Separate groups of techies use separate servers.

### Stored computer configuration

The computer is one generic exe; nothing is embedded or preconfigured. The only thing the user
types is the server's `hostname:port`. Everything else is learned on the first successful
connection and stored in `config.json` in the client's data directory:
`%LOCALAPPDATA%\helpbuoy` on Windows, `$XDG_DATA_HOME/helpbuoy` (by default
`~/.local/share/helpbuoy`) on Linux:

| Field | Example | Learned |
|---|---|---|
| server address | `helpme.example.com` (port 5959 unless given) | typed by the user; the server this PC got registered on |
| server host key | `ssh-ed25519 AAAA...` (`SHA256:3q2+7w...`) | first successful connection, pinned from then on |
| last address | `helpme.example.com` | the last address that reached a server, to prefill the field |
| known techies | `alice` → `ssh-ed25519 AAAA...` | each techie's key, pinned on the first session with that techie (3.5) |

The key is stored next to it, as its own application-specific key pair (6.1). Adding or removing a techie never touches
the computer: it trusts the techies the server announces. Connecting to another address pins
that server's key on first use; only once this PC is registered there does it replace the
stored server, and the techie pins of the old server are forgotten (techie names are only
unique within one server).

---

## 1. Architecture

```
  Techies (many)                  helpbuoy-server host:port                       Computers (many)
┌──────────────┐               ┌────────────────────────────────┐               ┌──────────────────┐
│ raylib desk  │── key: alice ▶│ auth: key -> techie / computer /  │◀ key: anna-pc │ computer (windigo) │
│ own app key, │               │       unknown                  │               │ persistent key   │
│ added by     │◀── presence ──│ registry: online techies,       │── connect ──▶ │ (own app key)    │
│ server admin  │── register ──▶│   online computers               │               │ sv-control       │
│              │── connect  ──▶│ pending connects               │◀─ allow/deny ─│ asks its user    │
│ accepts      │◀─ sv-session ─│ splice: one channel pair per   │◀─ sv-session ─│ opens a session  │
│ sv-session   │               │   techie<->computer session       │               │ once the user    │
│ (one per     │               │                                │               │ allowed it       │
│ computer)      │               └────────────────────────────────┘               │                  │
│ inner SSH    │ ═════════════ inner SSH through each spliced channel ═══════════│ inner SSH server │
│ computer, pins │     channels: screen (computer→techie), input (techie→computer)       │ host key = its   │
│ computer key   │                                                                 │ key; checks the  │
└──────────────┘                                                                 │ techie key        │
┌──────────────┐                                                                 └──────────────────┘
│ techie (bob)  │── ... same, any number of techies and sessions in parallel ...
└──────────────┘
```

### First contact (once per computer key)

1. The client starts for the first time and asks for the server's `hostname:port` (the techie
   tells the user what to type, e.g. `helpme.example.com`, with `:port` only when the server
   doesn't use the default 5959). On this first start
   it generates its own Ed25519 key pair in
   `%LOCALAPPDATA%\helpbuoy\` (section 6.1), with the comment `user@machine`, e.g.
   `anna@ANNA-LAPTOP`, so the public key line reads `ssh-ed25519 AAAAC3Nz... anna@ANNA-LAPTOP`.
2. The computer connects to that address with the key. It shows the server's host key
   fingerprint and pins it (trust on first use). The key is unknown to the server, so the
   connection is **pending**: it can't open anything, it only waits for `Registered`. If the
   connection fails (wrong address, nothing listening, not an helpbuoy-server), the computer says so and
   lets the user correct the address; nothing is stored yet.
3. Once connected, the client shows the public key line (the contents of the `.pub` file)
   with a **Copy key** button and its fingerprint. The user pastes it into a chat with the
   techie (WhatsApp, Signal, mail). The key is public, so the chat needs no secrecy,
   only the assurance that the message really comes from the user.
4. The techie pastes the key line into the desk app (7.2) and, if in doubt, compares the
   fingerprint with the user by phone. The computer is registered under the key's comment
   (`user@machine`); there is no separate label to type. The server stores it permanently
   as a registered computer and sends the computer `Registered`. The computer now stores the
   server address and server host key (the stored configuration above) and switches to its normal window.
5. From now on this key **is** `anna@ANNA-LAPTOP` on this server. It stays registered until a
   techie or admin removes it: no expiry, and restarts, reboots and network changes don't
   matter. Later starts connect to the stored address by themselves and wait for a techie,
   using the pinned server key. The key is never regenerated automatically; it is
   only removed when the user explicitly does so in the app (6.1).

### Session (every time)

The apps carry no text between user and techie. Explaining the problem, and saying it's
fixed, happens over WhatsApp or the phone. The apps also show nothing about sessions starting
or ending.

1. Computer and techies are connected to the server with their persistent keys. Techies see
   `anna@ANNA-LAPTOP` in their list of connected computers.
2. **The techie presses Connect** on the computer's row (7.2), usually while on the phone or
   in a WhatsApp chat with the user. The server creates a request taken by that techie and
   sends the computer `SessionReady`. Other techies see the computer as busy; if two techies
   connect at the same moment, the first one wins.
3. **The computer asks the user** whether that techie may take control, showing the techie's
   name and key fingerprint, with a separate warning if the key changed (3.5).
   If the user refuses, the computer cancels the request and the techie sees "Not allowed".
   The server ends a request whose session hasn't started within 1 minute, so the user has
   that long to answer; the computer then closes the question by itself.
4. Once the user allows it, the computer opens a `helpbuoy-session@helpbuoy` channel for that
   techie. The server opens a matching channel to the techie's connection and splices the
   two (3.6).
5. **Inner SSH** runs over the spliced channel (section 4). The computer is the SSH server with
   its persistent key as host key, the techie is the SSH client with its own key, which the server
   announced in `SessionReady`. Both check the other end's key (3.5).
6. **The client window minimizes**, without any banner or status text, so the techie sees
   the user's desktop and can do the work.
7. **The techie ends the session** when done, with **End session** in the viewer or by
   closing the session's tab. The client window comes back, waiting for the next techie.
8. If more is needed later, the techie presses **Connect** again and the user is asked
   again: every session needs the user's approval.
9. **One techie per computer.** While a techie is connecting or a session runs, no other
   techie can connect to that computer.
10. **Other ways a session ends**: Esc on the computer's own keyboard (the user takes back
   control), exiting the client application (which also disconnects the computer from the server), or
   revoking the computer key. There is no Disconnect button on the client. The server
   connections stay up for the next session.

The server only passes the inner SSH stream through. Nobody on the server, root included, can
read or alter screen data or keystrokes.

---

## 2. Repository layout

```
helpbuoy/
├── go.work                      # use ./proto ./server ./desk ./client-windows ./client-linux
├── PLAN.md
├── proto/                       # shared, pure Go, no GUI deps
│   ├── go.mod
│   ├── serveraddr/               # parse and normalize hostname:port (default port 5959)
│   ├── identity/                # Ed25519 key gen, OpenSSH format, fingerprints,
│   │                            # parsing of pasted keys
│   ├── appkey/                  # create-once, load-only app key in OpenSSH format
│   ├── control/                 # sv-control messages (both sides <-> server)
│   ├── inner/                   # inner SSH server/computer, pinning, channel plumbing
│   ├── screen/                  # screen stream messages, tile codecs, tile cache
│   ├── input/                   # input messages, W3C key codes
│   └── wire/                    # length-prefixed framing
├── server/                       # helpbuoy-server
│   ├── go.mod
│   ├── cmd/helpbuoy-server/             # "serve" plus admin subcommands (3.2)
│   ├── hostkey/
│   ├── store/                   # state.json: techies, computers; atomic writes
│   ├── auth/                    # key -> role lookup
│   ├── registry/                # presence, pending keys, session requests
│   ├── splice/                  # session channel pairing, large-buffer copy
│   ├── limits/                  # per-key then per-IP limits, caps, timeouts
├── desk/                      # raylib desk app (Linux / macOS / Windows)
│   ├── go.mod
│   ├── identity/                # app key (via proto/appkey)
│   ├── serverconn/               # login, reconnect, control channel
│   ├── session/                 # inner SSH client, decoder, input sender
│   ├── render/                  # GPU texture upload of dirty rects (5.5)
│   ├── ui/                      # minimal immediate-mode widgets, layout, fonts, DPI
│   ├── views/                   # computer list, session tabs and viewer, register, settings
│   └── keymap/                  # raylib key codes -> W3C code + scan code
├── client-windows/              # windigo app, Windows 10 2004+ / Windows 11
│   ├── go.mod
│   ├── resources/               # manifest (PerMonitorV2 DPI, comctl6), icon, .syso
│   ├── identity/                # app key in %LOCALAPPDATA%\helpbuoy (via proto/appkey)
│   ├── config/                  # first-run address entry, stored config.json
│   ├── capture/                 # GDI capture, damage detection, scroll detection
│   ├── encode/                  # tile classification and encoding, per-viewer state
│   ├── injector/                # SendInput, key mapping, Esc hook
│   ├── session/                 # server connection, inner SSH server, the one viewer
│   └── ui/                      # main window, request form
└── client-linux/                # static X11 client (jezek/xgb, no cgo)
    ├── capture/                 # X11 capture with MIT-SHM
    ├── injector/                # XTEST input, Esc watch
    └── ui/                      # its own X11 window and widgets
```

---

## 3. The server (`server/`)

### 3.1 Roles and what they may do

The server is deny-by-default. The role comes from the key alone.

| Role | Key is | Allowed |
|---|---|---|
| **techie** | listed as a techie key in `state.json` (plain Ed25519 public key, no certificates) | one `sv-control` channel; incoming `sv-session` channels; control messages |
| **computer** | registered | one `sv-control` channel; one techie connecting or in session at a time; outgoing `sv-session` channels, only toward the techie whose `Connect` its user allowed, and only one session at a time |
| **pending** | unknown | nothing but receiving `Registered`; not shown to any techie (techies register keys from the pasted key line, never from a list); idle timeout 24 h |

Not implemented at all: `session`, `shell`, `exec`, `subsystem`, `pty`, `direct-tcpip`,
`tcpip-forward`, `streamlocal`, techie and X11 forwarding. The attack surface is the SSH
transport in `x/crypto/ssh`, the auth callback, the control parser and the splice.

A key can't be both techie and computer.

### 3.2 State and admin

State directory in the user's data directory, next to the desk app's own data:
`~/.local/share/helpbuoy-server` on Linux, `~/Library/Application Support/helpbuoy-server` on macOS,
`%LOCALAPPDATA%\helpbuoy-server` on Windows (override with `--state`). It holds `hostkey_ed25519` (0600), `verification_salt` (0600, 12.4) and `state.json` with techies
(name, public key) and registered computers (key, `user@machine` name,
registered by, registered at, last seen). Computer registrations never expire; they are removed
only by `UnregisterComputer` or `helpbuoy-server computer remove`. They are added by
`RegisterComputer` or `helpbuoy-server computer add`. Writes are temp file + `fsync` + `rename`.

| Command | What it does |
|---|---|
| `helpbuoy-server serve [--listen :5959]` | run the server on its single port |
| `helpbuoy-server techie list` | list techies with their fingerprints |
| `helpbuoy-server techie add alice 'ssh-ed25519 ...'` | add a techie with the public key copied from their desk app |
| `helpbuoy-server techie remove alice` | remove a techie; takes effect immediately, open sessions are closed |
| `helpbuoy-server computer list` | list registered computers: name, fingerprint, registered by and at, last seen |
| `helpbuoy-server computer add 'ssh-ed25519 ...' ['Anna laptop']` | register a computer key, like Register computer in the desk app; the name defaults to the key comment, and "registered by" is `admin` |
| `helpbuoy-server computer rename SHA256:... 'Anna laptop'` | rename a registered computer; the same as Rename in the desk app, every techie sees the new name |
| `helpbuoy-server computer remove SHA256:...` | remove a registered computer; an online computer is disconnected |
| `helpbuoy-server title ['Example Inc']` / `instruction ['Call +1 234 567 890 for support']` | print or set the title and instruction line the apps show (`''` clears); the same values techies edit on the desk app's Server tab |
| `helpbuoy-server hostkey` | print the host key fingerprint, for techies to compare with what computers show |
| `helpbuoy-server hostkey --new` | (12.3) replace the host key, keeping all techies and computers; like `verification --new` it takes effect at the next server start, since the running server only watches `state.json`; every computer then shows the changed server identity warning (3.5) |
| `helpbuoy-server verification [--new]` | (12.7) print the host key line and the id to enter in the HelpBuoy portal; `--new` makes a new salt, used from the next server start on, and prints its id |

**Techies are plain keys**: there are no certificates. A new techie sends
their public key line to the server admin, who adds it with `helpbuoy-server techie add`. Removing it
with `helpbuoy-server techie remove` (or Remove on the desk app's Techies tab) is the whole revocation story. The server watches `state.json` and
reloads it when the CLI changes it (no signals, so it works the same on Windows).

### 3.3 Control channel

`helpbuoy-control@helpbuoy`, framed messages (`proto/control`): each is a `proto/wire` frame
whose type byte names the message and whose payload is JSON. Control traffic is small and
rare, so JSON keeps it easy to debug; the screen and input streams use binary payloads.
Techie commands carry a sequence number and the server answers each with a `Reply`.

| Message | Direction | Content |
|---|---|---|
| `Hello` | server → any | role, server time, limits; the server's salt for the verified server check (hex), only for registered computers and techies, never for pending keys (12.4) |
| `ServerInfo` | server → any | title and instruction line (e.g. "Example Inc", "Call +1 234 567 890 for support"), right after `Hello` and again whenever they change |
| `ComputerHello` | computer → server | app version, current `user@machine` (the registered name comes from the key comment) |
| `Presence` | server → techies | full snapshot after every change: registered computers (`user@machine`, fingerprint, online, open or taken request with id and time, in session with whom) and techies |
| `RegisterComputer` / `UnregisterComputer` | techie → server | public key line or fingerprint (name taken from the key comment, or given when the comment is empty) |
| `RegisterTechie` | techie → server | public key line and name: adds another techie, like `helpbuoy-server techie add` |
| `UnregisterTechie` | techie → server | name: removes another techie, like `helpbuoy-server techie remove`; an online techie is disconnected, and a techie cannot remove itself |
| `SetServerInfo` | techie → server | new title (up to 64 characters) and instruction line (up to 160), one line each, empty to clear; stored in `state.json` |
| `Reply` | server → techie | answer to a techie command: sequence number and error text if it failed |
| `Registered` | server → computer | the key is now registered (no name: that is only for techies); the salt, like `Hello` (12.4) |
| `RequestHelp` | computer → server | (no content) |
| `CancelRequest` | computer → server | request id |
| `RequestStateMsg` | server → computer | request id and state (open / taken / cancelled / expired / ended) |
| `OpenRequest` | techie → server | request id; the server lets only the first techie win |
| `SessionReady` | server → computer | request id, techie name and public key: a techie wants to connect |
| `Error` | server → any | reason, sent just before the server closes a connection on purpose |

The apps skip a message of a type they don't know, and ignore JSON fields they don't
know, so a newer server can add both without breaking older apps. The server stays strict:
an unknown message from a peer closes its connection ("bad control message").

Session channels carry small SSH-encoded extra data: the computer's open names the request id;
the server's open towards the techie adds the computer's name, key line and current
`user@machine`, so the desk app can pin the computer key before the inner handshake.

### 3.4 Abuse limits

There are no bans and no long counting windows. Every limit is either a rate of one attempt
per second or a cap on what is open at the same time, so nothing stays blocked after the
abuse stops and legitimate computers behind one NAT or company proxy never lock each other
out. A key counts as proven only after its signature has been verified in the SSH handshake.

1. **One connection attempt per key per second**, for every role (techies, registered
   computers and unknown keys). A faster attempt is refused and nothing else happens: the key's
   existing connection is not touched. The apps reconnect with a backoff of at least a
   second anyway.
2. **One connection per key.** When a key connects again, the new connection replaces the
   old one, so a computer whose network dropped (laptop sleep, Wi-Fi switch) gets back in at
   once instead of waiting for the dead connection to time out.
3. **Before any key is proven**, per source IP: at most 10 handshakes at the same time;
   further connections wait for a slot (handshake timeout 10 s). Failed handshakes have no
   further consequences.
4. **Pending connections** (unknown keys): at most 10 per source IP and 200 for the whole
   server at the same time, so someone generating fresh keys from one place can't take every
   pending slot. Closing a pending connection, or registering its key, frees its slot at once.

Role caps and timeouts:
- Per techie: max 20 concurrent sessions. Per computer: exactly one session at a time, enforced by the server and again by the computer.
- Handshake timeout 10 s, keepalive 15 s, dead connections dropped after 3 missed.
- Control frames are length-checked before they are read: 16 KiB from computers and pending
  connections, 256 KiB from techies, so an unauthenticated peer can't make the server buffer
  large frames.
- JSON log to stdout and to `server.log` in the state directory (rotated at 10 MB): auth result, role, fingerprints, source IP, session start/end,
  byte counts. Never payload.

### 3.5 Trust model

- **The server is trust on first use (TOFU)**, for computers and techies alike, like `ssh` with
  `known_hosts`. Its host key is pinned on the first successful connection. Someone who
  controls the network at exactly that moment could substitute their own server. Mitigation:
  the client shows the server fingerprint, and the desk app shows it next to the key it just
  registered, so it can be compared on the same call. After that any change stops the
  computer ("server identity changed") and shows a warning with the old and new fingerprints,
  with Cancel as the default. Only if the user explicitly trusts the new key (after checking
  it with the techie by phone) is it pinned; never a silent update.
- **Techies are vouched for by the server.** Without certificates the computer has no
  independent way to know the techies, so it accepts the techie key the (pinned) server
  announces in `SessionReady`. On top of that, each techie's key is pinned per techie name
  the first time (TOFU again). Before every session the computer asks the user whether the
  techie may take control, showing its name and key fingerprint; if `alice` later
  shows up with a different key, that question also carries a warning.
- **Computer** is trusted because a techie registered its key after receiving it (or its
  fingerprint) over a trusted channel. The desk app pins registered computer keys in the inner
  handshake, independent of what the server says.
- **The user stays in charge**: techies can only ask; every session needs the user's
  approval at the PC for that techie, and Esc or exiting the client application always ends the
  session locally, even if the server misbehaves.
- **Pending keys** are never shown to anyone.
- **Unknown servers**: anyone can run a server and give it any title. The clients will check
  with helpbuoy.com, without a public list, whether the server a techie asks to connect
  through belongs to a company HelpBuoy checked, and show which company if so (12).
- **Server compromise**: sees metadata (who, when, how much) and can deny service. Because the
  server vouches for techies, a compromised server *could* present a techie key of its own to a
  computer that files a request. It can't do so silently for a techie the computer has already
  met (key change warning), it can't impersonate a computer to a techie (the desk app pins
  the registered computer key), and it still needs the user to file a request first. A
  passive or curious server operator can't read sessions: the inner SSH is end-to-end.
- **Computer private key** is an unencrypted, application-specific key protected by a
  user-only file ACL. It is used for nothing else, so stealing it gives no other SSH access;
  it only lets an attacker
  *pose as* that PC to techies (showing a fake screen), never control the real PC.
  The registered `user@machine` is fixed at registration; the inner `Hello` reports the
  current user and machine, and the desk app warns if they differ (key copied to another
  PC or account, or the PC was renamed).

### 3.6 Splice

Each session is one channel pair copied with 256 KiB buffers in both directions and half-close
on EOF. `x/crypto/ssh` fixes the channel window at 2 MiB, which caps one session at about
2 MiB per round trip (e.g. 160 Mbit/s at 100 ms); phase 3 measures whether that matters.
Per-server memory is bounded by the session caps. TCP sockets
use `TCP_NODELAY`. No SSH compression on either layer (the screen stream compresses itself).

---

## 4. Inside the tunnel (`proto/`)

The spliced channel carries a complete **inner SSH connection** (`proto/inner`):

- Inner SSH server = client app. Host key = its persistent key. Accepts public key auth only
  from the techie key announced in `SessionReady` for this session, and checks it against the
  pinned key for that techie name if there is one.
- Inner SSH client = desk app. `HostKeyCallback` accepts only the registered key of the
  computer it asked for.
- Cipher preference `aes128-gcm@openssh.com` (AES-NI, cheap even with two SSH layers), then
  `chacha20-poly1305@openssh.com`. No compression.
- The inner server accepts exactly two channel types and rejects everything else:

| Channel | Direction | Content |
|---|---|---|
| `screen` | computer → techie | `Hello` (hostname, OS user, monitors, DPI), `Frame`, `Cursor`, `CursorPos`; techie sends `Ack`, `SelectMonitor`, `Refresh`, `Quality` |
| `input` | techie → computer | `MouseMove` (absolute, normalized 0..65535), `MouseButton`, `Wheel`, `Key` (W3C `code` + scan code), `Text`, `ReleaseAll` |

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
| Computer CPU while idle screen | < 2 % of one core |
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
- **Tile cache**: the techie keeps 8192 numbered slots (≈ 128 MiB for 64×64 tiles); the
  computer keeps only 64-bit hashes and owns the LRU. A lossless tile can carry "store in slot
  N", and a damaged tile whose hash matches a cached one is sent as a tiny `CacheRef` to slot
  N. Because the computer chooses every slot and frames are applied in order, the two sides
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
  a list of rect ops, so the techie never shows half a frame. A wire frame is at most 16 MiB,
  so a full raw frame above about 4 megapixels (a 4K screen is 33 MB raw) does not fit; with
  the codecs above that is never close, but a full refresh of a noisy 4K screen may still be
  split into parts with a "last part" flag, applied by the techie only when the last part
  arrives. Phase 2 sends raw tiles and caps the test pattern at 1920x1080.
- Later option, only if video-heavy work needs it: H.264 through Media Foundation for large,
  continuously changing regions. Capture stays GDI either way.

With one techie per computer there is exactly one viewer, so capture, damage tracking and the
ack window are a single pipeline with no per-viewer state.

### 5.5 Flow control and the techie side

- At most 2 unacked frames in flight. When blocked, damage accumulates and the next
  frame carries the newest pixels, so latency never builds up in SSH or TCP buffers.
- **Refresh without a cascade**: the first frame after an encoder reset (session start,
  Refresh, new Hello) is marked Full. If the techie can't apply a frame it resets its decoder,
  asks for a Refresh, and then acks but skips frames until the Full frame arrives, because
  frames already in flight were encoded against the old cache. The computer resends the current
  cursor shape after every Hello and Refresh.
- **Alpha**: GDI leaves the alpha byte undefined, so the encoder ignores it in damage
  detection and hashing and sends every pixel with A = 255.
- The rate controller measures ack RTT and throughput and adjusts JPEG quality and frame rate.
- **Cursor** is not in the captured image. Shape changes are sent as `Cursor` (cached by
  handle), position as tiny `CursorPos` messages, and the techie draws it locally. While the
  user allows input, the techie's own pointer is used directly, so pointer movement has zero network
  latency, exactly like RDP.
- **Techie rendering**: decode tiles on a worker pool into an `*image.RGBA`, then upload only
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

The computer has its own key, used for nothing but helpbuoy. It is not the user's
`~/.ssh` key: no techie, no passphrase, no discovery, and no risk that stealing it opens other
SSH logins. Shared code in `proto/appkey`, also used by the desk app.

- **Location**: `%LOCALAPPDATA%\helpbuoy\id_ed25519` and `id_ed25519.pub`. Local, not
  roaming, so the key never follows the user to another PC.
- **Algorithm**: Ed25519 only (`ssh-ed25519`), the modern default of OpenSSH. It is fast,
  has small keys (the public line fits in one chat message) and has no parameter choices to
  get wrong. Server, inner SSH and desk app accept only Ed25519 keys.
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
  confirmation that says techies will have to register this PC again. It deletes
  `id_ed25519`, `id_ed25519.pub` and `config.json`, and the computer continues with a new key. The old
  registration can be removed in the desk app. Deleting the folder by hand has the same
  effect.
- The key is per Windows user, so two users on one PC are two separate registered computers
  (`anna@PC1`, `bob@PC1`).
- The registered name is the key's comment, `user@machine`.

### 6.2 Address and key screens
- "Server address" field (`hostname:port`, port defaults to 5959 if left out) and
  **Connect**. With a stored address the computer connects to it at start by itself, with
  the field locked while it does; the field can only be edited when that fails or after
  **Disconnect**. Errors are shown inline: name not found, connection refused, timeout, not a
  helpbuoy server. A changed server key brings the user back here with the warning (3.5).
- If the server doesn't know this PC's key yet: server fingerprint, the public key line in a
  read-only box, the key fingerprint, a big **Copy key** button, and "Paste this key into a
  chat with a techie (for example WhatsApp)." A link goes back to the address field.
- Waits for `Registered`, then saves the configuration and moves to the waiting screen.

### 6.3 Waiting screen
- The comment of this PC's own key (`anna@ANNA-LAPTOP`) and "Waiting for a techie", with the
  server's title and instruction line below it (see below). The techie connects,
  and the computer asks the question from section 1 (Session, step 3) in a modal dialog.
- **Disconnect** is always available while connected: it leaves the server and goes back to
  the address screen. During a session the same button reads **End session** and ends only
  the session (like Esc); afterwards it is Disconnect again.
- The computer never shows the name techies registered or renamed it under: the server does
  not send it to the computer (neither `Hello` nor `Registered` carry it). That name is only
  for techies.
- There is no settings window.
- **Server title and instruction**: below "Waiting for a techie" the waiting screen shows
  the server's title and, under it in place of the old status text, its instruction line
  (both set by a techie on the desk app's Server tab or with `helpbuoy-server title` /
  `instruction`). The title also goes in the window caption ("Example Inc - HelpBuoy").
  They always come from the server on the current connection; the computer does not store
  them. Without a title nothing is shown there; without an instruction line the computer
  shows the default "A techie can now ask to connect. You will be asked first, and
  pressing Esc always takes back control." Errors and notices take that place while they
  apply.
- **During a session** the window is minimized (`SW_MINIMIZE`), nothing else is shown.
- **When the session ends** (the techie ended it, or Esc), the window is restored and brought
  to the front, waiting for the next techie.
- No tray icon and no automatic start in the first version. The client runs while its window
  is open (minimizing is fine). Exiting the application (closing the window, also from the
  taskbar while minimized) ends any session and disconnects from the server. The user starts the exe when they need help.

### 6.4 Input (`injector/`)
- During a session all input from the connected techie is injected. There is no view-only
  mode and no side panel or on-screen control; the user ends the session with Esc or by
  exiting the application.
- `SendInput` with `MOUSEEVENTF_ABSOLUTE | MOUSEEVENTF_VIRTUALDESK`, keys as scan codes
  (`KEYEVENTF_SCANCODE`, extended flag where needed), `Text` via `KEYEVENTF_UNICODE`.
- All held keys and buttons are released when the session ends.
- **Esc stops the session**: during an active session the computer installs a low-level
  keyboard hook (`WH_KEYBOARD_LL`) on its own thread. A local Esc press (no `LLKHF_INJECTED`
  flag) is swallowed and ends the session; an injected Esc from the techie (flag set) passes
  through to applications as normal, so the techie can still use Esc remotely. The hook
  exists only during a session, so Esc behaves normally otherwise.
- **Stopping** blocks input injection first, releases held keys, stops capture, then closes
  the inner SSH connection. Restoring the window uses `ShowWindow` +
  `SetForegroundWindow`, which is allowed because the computer process handled the last input
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
- **Updates**, like otp4win, but only on request. Like most apps, the window title shows
  the version ("HelpBuoy v0.4.19"), and the waiting screen has a "Check for updates" text
  link in the bottom right corner. Clicking it asks the GitHub API for the latest release;
  if its MSI is newer, the app downloads it to the temp folder, checks its size, starts
  `msiexec` and closes. Otherwise a dialog says this is the latest version, or that the
  check failed. The app never checks by itself. The MSI starts the app when the
  installation is done. A development build has no version in its title and always reports
  it is up to date. It needs the GitHub repository to be public, since the release API
  doesn't show private releases to anonymous callers. Not planned, but it would be good
  once releases are Authenticode-signed: the updater could check the MSI's signature
  (`WinVerifyTrust`, with our certificate) before starting `msiexec`, so that whoever
  controls the GitHub repository or its releases can't push code to every client that
  checks for updates.
- `release.sh [major|minor|build]` bumps the version in both `winres.json` files, runs
  `make vet test dist`, commits, tags `v<version>`, pushes and creates the GitHub release
  with everything in `dist/`: only installers, never a bare binary. That is the client and desk
  app MSIs and the Debian packages of the Linux client, the desk app and `helpbuoy-server`.
  The updaters pick their MSI by name prefix: `helpbuoy_` for the client, `helpbuoy-desk_`
  for the desk app on Windows, which updates the same way: its title shows the version and an
  "Check for updates" link sits in the top right corner (in the tab bar once connected). With sessions
  open it asks to end them first. For now only Linux and
  Windows on amd64 are built (`OSES` and `ARCHES` in the Makefile).
- The server also ships as a Debian 13 package (`make deb`, `packaging/debian/`): `helpbuoy-server`
  as a hardened systemd service under its own system user with its state in
  `/var/lib/helpbuoy-server`, settings in `/etc/default/helpbuoy-server`, and `helpbuoy-serverctl` to run
  admin commands as the service user.
- All state lives in `%LOCALAPPDATA%\helpbuoy`, not next to the exe, so a newer exe
  picks up the same key and registration.

---

## 7. Desk app (`desk/`, raylib)

### 7.0 Toolkit: raylib through raylib-go, without cgo

- `github.com/gen2brain/raylib-go/raylib` built with `CGO_ENABLED=0`. In that mode it calls
  raylib through `purego` and embeds the raylib 6 shared library for Linux, macOS and Windows
  on amd64 and arm64. So the desk app cross-compiles from Linux with plain `go build` for
  every target, like `helpbuoy-server` and the computer: no C toolchain, no Docker.
- On first start the embedded library is written once to the user cache directory
  (`~/.cache/github.com/gen2brain/raylib-go/6.0/` on Linux, the equivalent under
  `%LOCALAPPDATA%` and `~/Library/Caches` elsewhere) and loaded from there. Linux needs the
  usual desktop OpenGL and X11/Wayland libraries at run time, which every desktop has.
- **One window.** raylib has a single window per process, so the desk app is one window
  with a tab bar that is always visible: the fixed tabs **Computers**, **Techies** and **Server**,
  then one tab per session, marked with the lifebuoy app icon and a close button
  that ends the session. Session tabs are wider than the fixed tabs. A session tab shows
  only the session, without the computer list. Fullscreen applies to the active session.
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

### 7.1 Identity and server connection
- Same key handling as the computer (6.1, `proto/appkey`): its own unencrypted Ed25519 key in
  the app's data directory (`%LOCALAPPDATA%\helpbuoy-desk` on Windows,
  `~/.local/share/helpbuoy-desk` on Linux, `~/Library/Application Support/helpbuoy-desk` on
  macOS; not the computer's `helpbuoy`, so the two apps never share a key), generated once on first start, never regenerated automatically. Like the computer,
  **Remove key** is only offered on the damaged key screen (a centered box with the error and
  the key folder). The public key, with **Copy public key**, shows on the connect flow when a
  server does not know this techie yet. No certificates.
- **Connect screen**, like the computer's: every start shows a centered box on the grey window
  with "Connect to your computer", "Type the server address you told the computer to use, for
  example helpme.example.com.", the address field (prefilled with the last address) and
  **Connect**, with the hint "Leave empty to run a local server on this computer, with you as
  its techie." Like the computer, it connects by itself to the last address once that server
  was reached (its host key is pinned), right at the start. Errors show in the box. The server host key is pinned on the first
  connection per address, like the computer; a changed key brings the user back here with a
  warning and an explicit "Trust the new key". If the server does not know this key as a
  techie, the box shows the public key with **Copy public key** to send to a techie on it
  (**Add techie** on the Techies tab) or its admin (`helpbuoy-server techie add`) and keeps retrying, so it moves on by itself once the key is added.
- Once online the tabs appear. The **Server** tab shows the connection status, the address,
  the server fingerprint to read out to users, the server's **title** and **instruction line**
  with **Save** (any techie can change them; every computer and techie gets the new values
  right away), and **Disconnect** (not while sessions are open; back to the connect screen,
  stopping the local server). There is no settings tab: nothing else is configurable.
- One server connection with the control channel, keepalive, reconnect with backoff. All of
  this techie's sessions run as channels over that one connection.
- **Local server**: the server is compiled into the desk app (`server/server`, the same code
  as `helpbuoy-server`). Connecting with an empty address (or `localhost`, or `localhost:<port>`)
  serves the server in-process on that port until Disconnect or exit (unless something already listens on the
  port). There is no separate executable
  to find, so `go run .` works too. It uses the same state directory as `helpbuoy-server serve`, so
  the standalone server and its admin commands share the techies, computers and host key. The
  app connects to `localhost:<port>` itself, while computers and other techies use the
  machine's public `hostname:port`.
- **This techie on the local server**: if this techie's key is not a techie there yet, the app
  adds it to the server state directly (with a number after the name if that name is taken),
  so nobody has to copy keys around. Other techies are added with **Add techie** on the Techies tab
  or with `helpbuoy-server techie add` by whoever runs the server.
- **Distribution**: an installer per OS containing only the desk app: a per-user MSI on
  Windows (`installer/helpbuoy-desk.wxs`, into `%LOCALAPPDATA%\Programs\helpbuoy-desk`), a
  Debian package (`make deb-desk`, needs glibc 2.38 for the embedded raylib, so Debian 13 or
  Ubuntu 24.04) and the macOS `.app` bundle. The server is built in, so `helpbuoy-server`
  is not packaged with it; it ships separately as the Debian package. All built on Linux with
  `CGO_ENABLED=0`; the macOS `.app` is assembled by a script (Info.plist, `.icns` written in
  Go from the PNG icon). No Docker image.

### 7.2 Computer list and connecting
- The **Computers** tab shows one row per online computer
  (`user@machine`). Registered computers that are offline are in a collapsed "Offline" section
  below.
- The **Techies** tab lists every techie on the server, online first, with an online dot,
  "(you)" for this techie, and whom they are helping (otherwise the key fingerprint).
- **Connect**: every online computer that nobody is helping has a **Connect** button. It
  sends `Connect` with a fresh request id; the row then says "Waiting for the user to allow
  the session…" until the session channel arrives (a tab opens) or the request goes away,
  which shows "Not allowed: the user refused or did not answer". Rows of computers another
  techie is connecting to or helping show that instead of the button.
- **Register computer** and **Register techie** are tabs that only show after a click on
  **Add computer** (top right of the Computers tab, next to the title) or **Add techie** (same
  place on the Techies tab). At most one of them is open, always in the same place, between
  Techies and Server, with a close button. Add on an already open one just switches to it;
  the other one replaces it.
- **Register computer**: a text field for the key line the user sent in the chat. It tolerates
  what chat apps do to pasted text: line breaks, leading/trailing spaces, quotes, non-breaking
  spaces, and a preceding message ("here is my key: ssh-ed25519 ..."). Accepts only
  `ssh-ed25519`; an RSA or ECDSA key gets a clear "this is not a helpbuoy key, ask the
  user to copy the key shown in the app" message. The app
  shows the name and the fingerprint large for comparing on the phone, plus the server fingerprint
  the computer should be showing. The name field is
  prefilled with the key's comment (`user@machine`) and always editable ("Anna laptop");
  it is required when the comment is empty. Registrations are permanent until removed.
- **Register techie** works like Register computer for another techie's key line (their
  app shows it when they connect to this server before being added): the name is one word, prefilled from the user part of the key comment,
  and the fingerprint is shown large for the phone. Any techie on a server can add techies.
- **Remove techie**: hovering a techie row on the Techies tab shows Remove (not on your own
  row); it asks "Remove this techie?" with Remove and Cancel, like removing a computer, and sends
  `UnregisterTechie`. The removed techie is disconnected at once, ending its sessions.
- **Forms reset** when their tab is closed (or replaced): the Register form and its result
  are cleared. Leaving the Computers tab drops open renames and confirmations.
- **Rename**: hovering a computer row shows Rename and Remove; Rename (or a double-click) turns
  the name into an inline field (Enter saves, Esc cancels) and sends `RenameComputer`. The
  server stores the name for everyone, so every techie's list shows it. When the
  `user@machine` the computer reports differs from the name, it shows as a muted second line.
- **Remove** a computer (unregister) from the list.
- Each opened session becomes a tab after the fixed ones; any number at once. When a
  session ends its tab goes away and the Computers tab is shown. Closing the window ends all
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
  combinations are never sent to the computer; Right Ctrl with any other key, or with a mouse
  button, acts as a normal Ctrl on the remote side, and tapped alone it is sent as a press
  and release.
- **Cursor**: while the pointer is over the view, a pointer is always drawn at the local
  mouse position (the remote shape when known, else an arrow) and the OS cursor is hidden,
  so moving has no network latency and the pointer never disappears, even when the computer
  reports its cursor as hidden. Elsewhere the remote cursor is drawn where the computer
  reports it.
- Input while the mouse is over the view or the tab has keyboard focus: mouse position and
  buttons, wheel, keys (mapped in `desk/keymap` to W3C codes, checked against
  `input.Codes`), and characters without a physical key as `Text`.
- **Toolbar**: End session, Win, Alt+Tab, monitor selector, quality (Auto / Sharp / Fast),
  Fit / 1:1, Resize, Fullscreen, and an fps, RTT and Mbit/s readout.
- **Ending**: **End session** in the toolbar, or the close button on the session's tab,
  closes the inner SSH connection; no confirmation dialog. When the user ends the session (Esc or
  exiting the client application), the tab just closes. Esc pressed in the viewer is sent to
  the computer as a normal key.

---

## 8. Phased delivery

**Phase 0 - skeleton (1 day)** (done)
`go.work`, four modules, CI: tests for `proto` and `server`, cross-build of the Windows client
from Linux, `CGO_ENABLED=0` cross-builds of the desk app (raylib-go purego) for Linux,
macOS and Windows, release archives per OS with the desk app and `helpbuoy-server` side by side,
macOS `.app` assembled on Linux.

**Phase 1 - server core (3-4 days)** (done)
`helpbuoy-server serve`, host key, state store, key-to-role auth, techies, pending
state, control channel, presence, session requests, splice, limits, admin CLI. Headless
`cmd/faketechie` and `cmd/fakecomputer` in Go. Tests for every role × every channel and request
type, techie and computer removal, many techies and many computers at once.

**Phase 2 - apps on the server (3-4 days)** (done)
`proto/identity`, `proto/serveraddr`, `proto/appkey`, `proto/inner` with mutual pinning, fuzz
tests. Desk app: settings, computers list, paste registration, help requests.
Windows client: app key creation, loading and removal, first-run
address entry and key window, stored configuration,
Request help button, techie computer list with request notifications, sessions with a test pattern instead of the
screen.

**Phase 3 - fast view only (5-7 days)** (done; `helpbuoy-bench` passes the 5.1 targets on synthetic scenes)
GDI capture into DIB sections, tile damage, solid/palette/JPEG codecs, tile cache, flow control,
techie renderer benchmark and GPU upload path. Benchmark harness
that replays recorded screen sequences (typing, scrolling, video) and reports fps, bytes and
latency per scenario. Milestone: meets the 5.1 targets for typing and menus.

**Phase 4 - control and scrolling (4-6 days)** (implemented and checked under Wine; the side-by-side RDP comparison needs real Windows)
Mouse, wheel, keys, release of held keys, minimize on start, techie ends the session, Esc stops
the session via keyboard hook, window restored for the next request, local cursor rendering, scroll
detection with `CopyRect`, lossless refine, rate controller. Milestone: meets all 5.1 targets
side by side with an RDP session on the same machine and network.

**Phase 5 - polish**
Multi-monitor, performance mode, restart as administrator, code signing,
server metrics endpoint (localhost only).

**Phase 6 - verified servers (12)** (apps and server done; the site and the portal are
still to do)
The salt in `Hello` and `Registered`, `helpbuoy-server verification` and `hostkey --new`,
the check and dialog in both clients, the Server tab check in the desk app, the cleanup of
server text (all in `proto/verify`, `server/salt` and the apps), and the files, cache
headers and privacy text on the site.

**Later**: tray icon and optional "Start with Windows" (HKCU Run entry, no admin), so the
computer can stay online in the background; `client-macos/` reusing `proto`, the encoder
and the `Capturer` / `Injector` interfaces, like `client-linux/` does.

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
- `server`: Go tests with `x/crypto/ssh` computers: pending key can do nothing, computer can only
  open sessions for a `Connect` it was sent, toward that techie, a techie can't cause a
  session without the computer opening one, only one techie wins when several connect at once, a second
  techie can never open a session to a computer that is already in one, removed techies and computers are refused at once, 100 techies and 1000 computers connected at once,
  server restart keeps registrations, one attempt per key per second (a refused attempt
  leaves the existing connection alone), 500 registered computers behind one IP all connect
  while that IP's pending slots are full, a flood of fresh keys from one IP fills only that
  IP's pending slots, and nothing stays blocked after failed handshakes.
- Malicious server test: a patched server that splices a techie to the wrong computer (the techie
  app must refuse the inner handshake), changes its host key (both apps must refuse; the computer warns and only reconnects if the
  user trusts the new key), or
  announces a different key for a techie the computer already knows (the computer must warn
  and not connect without the user's consent).
- Verified servers (12): old apps keep working against a server that sends the salt; a
  pending key never gets the salt; an older binary saving `state.json` leaves the salt
  unchanged; `hostkey --new` keeps every techie and computer; the id matches fixed test
  values; a salt of the wrong length or a non-Ed25519 host key means not verified; host
  names match without port, case or trailing dot, and an IP address never matches; a file
  with another `hostkey` or without the host name is not verified; a malicious server that
  sends a verified server's salt is not verified; a 500, a timeout, a redirect or a file
  over 4 KB is "could not be checked"; a file whose `verified` date is more than 13 months
  ago is not verified; a flood of requests starts at most one check every 5 seconds; a
  late answer never reaches a later request; server text with check marks, "verified",
  control characters or bidi overrides is cleaned everywhere it is shown.
- Performance: the replay harness from phase 3 in CI (client encoder against techie decoder,
  no GUI), with regression thresholds; `tc netem` runs at 20 ms / 80 ms RTT and 10 / 50 Mbit/s.
- Session end tests (`faketechie`): End session on the techie closes the session and
  restores the client window; local Esc ends the session and no techie input is injected
  after it; injected Esc does not end the session; the next Connect asks the user again
  and gives a new session; refusing a Connect starts nothing; closing the client window ends the session and disconnects the computer
  from the server.
- Windows: capture and key-map tests on a `windows-latest` runner; manual matrix of Windows 11
  at 100/150/200% scaling, multiple monitors, RDP session, elevated window (including Esc while an elevated window has focus), and a side-by-side
  comparison with `mstsc` for the 5.1 scenarios.

## 10. Open questions

- Default port: 5959, clear of TeamViewer's 5938, so it doesn't clash with a TeamViewer
  installed on the same machine. Above 1024, so no privileges are needed; 443 would pass
  more corporate firewalls but needs privileges on Linux and may still meet TLS inspection.
- Does a server on the techie's own machine behind a home or office router need a dynamic DNS
  name in the docs, since computers store `hostname:port` once and never change it?
- Is TOFU for the server plus server-vouched techies enough, or should the computer also be able
  to check a short code (e.g. the first 8 characters of the server fingerprint) that the techie
  reads out before anything is stored?
- macOS: an `.app` built on Linux is only ad-hoc signed, so Gatekeeper asks users to allow
  it the first time (right-click, Open). Is that acceptable for techies, or is a Developer ID
  signature and notarization (needs a Mac or a signing service) required?
- Is JPEG enough for video-heavy work, or is the Media Foundation H.264 path needed in v1?

## 11. Translations

The Windows client and the desk app are translated; `helpbuoy-server` stays English. The
approach is deliberately minimal, with no dependency:

- **`proto/i18n`**: one function, `i18n.Trans(text string, args ...any) string`. The English
  text is the key (`i18n.Trans("Connect")`, `i18n.Trans("Allow {0} ({1}) to help you?", name,
  fingerprint)`), so there is no English catalog and call sites stay readable. After lookup,
  each `{0}`, `{1}`, ... is replaced with `fmt.Sprint` of the argument at that index, so
  translators can reorder or repeat placeholders freely. No `fmt.Sprintf`, so a stray `%` in a
  translation is harmless. A placeholder without a matching argument is left as is.
- **Catalogs**: one flat JSON object per language mapping English to translated text,
  `proto/i18n/locales/nl.json` and so on, compiled into every binary with
  `//go:embed locales/*.json`. Nothing is read from disk at run time.
- **Language**: picked once at start from the OS (Windows `GetUserDefaultUILanguage`, Linux
  `LANG`, macOS `AppleLanguages`). An unknown language, or a string missing from the
  catalog, shows English.
- **No plurals**: counted strings are phrased so the number needs no agreement
  ("Computers online: 3").
- **Test**: every key in every catalog must appear as an `i18n.Trans` string literal in the
  code, and its translation must use the same set of `{n}` placeholders.
- **Desk app fonts**: the codepoint set in `desk/ui/ui.go` adds the characters used by the
  embedded catalogs. Scripts the Go fonts lack (CJK, Arabic, Hebrew) are out of scope.

Dutch, German, French, Spanish and Italian are done (about 210 strings each). Rejected:
go-i18n, `x/text` with `gotext`, and gettext `.po`, all more machinery than this needs.

## 12. Verified servers

The server and the apps are built (`proto/verify`, `server/salt`); the site and the portal
are not yet. When a techie asks to connect, the client checks with helpbuoy.com whether
the server it is connected to belongs to a company HelpBuoy checked, and the approval
dialog says so: "Verified: Example Inc, Netherlands". Both clients (Windows and Linux)
make the check and show the same dialog. There is no public list: who our customers are
must never be readable or enumerable from what we publish. Scam reports go to the email
address on the site, with the server address the scammer used.

### 12.1 The problem

Remote support tools are popular with scammers. They call someone, pretend to be the
bank, Microsoft or the police, and talk the victim through installing the tool and
letting them in. HelpBuoy already makes this harder than most: the user allows every
session, Esc always takes back control, and there is no unattended access. But anyone
can run `helpbuoy-server`, give it a title like "Microsoft Support", tell the victim its
address and ask them to click Allow.

### 12.2 Requirements

1. At the moment someone asks to take control, the user sees whether a company HelpBuoy
   checked is behind the server.
2. Nobody can list or enumerate our customers: not from anything we publish, and not by
   scanning the internet for servers and checking each one.
3. A scammer can't look verified: not with a server title, a lookalike address, or values
   or files copied from a verified company.
4. Checked on every connection request, without a cache in the client or on the way, so a
   removal works at once.
5. The check during a connection request uses only static files on helpbuoy.com over
   HTTPS, at URLs that can't be guessed. The portal where companies ask for verification
   (12.7) is never contacted by the apps.
6. No signed statements, and nothing for server admins to manage: the server makes its
   own salt and keeps it.
7. Unverified servers keep working: families, IT shops and the local server of the desk
   app are warned about, never blocked.
8. When helpbuoy.com can't be reached, the client says so and never shows "Verified".
9. Adding or removing a verification is adding or deleting a file and releasing the site.
10. helpbuoy.com learns as little as possible, and the privacy text on the site says what.

### 12.3 One file per server

A scanner can get any server's host key, so the host key alone can't name the file. The
server therefore adds a value that a scanner never gets: a random 256-bit salt, made once
when there is none (on the first start, or the first start after an upgrade) and kept in
its own file next to the host key (3.2, 12.4). The server sends it only to registered
computers and techies, never to pending or unknown keys (12.4).

```
https://www.helpbuoy.com/verified/<id>.json
id = hex(SHA-256("helpbuoy verified server v1\n" + host key in wire format + salt))
```

```json
{
  "hostkey": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA...",
  "addresses": ["helpme.example.com"],
  "company": "Example Inc",
  "country": "NL",
  "verified": "2026-11-06"
}
```

- **Not enumerable:** to compute an id you need the salt, and to get it you must be a
  registered computer or a techie on that server. Scanning servers only yields host
  keys. The id is 256 bits, there is no index, list or directory listing, and an unknown
  id gives the same 404 as any missing page. A company's own users can at most learn
  about their own company, which they already know they are talking to.
- **Not spoofable:** a scammer who gets the salt and the file of a verified company (by
  registering a computer there, for example) can make their own server send that salt,
  so the client finds the file. But the client only shows the badge if the file's
  `hostkey` is the host key it pinned for the server it is connected to, and presenting
  that host key in the SSH handshake takes the server's private key. All of this is
  checked before the dialog says anything.
- **Why the file holds the host key:** the id already depends on the host key, so a
  scammer's own key never finds the file. But we can't check the id a company gives us
  without its salt (12.7). A dishonest company could show us its real server at its
  domain and give us the id of a second server, run elsewhere for scams. The `hostkey`
  field, which we did check, is what stops that: the client must always compare it, and
  it must never be dropped as redundant.
- **Not cloneable without the company's DNS:** the host private key and the salt are in
  the state directory, so whoever has a copy of it (a backup, a departing admin, malware
  running as that user, or a techie laptop running the desk app's local server, which
  uses the same directory (7.1)) can run the same server anywhere. That is why the file
  also lists the server's `addresses`, the host names we checked on the company's domain
  (12.7). The client only shows the badge if the host name of the address of this
  connection is one of them (12.5), and shows that address in the badge. A clone then also
  needs to take over the company's DNS. A host name is compared without port, in lower
  case and without a trailing dot; an IP address never matches.
- **A leaked salt** only tells someone that one server is verified, nothing more. It can
  be rotated (12.7).
- **`company`** and **`country`** come from our check of the company (12.7). The client
  shows them in its own text and style; the server's title can say anything and never
  makes anything look verified.
- **The files live in `static/verified/` of the site repository**, which must stay
  private (it is now). If the site repository is ever made public, the files move to a
  separate private repository that is copied in when the site is built. The site keeps
  directory listing off for `/verified/`, and no page or sitemap links to the files. The
  same goes for the built site: it must never be pushed to a public repository (such as
  one serving GitHub Pages), where every file could be listed.
- **Trust:** the files are trusted because they come from helpbuoy.com over HTTPS; there
  is no signature. So a file is as safe as whatever can change what helpbuoy.com serves:
  the domain, the TLS certificate, the web host and push access to the site's production
  remote. The clients use the operating system's normal certificate checks and ship no
  root certificates of their own: pinning roots would tie every CA change to a client
  release, a mistake there would make every check fail at once, and a company network
  that inspects TLS would never see the badge. helpbuoy.com gets CAA records and DNSSEC
  (12.10), which cost the apps nothing.

What this design gives up: there is no techie name in the file, only the company. Every
techie the company adds to its server is vouched for by the company, and the badge can
only be taken away from the whole server, not from one techie. A new host key (a
reinstall without keeping the state directory) needs a new file, but registered
computers already refuse a changed host key (3.5), so that is a new setup anyway. A new
or changed host name needs a changed file. Computers that reach the server by a name we
can't check from the internet (an internal name like `helpdesk.corp.local`) or by an IP
address never get the badge, so a verified company should have all its computers use the
public host name.

The state directory of a verified server must be guarded like a private key. When a
company thinks a copy has leaked, it tells us, and together we decide what it takes. A
copy alone is not enough to look verified, because a clone also needs the company's DNS
(above), so often closing the leak is enough and the badge stays. A new salt (12.7) does
not help against a clone: whoever has the copy has the host key too, and can learn the
new salt like anyone else, by getting a computer registered on the real server. It only
keeps the fact that the server is verified from spreading further.
Only when the DNS may be at risk too, or the leak can't be closed, do we delete the file
(12.8) and does the company make a new host key with `helpbuoy-server hostkey --new`,
which keeps all techies and computers. Every registered computer then shows the changed
server identity warning (3.5). That is a risk of its own: "your helpdesk changed its key,
click Trust" is exactly what a scammer would say, so a new host key is the last resort,
and the company tells its users by its own channels, not through the server's title or
instruction line.

Risks we accept:

- **Whoever controls a verified server's process can vouch for anyone.** The server
  announces the techie key in `SessionReady` (3.5), so a hacked server, or its admin,
  can present any techie under the badge. Only the company's own security protects this.
- **A rogue techie on a verified server is vouched for**, and so is someone using a
  hacked desk app of a techie there. Any techie can also add more techies with
  `RegisterTechie`, without us or the company's contact person seeing it. Only the
  company can stop this, by removing the key (12.8), and if it doesn't, the whole server
  loses its badge.
- **A leaked id or file reveals the host key**, and internet scanners such as Shodan
  index SSH host keys, so the host key leads to the server's IP address. Someone with a
  leaked file can find that company's server, but still learns nothing about any other
  customer.
- **Whoever can make a computer trust a forged helpbuoy.com certificate can show any
  file**, with their own `hostkey` in it: a TLS-inspecting proxy, or a root certificate
  the user was talked into installing. A company proxy is trusted by its own computers
  anyway, and a scammer who can get a root certificate installed could get far worse
  installed too, so this doesn't justify shipping our own roots (see Trust above).
- **The web host sees which IP addresses ask for which id**, and with access to the
  files it can link computers to companies. We keep that to the web host we choose, and
  the privacy text says so (12.10).

### 12.4 Protocol change

No new message: the salt is a `salt` field (hex) in `Hello`, only when the role is computer
or techie, and in `Registered` (3.3). A pending key never gets it. Older apps ignore the
field, so they keep working against a newer server. Since v0.4.22 the apps also skip
message types they don't know (3.3), so a later change can still add a message.

The salt is exactly 32 bytes from `crypto/rand`. It is kept in its own file,
`verification_salt` in the state directory, created once with mode 0600 and never
rewritten, exactly like `hostkey_ed25519`. Not in `state.json`: an older desk app or
`helpbuoy-server` sharing the state directory (7.1) drops fields it doesn't know when it
saves `state.json`, which would silently replace the salt and break the badge. The salt is
never printed or logged: `helpbuoy-server verification` prints only the host key line and
the id (12.7). Clients keep it only in memory for the connection, and treat a salt of any
other length as no salt, so a broken server can never end up with an id a scanner could
compute.

During a rotation (12.7) the next salt is in `verification_salt.next`, made the same way,
and the next server start renames it over `verification_salt`. It is not sent to anyone
until then: that start drops every connection, and each app gets the new salt in its next
`Hello`. A new host key from
`hostkey --new` waits the same way, in `hostkey_ed25519.new`.

### 12.5 When the client checks

On every `SessionReady`, without a cache:

- The client computes the id from the host key the server presented in this connection's
  handshake, which is the one it pinned for the address of this connection: a connection
  with a host key that is not pinned (3.5) never gets this far. Only Ed25519 host keys
  count. Without a valid salt (an older server), the server is "not verified", and there
  is no fallback to an id without salt.
- The check starts as soon as the request arrives. The approval dialog opens with the
  result, or after 3 seconds with "Checking who runs this server..." and the same warning
  as not verified (12.6), and updates when the answer arrives. Each check belongs to one
  request id, so a late answer is dropped once that request is answered, cancelled or
  expired, and can never show up in a later dialog.
- Only `https://www.helpbuoy.com/verified/<id>.json`: no redirects, no plain HTTP, no other
  host and no address from configuration. At most 4 KB and 10 seconds.
- At most one check in flight, and at most one started every 5 seconds; the check for a
  request that comes sooner starts when the 5 seconds are up, so the dialog shows
  "Checking who runs this server..." until then. So a hostile server can't use its
  computers to flood helpbuoy.com.
- A 404 means not verified. A file whose `hostkey` is not the pinned host key, or whose
  `addresses` don't hold the host name of the address of this connection, also means not
  verified. At first contact that is the address the user just typed, even before it
  replaces the stored server (section 0). A file whose `verified` date is more than 13
  months ago also means not verified, so a company that drops out of the yearly check
  (12.7) loses the badge even if we forget to delete its file. Any other status, a timeout
  or a file that does not parse means "could not be checked". Neither is ever shown as
  verified.
- The request sends `Cache-Control: no-cache`, so a proxy on the way can't answer it
  from its cache.
- The request carries nothing but the id, with `helpbuoy/<version>` as User-Agent.
  helpbuoy.com does see the computer's IP address and which server id it asked for, so
  the privacy text on the site must say so, and the access logs should be kept short or
  without the path (12.10).

Before registration there is no salt, so the first connection to any new server gets
the same general warning (12.6). That is fine: the scam needs the Allow click, and that
is where the check happens.

The desk app makes the same check for its own server on the Server tab ("Verified:
Example Inc, for helpme.example.com" with the file's host names, or "Not verified"), so
the company can see its file works. It compares the host key only, not the address: on its
local server (7.1) it connects to `localhost`, which is never in `addresses`, and its user
is the techie, who sees which host names the badge is for. It shows no approval dialog.

### 12.6 What the user sees

When a techie asks to connect:

- **Verified:** "Verified: Example Inc, Netherlands", with the server address on the line
  below ("Server: helpme.example.com"), above the usual question with the techie's name
  and key fingerprint, and below it a small line: "HelpBuoy checked this company. It
  vouches for its support staff." The country comes from the file, the address is the
  one of this connection, matched against the file (12.5), and the techie's name is the
  one the company's server gives.
- **Not verified:** "HelpBuoy has not verified who runs this server. Only allow this if
  you know the person asking personally. Banks, Microsoft and the police never ask for
  this."
- **Could not be checked:** "HelpBuoy could not check who runs this server. Only allow this
  if you know the person asking personally. Banks, Microsoft and the police never ask for
  this." The same warning as not verified, never a softer one, so "it says could not be
  checked, that's normal" doesn't help a scammer.

Right after the first successful connection to a new server, before the key screen
(6.2), the client shows a full warning:
"Only continue if someone you know personally gave you this address. If someone called
you unexpectedly, hang up: banks, Microsoft and the police never ask you to install
this." Continue only works after 5 seconds. It is shown once per server key, so people
who run their own server see it once per computer.

Nothing is ever blocked: a techie on an unverified server can still be allowed. Blocking
unverified servers can come later as an option for managed installs.

The server title, the instruction line and the techie name are text from the server, so
they could try to imitate the badge. Everywhere the client shows them (the approval
dialog, the waiting screen and the window caption, 6.3), it shows each on one line,
removes control characters and bidi overrides (such as U+202E), and replaces a check mark
(✓, ✔, ☑ and the like) or the word "verified", in any language the apps are translated to,
with nothing. The badge has its own area of the dialog, with its own icon and color, where
server text is never shown.

### 12.7 Getting verified

Verification is for paying customers only, as part of a support plan; which plans
include it is still to decide. A company asks in our portal and we check it once:

1. The company is registered (for example in the Dutch KvK), under the name it gives.
   Later this can be eHerkenning, the Dutch eIDAS means for acting on behalf of a company.
2. The company does real support work: it says what it does and for whom, and that
   matches its registration and its website. A paying customer is not automatically an
   honest one, and tech support scams are often run by registered companies. We refuse
   a company whose name or domain resembles a bank, a large tech company, a government
   body, the police or HelpBuoy itself, and one that finds its customers by calling
   people unasked. When in doubt, we refuse.
3. Its server addresses are host names on a domain of that company.
4. A contact person at the company, signed in to the portal with an email address on
   that domain, asks for the verification and agrees to the rules in 12.8. The contact
   person is kept in our own records, never in the file.
5. The company's admin runs `helpbuoy-server verification` on the server, which prints
   the host key line and the id, and the contact person enters both in our portal, with
   the server's host names.
6. We connect to each host name ourselves and check that it presents that host key.
   This proves the key belongs to a server on the company's domain. We can't check the
   id without the salt, which is why the client also checks the file's `hostkey` and
   `addresses` (12.3). An id that is simply wrong only means the company's own file is
   never found.
7. We write the file under that id and release the site; publishing stays a manual step,
   so nothing writes to the site by itself.

To rotate the salt, `helpbuoy-server verification --new` makes the next salt (12.4) and
prints its id; the server uses it from its next start on. The company enters the id in
our portal and we add the new file next to the old one. The company should restart the
server only once the portal says the new file is live. A restart before that, planned or
not (a reboot, or a Debian package upgrade restarting the service), makes computers see
the server as not verified until the file is live: the full warning shows, never a wrong
badge, so that is acceptable for something as rare as a rotation. We delete the old file
when the company tells us in the portal it restarted, or 30 days after the new file went
live, whichever comes first.
A new host key goes the same way, as a new file next to the old one. A new or removed
host name only changes the file, after we checked the new name as in step 6.

We check the company again every year and then set `verified` in its files to that date;
the clients stop trusting a file 13 months after its `verified` date (12.5).

The portal holds every verified company with its ids, host names and contact persons,
so it is the most sensitive part: whoever gets into it can list our customers. It never
tells anyone whether a host key, id or host name is already verified, also not as an
error when one is entered twice. Contact persons sign in with a second factor. A
removal or rotation asked for in the portal is confirmed by email to every contact
person of that company before we act on it, so one taken-over account can't quietly
change the company's verification.

### 12.8 Removing a server

We delete the server's file and release the site when:

- the company asks, or a copy of its state directory leaked and we decided the badge
  can't stay (12.3);
- its server's host key or salt changed: then we delete the old file as 12.7 describes,
  once the new one is live;
- the server or company is used for scams;
- the company no longer exists, stops paying for its plan or does not answer the yearly
  check (then all its files).

Because the clients check on every request and the site serves these files uncached
(12.10), the next request already shows the server as not verified. Removing is never
wrong: the company can ask again.

The company is responsible for every techie key on its server, and must remove a techie
who leaves the same day. A rogue techie at a verified company can only be stopped by the
company removing their key; if it doesn't, the whole server loses its badge.

### 12.9 Rules that stay

These keep HelpBuoy a poor tool for scammers, verified or not:

- The user allows every session. No unattended access, and the techie can't turn the
  question off.
- Esc always takes back control. The techie can never block the user's own keyboard or
  mouse.
- The user's screen is never blanked or hidden. Scammers use that to hide what they do
  in the victim's bank.

### 12.10 To do on the site

- A warning at the top of the download page, like RustDesk's: only install HelpBuoy if
  someone you know personally asked you to.
- The support plans (`data/plans.yaml`) must offer verified servers, once it is decided
  which plan includes it.
- The web host's access logs contain the server ids of every verification request, so
  they should be kept short or without the path.
- The web host and any CDN must serve `/verified/`, 404s included, with
  `Cache-Control: no-store`, so a deleted file is gone at once. A release purges any
  cache for `/verified/`, and after removing a server we check that its URL answers 404.
- No analytics or "top pages" dashboard may cover `/verified/`, from the web host or
  anyone else: such a list of paths is a list of our customers' ids.
- No rate limit on `/verified/` that a large office behind one IP address could hit; the
  clients already limit themselves (12.5).
- CAA records for helpbuoy.com that allow only the CA that issues its certificate, and
  DNSSEC for the domain (12.3).
