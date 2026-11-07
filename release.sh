#!/bin/bash
#
# sudo apt install jq zip wixl msitools gh dpkg
# plus zig, the macOS SDK and rcodesign for the macOS client (README.md)
#
# Releases a new version of helpbuoy:
#
#   ./release.sh          # 0.1.0 -> 0.1.1 (build, the default)
#   ./release.sh minor    # 0.1.1 -> 0.2.0
#   ./release.sh major    # 0.2.0 -> 1.0.0
#
# The version is file_version in client-windows/resources/winres.json, as
# "major.minor.build.revision". Windows Installer only compares the first
# three numbers, so every release raises at least the build. The revision
# goes up on every release as well. The desk app's winres.json gets the same
# version.
#
# The script builds the release packages with "make dist" (MSIs of the
# client and desk app, Debian packages of the client, desk app and
# helpbuoy-server, the zipped macOS client, all in dist/), commits the
# version bump, tags and pushes it and creates the GitHub release v<version>
# with all files in dist/.
set -e
cd "$(dirname "$0")"

part=${1:-build}
case $part in major | minor | build) ;; *)
	echo "usage: $0 [major|minor|build]" >&2
	exit 1
	;;
esac
if [ -n "$(git status --porcelain)" ]; then
	echo "working tree not clean, commit first" >&2
	exit 1
fi

# Bump the version.
client=client-windows/resources/winres.json
desk=desk/resources/winres.json
ver=$(jq -r '.RT_VERSION["#1"]["0000"].fixed.file_version' $client)
IFS=. read -r major minor build revision <<<"$ver"
case $part in
major) major=$((major + 1)) minor=0 build=0 ;;
minor) minor=$((minor + 1)) build=0 ;;
build) build=$((build + 1)) ;;
esac
revision=$((revision + 1))
version=$major.$minor.$build
if git rev-parse -q --verify "refs/tags/v$version" >/dev/null; then
	echo "tag v$version already exists" >&2
	exit 1
fi
for json in $client $desk; do
	jq --indent 2 --arg v "$version" --arg fv "$version.$revision" '
	  .RT_VERSION["#1"]["0000"].fixed.file_version = $fv |
	  .RT_VERSION["#1"]["0000"].fixed.product_version = $fv |
	  .RT_VERSION["#1"]["0000"].info["0409"].FileVersion = $v |
	  .RT_VERSION["#1"]["0000"].info["0409"].ProductVersion = $v |
	  .RT_MANIFEST["#1"]["0409"].identity.version = $fv
	' $json >$json.tmp && mv $json.tmp $json
done
echo "Releasing helpbuoy $version (revision $revision)"

# Build, test and package. VERSION goes into main.version of every binary and
# into the file names in dist/.
make vet test dist VERSION="v$version"

# Commit, tag, push and publish.
git commit -q -m "Release v$version" $client $desk
git tag "v$version"
git push
git push origin "v$version"
gh release create "v$version" --title "v$version" --verify-tag dist/*
