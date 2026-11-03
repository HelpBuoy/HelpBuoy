# help-me-win build. Run "make help" for the targets.
#
# Everything builds on Linux with only the Go toolchain and CGO_ENABLED=0:
# hmw-relay, the Windows client, and the raylib helper app (raylib-go purego,
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

HELPER_SYSO := $(foreach a,$(ARCHES),helper/rsrc_windows_$(a).syso)
CLIENT_SYSO := $(foreach a,$(ARCHES),client-windows/rsrc_windows_$(a).syso)
CLIENT_ICONS := $(addprefix client-windows/resources/,icon.png icon48.png icon32.png icon16.png)
# Four part version of the client exe, which the MSI needs for the upgrade.
FILE_VERSION = $(shell jq -r '.RT_VERSION[]["0000"].fixed.file_version' client-windows/resources/winres.json)
MSI := $(DIST)/help-me-win_$(VERSION)_windows-amd64.msi

.PHONY: help all test vet fuzz relay client helper dist msi deb icon clean

help:
	@echo "make test     run proto and relay tests"
	@echo "make vet      go vet all modules (client for windows too)"
	@echo "make fuzz     short fuzz run of the fuzz targets"
	@echo "make relay    build hmw-relay ($(TARGETS)) into $(BIN)/"
	@echo "make client   build the Windows client ($(ARCHES)) into $(BIN)/"
	@echo "make helper   build the helper app ($(TARGETS)) into $(BIN)/"
	@echo "make dist     helper/relay archives in $(DIST)/ (the client ships only as the MSI)"
	@echo "make msi      dist plus the client installer (needs wixl and msitools)"
	@echo "make deb      Debian 13 package of hmw-relay with a systemd service (needs dpkg-deb)"
	@echo "make icon     regenerate the lifebuoy app icon (client, copied to the helper)"

all: vet test relay client helper

test:
	go test ./proto/... ./relay/...

vet:
	go vet ./proto/... ./relay/... ./helper/... ./client-windows/...
	GOOS=windows go vet ./client-windows/... ./helper/...
	GOOS=darwin go vet ./helper/...

fuzz:
	go test ./proto/relayaddr -run '^$$' -fuzz FuzzParse -fuzztime 20s

# build-all NAME PKG EXTRA_LDFLAGS: build PKG for every target in TARGETS.
define build-all
	@for t in $(TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "$(1) $$os/$$arch"; \
		GOOS=$$os GOARCH=$$arch go build $(GOFLAGS) -ldflags "$(LDFLAGS) $(if $(3),$$( [ $$os = windows ] && echo '$(3)'))" \
			-o $(BIN)/$$os-$$arch/$(1)$$ext $(2) || exit 1; \
	done
endef

relay:
	$(call build-all,hmw-relay,./relay/cmd/hmw-relay,)

helper: $(HELPER_SYSO)
	$(call build-all,hmw-helper,./helper,-H=windowsgui)

$(HELPER_SYSO): helper/resources/winres.json helper/resources/icon.png
	cd helper && $(WINRES) make --in resources/winres.json --arch $(WINRES_ARCH)

$(CLIENT_SYSO): client-windows/resources/winres.json $(CLIENT_ICONS)
	cd client-windows && $(WINRES) make --in resources/winres.json --arch $(WINRES_ARCH)

client: $(CLIENT_SYSO)
	@for arch in $(ARCHES); do \
		echo "help-me-win.exe windows/$$arch"; \
		GOOS=windows GOARCH=$$arch go build $(GOFLAGS) -ldflags "$(LDFLAGS) -H=windowsgui" \
			-o $(BIN)/windows-$$arch/help-me-win.exe ./client-windows || exit 1; \
	done

# Release archives: helper app and hmw-relay side by side per OS/arch. macOS
# gets a zipped .app with hmw-relay in Contents/MacOS. The client exe is built
# into bin/ for the MSI (make msi) and not released on its own.
dist: relay helper client
	@rm -rf $(DIST) && mkdir -p $(DIST)
	@for t in $(subst /,-,$(filter linux/%,$(TARGETS))); do \
		tar -czf $(DIST)/hmw-helper_$(VERSION)_$$t.tar.gz -C $(BIN)/$$t hmw-helper hmw-relay; \
	done
	@for t in $(subst /,-,$(filter windows/%,$(TARGETS))); do \
		(cd $(BIN)/$$t && zip -q ../../$(DIST)/hmw-helper_$(VERSION)_$$t.zip hmw-helper.exe hmw-relay.exe) || exit 1; \
	done
	@for t in $(subst /,-,$(filter darwin/%,$(TARGETS))); do \
		GOWORK=off GO111MODULE=off go run tools/macapp/main.go -app hmw-helper -id help-me-win.helper \
			-version $(APP_VERSION) -exe $(BIN)/$$t/hmw-helper -relay $(BIN)/$$t/hmw-relay \
			-icon helper/resources/icon.png -out $(DIST)/hmw-helper_$(VERSION)_$$t.zip || exit 1; \
	done
	@cd $(DIST) && rm -f SHA256SUMS && sha256sum * > SHA256SUMS
	@ls -l $(DIST)

# Client installer, per user and without admin rights (installer/help-me-win.wxs).
# wixl removes the old version right after InstallValidate, outside the
# installation transaction: if the new install then fails, nothing is left.
# RemoveExistingProducts moves after InstallInitialize, so a failed upgrade
# rolls back to the old version (WiX: Schedule="afterInstallInitialize").
msi: dist
	GOWORK=off GO111MODULE=off go run tools/mkico/main.go $(BIN)/help-me-win.ico $(CLIENT_ICONS)
	wixl -a x64 -D Version="$(APP_VERSION)" -D FileVersion="$(FILE_VERSION)" \
		-D ExePath=$(BIN)/windows-amd64/help-me-win.exe -D IconPath=$(BIN)/help-me-win.ico \
		-o $(MSI) installer/help-me-win.wxs
	msiinfo export $(MSI) InstallExecuteSequence > $(BIN)/seq.idt
	sed -i 's/^RemoveExistingProducts\t\t1401\r\?$$/RemoveExistingProducts\t\t1501\r/' $(BIN)/seq.idt
	grep -q $$'^RemoveExistingProducts\t\t1501\r$$' $(BIN)/seq.idt
	msibuild $(MSI) -i $(BIN)/seq.idt
	@# The exe must be versioned in the File table, see installer/help-me-win.wxs.
	msiinfo export $(MSI) File | grep -q $$'\t'"$(FILE_VERSION)"$$'\t'
	@cd $(DIST) && rm -f SHA256SUMS && sha256sum * > SHA256SUMS
	@ls -l $(DIST)

# Debian 13 package of hmw-relay (packaging/debian): the binary, a hardened
# systemd service running as the hmw-relay system user with its state in
# /var/lib/hmw-relay, and hmw-relayctl for admin commands. One per Linux arch.
DEB_ARCHES := $(patsubst linux/%,%,$(filter linux/%,$(TARGETS)))
DEB_DATE    = $(shell date -R -d @$$(git log -1 --format=%ct 2>/dev/null || date +%s))
deb: relay
	@mkdir -p $(DIST)
	@for arch in $(DEB_ARCHES); do \
		root=$(BIN)/deb-$$arch; rm -rf $$root; \
		install -D -m 0755 $(BIN)/linux-$$arch/hmw-relay $$root/usr/bin/hmw-relay; \
		install -D -m 0755 packaging/debian/hmw-relayctl $$root/usr/sbin/hmw-relayctl; \
		install -D -m 0644 packaging/debian/hmw-relay.service $$root/usr/lib/systemd/system/hmw-relay.service; \
		install -D -m 0644 packaging/debian/hmw-relay.sysusers $$root/usr/lib/sysusers.d/hmw-relay.conf; \
		install -D -m 0644 packaging/debian/hmw-relay.default $$root/etc/default/hmw-relay; \
		install -D -m 0644 packaging/debian/README.Debian $$root/usr/share/doc/hmw-relay/README.Debian; \
		install -D -m 0644 packaging/debian/copyright $$root/usr/share/doc/hmw-relay/copyright; \
		printf 'hmw-relay (%s) unstable; urgency=medium\n\n  * Release %s.\n\n -- Maurits van der Schee <maurits@vdschee.nl>  %s\n' \
			"$(APP_VERSION)" "$(VERSION)" "$(DEB_DATE)" | gzip -9n > $$root/usr/share/doc/hmw-relay/changelog.gz; \
		chmod 0644 $$root/usr/share/doc/hmw-relay/changelog.gz; \
		install -d $$root/usr/share/man/man1 $$root/usr/share/man/man8; \
		gzip -9n < packaging/debian/hmw-relay.1 > $$root/usr/share/man/man1/hmw-relay.1.gz; \
		gzip -9n < packaging/debian/hmw-relayctl.8 > $$root/usr/share/man/man8/hmw-relayctl.8.gz; \
		chmod 0644 $$root/usr/share/man/man1/hmw-relay.1.gz $$root/usr/share/man/man8/hmw-relayctl.8.gz; \
		install -D -m 0644 packaging/debian/lintian-overrides $$root/usr/share/lintian/overrides/hmw-relay; \
		install -d $$root/DEBIAN; \
		install -m 0755 packaging/debian/postinst packaging/debian/prerm packaging/debian/postrm $$root/DEBIAN/; \
		echo /etc/default/hmw-relay > $$root/DEBIAN/conffiles; \
		size=$$(du -sk --exclude=DEBIAN $$root | cut -f1); \
		sed "s/@VERSION@/$(APP_VERSION)/; s/@ARCH@/$$arch/; s/@SIZE@/$$size/" packaging/debian/control.in > $$root/DEBIAN/control; \
		(cd $$root && find usr -type f -exec md5sum {} + | sort -k2) > $$root/DEBIAN/md5sums; \
		dpkg-deb --root-owner-group -Zxz --build $$root $(DIST)/hmw-relay_$(APP_VERSION)_$$arch.deb || exit 1; \
	done
	@cd $(DIST) && rm -f SHA256SUMS && sha256sum * > SHA256SUMS
	@ls -l $(DIST)

icon:
	GOWORK=off GO111MODULE=off go run tools/genicon/main.go client-windows/resources/icon.png
	for n in 48 32 16; do \
		GOWORK=off GO111MODULE=off go run tools/genicon/main.go client-windows/resources/icon$$n.png $$n || exit 1; \
	done
	cp client-windows/resources/icon.png helper/resources/icon.png

clean:
	rm -rf $(BIN) $(DIST) helper/rsrc_windows_*.syso client-windows/rsrc_windows_*.syso
