#!/bin/bash
#
# Submits the MSIX packages of a release to the Microsoft Store, for both
# apps, with the Microsoft Store Developer CLI (msstore):
#
#   msstore/publish.sh            # the version of the last tag
#   msstore/publish.sh 0.5.4      # a given version
#   msstore/publish.sh 0.5.4 helpbuoy-desk
#   msstore/publish.sh --check    # only check the credentials: list the apps
#
# release.sh runs it after the GitHub release. It builds the packages with
# "make msix" when they are not in bin/ yet. msstore copies the last
# published submission of each app (listing, screenshots, pricing), puts the
# new package in and commits it, after which the Store certifies it, which
# takes from hours to a few days. Only one submission per app can be in
# progress: while the previous one is still being certified, this fails for
# that app; run it again later.
#
# It needs msstore and the Partner Center credentials in
# msstore/credentials.env, see "Publishing updates" in msstore/README.md.
set -e
cd "$(dirname "$0")/.."

check=
[ "$1" = --check ] && check=1 && shift
version=${1:-$(git describe --tags --abbrev=0)}
version=${version#v}
shift || true
apps=("$@")
[ ${#apps[@]} -gt 0 ] || apps=(helpbuoy helpbuoy-desk)

declare -A ids=([helpbuoy]=9NZXVS50R8VB [helpbuoy-desk]=9NM475B9T4BC)

if ! command -v msstore >/dev/null; then
	echo "msstore is not installed, see \"Publishing updates\" in msstore/README.md" >&2
	exit 1
fi

# The Partner Center credentials, from a file git ignores (see
# msstore/credentials.env.example). msstore is configured with them every
# time, so a new client secret only has to go into that file.
creds=msstore/credentials.env
if [ ! -f "$creds" ]; then
	echo "$creds is missing: copy msstore/credentials.env.example and fill it in" >&2
	exit 1
fi
if [ -n "$(find "$creds" -perm /077)" ]; then
	echo "$creds can be read by others: chmod 600 $creds" >&2
	exit 1
fi
# shellcheck source=/dev/null
. "$creds"
for v in MSSTORE_TENANT_ID MSSTORE_SELLER_ID MSSTORE_CLIENT_ID MSSTORE_CLIENT_SECRET; do
	[ -n "${!v}" ] || { echo "$v is empty in $creds" >&2; exit 1; }
done
msstore reconfigure --tenantId "$MSSTORE_TENANT_ID" --sellerId "$MSSTORE_SELLER_ID" \
	--clientId "$MSSTORE_CLIENT_ID" --clientSecret "$MSSTORE_CLIENT_SECRET" </dev/null >/dev/null
msstore settings --enableTelemetry false </dev/null >/dev/null
if [ -n "$check" ]; then
	msstore apps list </dev/null
	exit
fi

for app in "${apps[@]}"; do
	[ -n "${ids[$app]}" ] || { echo "unknown app $app, use: ${!ids[*]}" >&2; exit 1; }
	[ -f "bin/${app}_$version.0_x64.msix" ] || make msix VERSION="v$version"
done

failed=()
for app in "${apps[@]}"; do
	msix=bin/${app}_$version.0_x64.msix
	echo "Submitting $msix to the Store as ${ids[$app]}"
	if ! msstore publish bin --inputFile "$msix" --appId "${ids[$app]}"; then
		failed+=("$app")
	fi
done
if [ ${#failed[@]} -gt 0 ]; then
	echo "Not submitted: ${failed[*]}. Check \"msstore submission status <Store ID>\", then run msstore/publish.sh $version ${failed[*]}" >&2
	exit 1
fi
