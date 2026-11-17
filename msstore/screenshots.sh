#!/bin/bash
#
# sudo apt install wine xvfb xdotool x11-utils imagemagick curl unzip python3-fonttools
#
# Makes the Microsoft Store screenshots of the Windows client, on Linux:
#
#   msstore/screenshots.sh              # all languages
#   msstore/screenshots.sh nl-nl de-de  # some
#
# It runs the real helpbuoy.exe in Wine against the fake server
# (client-windows/internal/cmd/fakeserver) and writes, per language,
# msstore/screenshots/<language>/1-connect.png, 2-warning.png, 3-key.png,
# 4-lock.png, 5-waiting.png and 6-allow.png, at 1920x1080 with the window
# at 200%. The PC is anna@ANNA-PC, the helpdesk Example Helpdesk.
#
# The client runs with --screenshot: as the Store version (no update link),
# and without the underlined access keys that Windows only shows after Alt
# but Wine always shows. To look like Windows 11, Wine uses its Light theme,
# Selawik (Microsoft's open font with the metrics of Segoe UI) as the system
# font, ClearType and 200% DPI. Selawik Semibold is renamed to be the only
# heavy face of the Selawik family, so the semibold headings of the client
# come out semibold, as with Segoe UI, whatever the language. Without a window manager Wine draws only the inside of the
# windows; the script draws a Windows 11 title bar and border around them,
# measured from a real Windows 11 screenshot, msstore/reference/windows11-connect-nl.png.
#
# Everything runs in user, network and host name namespaces of its own
# (unshare, no root needed), so the fake server can listen on port 443,
# helpbuoy.example.com can point to it and the PC is called ANNA-PC, without
# touching the system: the user and host name end up in the key. The
# Wine prefix and the font are kept in bin/screenshots/.
set -e
cd "$(dirname "$0")/.."

all=(en-us nl-nl de-de fr-fr es-es it-it)
work=$PWD/bin/screenshots
out=$PWD/msstore/screenshots

if [ "$1" != --inside ]; then
	langs=("$@")
	[ ${#langs[@]} -gt 0 ] || langs=("${all[@]}")
	for l in "${langs[@]}"; do
		[[ " ${all[*]} " == *" $l "* ]] || { echo "unknown language $l, use: ${all[*]}" >&2; exit 1; }
	done
	for c in wine Xvfb xdotool xwininfo xprop convert import curl unzip unshare; do
		command -v $c >/dev/null || { echo "$c is missing, see the top of $0" >&2; exit 1; }
	done
	python3 -c 'import fontTools' 2>/dev/null || { echo "fontTools is missing, see the top of $0" >&2; exit 1; }
	# The title shows the version, so build the client as the last release.
	mkdir -p "$work"
	make client VERSION="$(git describe --tags --abbrev=0)" >/dev/null
	(cd client-windows && go build -o "$work/fakeserver" ./internal/cmd/fakeserver)
	# Selawik, pinned by its checksum.
	zip=$work/selawik.zip
	if ! echo "3f62c51e05e3b5a1e6241cf92a371f0be2ea1183aa87b30718bbd40832a8d423  $zip" | sha256sum -c --status 2>/dev/null; then
		curl -fsSL -o "$zip" https://github.com/microsoft/Selawik/releases/download/1.01/Selawik_Release.zip
		echo "3f62c51e05e3b5a1e6241cf92a371f0be2ea1183aa87b30718bbd40832a8d423  $zip" | sha256sum -c --quiet
	fi
	rm -rf "$work/fonts" && mkdir "$work/fonts" && unzip -q -j "$zip" selawk.ttf selawksb.ttf -d "$work/fonts"
	python3 -I - "$work/fonts/selawksb.ttf" <<-'EOF'
		# Selawik Semibold as the bold face of the Selawik family, weight 600.
		import sys
		from fontTools.ttLib import TTFont
		f = TTFont(sys.argv[1])
		for rec in f['name'].names:
		    if rec.nameID in (1, 16):
		        rec.string = 'Selawik'
		    elif rec.nameID in (2, 17):
		        rec.string = 'Bold'
		    elif rec.nameID == 4:
		        rec.string = 'Selawik Bold'
		    elif rec.nameID == 6:
		        rec.string = 'Selawik-Bold'
		os2 = f['OS/2']
		os2.fsSelection = (os2.fsSelection & ~0x40) | 0x20
		f['head'].macStyle |= 1
		f.save(sys.argv[1])
	EOF
	exec unshare -rnmu "$0" --inside "${langs[@]}"
fi
shift
langs=("$@")

# Inside the namespace: loopback, port 443 without root, our host names.
hostname anna-pc
export USER=anna LOGNAME=anna USERNAME=anna
ip link set lo up
sysctl -qw net.ipv4.ip_unprivileged_port_start=0
printf '127.0.0.1 localhost\n127.0.0.1 helpbuoy.example.com\n' >"$work/hosts"
mount --bind "$work/hosts" /etc/hosts

export WINEPREFIX=$work/pfx WINEDEBUG=-all WINEDLLOVERRIDES=winemenubuilder.exe=d
exec 3>"$work/display"
Xvfb -displayfd 3 -screen 0 1920x1080x24 -nolisten tcp >/dev/null 2>&1 &
xvfb=$!
until [ -s "$work/display" ]; do sleep 0.1; done
export DISPLAY=:$(cat "$work/display")
server=
cleanup() {
	wineserver -k 2>/dev/null || true
	[ -n "$server" ] && kill $server 2>/dev/null || true
	kill $xvfb 2>/dev/null || true
}
trap cleanup EXIT

# The Wine prefix, made once: Selawik, ClearType, 200%.
if [ ! -f "$WINEPREFIX/.helpbuoy-ready" ]; then
	echo "setting up the Wine prefix"
	wineboot -i >/dev/null 2>&1
	wineserver -w
	python3 -I - "$work/setup.reg" <<-'EOF'
		import struct, sys
		# A LOGFONTW of Selawik 9 pt (-12 px at 96 DPI; Windows scales it), ClearType quality.
		lf = struct.pack('<5i8B', -12, 0, 0, 0, 400, 0, 0, 0, 1, 0, 0, 5, 0) + 'Selawik'.encode('utf-16-le').ljust(64, b'\0')
		hexed = ','.join('%02x' % b for b in lf)
		reg = ['Windows Registry Editor Version 5.00', '',
		       r'[HKEY_CURRENT_USER\Control Panel\Desktop\WindowMetrics]']
		reg += ['"%s"=hex:%s' % (k, hexed) for k in ('MessageFont', 'CaptionFont', 'SmCaptionFont', 'MenuFont', 'StatusFont', 'IconFont')]
		reg += ['', r'[HKEY_CURRENT_USER\Control Panel\Desktop]',
		        '"FontSmoothing"="2"', '"FontSmoothingType"=dword:00000002',
		        '"FontSmoothingGamma"=dword:00000578', '"FontSmoothingOrientation"=dword:00000001',
		        '"LogPixels"=dword:000000c0',
		        '', r'[HKEY_LOCAL_MACHINE\System\CurrentControlSet\Hardware Profiles\Current\Software\Fonts]',
		        '"LogPixels"=dword:000000c0', '']
		open(sys.argv[1], 'w').write('\r\n'.join(reg))
	EOF
	wine regedit "$work/setup.reg"
	wineserver -w
	touch "$WINEPREFIX/.helpbuoy-ready"
fi
rm -f "$WINEPREFIX"/drive_c/windows/Fonts/selawk*.ttf
cp "$work"/fonts/*.ttf "$WINEPREFIX/drive_c/windows/Fonts/"

# The Windows 11 frame at 200%, from a real screenshot at 100%: a 30 px
# title bar in #f3f3f3 with the 16 px icon at 8,7 and the title at x 29,
# caption buttons 46 px wide with 10 px glyphs, a 1 px border.
s=2
bar=$((30 * s))
font=$work/fonts/selawk.ttf
icon=$PWD/client-windows/resources/icon32.png

# frame ID KIND OUT: the window ID with title bar and border, as a PNG with
# a shadow. KIND is main (minimize, and maximize grayed out since the window
# has a fixed size), dialog (close only) or inactive (main, behind a dialog:
# gray title and glyphs).
frame() {
	local id=$1 kind=$2 dst=$3 w h title close min max cy fg='#1a1a1a'
	eval "$(xdotool getwindowgeometry --shell "$id")"
	w=$WIDTH h=$HEIGHT
	title=$(xdotool getwindowname "$id")
	[ "$kind" = inactive ] && fg='#8f8f8f'
	cy=$((15 * s))
	close=$((w - 23 * s))
	local g=$((5 * s)) glyphs=()
	glyphs+=(-stroke "$fg" -draw "line $((close - g)),$((cy - g)) $((close + g)),$((cy + g))"
		-draw "line $((close - g)),$((cy + g)) $((close + g)),$((cy - g))")
	if [ "$kind" != dialog ]; then
		max=$((w - 69 * s)) min=$((w - 115 * s))
		glyphs+=(-stroke '#c2c2c2' -draw "rectangle $((max - g)),$((cy - g)) $((max + g)),$((cy + g))"
			-stroke "$fg" -draw "line $((min - g)),$cy $((min + g)),$cy")
	fi
	import -window "$id" "$work/client.png"
	convert \
		\( -size "${w}x${bar}" xc:'#f3f3f3' \
		\( "$icon" -resize $((16 * s))x$((16 * s)) \) -geometry +$((8 * s))+$((7 * s)) -composite \
		-fill "$fg" -stroke none -font "$font" -pointsize $((12 * s)) -gravity west \
		-annotate +$((29 * s))+0 "$title" -gravity northwest \
		-fill none -strokewidth $s "${glyphs[@]}" \) \
		"$work/client.png" -append \
		-bordercolor '#7a7a7a' -border 1 \
		\( +clone -background black -shadow 35x$((12 * s))+0+$((6 * s)) \) +swap \
		-background none -layers merge +repage "$dst"
}

# shot OUT: all HelpBuoy windows, the main one first, framed on a desktop.
shot() {
	local dst=$1 args=() ids=() dialog=0 id type x y
	xdotool mousemove 0 0
	for id in $(xdotool search --onlyvisible --classname helpbuoy.exe); do
		eval "$(xdotool getwindowgeometry --shell "$id")"
		[ "$WIDTH" -gt 50 ] || continue
		ids+=("$id")
		[[ $(xprop -id "$id" _NET_WM_WINDOW_TYPE) == *DIALOG* ]] && dialog=1
	done
	for id in "${ids[@]}"; do
		eval "$(xdotool getwindowgeometry --shell "$id")"
		type=$(xprop -id "$id" _NET_WM_WINDOW_TYPE)
		if [[ $type == *DIALOG* ]]; then
			frame "$id" dialog "$work/w-$id.png"
		elif [ $dialog = 1 ]; then
			frame "$id" inactive "$work/w-$id.png"
		else
			frame "$id" main "$work/w-$id.png"
		fi
		# The shadow adds 2 * 12 * s - 6 * s to the left and top.
		x=$((X - 1 - 18 * s)) y=$((Y - bar - 1 - 18 * s))
		if [[ $type == *DIALOG* ]]; then
			args+=("$work/w-$id.png" -geometry "+$x+$y" -composite)
		else
			args=("$work/w-$id.png" -geometry "+$x+$y" -composite "${args[@]}")
		fi
	done
	convert -size 1920x1080 gradient:'#dfe8f3-#c9d8ea' "${args[@]}" "$dst"
	echo "  $dst"
}

for lang in "${langs[@]}"; do
	echo "$lang"
	ll=${lang%-*}
	cc=${lang#*-}
	mkdir -p "$out/$lang"
	wineserver -k 2>/dev/null || true
	rm -rf "$WINEPREFIX"/drive_c/users/*/AppData/Local/helpbuoy

	rm -f "$work/cmd" && mkfifo "$work/cmd"
	"$work/fakeserver" -listen 127.0.0.1:443 -stdin -techie Sam \
		-company "Example Helpdesk" -phone "+1 555 0100" <"$work/cmd" >"$work/server.log" 2>&1 &
	server=$!
	exec 4>"$work/cmd"

	LANG=${ll}_${cc^^}.UTF-8 wine bin/windows-amd64/helpbuoy.exe --screenshot >"$work/client.log" 2>&1 &
	main=$(xdotool search --sync --onlyvisible --name 'HelpBuoy' | head -1)
	sleep 2
	xdotool windowfocus --sync "$main"
	xdotool type --delay 20 helpbuoy.example.com
	sleep 0.5
	shot "$out/$lang/1-connect.png"

	# A first connection shows a warning, with Continue after 5 seconds.
	xdotool key Return
	sleep 7
	shot "$out/$lang/2-warning.png"

	xdotool key Return
	sleep 3
	shot "$out/$lang/3-key.png"

	# Once registered, the client offers to lock this PC to the helpdesk.
	echo register >&4
	sleep 4
	shot "$out/$lang/4-lock.png"

	xdotool key Escape
	sleep 2
	shot "$out/$lang/5-waiting.png"

	echo connect >&4
	sleep 3
	shot "$out/$lang/6-allow.png"

	exec 4>&-
	kill $server 2>/dev/null || true
	server=
done
