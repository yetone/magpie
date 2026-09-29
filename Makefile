VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  = -s -w -X main.version=$(VERSION)
TAGS     = production
TARGETS  = darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64 windows/arm64

# The GUI links the platform webview through cgo, so it is built natively.
# `nogui` builds the terminal-only magpie, which cross-compiles anywhere.
# macOS 12 is the oldest Go (1.25 and later) runs on; Info.plist says so too.
ifeq ($(shell uname -s),Darwin)
  export CGO_CFLAGS  = -mmacosx-version-min=12.0
  export CGO_LDFLAGS = -mmacosx-version-min=12.0
endif
# On Linux, GTK 3 and WebKitGTK 4.1: older distributions have them, where
# Wails' default GTK 4 and WebKitGTK 6 would leave them out.
ifeq ($(shell uname -s),Linux)
  TAGS += gtk3
endif

.PHONY: build cli install test app icons release release-cli release-windows release-linux clean dev dev-once

build:
	go build -tags "$(TAGS)" -trimpath -ldflags="$(LDFLAGS)" -o magpie .

cli:
	CGO_ENABLED=0 go build -tags nogui -trimpath -ldflags="$(LDFLAGS)" -o magpie .

install:
	go install -tags "$(TAGS)" -trimpath -ldflags="$(LDFLAGS)" .

test:
	go vet ./... && go test ./...

# macOS bundle: menu bar app with no Dock icon (LSUIElement).
app: build
	@rm -rf magpie.app
	@mkdir -p magpie.app/Contents/MacOS magpie.app/Contents/Resources
	@cp magpie magpie.app/Contents/MacOS/magpie
	@cp build/darwin/magpie.icns magpie.app/Contents/Resources/magpie.icns
	@cp build/darwin/Assets.car magpie.app/Contents/Resources/Assets.car
	@sed 's/@VERSION@/$(VERSION)/' build/darwin/Info.plist > magpie.app/Contents/Info.plist
	@echo "  magpie.app"

icons:
	@go run build/icon/gen.go tray internal/gui/tray.png
	@go run build/icon/gen.go tray-flap internal/gui/trayflap
	@go run build/icon/gen.go app 64 internal/gui/icon.png
	@go run build/icon/gen.go app 1024 internal/gui/icon-1024.png
	@rm -rf build/darwin/magpie.iconset && mkdir -p build/darwin/magpie.iconset
	@for s in 16 32 128 256 512; do \
		go run build/icon/gen.go app $$s build/darwin/magpie.iconset/icon_$${s}x$${s}.png; \
		go run build/icon/gen.go app $$((s*2)) build/darwin/magpie.iconset/icon_$${s}x$${s}@2x.png; \
	done
	@iconutil -c icns build/darwin/magpie.iconset -o build/darwin/magpie.icns && rm -rf build/darwin/magpie.iconset
	@# macOS 26 draws the Dock icon from AppIcon.icon, light and dark (Xcode 26's actool);
	@# the compiled Assets.car is kept in the tree so a release needn't have it
	@go run build/icon/gen.go glyph 1024 build/darwin/AppIcon.icon/Assets/magpie.png
	@rm -rf build/darwin/actool && mkdir -p build/darwin/actool
	@xcrun actool build/darwin/AppIcon.icon --compile build/darwin/actool --platform macosx \
		--minimum-deployment-target 11.0 --app-icon AppIcon --output-partial-info-plist build/darwin/actool/partial.plist >/dev/null
	@mv build/darwin/actool/Assets.car build/darwin/Assets.car && rm -rf build/darwin/actool

release: clean build
	@mkdir -p dist
	@cp magpie dist/magpie-$(shell go env GOOS)-$(shell go env GOARCH)
	@$(MAKE) --no-print-directory release-cli

release-cli:
	@mkdir -p dist
	@for t in $(TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; ext=""; [ $$os = windows ] && ext=.exe; \
		echo "  $$os/$$arch (cli)"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -tags nogui -trimpath -ldflags="$(LDFLAGS)" -o dist/magpie-cli-$$os-$$arch$$ext . ; \
	done

# The desktop app for Windows: the system WebView2 needs no cgo, so both
# arches cross-compile. Linked as a GUI program (no console window), with
# the icon, version and a DPI-aware manifest from build/windows.
WINVER = $(shell echo $(VERSION) | sed -E 's/^v//; s/[^0-9.].*//')
release-windows:
	@mkdir -p dist
	@go run github.com/tc-hib/go-winres@v0.3.3 make --in build/windows/winres.json --arch amd64,arm64 --out rsrc \
		--file-version "$(or $(WINVER),0.0.0)" --product-version "$(or $(WINVER),0.0.0)"
	@for arch in amd64 arm64; do \
		echo "  windows/$$arch (app)"; \
		CGO_ENABLED=0 GOOS=windows GOARCH=$$arch go build -tags production -trimpath -ldflags="$(LDFLAGS) -H windowsgui" -o dist/magpie-windows-$$arch.exe . || exit 1; \
	done; rm -f rsrc_windows_*.syso

# The desktop app for Linux links GTK 3 and WebKitGTK 4.1 through cgo, so it
# is built natively, one arch per machine.
release-linux:
	@mkdir -p dist
	@echo "  linux/$(shell go env GOARCH) (app)"
	@go build -tags "$(TAGS)" -trimpath -ldflags="$(LDFLAGS)" -o dist/magpie-linux-$(shell go env GOARCH) .

clean:
	rm -rf magpie magpie.exe magpie.app dist rsrc_windows_*.syso

# Development: the UI is served from internal/gui/assets and the window
# reloads itself when a file there is saved. With fswatch installed, the
# windows stay up and a Go change rebuilds and restarts only the backend
# behind them (build/dev.sh); without it, dev-once runs the app as one
# process. Uses its own ports so a running magpie keeps serving the agents.
DEV_ADDR ?= 127.0.0.1:3426
DEV_UI ?= 127.0.0.1:3427
DEV_BACKEND ?= 127.0.0.1:3428
DEV_CONTROL ?= 127.0.0.1:3429
dev:
	@if command -v fswatch >/dev/null; then \
	  MAGPIE_ADDR=$(DEV_ADDR) MAGPIE_DEV_UI=$(DEV_UI) MAGPIE_DEV_BACKEND=$(DEV_BACKEND) MAGPIE_DEV_CONTROL=$(DEV_CONTROL) build/dev.sh; \
	else $(MAKE) --no-print-directory dev-once; fi

dev-once:
	@go build -tags dev -o magpie-dev . && echo "  magpie-dev · UI from internal/gui/assets, reload on save · gateway $(DEV_ADDR)"
	@MAGPIE_ADDR=$(DEV_ADDR) MAGPIE_DEV_UI=$(DEV_UI) ./magpie-dev app
