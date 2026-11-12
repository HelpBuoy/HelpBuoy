#!/bin/bash
#
# sudo apt install dpkg-dev apt-utils gnupg rsync
#
# Publishes the Debian packages in dist/ to the HelpBuoy APT repository,
# https://apt.helpbuoy.com/. release.sh runs it after the GitHub release;
# run it by hand to publish dist/ again when that step failed:
#
#   ./apt-repo.sh
#
# It is a flat repository: one directory with the .debs of all packages and
# architectures, one Packages index and a signed Release, which apt finds
# with "Suites: ./" (see packaging/debian/apt-source.postinst). The script
# fetches the current repository, adds the .debs from dist/, keeps the last
# HELPBUOY_APT_KEEP versions of each package and architecture so a rollback
# is possible, rebuilds and signs the index and uploads it all again.
#
# HELPBUOY_APT_DEST  rsync location of the repository, by default
#                    helpbuoy-apt@ams01.usecue.nl:public_html/.
# HELPBUOY_APT_KEY   GPG key to sign with, by default the key of
#                    packaging/debian/helpbuoy.asc, the one the packages
#                    install. It must be in your keyring.
# HELPBUOY_APT_KEEP  versions to keep of each package and architecture,
#                    by default 3.
set -e
cd "$(dirname "$0")"

pubkey=packaging/debian/helpbuoy.asc
keep=${HELPBUOY_APT_KEEP:-3}
key=${HELPBUOY_APT_KEY:-$(gpg --show-keys --with-colons $pubkey | awk -F: '$1 == "fpr" { print $10; exit }')}
dest=${HELPBUOY_APT_DEST:-helpbuoy-apt@ams01.usecue.nl:public_html/}
if ! gpg --list-secret-keys "$key" >/dev/null 2>&1; then
	echo "no secret key $key in your keyring to sign the repository with" >&2
	exit 1
fi
shopt -s nullglob
debs=(dist/*.deb)
if [ ${#debs[@]} = 0 ]; then
	echo "no .deb files in dist/, run make dist first" >&2
	exit 1
fi

# Fetch the repository as it is, so old versions stay. A failure stops here:
# an empty copy would delete them on upload. Create the directory once by
# hand for a new repository.
repo=bin/apt
mkdir -p $repo
echo "Fetching $dest"
rsync -a --delete "$dest" $repo/ || {
	echo "can't fetch $dest; for a new repository create it first" >&2
	exit 1
}
cp "${debs[@]}" $repo/

# Keep the last $keep versions of each package and architecture. Debian
# package names have no underscores, so the file name is
# name_version_arch.deb.
cd $repo
for group in $(ls -1 -- *.deb | sed 's/^\([^_]*\)_[^_]*_\([^_]*\)\.deb$/\1:\2/' | sort -u); do
	name=${group%:*} arch=${group#*:}
	ls -1 -- "${name}"_*_"${arch}".deb | sed "s/^${name}_\(.*\)_${arch}\.deb$/\1/" |
		sort -V | head -n -"$keep" | while read -r v; do
		echo "Removing ${name}_${v}_${arch}.deb"
		rm -f -- "${name}_${v}_${arch}.deb"
	done
done

# Index and sign. apt-ftparchive adds the checksums dpkg-scanpackages leaves
# out; Origin is what unattended-upgrades matches on.
rm -f Packages Packages.gz Release InRelease Release.gpg
dpkg-scanpackages --multiversion . >Packages 2>/dev/null
gzip -9nk Packages
apt-ftparchive \
	-o APT::FTPArchive::Release::Origin=HelpBuoy \
	-o APT::FTPArchive::Release::Label=HelpBuoy \
	-o APT::FTPArchive::Release::Description="HelpBuoy, self-hosted remote support" \
	release . >../Release.tmp
mv ../Release.tmp Release
gpg --batch --yes --local-user "$key" --digest-algo SHA512 --clearsign -o InRelease Release
gpg --batch --yes --local-user "$key" --digest-algo SHA512 -abs -o Release.gpg Release
cd - >/dev/null
gpg --dearmor <$pubkey >bin/helpbuoy-apt.gpg
gpgv --keyring "$PWD/bin/helpbuoy-apt.gpg" $repo/InRelease 2>/dev/null || {
	echo "InRelease is not signed with the key in $pubkey, which the packages install" >&2
	exit 1
}
echo "$(grep -c '^Package:' $repo/Packages) packages in the index"

# Upload the new .debs first and then the index, with the removals and with
# all files renamed into place at the end, so apt never sees an index whose
# files are missing.
echo "Uploading to $dest"
rsync -a --ignore-existing $repo/*.deb "$dest"
rsync -a --delete --delay-updates $repo/ "$dest"
