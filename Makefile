# help-me-win build. Run "make help" for the targets.
#
# Everything builds on Linux with only the Go toolchain and CGO_ENABLED=0:
# hmw-relay, the Windows client, and the raylib helper app (raylib-go purego,
# which embeds the raylib shared library per OS/arch). No Docker, no C compiler.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# Numeric version for Info.plist: v1.2.3-4-gabc -> 1.2.3, anything else -> 0.0.1.
APP_VERSION := $(shell echo $(VERSION) | sed 's/^v//; s/-.*//; /^[0-9][0-9.]*$$/!s/.*/0.0.1/')
LDFLAGS := -s -w -X main.version=$(VERSION)
GOFLAGS := -trimpath
export CGO_ENABLED := 0

BIN  := bin
DIST := dist

TARGETS       := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64
CLIENT_ARCHES := amd64 arm64
WINRES        := go run github.com/tc-hib/go-winres@v0.3.3

.PHONY: help all test vet fuzz relay client helper dist icon clean

help:
	@echo "make test     run proto and relay tests"
	@echo "make vet      go vet all modules (client for windows too)"
	@echo "make fuzz     short fuzz run of the fuzz targets"
	@echo "make relay    build hmw-relay for all OS/arch into $(BIN)/"
	@echo "make client   build the Windows client (amd64, arm64) into $(BIN)/"
	@echo "make helper   build the helper app for all OS/arch into $(BIN)/"
	@echo "make dist     release archives in $(DIST)/"
	@echo "make icon     regenerate the placeholder icon"

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

helper: helper/rsrc_windows_amd64.syso helper/rsrc_windows_arm64.syso
	$(call build-all,hmw-helper,./helper,-H=windowsgui)

helper/rsrc_windows_amd64.syso helper/rsrc_windows_arm64.syso: helper/resources/winres.json helper/resources/icon.png
	cd helper && $(WINRES) make --in resources/winres.json --arch amd64,arm64

client-windows/rsrc_windows_amd64.syso client-windows/rsrc_windows_arm64.syso: client-windows/resources/winres.json client-windows/resources/icon.png
	cd client-windows && $(WINRES) make --in resources/winres.json --arch amd64,arm64

client: client-windows/rsrc_windows_amd64.syso client-windows/rsrc_windows_arm64.syso
	@for arch in $(CLIENT_ARCHES); do \
		echo "help-me-win.exe windows/$$arch"; \
		GOOS=windows GOARCH=$$arch go build $(GOFLAGS) -ldflags "$(LDFLAGS) -H=windowsgui" \
			-o $(BIN)/windows-$$arch/help-me-win.exe ./client-windows || exit 1; \
	done

# Release archives: helper app and hmw-relay side by side per OS/arch, and the
# client exe alone. macOS gets a zipped .app with hmw-relay in Contents/MacOS.
dist: relay helper client
	@rm -rf $(DIST) && mkdir -p $(DIST)
	@for t in linux-amd64 linux-arm64; do \
		tar -czf $(DIST)/hmw-helper_$(VERSION)_$$t.tar.gz -C $(BIN)/$$t hmw-helper hmw-relay; \
	done
	@for t in windows-amd64 windows-arm64; do \
		(cd $(BIN)/$$t && zip -q ../../$(DIST)/hmw-helper_$(VERSION)_$$t.zip hmw-helper.exe hmw-relay.exe) || exit 1; \
	done
	@for t in darwin-amd64 darwin-arm64; do \
		GOWORK=off GO111MODULE=off go run tools/macapp/main.go -app hmw-helper -id help-me-win.helper \
			-version $(APP_VERSION) -exe $(BIN)/$$t/hmw-helper -relay $(BIN)/$$t/hmw-relay \
			-icon helper/resources/icon.png -out $(DIST)/hmw-helper_$(VERSION)_$$t.zip || exit 1; \
	done
	@for arch in $(CLIENT_ARCHES); do \
		cp $(BIN)/windows-$$arch/help-me-win.exe $(DIST)/help-me-win_$(VERSION)_windows-$$arch.exe; \
	done
	@cd $(DIST) && sha256sum * > SHA256SUMS
	@ls -l $(DIST)

icon:
	GOWORK=off GO111MODULE=off go run tools/genicon/main.go helper/resources/icon.png
	cp helper/resources/icon.png client-windows/resources/icon.png

clean:
	rm -rf $(BIN) $(DIST) helper/rsrc_windows_*.syso client-windows/rsrc_windows_*.syso
