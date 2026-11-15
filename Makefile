GO ?= go
GO_LDFLAGS := -linkmode=external -s -w

.PHONY: all server app test clean

all: server app

server:
	@mkdir -p dist
	GOCACHE=$${GOCACHE:-/tmp/go-build} GOTMPDIR=$${GOTMPDIR:-/tmp} $(GO) -C server build -ldflags="$(GO_LDFLAGS)" -o ../dist/subbar-server ./cmd/subbar-server

app:
	CLANG_MODULE_CACHE_PATH=$${CLANG_MODULE_CACHE_PATH:-/tmp/clang-module-cache} \
	SWIFTPM_MODULECACHE_OVERRIDE=$${SWIFTPM_MODULECACHE_OVERRIDE:-/tmp/swiftpm-module-cache} \
	./macos/Scripts/bundle-app.sh

test:
	GOCACHE=$${GOCACHE:-/tmp/go-build} GOTMPDIR=$${GOTMPDIR:-/tmp} $(GO) -C server test -ldflags="-linkmode=external" -v ./...

clean:
	rm -rf dist macos/.build
