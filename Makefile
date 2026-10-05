BIN     := ship
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
LDFLAGS := -s -w -X github.com/thehenrymcintosh/ship/internal/brand.Version=$(VERSION)

PREFIX  ?= $(HOME)/.local/bin

.PHONY: build install release schema test test-race test-live lint clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BIN) ./cmd/ship

# Build and install to ~/.local/bin (PREFIX=… to change). A running daemon
# keeps the old version until `ship serve --restart`.
install:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(PREFIX)/$(BIN) ./cmd/ship
	@echo "installed $$($(PREFIX)/$(BIN) version | head -n 1) to $(PREFIX)/$(BIN)"

# Test, install locally, tag and push (BUMP=patch|minor|major|X.Y.Z).
release:
	scripts/release.sh $(or $(BUMP),patch)

# Regenerate the committed JSON Schemas from the Go structs.
schema:
	go run ./cmd/ship schema > schema/pipeline.json
	go run ./cmd/ship schema --config > schema/config.json

# -count=1: the CLI tests build the binary themselves, so cached results can
# miss changes to what it embeds.
test:
	go test -count=1 ./...

test-race:
	go test -count=1 -race ./...

# One real Claude call with haiku (costs a few cents).
test-live:
	SHIP_LIVE_CLAUDE=1 go test ./internal/agent/claude/ -run TestLive -v

lint:
	gofmt -l . | (! grep .)
	go vet ./...

clean:
	rm -rf bin dist
