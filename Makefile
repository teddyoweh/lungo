# Lungo: `make install` puts the `sky` CLI in ~/.local/bin; `make desktop-install` puts Lungo.app in ~/Applications.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
BIN     := $(HOME)/.local/bin
export PATH := $(HOME)/go/bin:$(PATH)

.PHONY: build install test vet cli-all desktop desktop-install clean

build:
	go build -ldflags "$(LDFLAGS)" -o bin/sky ./cmd/sky

install: build
	mkdir -p $(BIN)
	install -m 755 bin/sky $(BIN)/sky
	@echo "installed $(BIN)/sky"

test:
	go test ./internal/... ./cmd/...

vet:
	go vet ./internal/... ./cmd/...

# Cross-compiled CLI binaries for every OS.
cli-all:
	@for t in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do \
		os=$${t%/*}; arch=$${t#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "sky $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -ldflags "$(LDFLAGS)" -o dist/sky-$$os-$$arch$$ext ./cmd/sky || exit 1; \
	done

desktop:
	cd desktop && wails build -clean -ldflags "-X main.version=$(VERSION)"

desktop-install: desktop
	mkdir -p $(HOME)/Applications
	rm -rf $(HOME)/Applications/Lungo.app
	@# Skybuild.app is its old name. A real app there goes; a bare link there stays: running
	@# sessions get their folder permissions through it (internal/engine/anchor.go).
	@if [ -f $(HOME)/Applications/Skybuild.app/Contents/Info.plist ]; then rm -rf $(HOME)/Applications/Skybuild.app; fi
	cp -R desktop/build/bin/Lungo.app $(HOME)/Applications/
	@echo "installed ~/Applications/Lungo.app"

clean:
	rm -rf bin dist desktop/build/bin
