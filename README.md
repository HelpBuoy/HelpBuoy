# HelpBuoy

Self-hosted remote support for Windows, a small alternative to TeamViewer QuickSupport.

- `relay/`: `helpbuoy-relay`, a single Go binary that speaks SSH on one port (default 5959). It authenticates keys and connects agents to clients. No shell, no file transfer, no forwarding.
- `desk/`: the cross-platform app the support agent giving help runs.
- `client-windows/`: the Windows app the person receiving help runs. It shows its public key to send to the agent, and asks for approval before anyone takes control.

A session only carries the screen (client to agent) and mouse and keyboard input (agent to client). See [PLAN.md](PLAN.md) for the design.
