# HelpBuoy: self-hosted remote support over a custom Go SSH server

A self-hosted alternative to TeamViewer QuickSupport. The server is **a single Go binary
(`helpbuoy-server`) that speaks SSH over a WebSocket over HTTPS** (section 0). Everybody, techies and computers,
connects to that same `hostname` (port 443 unless given). **The public key a connection authenticates with is its identity**; the SSH
user name is ignored. Keys are long-lived: they are generated once on first start and reused
for every session.

The server is not OpenSSH. It has no shell, no filesystem access, no file transfer and no TCP
forwarding to real hosts. It only authenticates keys, tells techies and computers about each
other, and splices their channels together.

- **server/**: `helpbuoy-server`, a plain executable (also built into the desk app) that runs
  locally on the techie's machine as a normal user process, or on a server behind a reverse
  proxy. It keeps techies and registered computers in a small state directory, one per virtual
  host. No container.
- **desk/**: the cross-platform raylib app the *support techie* runs (`raylib-go`, no cgo). Many techies can be online at
  once, and each techie can run sessions with many computers at once.
- **client-windows/** and **client-linux/**: the app the person *receiving* help runs,
  for Windows (windigo) and for Linux (X11 and Wayland). The Windows client is described here and in
  section 6; the Linux client works the same way, with three differences. On Wayland it
  shares through the XDG remote desktop portal (RemoteDesktop with ScreenCast, over D-Bus):
  after the user allows the techie, the compositor also asks which monitor to share, and
  the frames come from the portal's PipeWire stream through a `gst-launch-1.0 pipewiresrc`
  child process, which needs `gstreamer1.0-pipewire` and `gstreamer1.0-tools` (without
  them, or without the portal, it explains what to install or how to log in with X11).
  The portal's restore token is kept in the data directory, so later sessions may skip the
  monitor choice. On X11 none of that is used. And a local Esc ends the
  session but also reaches the focused application, since X can't take one key away from
  other applications without also taking the injected presses (on Wayland it is only seen
  while an X11 window, such as HelpBuoy's own, has the focus). **client-macos/** shares the
  Linux client's drawn window and works the same way too; before anything else it asks for
  the Screen Recording and Accessibility permissions, and like Windows it swallows the local
  Esc (a CGEventTap, which tells the injected Esc apart by a mark the injector puts on its
  events). It captures each display at its size in points rather than Retina pixels, a
  quarter of the pixels to encode and send. On first start it asks
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
- **computer → techie**: screen updates and the cursor shape (GDI on Windows, X11 or the
  portal's PipeWire stream on Linux, ScreenCaptureKit or CoreGraphics on macOS).
- **techie → computer**: mouse and keyboard input.
- No file transfer, no clipboard, no shell, no audio.

Performance target: **as fast as RDP** for normal desktop work (section 5).

---

## 0. Deployment model and constraints

- **Transport**: the apps open `wss://host:port/helpbuoy` (`proto/transport`) and run SSH
  inside the WebSocket, one binary message per write, with the subprotocol `helpbuoy-ssh`.
  This gets through office, hotel and school networks that only let web traffic out, and
  through HTTP proxies: `HTTPS_PROXY`, else the system's proxy settings (`proto/sysproxy`:
  Internet Options on Windows, `scutil --proxy` on macOS, GNOME's settings on Linux). A
  "Use system proxy settings" box under the address field, ticked by default, turns that
  off; the apps then connect directly. The TLS certificate is not checked: the pinned SSH
  host key identifies the server (3.5), so self-signed certificates and TLS-inspecting
  proxies work and can't pose as the server.
- **Server**: the `helpbuoy-server` executable (`helpbuoy-server.exe` on Windows). It runs in
  one of three modes; exactly one is on per server. By default (`--mode ssh`) it serves plain
  SSH on `:5959` (direct connect, below). With `--mode http` it listens on plain HTTP on
  `127.0.0.1:5959` behind a reverse proxy (Apache, nginx, HAProxy) that serves HTTPS on 443
  and passes `/helpbuoy` on, keeping the `Host` header and adding `X-Forwarded-For` (read only
  from `--trusted-proxy`, default `127.0.0.1,::1`); `docs/proxy.md` has tested examples for
  each proxy. With `--mode https` it serves HTTPS itself on `:443` (or the `--listen`
  port), with certificate files, Let's Encrypt (`--acme`, TLS-ALPN) or a self-signed
  certificate. It also runs inside the desk app (7.1), on the techie's own
  Linux, macOS or Windows machine, in ssh mode (plain SSH, see direct connect below) on port
  5959, which usually means one port forward on the router and allowing the OS firewall
  prompt on first start.
- **Direct connect**: `--mode ssh`, the default, serves plain SSH without the WebSocket and TLS, on `:5959`.
  The apps always try HTTPS first; when that fails without an HTTP server answering, they
  connect again with plain SSH and keep that connection only if the port sends an SSH
  banner. Not with virtual hosts, since plain SSH can't name the host.
- **One address for everyone**: techies and computers all use the same `hostname` or
  `hostname:port`, and an address typed without a port means 443, so behind a proxy users only
  type the host name. There are no per-techie or per-session ports.
- **Virtual hosts**: `serve --vhosts DIR` serves one helpdesk per host name from one process
  and one port, for managed hosting (relay.helpbuoy.com). Every subdirectory of `DIR` named
  after a host name is a state directory of its own (host key, `state.json`, `server.log`); the
  `Host` header picks it, and an unknown name is refused. `helpbuoy-server vhost add NAME`
  makes one, deleting its directory removes it, and a running server notices both within a
  second. The admin commands take `--vhost NAME`. Techies of one virtual host never see
  the computers of another.
- **Runs unprivileged**: no root, no admin, no Docker image. Started by hand or inside the
  desk app it needs no service registration; on a Debian server it can also run as a
  hardened systemd service under its own system user (6.5). The
  server runs as long as its process runs; when the machine or the process stops, computers and
  other techies simply see the server as unreachable until it is started again.
- **Identity = key**. The server maps each public key to exactly one of: techie, registered
  computer, or unknown. User names in the SSH handshake are ignored (computers send `sv`).
- **One helpdesk per server**: every techie on a server sees every registered computer and
  can ask to connect to it, unless the techie is limited to some customers (13). The
  computers are grouped per customer, so one helpdesk serves many customers from one server.
  Separate helpdesks use separate servers or virtual hosts.

### Stored computer configuration

The computer is one generic exe; nothing is embedded or preconfigured. The only thing the user
types is the server's `hostname:port`. Everything else is learned on the first successful
connection and stored in `config.json` in the client's data directory:
`%LOCALAPPDATA%\helpbuoy` on Windows, `$XDG_DATA_HOME/helpbuoy` (by default
`~/.local/share/helpbuoy`) on Linux:

| Field | Example | Learned |
|---|---|---|
| server address | `helpme.example.com` (port 443 unless given) | typed by the user; the server this PC got registered on |
| server host key | `ssh-ed25519 AAAA...` (`SHA256:3q2+7w...`) | first successful connection, pinned from then on |
| last address | `helpme.example.com` | the last address that reached a server, to prefill the field |
| known techies | `alice` → `ssh-ed25519 AAAA...` | each techie's key, pinned on the first session with that techie (3.5) |
| lock (12) | primary server address and host key, approved servers | when the user agrees to lock to a server |

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
   doesn't use the default 443). On this first start
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
├── go.work                      # use ./proto ./server ./desk ./client-windows ./client-linux ./client-macos
├── PLAN.md
├── proto/                       # shared, pure Go, no GUI deps
│   ├── go.mod
│   ├── serveraddr/               # parse and normalize hostname:port (default port 443)
│   ├── transport/               # SSH over WebSocket over HTTPS: Dial, Upgrade, Listener
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
│   ├── cmd/helpbuoy-server/             # "serve" plus admin subcommands (3.2), virtual hosts
│   ├── front/                   # HTTP side: Host -> virtual host, X-Forwarded-For
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
├── client-linux/                # static X11 and Wayland client (jezek/xgb, godbus, no cgo)
│   ├── capture/                 # X11 capture with MIT-SHM
│   ├── portal/                  # XDG remote desktop portal session (Wayland)
│   ├── pwcapture/               # PipeWire capture through gst-launch-1.0 (Wayland)
│   ├── injector/                # XTEST or portal input, Esc watch
│   └── ui/                      # its own drawn window and widgets, shown by a Host (X11 here)
└── client-macos/                # macOS 12.3+ client (cgo, cross compiled with zig)
    ├── capture/                 # ScreenCaptureKit or CoreGraphics at display points, NSCursor
    ├── injector/                # CGEvent input, CGEventTap that swallows the local Esc
    ├── macwin/                  # AppKit Host for client-linux/ui
    └── perm/                    # Screen Recording and Accessibility checks
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

A techie limited to some customers (13.3) only sees and reaches the computers of those
customers, and can't manage techies, customers or the server info.

### 3.2 State and admin

State directory in the user's data directory, next to the desk app's own data:
`~/.local/share/helpbuoy-server` on Linux, `~/Library/Application Support/helpbuoy-server` on macOS,
`%LOCALAPPDATA%\helpbuoy-server` on Windows (override with `--state`). It holds `hostkey_ed25519` (0600) and `state.json` with who runs the server (address,
company name, phone number, description; 12.4), techies (name, public key) and registered
computers (key, `user@machine` name, registered by, registered at, last seen, approved
servers and open listing requests, 12.3). Computer registrations never expire; they are removed
only by `UnregisterComputer` or `helpbuoy-server computer remove`. They are added by
`RegisterComputer` or `helpbuoy-server computer add`. Writes are temp file + `fsync` + `rename`.
Fields in `state.json` that a version doesn't know, at the top level or in a techie or
computer entry, are kept and written back on every save, so a newer version can add fields
without an older one sharing the state directory dropping them.
It also holds the customers, the customer of every computer, and the limited techies in a
list of their own (13.2, 13.3).

| Command | What it does |
|---|---|
| `helpbuoy-server serve [--listen :5959]` | run the server on plain SSH (direct connect), the default mode (0) |
| `helpbuoy-server serve --mode http [--listen 127.0.0.1:5959]` | run the server on plain HTTP behind a reverse proxy |
| `helpbuoy-server serve --mode https [--listen :443] [--cert F --key F \| --acme --acme-host H]` | run the server with its built-in HTTPS server |
| `helpbuoy-server serve --mode http --vhosts DIR` | serve every virtual host in `DIR` (http or https mode) |
| `helpbuoy-server vhost list` / `vhost add NAME` | list or add virtual hosts; the other commands take `--vhost NAME` |
| `helpbuoy-server techie list` | list techies with their fingerprints |
| `helpbuoy-server techie add alice 'ssh-ed25519 ...'` | add a techie with the public key copied from their desk app |
| `helpbuoy-server techie remove alice` | remove a techie; takes effect immediately, open sessions are closed |
| `helpbuoy-server computer list` | list registered computers: name, fingerprint, registered by and at, last seen |
| `helpbuoy-server computer add 'ssh-ed25519 ...' ['Anna laptop']` | register a computer key, like Register computer in the desk app; the name defaults to the key comment, and "registered by" is `admin` |
| `helpbuoy-server computer rename SHA256:... 'Anna laptop'` | rename a registered computer; the same as Rename in the desk app, every techie sees the new name |
| `helpbuoy-server computer remove SHA256:...` | remove a registered computer; an online computer is disconnected |
| `helpbuoy-server info [--company C] [--phone P] [--address A] [--description D]` | print or set who runs the server (12.4); only the flags given change, and the company name and phone number can't be cleared. The same values techies edit on the desk app's Server tab. `serve` asks for the company name and phone number when they are missing and it runs in a terminal; until they are set the server refuses computers |
| `helpbuoy-server customer list` / `add` / `rename` / `remove` | manage the customers on the server (13.2) |
| `helpbuoy-server computer move SHA256:... 'Acme BV' \| --none` | move a computer to another customer, or out of its customer (13.2); `computer add` and `computer list` take `--customer` |
| `helpbuoy-server techie limit alice 'Acme BV' ... \| --all` | limit a techie to some customers, or lift the limit (13.3); `techie add` takes `--customer` (repeatable) too |
| `helpbuoy-server limit [--sessions N]` | print or set how many sessions the server allows at the same time, for all techies together (3.4); 0 is no limit, the default. Techies can't change it |
| `helpbuoy-server hostkey` | print the host key fingerprint, for techies to compare with what computers show |
| `helpbuoy-server hostkey --new` | replace the host key, keeping all techies and computers; it takes effect at the next server start, since the running server only watches `state.json`; every computer then shows the changed server identity warning (3.5), and servers that approved this one for their computers (12.3) must approve it again |

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
| `Hello` | server → any | role, server time, limits |
| `ServerInfo` | server → any | who runs the server: address, company name, phone number and description (12.4), right after `Hello` and again whenever they change |
| `ApprovedServers` | server → computer | the servers this server approved for the computer, each an address and host key fingerprint (12.3), after `Hello` or `Registered` and again whenever the list changes |
| `RequestListing` | computer → server | address and host key fingerprint of a server the locked computer wants to use (12.3) |
| `ListingRefused` | server → computer | a techie refused that request |
| `DecideListing` | techie → server | computer fingerprint, address, fingerprint and allow or refuse |
| `ComputerHello` | computer → server | app version, current `user@machine` (the registered name comes from the key comment) |
| `Presence` | server → techies | full snapshot after every change: registered computers (`user@machine`, fingerprint, online, open or taken request with id and time, in session with whom, approved servers and open listing requests) and techies |
| `RegisterComputer` / `UnregisterComputer` | techie → server | public key line or fingerprint (name taken from the key comment, or given when the comment is empty) |
| `RegisterTechie` | techie → server | public key line and name: adds another techie, like `helpbuoy-server techie add` |
| `UnregisterTechie` | techie → server | name: removes another techie, like `helpbuoy-server techie remove`; an online techie is disconnected, and a techie cannot remove itself |
| `SetServerInfo` | techie → server | new address (optional, a valid server address), company name (up to 64 characters), phone number (up to 32) and description (optional, up to 160), one line each; stored in `state.json` |
| `Reply` | server → techie | answer to a techie command: sequence number and error text if it failed |
| `Registered` | server → computer | the key is now registered (no name: that is only for techies) |
| `RequestHelp` | computer → server | (no content) |
| `CancelRequest` | computer → server | request id |
| `RequestStateMsg` | server → computer | request id and state (open / taken / cancelled / expired / ended) |
| `OpenRequest` | techie → server | request id; the server lets only the first techie win |
| `SessionReady` | server → computer | request id, techie name and public key: a techie wants to connect |
| `Error` | server → any | reason, sent just before the server closes a connection on purpose |

Customers (13.5): `Presence` carries the customers and the customer of every computer,
filtered for limited techies, and techies have `AddCustomer`, `RenameCustomer`,
`RemoveCustomer`, `MoveComputer` and `LimitTechie`. Nothing changes for computers.

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

The HTTP layer comes first: request headers within 10 s and at most 16 KiB, only the
WebSocket upgrade on `/helpbuoy` with the `helpbuoy-ssh` subprotocol. The source IP for these
limits is the peer of the HTTP connection, or the client in `X-Forwarded-For` when that peer
is a trusted proxy. The limits apply per virtual host; process-wide limits across virtual
hosts are not there yet.

Role caps and timeouts:
- Per techie: max 20 concurrent sessions. Per computer: exactly one session at a time, enforced by the server and again by the computer.
- Per server: optionally at most `max_sessions` sessions at the same time, for all techies
  together (`helpbuoy-server limit --sessions N`, 0 or absent is no limit). A session counts
  from the moment a techie asks to connect until it ends, so a request waiting for the user
  holds its place. A Connect beyond it gets an error Reply ("this server allows one session at
  a time; wait until the other session ends"); running sessions are never ended by it. It is
  set by the admin only, never through the control channel, so a host can sell a helpdesk
  (a virtual host on relay.helpbuoy.com) with a fixed number of sessions. A change takes effect
  at the next Connect.
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
- **TLS is transport only.** The apps don't check the certificate of the HTTPS connection the
  SSH runs in; the pinned host key is what identifies the server. A proxy that re-signs TLS
  can read no more than the server can, and can't pose as the server without its host key.
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
- **Limited techies** (13.3) are limited by the server, not by the desk app hiding
  rows: they never get the computers of other customers in `Presence`, and the server refuses
  their commands for those computers.
- **A locked computer** (12) only connects to its primary server and the servers
  that server approved for it, and only removing its local files undoes the lock.
- **Unknown servers**: anyone can run a server and give it any company name. The client
  warns in full on the first connection to a new server (6.2), and a locked client only
  reaches the servers its primary server approved (12). Nothing depends on helpbuoy.com.
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
- "Server address" field (`hostname:port`, port defaults to 443 if left out) and
  **Connect**. With a stored address the computer connects to it at start by itself, with
  the field locked while it does; the field can only be edited when that fails or after
  **Disconnect**. Errors are shown inline: name not found, connection refused, timeout, not a
  helpbuoy server. A changed server key brings the user back here with the warning (3.5).
  On a locked PC (12) the hint above the field says which company the PC gets help
  through and that other servers need its approval.
- Right after the first successful connection to a new server, before the key screen, the
  client shows a full warning: "Only continue if someone you know personally gave you this
  address. If someone called you unexpectedly, hang up: banks, Microsoft and the police never
  ask you to install this." Continue only works after 5 seconds. It is shown once per server
  key, so people who run their own server see it once per computer.
- If the server doesn't know this PC's key yet: server fingerprint, the public key line in a
  read-only box, the key fingerprint, a big **Copy key** button, and "Paste this key into a
  chat with a techie (for example WhatsApp)." A link goes back to the address field.
- Waits for `Registered`, then saves the configuration and moves to the waiting screen.

### 6.3 Waiting screen
- The comment of this PC's own key (`anna@ANNA-LAPTOP`) and "Waiting for a techie", with
  who the user is waiting for below it (see below). The techie connects, and the computer
  asks the question from section 1 (Session, step 3) in a modal dialog, with a warning icon
  and "Only allow this if you know the person asking personally. Banks, Microsoft and the
  police never ask for this."
- **Disconnect** is always available while connected: it leaves the server and goes back to
  the address screen. During a session the same button reads **End session** and ends only
  the session (like Esc); afterwards it is Disconnect again.
- The computer never shows the name techies registered or renamed it under: the server does
  not send it to the computer (neither `Hello` nor `Registered` carry it). That name is only
  for techies.
- There is no settings window.
- **Who the user is waiting for** (12.4): below "Waiting for a techie" the waiting screen
  shows the company name of the server, and under it the phone number ("Phone: ..."), the
  description if there is one, and the address this PC is connected to ("Server: ...").
  The company name also goes in the window caption ("Example Inc - HelpBuoy"). They come from
  the server on the current connection (set by a techie on the desk app's Server tab or with
  `helpbuoy-server info`) and are shown on one line each, without control characters or bidi
  overrides; the same goes for techie names. Below them the computer shows "A techie can now
  ask to connect. You will be asked first, and pressing Esc always takes back control."
  Errors and notices take that place while they apply.
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
  app MSIs, the Debian packages of the Linux client, the desk app and `helpbuoy-server` for
  amd64 and arm64, and
  the zipped macOS client and desk app (`helpbuoy_<version>_macos.zip` and
  `helpbuoy-desk_<version>_macos.zip`, universal apps that are not signed or notarized; only
  the arm64 slice carries the ad hoc signature zig and the Go linker give it).
  The updaters pick their MSI by name prefix: `helpbuoy_` for the client, `helpbuoy-desk_`
  for the desk app on Windows, which updates the same way: its title shows the version and an
  "Check for updates" link sits in the top right corner (in the tab bar once connected). With sessions
  open it asks to end them first. The Linux and macOS apps have no update check yet.
  Linux is built for amd64 and arm64 (`LINUX_ARCHES` in the Makefile), Windows for amd64 only
  (`WIN_ARCHES`, the MSIs are x64); the macOS apps are built for amd64 and arm64 and merged
  into one universal binary with `tools/lipo`.
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
  the server fingerprint to read out to users, and who runs the server (12.4): **Company
  name** and **Phone number** (required) and the optional **Address computers use** and
  **Description**, with **Save** (any techie can change them; every computer and techie gets
  the new values right away). While the company name or phone number is missing, the
  Computers tab and Add computer say that computers can't connect yet, with a button to the
  Server tab, and **Disconnect** (not while sessions are open; back to the connect screen,
  stopping the local server). There is no settings tab: nothing else is configurable.
- One server connection with the control channel, keepalive, reconnect with backoff. All of
  this techie's sessions run as channels over that one connection.
- **Local server**: the server is compiled into the desk app (`server/server`, the same code
  as `helpbuoy-server`). Connecting with an empty address (or `localhost`, or `localhost:<port>`)
  serves the server in-process on that port (5959 when left out, since a normal user can't
  listen on 443) in ssh mode (plain SSH on all interfaces, the direct connect of section 0), until
  Disconnect or exit (unless something already listens on the port). A techie's own PC has
  no reverse proxy or certificate, and the apps fall back from HTTPS to plain SSH within one
  round trip, since the TLS handshake fails as soon as the SSH banner arrives. There is no separate executable
  to find, so `go run .` works too. It uses the same state directory as `helpbuoy-server serve`, so
  the standalone server and its admin commands share the techies, computers and host key. The
  app connects to `localhost:<port>` itself, while computers and other techies use the
  machine's public `hostname:port`, with the port, since it is not 443.
- **Address to give out** (issue #17): with the local server running, the Server tab shows
  the address in the local network instead of `localhost:5959`, with **Copy**, and says to
  forward the port on the router to it and to give out the public address or a dynamic DNS
  name with `:5959`. The address (`desk/reach`) is the IPv4 address on the interface of the
  default route (found with a UDP "dial" to a public address, which sends nothing), skipping
  loopback, link-local, point-to-point (VPN) interfaces and interfaces named like container,
  VM or VPN adapters (`docker0`, `br-…`, `veth…`, `virbr…`, `wg…`, `tailscale…`,
  `vEthernet (…)`, VirtualBox, VMware); without such a route, the first private address. The
  app only shows it after a TCP connect to it succeeds. The app does not look up the public
  address or check from outside: that would need a service on helpbuoy.com, and a techie
  for whom setting up a server is too difficult is better off with managed hosting. With no
  IPv4 route, or a local address in `100.64.0.0/10` (carrier-grade NAT), the tab says a port
  forward can't work and points to the always-on server page. No UPnP or NAT-PMP: the
  router is never changed without the techie doing it.
- **This techie on the local server**: if this techie's key is not a techie there yet, the app
  adds it to the server state directly (with a number after the name if that name is taken),
  so nobody has to copy keys around. Other techies are added with **Add techie** on the Techies tab
  or with `helpbuoy-server techie add` by whoever runs the server.
- **Distribution**: an installer per OS containing only the desk app: a per-user MSI on
  Windows (`installer/helpbuoy-desk.wxs`, into `%LOCALAPPDATA%\Programs\helpbuoy-desk`), a
  Debian package for amd64 and arm64 (`make deb-desk`, needs glibc 2.38 for the embedded
  raylib, so Debian 13 or Ubuntu 24.04) and a zipped universal `HelpBuoy Desk.app` for macOS 12.3 or later. The server is built in, so `helpbuoy-server`
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
- **Server requests** (12.3): a locked computer that asks for another server shows the
  requested address and fingerprint below its row, with **Allow** and **Refuse**.
- **Customers** (13.4): the computers show per customer in collapsible sections,
  with a filter field, **Move** next to Rename and Remove, and **Add customer** next to Add
  computer.
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

**Phase 6 - verified servers** (removed)
A check with helpbuoy.com whether a server belonged to a company HelpBuoy checked was built,
and then replaced by the lock to a primary server (phase 8) before any site or portal
existed.

**Phase 7 - Wayland and macOS** (done; the client and the desk app are tested on a real
Wayland desktop and a real Mac)
The Linux client shares Wayland desktops through the XDG remote desktop portal with
PipeWire capture through `gst-launch-1.0`. `client-macos/` with ScreenCaptureKit (CGWindowListCreateImage on Macs without Metal: in a
macOS 14 VM any display stream, also the one behind CGDisplayCreateImage, crashed WindowServer
in WSSelectiveSharingUpdateDisplayStreamSurface), CGEvent and
an AppKit host for the shared drawn window from `client-linux/ui`, cross compiled with zig.
The desk app ships as a universal `.app` as well. Both macOS apps are unsigned.
The Linux client, the desk app and `helpbuoy-server` also ship as arm64 Debian packages
(ARM laptops, servers and a 64-bit Raspberry Pi), built the same way with `GOARCH=arm64`.

**Phase 8 - locking to a primary server (12)** (done, issue #18)
The lock question, the approved list and listing requests, mandatory company name and phone
number on every server, and the waiting screen showing who the user waits for.

**Phase 9 - customers (13)** (done, issue #20)
Customers on the server, the customer of every computer, techies limited to customers, and
the Computers tab grouped per customer.

**Later**: tray icon and optional "Start with Windows" (HKCU Run entry, no admin), so the
computer can stay online in the background; an update check in the Linux and macOS
clients.

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
- Lock (12): a server without a company name and phone number refuses computers and
  pending keys but lets techies in; listing requests are stored, deduplicated, shown in
  `Presence` and decided only by techies; allowing sends the new approved list (also after a
  reconnect), refusing sends `ListingRefused`; the client asks the lock question once per
  connection and only once registered, locks to the pinned host key, asks the primary server
  about a server that is not approved after seeing only its host key, connects there once
  approved, pins approved servers by the approved fingerprint, starts on the primary server
  when its last address is no longer allowed, and keeps the lock through Remove key.
- Customers (13): a state without customers loads with every computer in "No
  customer"; removing a customer keeps its computers; a limited techie never gets other
  customers' computers or techies in `Presence`, and the server refuses its `OpenRequest`,
  `RegisterComputer`, renames, moves and `DecideListing` for them and every command that
  manages techies, customers or the server info; changing a limit takes effect at once,
  closing sessions it no longer allows; an older server on the same state refuses limited
  techies.
- Performance: the replay harness from phase 3 in CI (client encoder against techie decoder,
  no GUI), with regression thresholds; `tc netem` runs at 20 ms / 80 ms RTT and 10 / 50 Mbit/s.
- Session end tests (`faketechie`): End session on the techie closes the session and
  restores the client window; local Esc ends the session and no techie input is injected
  after it; injected Esc does not end the session; the next Connect asks the user again
  and gives a new session; refusing a Connect starts nothing; closing the client window ends the session and disconnects the computer
  from the server.
- Linux and macOS: the client and the desk app are tried by hand on a Wayland desktop and
  on a Mac.
- Windows: capture and key-map tests on a `windows-latest` runner; manual matrix of Windows 11
  at 100/150/200% scaling, multiple monitors, RDP session, elevated window (including Esc while an elevated window has focus), and a side-by-side
  comparison with `mstsc` for the 5.1 scenarios.

## 10. Open questions

- The apps use a proxy entered by hand in the system settings, but not automatic proxy
  configuration (PAC files, WPAD).
- Does a server on the techie's own machine behind a home or office router need a dynamic DNS
  name in the docs, since computers store `hostname:port` once and never change it?
- Is TOFU for the server plus server-vouched techies enough, or should the computer also be able
  to check a short code (e.g. the first 8 characters of the server fingerprint) that the techie
  reads out before anything is stored?
- macOS: both `.app`s are built on Linux and not signed, so Gatekeeper refuses them
  the first time and users have to click Open Anyway in System Settings, Privacy & Security.
  The client also loses its Screen Recording and Accessibility permissions with every update,
  since the app has no stable signature. Is that acceptable for the people being
  helped, or is a Developer ID signature and notarization (rcodesign can do both from Linux,
  with an Apple developer account) required?
- Is JPEG enough for video-heavy work, or is the Media Foundation H.264 path needed in v1?

## 11. Translations

The Windows, Linux and macOS clients and the desk app are translated; `helpbuoy-server` stays English. The
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

Dutch, German, French, Spanish and Italian are done (about 250 strings each). Rejected:
go-i18n, `x/text` with `gotext`, and gettext `.po`, all more machinery than this needs.

## 12. Locking to a primary server

Issue #18. Every server is treated as an MSP: the support company or person behind it can
be the mandatory first point of contact for the PCs it helps. Without a lock, anyone at the
keyboard can press **Disconnect** on the waiting screen and type another address (0, 6.2).
The client warns on the first connection to a new server but never stops the user, which
is exactly what the helpdesk scam relies on.

A client registered on a server is asked to **lock** to it. A locked client only connects to
that server, its **primary server**, and to the servers the primary server approved for it.
There is no setting for this on the server: every server asks.

### 12.1 The lock question

When an unlocked client connects to a server, it asks the user:

"To protect you from scam calls, do you want this PC to get help via HelpBuoy only through
Example Inc? Other helpdesks can then only reach this PC after Example Inc allowed them."

with **Lock to Example Inc** and **Not now**. The company name is the one the server gives
(12.4), shown with the same cleanup as all server text (6.3). The question is asked on any
server, not only the first one the client used, so PCs that already used HelpBuoy can be
locked too. **Not now** only skips it for this connection: the question comes back on every
new connection while the client is unlocked.

A lock is only set after a successful connection: the handshake succeeded with the pinned
host key and the server accepted the computer's key as registered. A server that only sees
a pending key can't ask, and a lock is never set silently.

Once locked, the user can't enter free addresses: the client only connects to the primary
server and to the servers on its approved list, each with its address and host key, so an
approval is for one specific server and not just a name. With an empty list the client is
tied to the primary server only.

### 12.2 Unlocking

A lock is only undone by removing the client's local files (`config.json` and the key in the
data directory, 6.1); the client then starts over as a new computer with a new key. There is
no unlock in the client, and nothing the server does unlocks it: not removing the computer
from the computer list, and not losing `state.json`. A locked client whose key the primary
server no longer accepts stays locked and waits on the key screen (6.2) of the primary
server until a techie registers it again. A customer who leaves their helpdesk also removes
the files.

Removing files takes deliberate effort, which is what we want: a scam caller has to talk
the user through deleting files instead of just typing an address. It is a soft lock: someone
at the keyboard can still delete the files, and a machine-wide policy set by an administrator
(issue #16) remains the option for those who need more, and always wins over a lock.

### 12.3 Connecting to another server

The client's screens stay the same. A locked user who needs help from someone else (a
software vendor, a second helpdesk) types the address as they do today. If that server is not
on the approved list, the connection becomes a request to the primary server to list it: the
client connects only far enough to see the host key, does nothing else with that server, and
then connects to the primary server, as a computer of its own, and sends it the address and
host key fingerprint (`RequestListing`). It keeps that connection while it waits, and sends
the request again after a reconnect.

In the desk app the request shows in the computer list, on the row of that computer, with the
requested address and fingerprint and **Allow** and **Refuse** (7.2). A techie approves it,
and the approval is permanent: the server stays on that computer's approved list (one entry
per address, so approving a new key for an address replaces the old one), and the client
continues its normal flow with it, pinning that server's host key by the approved
fingerprint. Until a techie decides, the client shows its waiting for a techie screen (6.3)
for the primary server, with "Waiting until Example Inc allows server:port." Refusing sends
the client back to the address screen with "Example Inc did not allow this PC to connect to
server:port." A scam caller then has to get a techie at the primary server to approve their
server.

Only the primary server's changed host key can be trusted by the user (3.5); an approved
server whose key changed needs a new approval. Remove key, shown only when the key is
damaged (6.1), keeps the lock: the PC then starts over as a new computer of its primary
server.

### 12.4 Who the user is waiting for

The waiting for a techie screen (6.3) always shows who the user is waiting for, so the user
knows who to call: the server address, the company name, the phone number and optionally a
free description (for example opening hours). The company name and phone number are
mandatory for every server, since every server can become a primary server:
`helpbuoy-server serve` asks for them in a terminal (3.2), the desk app says so on the
Computers tab until they are set (7.1), and a server without them refuses computers and
pending keys with an `Error`. They are separate fields in `ServerInfo` and `SetServerInfo`,
so the client shows them in its own layout. The address shown is the one the PC is
connected to; the address field in `ServerInfo` is what techies give out.

### 12.5 Where it lives

- The client stores the lock in `config.json` (0): the primary server's address, host key,
  last known company name and the approved list, apart from the server it is connected to
  now (`client-windows/config`, `client-windows/app`). Once locked, the primary stays when
  the user connects to an approved server. Older clients skip the new messages (3.3), so
  they keep working but never lock.
- The server stores the company name, phone number, address and description, and per
  computer its approved servers and open requests, in `state.json` (3.2). No extra file.
- A machine-wide policy set by an administrator (issue #16) is not built yet; when it is,
  it always wins over a lock the user agreed to.

There is no check with helpbuoy.com: the user's own helpdesk decides which other servers may
reach the PC, so nothing in a session depends on helpbuoy.com being reachable.

## 13. Customers on one server

Issue #20. A server is one flat group today (0): every techie sees every
registered computer in one list. That fits a single helpdesk, but an MSP helps many
customers from one team. With a few hundred computers from twenty companies in one list,
the techie can't tell whose PC is whose, and the only way to keep customers apart is a
server per customer, each with its own address, host key, techies and lock (12).

Every customer is a group of computers on the server, and every registered computer belongs
to one customer. The server, its host key, its company name and phone number and its
techies stay shared, so a customer's PCs still lock to the helpdesk's server and the
helpdesk is still who the user calls. Customers only organize the computers and,
optionally, limit which techies can reach them.

This is different from virtual hosts (0). A virtual host is a separate helpdesk with its own
techies, host key and state, sharing only the process. A customer is a group of computers
within one helpdesk. The two combine: every virtual host has its own customers.

### 13.1 What stays the same

- Computers don't learn their customer. The lock (12), the waiting screen (12.4),
  `ServerInfo` and the approved servers stay per server and per computer, so the clients
  don't change.
- A server that never adds a customer looks and behaves as it does today.
- Computers are registered by techies only (3.1), and pending keys are still never shown,
  so there is no step where a computer picks its own customer.

### 13.2 Customers on the server

`state.json` (3.2) gets a list of customers, each with an id and a name ("Acme BV"), and
every computer entry gets the id of its customer. A computer without a customer is in
"No customer", which is where every computer of an existing server starts. Customer names
are unique on the server, one line of at most 64 characters, with the same cleanup as
computer names.

A computer is added to a customer when it is registered: Register computer in the desk app
gets a customer field (13.4) and `helpbuoy-server computer add` gets `--customer`. A techie
can move a registered computer to another customer later. Removing a customer moves its
computers to "No customer" instead of removing them, since a registration is only ever
removed on purpose.

| Command | What it does |
|---|---|
| `helpbuoy-server customer list` | list the customers with the number of computers in each |
| `helpbuoy-server customer add 'Acme BV'` | add a customer |
| `helpbuoy-server customer rename 'Acme BV' 'Acme Holding'` | rename a customer; every techie sees the new name |
| `helpbuoy-server customer remove 'Acme BV'` | remove a customer; its computers move to "No customer" |
| `helpbuoy-server computer move SHA256:... 'Acme BV'` | move a computer to another customer, or to "No customer" with `--none` |
| `helpbuoy-server computer add --customer 'Acme BV' ...` | register a computer for a customer |
| `helpbuoy-server computer list [--customer 'Acme BV']` | also shows the customer, and lists one customer when given |

### 13.3 Techies limited to customers

By default a techie sees every customer, as every techie sees every computer today. A
techie can also be limited to one or more customers, for example a customer's own IT person
who helps their colleagues but must not see other customers, or a contractor who only covers
some customers. A limited techie:

- only gets the computers of their customers in `Presence`, and the server refuses
  `OpenRequest`, `RegisterComputer`, renames, moves, `UnregisterComputer` and
  `DecideListing` (12.3) for any other computer
- registers new computers only into their own customers, and the desk app offers only those
- can't manage techies, customers or the server info (`RegisterTechie`, `UnregisterTechie`,
  the customer commands, `SetServerInfo`), since those affect every customer
- only sees the techies that share a customer with them, and techies that aren't limited

The server enforces the limit, not the desk app, so an older desk app connected as a limited
techie still only sees what it may see (3.5). Changing a limit takes effect at once: a
session with a computer the techie may no longer reach is closed, as when a techie is
removed. A limited techie whose last customer is removed stays limited and sees no
computers until given another.

Limited techies are stored in a list of their own in `state.json`, next to the techies
(with name, public key and customer ids), not as a field on a techie entry. An older server
sharing the state directory keeps that list (3.2) but doesn't know it, so it treats those
keys as unknown and refuses them, instead of letting them see every computer.

| Command | What it does |
|---|---|
| `helpbuoy-server techie add alice 'ssh-ed25519 ...' [--customer 'Acme BV' ...]` | add a techie, limited when customers are given |
| `helpbuoy-server techie limit alice 'Acme BV' ...` | limit a techie to these customers, replacing the old limit |
| `helpbuoy-server techie limit alice --all` | lift the limit |
| `helpbuoy-server techie list` | also shows the customers of limited techies |

### 13.4 The desk app

- The **Computers** tab (7.2) shows the computers per customer, in collapsible sections
  sorted by name with "No customer" last, each with the customer name and the number online.
  The collapsed "Offline" part moves inside each customer. A filter field at the top matches
  computer and customer names. With only "No customer" in use, the tab looks as it does
  today, without section headers.
- **Add customer** sits next to Add computer and adds a section with an inline name field.
  Hovering a section header shows **Rename** and **Remove**; Remove asks "Remove this
  customer? Its computers move to No customer." with Remove and Cancel.
- Hovering a computer row shows **Move** next to Rename and Remove: the customers to pick
  from open below the row as buttons, with Cancel. Dragging rows between sections is not
  planned.
- **Register computer** gets a customer choice, prefilled with the customer last used in
  this app, and "No customer" for a techie who isn't limited. A limited techie only gets its
  own customers and always has one chosen.
- **Register techie** gets "All customers, and computers without customer", checked by
  default; unchecked, at least one customer must be chosen. The **Techies** tab shows "Only
  for" the customers of a limited techie in place of its fingerprint, and a hover action
  **Customers** changes the limit with the same choice, Save and Cancel.
- A limited techie doesn't see Add customer, Add techie, the section actions, Remove and
  Customers on the Techies tab, or the edit fields on the Server tab, which shows who runs
  the server read-only.
- A session tab shows the customer name before the computer name, so a techie with several
  sessions open sees which customer each belongs to.

### 13.5 Protocol

- `Presence` (3.3) gets the list of customers (id and name) and a customer id per computer,
  and for every techie whether it is limited and to which customers, all filtered per
  techie as in 13.3. Older desk apps ignore the new fields (3.3) and show the flat list.
- New techie commands, answered with `Reply`: `AddCustomer` (name), `RenameCustomer` (id and
  name), `RemoveCustomer` (id), `MoveComputer` (computer fingerprint and customer id, empty
  for "No customer") and `LimitTechie` (name, a `Limited` flag and customer ids; a techie
  can't change its own limit). The protocol names customers by id only.
- `RegisterComputer` gets an optional customer id, and `RegisterTechie` the same `Limited`
  flag and customer ids as `LimitTechie`.
- Nothing is sent to computers.

### 13.6 Open points

- Whether a customer can have its own phone number or description on the waiting screen
  (12.4), for customers with their own support line or opening hours. Computers would then
  learn something from their customer, through a `ServerInfo` per computer.
- Whether a server can be approved (12.3) for a whole customer at once, for example a
  software vendor that every PC of one customer may use, next to the approval per computer.
- Whether a limited techie may add other techies limited to their own customers, so a
  customer's IT department can manage its own people.
