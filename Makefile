BIN     := ship
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
LDFLAGS := -s -w -X github.com/thehenrymcintosh/ship/internal/brand.Version=$(VERSION)

.PHONY: build schema test test-race test-live lint clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BIN) ./cmd/ship

# Regenerate the committed JSON Schemas from the Go structs.
schema:
	go run ./cmd/ship schema > schema/pipeline.json
	go run ./cmd/ship schema --config > schema/config.json

test:
	go test ./...

test-race:
	go test -race ./...

# One real Claude call with haiku (costs a few cents).
test-live:
	SHIP_LIVE_CLAUDE=1 go test ./internal/agent/claude/ -run TestLive -v

lint:
	gofmt -l . | (! grep .)
	go vet ./...

clean:
	rm -rf bin dist
