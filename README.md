# HelpBuoy

Self-hosted remote support for Windows, a small alternative to TeamViewer QuickSupport.

- `server/`: `helpbuoy-server`, a single Go binary that speaks SSH on one port (default 5959). It authenticates keys and connects techies to computers. No shell, no file transfer, no forwarding.
- `desk/`: the cross-platform app the support techie uses.
- `client-windows/`: the Windows app the person receiving help runs. It shows its public key to send to the techie, and asks for approval before anyone takes control.

A session only carries the screen (computer to techie) and mouse and keyboard input (techie to computer). See [PLAN.md](PLAN.md) for the design.

![The desk app listing computers](screenshot.png)

## Scripts

- `./run.sh`: builds and starts a server, the desk app and the Windows client (in wine) on this machine for testing. All state lives in `.run/`; `./run.sh reset` clears it.
- `./screenshot.sh [page]`: makes or updates `screenshot.png`, the desk app in demo mode at 800x500, for this README. Uses `xvfb-run` when installed, otherwise a window shows briefly.
- `./release.sh [major|minor]`: bumps the version (build number by default), builds and tests everything, then tags, pushes and creates the GitHub release with the client MSI, the desk app archives and the server Debian package.
