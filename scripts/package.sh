#!/usr/bin/env bash
# Builds release files into dist/. Build the web UI first (make web).
#
#   scripts/package.sh linux   [VERSION]   .tar.gz, .deb, .rpm and .AppImage
#   scripts/package.sh windows [VERSION]   gotable.exe, and a .zip with it
#
# VERSION is a tag such as v1.2.0; without one, a dev version is used.
# Linux needs gcc and WebKitGTK 4.1 headers (see README). nfpm, go-winres,
# linuxdeploy and appimagetool are fetched on first use.
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
LINUXDEPLOY_URL=https://github.com/linuxdeploy/linuxdeploy/releases/download/continuous/linuxdeploy-x86_64.AppImage
LINUXDEPLOY_GTK_URL=https://raw.githubusercontent.com/linuxdeploy/linuxdeploy-plugin-gtk/master/linuxdeploy-plugin-gtk.sh

# fetch downloads a tool once into build/.
fetch() { [[ -x $1 ]] || { curl -fsSL -o "$1" "$2" && chmod +x "$1"; }; }

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

	# AppImage, self-contained: GoTable plus WebKitGTK, GTK and their
	# libraries, so it runs where WebKitGTK isn't installed. Graphics
	# drivers and the C++ runtime still come from the system.
	appdir=build/linux/GoTable.AppDir
	rm -rf "$appdir" && mkdir -p "$appdir/usr/bin"
	cp build/linux/gotable "$appdir/usr/bin/"
	fetch build/linuxdeploy "$LINUXDEPLOY_URL"
	fetch build/linuxdeploy-plugin-gtk.sh "$LINUXDEPLOY_GTK_URL"

	# WebKit starts helper programs (WebKitWebProcess, …) from paths fixed at
	# build time, e.g. /usr/libexec/webkit2gtk-4.1. Copy those folders into
	# the AppDir at the same place under usr/.
	wk=$(pkg-config --variable=libdir webkit2gtk-4.1)/libwebkit2gtk-4.1.so.0
	# (helpers, and the injected-bundle module the web process loads)
	mapfile -t wkdirs < <(strings "$wk" | grep -E '^/usr/[^ ]*webkit2gtk-4\.1(/injected-bundle)?/?$' |
		sed -E 's|/injected-bundle/?$||; s|/$||' | sort -u)
	deps=()
	for d in "${wkdirs[@]}"; do
		mkdir -p "$appdir$(dirname "$d")"
		cp -r "$d" "$appdir$d"
		rm -f "$appdir$d/MiniBrowser" "$appdir$d/jsc" # developer tools
		deps+=(--deploy-deps-only "$appdir$d")
	done
	PATH="$PWD/build:$PATH" DEPLOY_GTK_VERSION=3 APPIMAGE_EXTRACT_AND_RUN=1 build/linuxdeploy --appdir "$appdir" \
		--executable "$appdir/usr/bin/gotable" "${deps[@]}" \
		--desktop-file packaging/gotable.desktop --icon-file packaging/gotable.png --plugin gtk

	# Libraries that must match the system's graphics drivers stay out.
	(cd "$appdir/usr/lib" && rm -f libstdc++.so* libgcc_s.so* libdrm*.so* libgbm.so* libEGL*.so* libGL*.so* \
		libGLX*.so* libGLdispatch.so* libglapi.so* libwayland-*.so*)
	# Point WebKit's fixed paths at the bundled copies: "/usr" becomes
	# "././" (same length), which AppRun resolves from inside usr/.
	sed -i 's|/usr|././|g' "$appdir/usr/lib/libwebkit2gtk-4.1.so.0"
	cat >"$appdir/AppRun" <<-'EOF'
		#!/bin/sh
		# GoTable with its bundled WebKitGTK. WebKit's helper paths are
		# relative to usr/ (see scripts/package.sh), hence the cd.
		APPDIR=${APPDIR:-$(dirname "$(readlink -f "$0")")}
		export APPDIR
		for hook in "$APPDIR"/apprun-hooks/*.sh; do [ -e "$hook" ] && . "$hook"; done
		export LD_LIBRARY_PATH="$APPDIR/usr/lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
		cd "$APPDIR/usr" || exit 1
		exec "$APPDIR/usr/bin/gotable" "$@"
	EOF
	chmod +x "$appdir/AppRun"
	fetch build/appimagetool "$APPIMAGETOOL_URL"
	ARCH=x86_64 APPIMAGE_EXTRACT_AND_RUN=1 build/appimagetool --no-appstream "$appdir" "dist/GoTable-$pkgver-x86_64.AppImage"
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
