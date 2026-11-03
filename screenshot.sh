#!/bin/bash
#
# sudo apt install xvfb   (optional, see below)
#
# Makes or updates screenshot.png, the demo screenshot in the README:
#
#   ./screenshot.sh              # the Computers page
#   ./screenshot.sh server       # another start page: register, server, session
#
# It builds the desk app and runs it in demo mode (fake computers, no network,
# nothing stored) at 800x500. The app saves one frame and exits. With xvfb-run
# installed it renders off-screen, so the result does not depend on your
# desktop; otherwise a window shows briefly on the current display.
set -e
cd "$(dirname "$0")"

page=${1:-}
out=screenshot.png
size=800x500

make desk TARGETS=linux/amd64 >/dev/null
desk=bin/linux-amd64/helpbuoy-desk
args=(--demo --page "$page" --size "$size" --screenshot "$PWD/$out")

run=()
if command -v xvfb-run >/dev/null; then
	run=(xvfb-run -a -s "-screen 0 1280x1024x24")
fi
# raylib prints warnings about a few glyphs on every start; drop those.
"${run[@]}" "$desk" "${args[@]}" 2>&1 | { grep -v '^WARNING: FONT:' || true; }
[ "${PIPESTATUS[0]}" = 0 ]
echo "wrote $out ($(file -b "$out" | cut -d, -f2 | tr -d ' '))"
