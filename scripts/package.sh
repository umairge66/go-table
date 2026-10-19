#!/usr/bin/env bash
# Builds release files into dist/. Build the web UI first (make web).
#
#   scripts/package.sh linux   [VERSION]   .tar.gz, .deb, .rpm and .AppImage
#   scripts/package.sh windows [VERSION]   gotable.exe, and a .zip with it
#
# VERSION is a tag such as v1.2.0; without one, a dev version is used.
# Linux needs gcc and WebKitGTK 4.1 headers (see README). nfpm, go-winres
# and appimagetool are fetched on first use.
set -euo pipefail
cd "$(dirname "$0")/.."

target=${1:?usage: scripts/package.sh linux|windows [VERSION]}
version=${2:-}
if [[ $version =~ ^v?[0-9]+\.[0-9]+\.[0-9]+ ]]; then
	pkgver=${version#v}
else
	version="dev-$(git rev-parse --short HEAD 2>/dev/null || echo local)"
	pkgver="0.0.0~${version//-/.}" # sorts before any real release
fi
ldflags="-s -w -X main.version=$version"

NFPM=github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.48.0
WINRES=github.com/tc-hib/go-winres@v0.3.3
APPIMAGETOOL_URL=https://github.com/AppImage/appimagetool/releases/download/continuous/appimagetool-x86_64.AppImage

if [[ ! -f internal/webui/dist/index.html ]]; then
	echo "The web UI isn't built: run make web first." >&2
	exit 1
fi
mkdir -p dist build

case $target in
linux)
	mkdir -p build/linux
	CGO_ENABLED=1 go build -trimpath -tags desktop,production,webkit2_41 -ldflags "$ldflags" -o build/linux/gotable ./cmd/gotable

	# Plain archive.
	stage=build/linux/gotable-$pkgver
	rm -rf "$stage" && mkdir -p "$stage"
	cp build/linux/gotable packaging/gotable.desktop packaging/gotable.png LICENSE README.md "$stage/"
	tar -C build/linux -czf "dist/gotable-$pkgver-linux-amd64.tar.gz" "gotable-$pkgver"

	# Debian/Ubuntu and Fedora packages.
	for p in deb rpm; do
		VERSION=$pkgver go run "$NFPM" package --config packaging/nfpm.yaml --packager "$p" --target dist/
	done

	# AppImage: the binary with its desktop entry and icon. It uses the
	# system's WebKitGTK 4.1 (bundling WebKit is not reliable).
	appdir=build/linux/GoTable.AppDir
	rm -rf "$appdir" && mkdir -p "$appdir/usr/bin"
	cp build/linux/gotable "$appdir/usr/bin/"
	cp packaging/gotable.desktop packaging/gotable.png "$appdir/"
	cat >"$appdir/AppRun" <<-'EOF'
		#!/bin/sh
		exec "$(dirname "$(readlink -f "$0")")/usr/bin/gotable" "$@"
	EOF
	chmod +x "$appdir/AppRun"
	tool=build/appimagetool
	[[ -x $tool ]] || { curl -fsSL -o "$tool" "$APPIMAGETOOL_URL" && chmod +x "$tool"; }
	ARCH=x86_64 APPIMAGE_EXTRACT_AND_RUN=1 "$tool" --no-appstream "$appdir" "dist/GoTable-$pkgver-x86_64.AppImage"
	;;
windows)
	mkdir -p build/windows
	# Icon and manifest (DPI awareness) as a Windows resource next to main.
	go run "$WINRES" simply --icon packaging/gotable.png --manifest gui --product-name GoTable \
		--original-filename gotable.exe --out cmd/gotable/rsrc
	trap 'rm -f cmd/gotable/rsrc_windows_*.syso' EXIT
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -tags desktop,production \
		-ldflags "$ldflags -H windowsgui" -o build/windows/gotable.exe ./cmd/gotable
	cp LICENSE README.md build/windows/
	(cd build/windows && zip -q -r "../../dist/gotable-$pkgver-windows-amd64.zip" gotable.exe LICENSE README.md)
	# The bare .exe too, for people who just want to run it.
	cp build/windows/gotable.exe "dist/gotable-$pkgver-windows-amd64.exe"
	;;
*)
	echo "unknown target $target (linux or windows)" >&2
	exit 1
	;;
esac
ls -l dist/
