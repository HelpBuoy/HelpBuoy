#!/bin/bash
#
# sudo apt install wine xvfb xdotool x11-utils x11-xserver-utils imagemagick curl unzip python3-fonttools
#
# Makes the Microsoft Store screenshots of both apps, on Linux:
#
#   msstore/screenshots.sh                        # both apps, all languages
#   msstore/screenshots.sh helpbuoy               # one app
#   msstore/screenshots.sh helpbuoy-desk nl-nl de-de
#
# They go to msstore/<app>/screenshots/<language>/, at 1920x1080:
#
#   helpbuoy       1-connect, 2-warning, 3-key, 4-lock, 5-waiting, 6-allow
#   helpbuoy-desk  1-computers, 2-session, 3-server, 4-register
#
# helpbuoy, the Windows client, runs for real in Wine against the fake
# server (client-windows/internal/cmd/fakeserver), at 200%. It runs with
# --screenshot: as the Store version (no update link), without the
# underlined access keys that Windows only shows after Alt but Wine always
# shows, and with Windows' room around dialog buttons. To look like Windows
# 11, Wine uses its Light theme, Selawik (Microsoft's open font with the
# metrics of Segoe UI) as the system font, ClearType and 200% DPI. Selawik
# Semibold is renamed to be the only heavy face of the Selawik family, so the
# semibold headings of the client come out semibold, as with Segoe UI,
# whatever the language. The PC is anna@ANNA-PC, the helpdesk Example
# Helpdesk.
#
# helpbuoy-desk draws its own window with raylib, the same on every system,
# so its Linux build makes the pictures, in demo mode at 150% (Xft.dpi).
#
# Without a window manager only the inside of the windows is drawn; the
# script draws a Windows 11 title bar and border around them, measured from
# real Windows 11 screenshots in msstore/reference/.
#
# Everything runs in user, network and host name namespaces of its own
# (unshare, no root needed), so the fake server can listen on port 443,
# helpbuoy.example.com can point to it and the PC is called ANNA-PC, without
# touching the system: the user and host name end up in the key. The Wine
# prefix and the font are kept in bin/screenshots/.
set -e
cd "$(dirname "$0")/.."

all=(en-us nl-nl de-de fr-fr es-es it-it)
work=$PWD/bin/screenshots
store=$PWD/msstore

if [ "$1" != --inside ]; then
	apps=(helpbuoy helpbuoy-desk)
	case $1 in helpbuoy | helpbuoy-desk) apps=("$1") && shift ;; esac
	langs=("$@")
	[ ${#langs[@]} -gt 0 ] || langs=("${all[@]}")
	for l in "${langs[@]}"; do
		[[ " ${all[*]} " == *" $l "* ]] || { echo "unknown language $l, use: ${all[*]}" >&2; exit 1; }
	done
	for c in wine Xvfb xdotool xwininfo xprop xrdb convert import curl unzip unshare; do
		command -v $c >/dev/null || { echo "$c is missing, see the top of $0" >&2; exit 1; }
	done
	python3 -c 'import fontTools' 2>/dev/null || { echo "fontTools is missing, see the top of $0" >&2; exit 1; }
	# The titles show the version, so build the apps as the last release.
	mkdir -p "$work"
	tag=$(git describe --tags --abbrev=0)
	make client VERSION="$tag" >/dev/null
	make desk TARGETS=linux/amd64 VERSION="$tag" >/dev/null
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
	exec unshare -rnmu "$0" --inside "$tag" "${apps[*]}" "${langs[@]}"
fi
tag=$2
read -ra apps <<<"$3"
shift 3
langs=("$@")

# Inside the namespace: loopback, port 443 without root, our host names.
hostname anna-pc
export USER=anna LOGNAME=anna USERNAME=anna
ip link set lo up
sysctl -qw net.ipv4.ip_unprivileged_port_start=0
printf '127.0.0.1 localhost\n127.0.0.1 helpbuoy.example.com\n' >"$work/hosts"
mount --bind "$work/hosts" /etc/hosts

export WINEPREFIX=$work/pfx WINEDEBUG=-all WINEDLLOVERRIDES=winemenubuilder.exe=d
rm -f "$work/display"
exec 3>"$work/display"
# -noreset keeps the Xft.dpi resource when the last client leaves.
Xvfb -displayfd 3 -noreset -screen 0 1920x1080x24 -nolisten tcp >/dev/null 2>&1 &
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

# The Windows 11 frame, from real screenshots at 100%: a 30 px title bar in
# #f3f3f3 with the 16 px icon at 8,7 and the title at x 29, caption buttons
# 46 px wide with 10 px glyphs, a 1 px border. All sizes are scaled by p
# percent.
font=$work/fonts/selawk.ttf
icon=$PWD/client-windows/resources/icon.png
px() { echo $((($1 * p + 50) / 100)); }

# frame RAW TITLE KIND OUT: the window contents RAW with title bar and
# border, as a PNG with a shadow. KIND is fixed (minimize, maximize grayed
# out since the window has a fixed size), resizable (minimize, maximize),
# dialog (close only) or inactive (fixed, behind a dialog: gray title and
# glyphs).
frame() {
	local raw=$1 title=$2 kind=$3 dst=$4 w cy close min max g fg='#1a1a1a' maxc='#c2c2c2' glyphs=()
	w=$(identify -format %w "$raw")
	[ "$kind" = inactive ] && fg='#8f8f8f'
	[ "$kind" = resizable ] && maxc=$fg
	cy=$(px 15) close=$((w - $(px 23))) g=$(px 5)
	glyphs+=(-stroke "$fg" -draw "line $((close - g)),$((cy - g)) $((close + g)),$((cy + g))"
		-draw "line $((close - g)),$((cy + g)) $((close + g)),$((cy - g))")
	if [ "$kind" != dialog ]; then
		max=$((w - $(px 69))) min=$((w - $(px 115)))
		glyphs+=(-stroke "$maxc" -draw "rectangle $((max - g)),$((cy - g)) $((max + g)),$((cy + g))"
			-stroke "$fg" -draw "line $((min - g)),$cy $((min + g)),$cy")
	fi
	convert \
		\( -size "${w}x$(px 30)" xc:'#f3f3f3' \
		\( "$icon" -resize "$(px 16)x$(px 16)" \) -geometry "+$(px 8)+$(px 7)" -composite \
		-fill "$fg" -stroke none -font "$font" -pointsize "$(px 12)" -gravity west \
		-annotate "+$(px 29)+0" "$title" -gravity northwest \
		-fill none -strokewidth "$(px 1)" "${glyphs[@]}" \) \
		"$raw" -append \
		-bordercolor '#7a7a7a' -border 1 \
		\( +clone -background black -shadow "35x$(px 12)+0+$(px 6)" \) +swap \
		-background none -layers merge +repage "$dst"
}

# desktop OUT LAYERS...: the framed windows on a desktop.
desktop() {
	local dst=$1
	shift
	convert -size 1920x1080 gradient:'#dfe8f3-#c9d8ea' "$@" "$dst"
	echo "  $dst"
}

# The client in Wine, at 200%.
client() {
	local lang=$1 ll=${1%-*} cc=${1#*-} out=$store/helpbuoy/screenshots/$1 main
	p=200
	mkdir -p "$out"
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
	shot "$out/1-connect.png"

	# A first connection shows a warning, with Continue after 5 seconds.
	xdotool key Return
	sleep 7
	shot "$out/2-warning.png"

	xdotool key Return
	sleep 3
	shot "$out/3-key.png"

	# Once registered, the client offers to lock this PC to the helpdesk.
	echo register >&4
	sleep 4
	shot "$out/4-lock.png"

	xdotool key Escape
	sleep 2
	shot "$out/5-waiting.png"

	echo connect >&4
	sleep 3
	shot "$out/6-allow.png"

	exec 4>&-
	kill $server 2>/dev/null || true
	server=
}

# shot OUT: all windows of the client where Wine put them, the main one
# first, framed on a desktop.
shot() {
	local dst=$1 layers=() ids=() dialog=0 id kind x y
	xdotool mousemove 0 0
	for id in $(xdotool search --onlyvisible --classname helpbuoy.exe); do
		eval "$(xdotool getwindowgeometry --shell "$id")"
		[ "$WIDTH" -gt 50 ] || continue
		ids+=("$id")
		[[ $(xprop -id "$id" _NET_WM_WINDOW_TYPE) == *DIALOG* ]] && dialog=1
	done
	for id in "${ids[@]}"; do
		eval "$(xdotool getwindowgeometry --shell "$id")"
		kind=fixed
		[ $dialog = 1 ] && kind=inactive
		[[ $(xprop -id "$id" _NET_WM_WINDOW_TYPE) == *DIALOG* ]] && kind=dialog
		import -window "$id" "$work/client.png"
		frame "$work/client.png" "$(xdotool getwindowname "$id")" $kind "$work/w-$id.png"
		# The shadow adds 2 * 12 - 6 = 18 to the left and top, the border 1.
		x=$((X - 1 - $(px 18))) y=$((Y - $(px 30) - 1 - $(px 18)))
		if [ $kind = dialog ]; then
			layers+=("$work/w-$id.png" -geometry "+$x+$y" -composite)
		else
			layers=("$work/w-$id.png" -geometry "+$x+$y" -composite "${layers[@]}")
		fi
	done
	desktop "$dst" "${layers[@]}"
}

# The desk app's demo mode, at 150%: 1040x600 logical pixels.
desk() {
	local lang=$1 ll=${1%-*} cc=${1#*-} out=$store/helpbuoy-desk/screenshots/$1 page name n=0
	p=150
	mkdir -p "$out"
	echo 'Xft.dpi: 144' | xrdb -nocpp -merge
	for page in computers: session: server: register:; do
		n=$((n + 1)) name=${page%:}
		[ "$name" = computers ] && page= || page=$name
		LANG=${ll}_${cc^^}.UTF-8 bin/linux-amd64/helpbuoy-desk --demo --page "$page" \
			--size 1040x600 --screenshot "$work/desk.png" 2>&1 | { grep -v '^WARNING: FONT:' || true; }
		frame "$work/desk.png" "HelpBuoy Desk $tag" resizable "$work/desk-framed.png"
		desktop "$out/$n-$name.png" "$work/desk-framed.png" -gravity center -composite
	done
	xrdb -remove
}

for app in "${apps[@]}"; do
	if [ "$app" = helpbuoy ]; then
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
	fi
	for lang in "${langs[@]}"; do
		echo "$app $lang"
		if [ "$app" = helpbuoy ]; then client "$lang"; else desk "$lang"; fi
	done
done
