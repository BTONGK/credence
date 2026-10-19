GO ?= go
BIN := bin

.PHONY: all build ui test vet fmt check demo dev clean dist fresh-test release-check

all: build

build:
	$(GO) build -o $(BIN)/ ./cmd/...

ui:
	cd ui && npm ci && npm run build

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -w .

check: vet test
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:" && gofmt -l . && exit 1)
	cd ui && npm run typecheck

# A self-contained demo home and repo; nothing outside ./demo-home is touched.
demo: build
	$(BIN)/credence demo --dir ./demo-home

# UI with hot reload against a running daemon (default port 7171).
dev:
	cd ui && CREDENCE_URL=$${CREDENCE_URL:-http://127.0.0.1:7171} npx vite

# Release archives and checksums.txt, as the installer downloads them.
dist:
	sh scripts/dist.sh dist

# Install the way a new user does, inside a throwaway home folder.
fresh-test:
	sh scripts/fresh-install-test.sh

# Everything that runs before a release.
release-check: check fresh-test

clean:
	rm -rf $(BIN) dist
