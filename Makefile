MODULE := github.com/biohazard786/mpv-gotorrent
BIN_NAME := mpv-gotorrent
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "v0.1.0")
LDFLAGS := -s -w -X main.Version=$(VERSION)

.PHONY: all build test clean cross install iina-plugin

all: build

build:
	@mkdir -p bin
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o bin/$(BIN_NAME) ./cmd/$(BIN_NAME)
	@echo "Built bin/$(BIN_NAME) (version $(VERSION))"

test:
	go test -v ./...

cross:
	@mkdir -p bin
	@echo "==> Building Linux (amd64 & arm64)..."
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o bin/$(BIN_NAME)-linux-amd64 ./cmd/$(BIN_NAME)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o bin/$(BIN_NAME)-linux-arm64 ./cmd/$(BIN_NAME)
	@echo "==> Building Windows (amd64)..."
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o bin/$(BIN_NAME)-windows-amd64.exe ./cmd/$(BIN_NAME)
	@echo "==> Building macOS (amd64 & Apple Silicon arm64)..."
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o bin/$(BIN_NAME)-darwin-amd64 ./cmd/$(BIN_NAME)
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o bin/$(BIN_NAME)-darwin-arm64 ./cmd/$(BIN_NAME)
	@echo "==> Cross-compilation completed successfully."

iina-plugin:
	@mkdir -p dist/iina/mpv-gotorrent.iinaplugin/bin
	@mkdir -p dist/iina/mpv-gotorrent.iinaplugin/ui
	@echo "==> Building macOS companion binaries for IINA plugin..."
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o dist/iina/mpv-gotorrent.iinaplugin/bin/mpv-gotorrent-darwin-arm64 ./cmd/$(BIN_NAME)
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o dist/iina/mpv-gotorrent.iinaplugin/bin/mpv-gotorrent-darwin-amd64 ./cmd/$(BIN_NAME)
	@echo "==> Copying IINA plugin manifest and scripts..."
	cp -f iina-plugin/Info.json dist/iina/mpv-gotorrent.iinaplugin/
	cp -f iina-plugin/main.js dist/iina/mpv-gotorrent.iinaplugin/
	cp -f iina-plugin/global.js dist/iina/mpv-gotorrent.iinaplugin/
	cp -rf iina-plugin/ui/* dist/iina/mpv-gotorrent.iinaplugin/ui/
	@echo "==> Packaging dist/mpv-gotorrent.iinaplgz..."
	(cd dist/iina && zip -rq ../mpv-gotorrent.iinaplgz mpv-gotorrent.iinaplugin)
	@echo "Built dist/mpv-gotorrent.iinaplgz successfully!"

install: build
	@echo "==> Installing mpv-gotorrent..."
	@./install.sh --local

clean:
	rm -rf bin/ dist/

