# helpbuoy build. Run "make help" for the targets.
#
# Everything builds on Linux with only the Go toolchain and CGO_ENABLED=0:
# helpbuoy-server, the Windows and Linux clients, and the raylib desk app (raylib-go purego,
# which embeds the raylib shared library per OS/arch). No Docker, no C compiler.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# Numeric version for Info.plist and the MSI: v1.2.3-4-gabc -> 1.2.3, anything else -> 0.0.1.
APP_VERSION := $(shell echo $(VERSION) | sed 's/^v//; s/-.*//; /^[0-9][0-9.]*$$/!s/.*/0.0.1/')
LDFLAGS := -s -w -X main.version=$(VERSION)
GOFLAGS := -trimpath
export CGO_ENABLED := 0
SHELL := /bin/bash

BIN  := bin
DIST := dist

# Only Linux and Windows on amd64 for now; add darwin or arm64 here to build
# them again (the MSI stays x64).
OSES    := linux windows
ARCHES  := amd64
TARGETS := $(foreach os,$(OSES),$(addprefix $(os)/,$(ARCHES)))
WINRES  := go run github.com/tc-hib/go-winres@v0.3.3
comma   := ,
space   := $(subst ,, )
WINRES_ARCH := $(subst $(space),$(comma),$(ARCHES))

DESK_SYSO := $(foreach a,$(ARCHES),desk/rsrc_windows_$(a).syso)
CLIENT_SYSO := $(foreach a,$(ARCHES),client-windows/rsrc_windows_$(a).syso)
CLIENT_ICONS := $(addprefix client-windows/resources/,icon.png icon48.png icon32.png icon16.png)
# Four part versions of the Windows exes, which the MSIs need for the upgrade.
FILE_VERSION = $(shell jq -r '.RT_VERSION[]["0000"].fixed.file_version' client-windows/resources/winres.json)
DESK_FILE_VERSION = $(shell jq -r '.RT_VERSION[]["0000"].fixed.file_version' desk/resources/winres.json)
MSI := $(DIST)/helpbuoy_$(APP_VERSION)_amd64.msi
DESK_MSI := $(DIST)/helpbuoy-desk_$(APP_VERSION)_amd64.msi

.PHONY: help all test vet fuzz server client client-linux desk dist msi deb deb-client deb-desk icon clean

help:
	@echo "make test     run proto and server tests"
	@echo "make vet      go vet all modules (client for windows too)"
	@echo "make fuzz     short fuzz run of the fuzz targets"
	@echo "make server   build helpbuoy-server ($(TARGETS)) into $(BIN)/"
	@echo "make client   build the Windows client ($(ARCHES)) into $(BIN)/"
	@echo "make client-linux  build the static X11 Linux client ($(ARCHES)) into $(BIN)/"
	@echo "make desk     build the desk app ($(TARGETS)) into $(BIN)/"
	@echo "make dist     all release packages in $(DIST)/: msi, deb, deb-client and deb-desk"
	@echo "make msi      per-user installers of the Windows client and desk app (needs wixl and msitools)"
	@echo "make deb      Debian 13 package of helpbuoy-server with a systemd service (needs dpkg-deb)"
	@echo "make deb-client  Debian package of the Linux client with a menu entry and icons (needs dpkg-deb)"
	@echo "make deb-desk Debian package of the desk app with a menu entry and icons (needs dpkg-deb)"
	@echo "make icon     regenerate the lifebuoy app icon (client, copied to the desk app)"

all: vet test server client client-linux desk

test:
	go test ./proto/... ./server/... ./client-linux/...

vet:
	go vet ./proto/... ./server/... ./desk/... ./client-windows/... ./client-linux/...
	GOOS=windows go vet ./client-windows/... ./desk/...
	GOOS=darwin go vet ./desk/...

fuzz:
	go test ./proto/serveraddr -run '^$$' -fuzz FuzzParse -fuzztime 20s

# build-all NAME PKG EXTRA_LDFLAGS: build PKG for every target in TARGETS.
define build-all
	@for t in $(TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "$(1) $$os/$$arch"; \
		GOOS=$$os GOARCH=$$arch go build $(GOFLAGS) -ldflags "$(LDFLAGS) $(if $(3),$$( [ $$os = windows ] && echo '$(3)'))" \
			-o $(BIN)/$$os-$$arch/$(1)$$ext $(2) || exit 1; \
	done
endef

server:
	$(call build-all,helpbuoy-server,./server/cmd/helpbuoy-server,)

desk: $(DESK_SYSO)
	$(call build-all,helpbuoy-desk,./desk,-H=windowsgui)

$(DESK_SYSO): desk/resources/winres.json desk/resources/icon.png
	cd desk && $(WINRES) make --in resources/winres.json --arch $(WINRES_ARCH)

$(CLIENT_SYSO): client-windows/resources/winres.json $(CLIENT_ICONS)
	cd client-windows && $(WINRES) make --in resources/winres.json --arch $(WINRES_ARCH)

client: $(CLIENT_SYSO)
	@for arch in $(ARCHES); do \
		echo "helpbuoy.exe windows/$$arch"; \
		GOOS=windows GOARCH=$$arch go build $(GOFLAGS) -ldflags "$(LDFLAGS) -H=windowsgui" \
			-o $(BIN)/windows-$$arch/helpbuoy.exe ./client-windows || exit 1; \
	done

# The Linux client: one static binary that speaks X11 itself (jezek/xgb),
# so it runs on any Linux desktop without installing anything.
client-linux:
	@for arch in $(ARCHES); do \
		echo "helpbuoy linux/$$arch"; \
		GOOS=linux GOARCH=$$arch go build $(GOFLAGS) -ldflags "$(LDFLAGS)" \
			-o $(BIN)/linux-$$arch/helpbuoy ./client-linux || exit 1; \
	done

# Release packages, all in $(DIST)/: an installer per app and platform, no
# bare binaries. Windows gets per-user MSIs for the client and the desk app,
# Debian gets .debs for the client, the desk app and helpbuoy-server, and
# macOS (when darwin is in OSES) a zipped desk .app.
dist:
	@rm -rf $(DIST) && mkdir -p $(DIST)
	@$(MAKE) --no-print-directory msi deb deb-client deb-desk
	@for t in $(subst /,-,$(filter darwin/%,$(TARGETS))); do \
		GOWORK=off GO111MODULE=off go run tools/macapp/main.go -app helpbuoy-desk -id helpbuoy.desk \
			-version $(APP_VERSION) -exe $(BIN)/$$t/helpbuoy-desk \
			-icon desk/resources/icon.png -out $(DIST)/helpbuoy-desk_$(VERSION)_$$t.zip || exit 1; \
	done
	@cd $(DIST) && rm -f SHA256SUMS && sha256sum * > SHA256SUMS
	@ls -l $(DIST)

# build-msi WXS EXE FILE_VERSION OUT: a per-user MSI, without admin rights
# (installer/*.wxs). wixl removes the old version right after
# InstallValidate, outside the installation transaction: if the new install
# then fails, nothing is left. RemoveExistingProducts moves after
# InstallInitialize, so a failed upgrade rolls back to the old version
# (WiX: Schedule="afterInstallInitialize").
define build-msi
	wixl -a x64 -D Version="$(APP_VERSION)" -D FileVersion="$(3)" \
		-D ExePath=$(2) -D IconPath=$(BIN)/helpbuoy.ico -o $(4) $(1)
	msiinfo export $(4) InstallExecuteSequence > $(BIN)/seq.idt
	sed -i 's/^RemoveExistingProducts\t\t1401\r\?$$/RemoveExistingProducts\t\t1501\r/' $(BIN)/seq.idt
	grep -q $$'^RemoveExistingProducts\t\t1501\r$$' $(BIN)/seq.idt
	msibuild $(4) -i $(BIN)/seq.idt
	@# The exe must be versioned in the File table, see installer/helpbuoy.wxs.
	msiinfo export $(4) File | grep -q $$'\t'"$(3)"$$'\t'
endef

msi: client desk
	@mkdir -p $(DIST)
	GOWORK=off GO111MODULE=off go run tools/mkico/main.go $(BIN)/helpbuoy.ico $(CLIENT_ICONS)
	$(call build-msi,installer/helpbuoy.wxs,$(BIN)/windows-amd64/helpbuoy.exe,$(FILE_VERSION),$(MSI))
	$(call build-msi,installer/helpbuoy-desk.wxs,$(BIN)/windows-amd64/helpbuoy-desk.exe,$(DESK_FILE_VERSION),$(DESK_MSI))

# Debian 13 package of helpbuoy-server (packaging/debian): the binary, a hardened
# systemd service running as the helpbuoy-server system user with its state in
# /var/lib/helpbuoy-server, and helpbuoy-serverctl for admin commands. One per Linux arch.
DEB_ARCHES := $(patsubst linux/%,%,$(filter linux/%,$(TARGETS)))
DEB_DATE    = $(shell date -R -d @$$(git log -1 --format=%ct 2>/dev/null || date +%s))
deb: server
	@mkdir -p $(DIST)
	@for arch in $(DEB_ARCHES); do \
		root=$(BIN)/deb-$$arch; rm -rf $$root; \
		install -D -m 0755 $(BIN)/linux-$$arch/helpbuoy-server $$root/usr/bin/helpbuoy-server; \
		install -D -m 0755 packaging/debian/helpbuoy-serverctl $$root/usr/sbin/helpbuoy-serverctl; \
		install -D -m 0644 packaging/debian/helpbuoy-server.service $$root/usr/lib/systemd/system/helpbuoy-server.service; \
		install -D -m 0644 packaging/debian/helpbuoy-server.sysusers $$root/usr/lib/sysusers.d/helpbuoy-server.conf; \
		install -D -m 0644 packaging/debian/helpbuoy-server.default $$root/etc/default/helpbuoy-server; \
		install -D -m 0644 packaging/debian/README.Debian $$root/usr/share/doc/helpbuoy-server/README.Debian; \
		install -D -m 0644 packaging/debian/copyright $$root/usr/share/doc/helpbuoy-server/copyright; \
		printf 'helpbuoy-server (%s) unstable; urgency=medium\n\n  * Release %s.\n\n -- Maurits van der Schee <maurits@vdschee.nl>  %s\n' \
			"$(APP_VERSION)" "$(VERSION)" "$(DEB_DATE)" | gzip -9n > $$root/usr/share/doc/helpbuoy-server/changelog.gz; \
		chmod 0644 $$root/usr/share/doc/helpbuoy-server/changelog.gz; \
		install -d $$root/usr/share/man/man1 $$root/usr/share/man/man8; \
		gzip -9n < packaging/debian/helpbuoy-server.1 > $$root/usr/share/man/man1/helpbuoy-server.1.gz; \
		gzip -9n < packaging/debian/helpbuoy-serverctl.8 > $$root/usr/share/man/man8/helpbuoy-serverctl.8.gz; \
		chmod 0644 $$root/usr/share/man/man1/helpbuoy-server.1.gz $$root/usr/share/man/man8/helpbuoy-serverctl.8.gz; \
		install -D -m 0644 packaging/debian/lintian-overrides $$root/usr/share/lintian/overrides/helpbuoy-server; \
		install -d $$root/DEBIAN; \
		install -m 0755 packaging/debian/postinst packaging/debian/prerm packaging/debian/postrm $$root/DEBIAN/; \
		echo /etc/default/helpbuoy-server > $$root/DEBIAN/conffiles; \
		size=$$(du -sk --exclude=DEBIAN $$root | cut -f1); \
		sed "s/@VERSION@/$(APP_VERSION)/; s/@ARCH@/$$arch/; s/@SIZE@/$$size/" packaging/debian/control.in > $$root/DEBIAN/control; \
		(cd $$root && find usr -type f -exec md5sum {} + | sort -k2) > $$root/DEBIAN/md5sums; \
		dpkg-deb --root-owner-group -Zxz --build $$root $(DIST)/helpbuoy-server_$(APP_VERSION)_$$arch.deb || exit 1; \
	done

# deb-app NAME DESC: a Debian package of a desktop app: $(BIN)/linux-ARCH/NAME,
# its menu entry packaging/debian/NAME.desktop, the icon in the hicolor theme
# and packaging/debian/NAME.control.in. The hicolor-icon-theme and
# desktop-file-utils triggers refresh the caches, so it needs no maintainer
# scripts. One per Linux arch.
APP_DEB_ICONS := 16 32 48 256
define deb-app
	@mkdir -p $(DIST)
	@for arch in $(DEB_ARCHES); do \
		root=$(BIN)/deb-$(1)-$$arch; rm -rf $$root; \
		install -D -m 0755 $(BIN)/linux-$$arch/$(1) $$root/usr/bin/$(1); \
		install -D -m 0644 packaging/debian/$(1).desktop $$root/usr/share/applications/$(1).desktop; \
		for n in $(APP_DEB_ICONS); do \
			src=client-linux/resources/icon$$n.png; [ $$n = 256 ] && src=client-linux/resources/icon.png; \
			install -D -m 0644 $$src $$root/usr/share/icons/hicolor/$${n}x$$n/apps/$(1).png || exit 1; \
		done; \
		install -D -m 0644 packaging/debian/copyright $$root/usr/share/doc/$(1)/copyright; \
		printf '$(1) (%s) unstable; urgency=medium\n\n  * Release %s.\n\n -- Maurits van der Schee <maurits@vdschee.nl>  %s\n' \
			"$(APP_VERSION)" "$(VERSION)" "$(DEB_DATE)" | gzip -9n > $$root/usr/share/doc/$(1)/changelog.gz; \
		chmod 0644 $$root/usr/share/doc/$(1)/changelog.gz; \
		install -d $$root/DEBIAN; \
		size=$$(du -sk --exclude=DEBIAN $$root | cut -f1); \
		sed "s/@VERSION@/$(APP_VERSION)/; s/@ARCH@/$$arch/; s/@SIZE@/$$size/" packaging/debian/$(1).control.in > $$root/DEBIAN/control; \
		(cd $$root && find usr -type f -exec md5sum {} + | sort -k2) > $$root/DEBIAN/md5sums; \
		dpkg-deb --root-owner-group -Zxz --build $$root $(DIST)/$(1)_$(APP_VERSION)_$$arch.deb || exit 1; \
	done
endef

# The Linux client: static, so it runs on any Debian release and derivative.
deb-client: client-linux
	$(call deb-app,helpbuoy)

# The desk app: the embedded raylib needs glibc 2.38 (Debian 13, Ubuntu 24.04).
deb-desk: desk
	$(call deb-app,helpbuoy-desk)

icon:
	GOWORK=off GO111MODULE=off go run tools/genicon/main.go client-windows/resources/icon.png
	for n in 48 32 16; do \
		GOWORK=off GO111MODULE=off go run tools/genicon/main.go client-windows/resources/icon$$n.png $$n || exit 1; \
	done
	cp client-windows/resources/icon.png desk/resources/icon.png
	cp client-windows/resources/icon*.png client-linux/resources/

clean:
	rm -rf $(BIN) $(DIST) desk/rsrc_windows_*.syso client-windows/rsrc_windows_*.syso
