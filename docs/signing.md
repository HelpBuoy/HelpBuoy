# The APT repository and its signing key

The Debian packages (helpbuoy, helpbuoy-desk and helpbuoy-server, amd64 and arm64) are published in a flat APT repository at https://apt.helpbuoy.com/, so `apt upgrade` and unattended-upgrades pick up new versions. The repository is signed with one GPG key. This page describes how the repository works, how a release gets into it, and how to look after the key.

## On the user's machine

Every package adds the repository in its postinst (`packaging/debian/apt-source.postinst`, which the Makefile puts into each package):

| File | Contents |
| --- | --- |
| `/etc/apt/sources.list.d/helpbuoy.sources` | the repository, `URIs: https://apt.helpbuoy.com/` with `Suites: ./` |
| `/etc/apt/keyrings/helpbuoy.asc` | the public key, `Signed-By` of the sources file |
| `/etc/apt/apt.conf.d/52helpbuoy-unattended-upgrades` | lets unattended-upgrades install from origin `HelpBuoy` |

The sources file is added on a new installation and on an upgrade from a version before the repository existed (before 0.4.29), and only when it is not there yet. An admin who removes it to stop updates keeps it removed: later upgrades do not add it again. The unattended-upgrades file follows the same rules, so an admin who removes it keeps upgrading only by hand. It covers all three packages: for helpbuoy-server an unattended upgrade restarts the service, which ends the running sessions.

The key is written again on every upgrade, as long as the sources file is there. That is how a new key reaches installed machines, see [Changing the key](#changing-the-key).
Purging the last HelpBuoy package removes all three files (`packaging/debian/apt-source.postrm`). Purging one while another is still installed or not yet purged keeps them.

Packages released before the repository existed do not add it. Users of those install the new .deb from the GitHub release once by hand; after that, updates come through apt.

## The repository

A flat repository is one directory without a `dists/` tree: the .debs of all packages and architectures, and next to them

- `Packages` and `Packages.gz`, the index made by `apt-ftparchive packages`. One index serves all architectures, since every entry has its own `Architecture` field.
- `Release`, made by `apt-ftparchive release`, with the checksums of the index and `Origin: HelpBuoy`, which unattended-upgrades matches on.
- `InRelease` (the clearsigned `Release`) and `Release.gpg` (a detached signature). apt reads `InRelease` and falls back to the other two.

apt trusts the .debs through this chain: the signature on `InRelease` proves the index, and the index holds the SHA256 of every .deb. The .debs themselves are not signed.

The .debs of the last three GitHub releases stay in the repository, so an admin can roll back with `apt install helpbuoy=<version>`.

The repository lives in `public_html` of its own account, `helpbuoy-apt@ams01.usecue.nl`, apart from the site and its git repository, because the .debs would make that grow by some 20 MB per release.

## Publishing a release

`./release.sh` creates the GitHub release; nothing more is needed here. The APT server, `helpbuoy-apt@ams01.usecue.nl`, checks the GitHub releases every ten minutes and builds, signs and publishes the repository itself, with the secret key in its own keyring. The script, its cron line and its settings live in the `helpbuoy-apt` repository next to this one; its README describes the steps and what to check when a release does not show up.

To pull a broken release from the repository, delete it or mark it as a prerelease on GitHub. The next run drops its .debs, and apt offers the version before it again only to machines that did not upgrade yet; machines that did need `apt install helpbuoy=<version>`.

## Setting up, once

Make the key. ed25519 is small and supported by apt since Debian 9 and Ubuntu 18.04; `sign` limits it to signing. Give it a strong passphrase when gpg asks:

    gpg --quick-gen-key 'HelpBuoy APT repository' ed25519 sign never
    gpg --armor --export 'HelpBuoy APT repository' > packaging/debian/helpbuoy.asc

Commit `packaging/debian/helpbuoy.asc`. `make deb`, `make deb-client` and `make deb-desk` refuse to build without it, since the packages would install no key.

Then install the publisher on the APT server and import the secret key there, as described in the README of `helpbuoy-apt`.

## Looking after the key

Whoever has the secret key can publish packages that every HelpBuoy installation with the repository will install, unattended and as root. Treat it like that.

**Backup.** Without the secret key no update can be signed that installed machines accept, and they can only be moved to a new key by hand. Export it, together with the revocation certificate gpg made with the key (in `~/.gnupg/openpgp-revocs.d/<fingerprint>.rev`), and keep both offline, for example on two USB sticks or in a password manager:

    gpg --armor --export-secret-keys 'HelpBuoy APT repository' > helpbuoy-apt-secret.asc

The export stays protected by the passphrase. Keep the passphrase somewhere other than the export.

**Where it lives.** The APT server signs by itself, so the secret key is in the keyring of `helpbuoy-apt@ams01.usecue.nl`, with its passphrase stored next to the script or removed, because cron cannot type it. Anyone who can log in to that account can sign packages. Release machines do not need the key at all. To move the publisher to another server, import the export there and run `deploy.sh` of `helpbuoy-apt` against it.

**Expiry.** The key above never expires. apt rejects signatures by an expired key, so with an expiry date the key must be extended (`gpg --quick-set-expire`) and the extended public key shipped in a release well before the date, or every installation stops updating. Without expiry there is one less thing to forget; the price is that a lost key stays valid on installed machines.

**What the signature does not cover.** The server signs whatever .debs the last GitHub releases hold, so anyone who can upload a release asset on GitHub can get a package signed; keep two-factor authentication on every account with write access. `Release` has no `Valid-Until`, so someone who controls the server or the connection could keep serving an older, correctly signed index and hold machines on an old version. HTTPS makes the connection part hard. Adding `Valid-Until` would close it, with a daily `publish.sh --force` on the server to sign again before it runs out; but when that cron job breaks, every machine stops getting updates.

## Changing the key

Installed machines trust only the key in `/etc/apt/keyrings/helpbuoy.asc`, and only a package signed with that key can replace it. So a new key goes out in two steps:

1. Make the new key and ship both public keys in one file:

       gpg --armor --export <old fingerprint> <new fingerprint> > packaging/debian/helpbuoy.asc

   Import the new secret key on the APT server and run `deploy.sh` of `helpbuoy-apt`, which copies the new `helpbuoy.asc` there. The order of the keys in the file is not guaranteed, so set `HELPBUOY_APT_KEY=<old fingerprint>` in `~/helpbuoy-apt/config` on the server. Release a few versions like this, still signed with the old key. Every machine that upgrades now trusts both keys.
2. When most machines have had such an upgrade, set `HELPBUOY_APT_KEY=<new fingerprint>` there and sign again with `publish.sh --force`. In a later release export only the new key into `packaging/debian/helpbuoy.asc`, run `deploy.sh` again and remove the old secret key from the server.

Machines that did not upgrade during step 1 fail `apt update` with `NO_PUBKEY` after step 2. They get going again by installing the current .deb from the GitHub release by hand.

## When the key is lost or leaked

**Lost**, with no backup: installed machines cannot be moved to a new key through the repository. Make a new key, publish with it, and tell users (release notes, the site) to install the new .deb by hand once. Until they do, `apt update` reports the HelpBuoy repository as unsigned and skips it; the rest of their updates keep working.

**Leaked**: the old key can still sign updates that installed machines accept, and apt does not check revocation certificates or key servers, so revoking the key does not protect them. The only way out is the key change above, done fast: ship both keys in a release signed with the old key, switch to the new key soon after, then drop the old one. Publish the revocation certificate as well (`gpg --import` the `.rev` file and export the revoked key), so that anyone checking by hand sees it. Check the server for files that did not come from `publish.sh`, and the GitHub releases for assets that did not come from `release.sh`.

## Checking by hand

The signature of the published index, against the key in the repository:

    curl -sO https://apt.helpbuoy.com/InRelease
    gpg --dearmor < packaging/debian/helpbuoy.asc > /tmp/helpbuoy.gpg
    gpgv --keyring /tmp/helpbuoy.gpg InRelease

On an installed machine, what apt sees and what unattended-upgrades would do:

    apt-cache policy helpbuoy helpbuoy-desk helpbuoy-server
    sudo unattended-upgrade --dry-run --debug 2>&1 | grep -i helpbuoy
