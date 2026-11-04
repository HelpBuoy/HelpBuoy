# HelpBuoy

Self-hosted remote support for Windows, a small alternative to TeamViewer QuickSupport.

- `server/`: `helpbuoy-server`, a single Go binary that speaks SSH on one port (default 5959). It authenticates keys and connects techies to computers. No shell, no file transfer, no forwarding.
- `desk/`: the cross-platform app the support techie uses.
- `client-windows/`: the Windows app the person receiving help runs. It shows its public key to send to the techie, and asks for approval before anyone takes control.
- `client-linux/`: the same app for Linux. It is a single static binary that speaks the X11 protocol itself (with jezek/xgb), so it needs no libraries and runs on any X11 desktop. It can't share a Wayland desktop: there it says so and explains how to log in with an X11 session. A local Esc ends the session but still reaches the focused application.

A session only carries the screen (computer to techie) and mouse and keyboard input (techie to computer). See [PLAN.md](PLAN.md) for the design.

![The desk app listing computers](screenshot.png)

## Scripts

- `./run.sh`: builds and starts a server, the desk app and the Windows client (in wine) on this machine for testing. All state lives in `.run/`; `./run.sh reset` clears it.
- `./screenshot.sh [page]`: makes or updates `screenshot.png`, the desk app in demo mode at 800x500, for this README. Uses `xvfb-run` when installed, otherwise a window shows briefly.
- `./release.sh [major|minor]`: bumps the version (build number by default), builds and tests everything, then tags, pushes and creates the GitHub release with the client and desk app MSIs and the client, desk app and server Debian packages.

## Alternatives

Open source TeamViewer alternatives on GitHub, ranked by stars (November 2026). Descriptions are taken from GitHub.

| Project | Stars | Description | License |
| --- | ---: | --- | --- |
| [rustdesk/rustdesk](https://github.com/rustdesk/rustdesk) | 128,133 | An open-source remote desktop application designed for self-hosting, as an alternative to TeamViewer. | AGPL-3.0 |
| [galaxy-s10/billd-desk](https://github.com/galaxy-s10/billd-desk) | 9,146 | 基于Vue3 + WebRTC + Nodejs + Flutter搭建的远程桌面控制、游戏串流 | MIT |
| [Ylianst/MeshCentral](https://github.com/Ylianst/MeshCentral) | 7,614 | A complete web-based remote monitoring and management web site. Once setup you can install agents and perform remote desktop session to devices on the local network or over the Internet. | Apache-2.0 |
| [kunkundi/crossdesk](https://github.com/kunkundi/crossdesk) | 4,688 | A lightweight, cross-platform remote desktop software with support for Web Client access \| 一款支持 Web 客户端访问的轻量级跨平台远程桌面软件。 | GPL-3.0 |
| [miroslavpejic85/p2p](https://github.com/miroslavpejic85/p2p) | 4,086 | 🖥️ P2P Remote Desktop - Portable peer-to-peer remote desktop with no installation required. | MIT |
| [DarkCoderSc/PowerRemoteDesktop](https://github.com/DarkCoderSc/PowerRemoteDesktop) | 2,247 | Remote Desktop entirely coded in PowerShell. | Apache-2.0 |
| [dchapyshev/aspia](https://github.com/dchapyshev/aspia) | 1,966 | Remote desktop and file transfer tool. | GPL-3.0 |
| [ultravnc/UltraVNC](https://github.com/ultravnc/UltraVNC) | 1,526 | 👁️ UltraVNC Server, UltraVNC Viewer, UltraVNC Repeater and UltraVNC SC \| Official repository: https://github.com/ultravnc/UltraVNC | GPL-3.0 |
| [zibo-chen/SubnetDesk](https://github.com/zibo-chen/SubnetDesk) | 741 | LAN-only remote desktop based on RustDesk | AGPL-3.0 |
| [barry-ran/QuickDesk](https://github.com/barry-ran/QuickDesk) | 553 | QuickDesk is the first AI-native remote desktop — an open-source, free application with a built-in MCP (Model Context Protocol) Server that lets any AI agent see and control remote computers. | MIT |

---

[www.helpbuoy.com](https://www.helpbuoy.com)
