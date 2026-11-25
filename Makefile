BINARY    := dcompose
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X main.version=$(VERSION)
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

ifeq ($(OS),Windows_NT)
  BINARY := dcompose.exe
endif

all: build

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) .

install:
	go install -trimpath -ldflags "$(LDFLAGS)" .

run: build
	./$(BINARY)

vet:
	go vet ./...

test:
	go test ./...

# Integration tests against a real daemon. They bring up (and remove) a small
# throwaway compose project named "dcompose-it".
DIR ?= $(CURDIR)/internal/docker/testdata/compose-project
it-test:
	DCOMPOSE_IT_DIR=$(DIR) go test ./internal/docker -run Integration -v -count=1

# Cross-compile release archives for every platform into dist/.
dist:
	rm -rf dist && mkdir -p dist
	@set -e; for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		name=$(BINARY)_$(VERSION)_$${os}_$${arch}; \
		echo "building $$name"; \
		mkdir -p dist/$$name; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$$name/$(BINARY)$$ext .; \
		cp README.md dist/$$name/; \
		if [ $$os = windows ]; then (cd dist && zip -qr $$name.zip $$name); \
		else tar -C dist -czf dist/$$name.tar.gz $$name; fi; \
		rm -rf dist/$$name; \
	done
	cd dist && sha256sum * > checksums.txt

clean:
	rm -rf $(BINARY) dist

.PHONY: all build install run vet test it-test dist clean
