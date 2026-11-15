GO ?= go
GO_LDFLAGS := -linkmode=external -s -w

.PHONY: all server app test install-runtime clean

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

install-runtime: server app
	mkdir -p "$$HOME/.local/share/sub-cost-view" "$$HOME/Library/Application Support/SubCostView"
	chmod 700 "$$HOME/Library/Application Support/SubCostView"
	cp -f dist/subbar-server "$$HOME/.local/share/sub-cost-view/subbar-server"
	chmod 755 "$$HOME/.local/share/sub-cost-view/subbar-server"
	rm -rf "$$HOME/Applications/SubBar.app"
	mkdir -p "$$HOME/Applications"
	cp -R dist/SubBar.app "$$HOME/Applications/SubBar.app"

clean:
	rm -rf dist macos/.build
