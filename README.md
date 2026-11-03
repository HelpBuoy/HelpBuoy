# HelpBuoy

Self-hosted remote support for Windows, a small alternative to TeamViewer QuickSupport.

- `relay/`: `helpbuoy-relay`, a single Go binary that speaks SSH on one port (default 5959). It authenticates keys and connects agents to clients. No shell, no file transfer, no forwarding.
- `desk/`: the cross-platform app the support agent uses.
- `client-windows/`: the Windows app the person receiving help runs. It shows its public key to send to the agent, and asks for approval before anyone takes control.

A session only carries the screen (client to agent) and mouse and keyboard input (agent to client). See [PLAN.md](PLAN.md) for the design.

## Scripts

- `./run.sh`: builds and starts a relay, the desk app and the Windows client (in wine) on this machine for testing. All state lives in `.run/`; `./run.sh reset` clears it.
- `./release.sh [major|minor]`: bumps the version (build number by default), builds and tests everything, then tags, pushes and creates the GitHub release with the client MSI, the desk app archives and the relay Debian package.
