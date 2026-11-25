GO ?= go
UNAME_S := $(shell uname -s)
ifeq ($(UNAME_S),Darwin)
GO_LDFLAGS := -linkmode=external -s -w
GO_TEST_LDFLAGS := -ldflags="-linkmode=external"
else
GO_LDFLAGS := -s -w
GO_TEST_LDFLAGS :=
endif

.PHONY: all server app lint test accept verify verify-app clean

all: server app

server:
	@mkdir -p dist
	GOCACHE=$${GOCACHE:-/tmp/go-build} GOTMPDIR=$${GOTMPDIR:-/tmp} $(GO) -C server build -ldflags="$(GO_LDFLAGS)" -o ../dist/subbar-server ./cmd/subbar-server

app:
	CLANG_MODULE_CACHE_PATH=$${CLANG_MODULE_CACHE_PATH:-/tmp/clang-module-cache} \
	SWIFTPM_MODULECACHE_OVERRIDE=$${SWIFTPM_MODULECACHE_OVERRIDE:-/tmp/swiftpm-module-cache} \
	./macos/Scripts/bundle-app.sh

lint:
	./scripts/lint.sh

test:
	GOCACHE=$${GOCACHE:-/tmp/go-build} GOTMPDIR=$${GOTMPDIR:-/tmp} $(GO) -C server test $(GO_TEST_LDFLAGS) -race -count=1 -v ./...

accept:
	./scripts/verify-e2e.sh

verify: lint test accept

verify-app:
	./scripts/verify-app.sh

clean:
	rm -rf dist macos/.build
