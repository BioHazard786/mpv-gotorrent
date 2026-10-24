MODULE := github.com/biohazard786/mpv-gotorrent
BIN_NAME := mpv-gotorrent
LDFLAGS := -s -w

.PHONY: all build test clean cross install

all: build

build:
	@mkdir -p bin
	go build -ldflags="$(LDFLAGS)" -o bin/$(BIN_NAME) ./cmd/$(BIN_NAME)
	@cp bin/$(BIN_NAME) scripts/mpv-gotorrent/$(BIN_NAME)
	@chmod +x scripts/mpv-gotorrent/$(BIN_NAME)
	@echo "Built bin/$(BIN_NAME) and mirrored to scripts/mpv-gotorrent/$(BIN_NAME)"

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

install: build
	@echo "==> Installing mpv-gotorrent to mpv configuration directory..."
	@MPV_DIR="$${HOME}/.config/mpv/scripts/mpv-gotorrent"; \
	mkdir -p "$$MPV_DIR"; \
	cp scripts/mpv-gotorrent/main.lua "$$MPV_DIR/main.lua"; \
	cp bin/$(BIN_NAME) "$$MPV_DIR/$(BIN_NAME)"; \
	chmod +x "$$MPV_DIR/$(BIN_NAME)"; \
	echo "Installed main.lua and $(BIN_NAME) to $$MPV_DIR"

clean:
	rm -rf bin/ scripts/mpv-gotorrent/$(BIN_NAME)
