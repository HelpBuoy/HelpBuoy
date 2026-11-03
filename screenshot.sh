#!/bin/bash
#
# sudo apt install imagemagick xvfb   (xvfb is optional, see below)
#
# Makes or updates screenshot.png, the demo screenshot in the README:
#
#   ./screenshot.sh              # the Computers page
#   ./screenshot.sh server       # another start page: register, server, session
#
# It builds the desk app and runs it in demo mode (fake computers, no network,
# nothing stored) at 800x500. The app saves one frame and exits. With xvfb-run
# installed it renders off-screen, so the result does not depend on your
# desktop; otherwise a window shows briefly on the current display. The frame
# then gets a thin window frame (title bar, rounded corners, shadow) with ImageMagick.
set -e
cd "$(dirname "$0")"

page=${1:-}
out=screenshot.png
w=800
h=500
raw=$(mktemp --suffix=.png)
trap 'rm -f "$raw"' EXIT

make desk TARGETS=linux/amd64 >/dev/null
desk=bin/linux-amd64/helpbuoy-desk
args=(--demo --page "$page" --size "${w}x${h}" --screenshot "$raw")

run=()
if command -v xvfb-run >/dev/null; then
	run=(xvfb-run -a -s "-screen 0 1280x1024x24")
fi
# raylib prints warnings about a few glyphs on every start; drop those.
"${run[@]}" "$desk" "${args[@]}" 2>&1 | { grep -v '^WARNING: FONT:' || true; }
[ "${PIPESTATUS[0]}" = 0 ]

# Window chrome: a thin title bar with the app icon, minimize, maximize and close glyphs,
# rounded corners, a hairline border and a soft shadow, on transparency.
bar=26
r=6
H=$((h + bar))
cy=$((bar / 2))
cx=$((w - 16)) # close
mx=$((w - 38)) # maximize
nx=$((w - 60)) # minimize
convert \
	\( -size "${w}x${bar}" xc:'#f0f0f2' \
	-stroke '#5e6266' -strokewidth 1 -fill none \
	-draw "line $((cx - 3)),$((cy - 3)) $((cx + 3)),$((cy + 3))" \
	-draw "line $((cx - 3)),$((cy + 3)) $((cx + 3)),$((cy - 3))" \
	-draw "rectangle $((mx - 3)),$((cy - 3)) $((mx + 3)),$((cy + 3))" \
	-draw "line $((nx - 3)),$((cy + 3)) $((nx + 3)),$((cy + 3))" \
	-stroke none -fill '#5e6266' -font DejaVu-Sans -pointsize 11 \
	-gravity center -annotate +0+0 'HelpBuoy Desk' -gravity northwest \
	-fill '#e0e0e3' -draw "line 0,$((bar - 1)) $w,$((bar - 1))" \
	\( desk/resources/icon.png -resize 16x16 \) \
	-geometry "+8+$(((bar - 16) / 2))" -composite \) \
	"$raw" -append \
	\( -size "${w}x${H}" xc:none -fill white \
	-draw "roundrectangle 0,0 $((w - 1)),$((H - 1)) $r,$r" \) \
	-alpha set -compose DstIn -composite -compose over \
	-fill none -stroke 'rgba(0,0,0,0.15)' \
	-draw "roundrectangle 0,0 $((w - 1)),$((H - 1)) $r,$r" \
	\( +clone -background black -shadow 20x8+0+3 \) +swap \
	-background none -layers merge +repage -depth 8 "$out"
echo "wrote $out ($(file -b "$out" | cut -d, -f2 | tr -d ' '))"
