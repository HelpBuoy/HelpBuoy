# Help-Me-Win

Self-hosted remote support for Windows, a small alternative to TeamViewer QuickSupport.

- `relay/`: `hmw-relay`, a single Go binary that speaks SSH on one port (default 5938). It authenticates keys and connects helpers to clients. No shell, no file transfer, no forwarding.
- `helper/`: the cross-platform app the person giving help runs.
- `client-windows/`: the Windows app the person receiving help runs. It shows its public key to send to the helper, and asks for approval before anyone takes control.

A session only carries the screen (client to helper) and mouse and keyboard input (helper to client). See [PLAN.md](PLAN.md) for the design.
