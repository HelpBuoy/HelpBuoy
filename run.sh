#!/bin/bash
#
# sudo apt install wine openssh-client
#
# Runs HelpBuoy locally for testing: a relay, the desk app and the Windows
# client in wine, all on this machine.
#
#   ./run.sh          # build, then start the relay, the client and the desk app
#   ./run.sh reset    # forget all local test state (keys, registrations, wine)
#
# Everything lives in .run/ (relay state, the desk app's key and settings,
# the wine prefix), so your real keys and relay are never touched. The desk
# app's key is added to the relay as an agent before it starts. Closing the
# desk app stops the relay and the client.
#
# In the client type 127.0.0.1:5959 (or the HELPBUOY_PORT you set) and
# Connect, then register it in the desk app with Add client. The relay's
# title and instruction line are on the desk app's Relay tab.
set -e
cd "$(dirname "$0")"

port=${HELPBUOY_PORT:-5959}
run=$PWD/.run
relay=bin/linux-amd64/helpbuoy-relay

case ${1:-} in
reset)
	WINEPREFIX=$run/wine wineserver -k 2>/dev/null || true
	rm -rf "$run"
	echo "removed $run"
	exit 0
	;;
"") ;;
*)
	echo "usage: $0 [reset]" >&2
	exit 1
	;;
esac

make relay desk client
mkdir -p "$run/relay" "$run/desk"

# The desk app's key, made up front so the relay can know it as an agent.
if [ ! -f "$run/desk/id_ed25519" ]; then
	ssh-keygen -q -t ed25519 -N '' -C "$(whoami)@$(hostname)" -f "$run/desk/id_ed25519"
fi
if ! $relay agent list --state "$run/relay" | grep -q "$(ssh-keygen -lf "$run/desk/id_ed25519.pub" | cut -d' ' -f2)"; then
	$relay agent add "$(whoami)" "$(cat "$run/desk/id_ed25519.pub")" --state "$run/relay"
fi
if [ ! -f "$run/desk/agent.json" ]; then
	echo "{\"relay_addr\": \"127.0.0.1:$port\"}" >"$run/desk/agent.json"
fi

export WINEPREFIX=$run/wine WINEDEBUG=-all
cleanup() {
	kill "$relay_pid" 2>/dev/null || true
	wineserver -k 2>/dev/null || true
}
trap cleanup EXIT

$relay serve --listen "127.0.0.1:$port" --state "$run/relay" >"$run/relay.log" 2>&1 &
relay_pid=$!
echo "relay on 127.0.0.1:$port, log in $run/relay.log"

wine bin/windows-amd64/helpbuoy.exe >"$run/client.log" 2>&1 &
echo "client starting in wine (the first start creates the wine prefix and takes a while)"
echo "  in the client, connect to 127.0.0.1:$port"

bin/linux-amd64/helpbuoy-desk -datadir "$run/desk"
