# GoTable build. The web UI (frontend/) is built into internal/webui/dist and
# embedded in the binary.
#
#   make build          native window (Wails). Linux needs gcc and
#                       webkit2gtk4.1-devel (Fedora) / libwebkit2gtk-4.1-dev
#                       (Debian, Ubuntu).
#   make build-browser  CGO-free binary that shows the UI in a Chrome/Chromium
#                       app window instead.

.PHONY: build build-browser build-windows package-linux package-windows web test e2e dev clean

WAILS_TAGS = desktop,production,webkit2_41

build: web
	CGO_ENABLED=1 go build -tags $(WAILS_TAGS) -ldflags "-s -w" -o bin/gotable ./cmd/gotable

build-browser: web
	CGO_ENABLED=0 go build -tags browser -o bin/gotable ./cmd/gotable

# Release files in dist/ (see scripts/package.sh); VERSION=v1.2.3 optional.
package-linux: web
	scripts/package.sh linux $(VERSION)

package-windows: web
	scripts/package.sh windows $(VERSION)

# Windows (WebView2, built into Windows 10/11); cross-compiles from Linux too.
build-windows: web
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -tags desktop,production -ldflags "-s -w -H windowsgui" -o bin/gotable.exe ./cmd/gotable

web:
	cd frontend && npm ci && npm run build

test:
	go test ./...
	cd frontend && npx vitest run

# Database integration tests against PostgreSQL, MySQL and MariaDB containers.
test-dbs:
	scripts/test-dbs.sh ./...

# Browser end-to-end tests against the built binary (needs Chrome).
e2e: build
	cd frontend && npx playwright test

clean:
	rm -rf bin build dist frontend/node_modules
