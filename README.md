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
| [immense/Remotely](https://github.com/immense/Remotely) | 5,093 | A remote control and remote scripting solution, built with .NET 8, Blazor, and SignalR. | GPL-3.0 |
| [kunkundi/crossdesk](https://github.com/kunkundi/crossdesk) | 4,688 | A lightweight, cross-platform remote desktop software with support for Web Client access \| 一款支持 Web 客户端访问的轻量级跨平台远程桌面软件。 | GPL-3.0 |
| [miroslavpejic85/p2p](https://github.com/miroslavpejic85/p2p) | 4,086 | 🖥️ P2P Remote Desktop - Portable peer-to-peer remote desktop with no installation required. | MIT |
| [DarkCoderSc/PowerRemoteDesktop](https://github.com/DarkCoderSc/PowerRemoteDesktop) | 2,247 | Remote Desktop entirely coded in PowerShell. | Apache-2.0 |
| [dchapyshev/aspia](https://github.com/dchapyshev/aspia) | 1,966 | Remote desktop and file transfer tool. | GPL-3.0 |
| [ultravnc/UltraVNC](https://github.com/ultravnc/UltraVNC) | 1,526 | 👁️ UltraVNC Server, UltraVNC Viewer, UltraVNC Repeater and UltraVNC SC \| Official repository: https://github.com/ultravnc/UltraVNC | GPL-3.0 |
| [zibo-chen/SubnetDesk](https://github.com/zibo-chen/SubnetDesk) | 741 | LAN-only remote desktop based on RustDesk | AGPL-3.0 |
| [barry-ran/QuickDesk](https://github.com/barry-ran/QuickDesk) | 553 | QuickDesk is the first AI-native remote desktop — an open-source, free application with a built-in MCP (Model Context Protocol) Server that lets any AI agent see and control remote computers. | MIT |
| [DarkCoderSc/Arcane](https://github.com/DarkCoderSc/Arcane) | 335 | Arcane - A secure remote desktop application for Windows with the particularity of having a server entirely written in PowerShell and a cross-platform client (Python/QT6). | Apache-2.0 |
| [morrolinux/ChimeraDesk](https://github.com/morrolinux/ChimeraDesk) | 301 | Simple, fast and flexible DIY Remote Desktop software | GPL-2.0 |
| [bitbound/ControlR](https://github.com/bitbound/ControlR) | 266 | Open-source, self-hostable remote control and remote access. | MIT |
| [cea-sec/sanzu](https://github.com/cea-sec/sanzu) | 204 | Graphical remote desktop solution | GPL-3.0 |
| [RetGal/Dayon](https://github.com/RetGal/Dayon) | 175 | Dayon! - A cross-platform remote desktop assistance solution for your family and friends | GPL-3.0 |
| [DeskX11/DeskX](https://github.com/DeskX11/DeskX) | 161 | Universal light and fast program for remote control of a computer. | Apache-2.0 |
| [mmozeiko/ScreenBuddy](https://github.com/mmozeiko/ScreenBuddy) | 152 | Simple remote desktop sharing application for Windows | Unlicense |
| [boonkerz/teamscreen](https://github.com/boonkerz/teamscreen) | 142 | It should be an Teamviewer/Vnc/Something replacement | GPL-3.0 |
| [dyhwdnmd/RLink-RemoteCtrl](https://github.com/dyhwdnmd/RLink-RemoteCtrl) | 141 | RLink 是一款面向 Windows 的高性能开源远程桌面与多人协作工具，支持最高 4K、120 FPS 的低延迟桌面共享。用户可通过设备 ID 和一次性验证码快速发起远程协助，也可登录账户直接访问自己的在线设备，并支持多人音视频、屏幕共享、远程控制。 | GPL-3.0 |

---

[www.helpbuoy.com](https://www.helpbuoy.com)
