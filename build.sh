#!/bin/sh
set -eu
cd "$(dirname "$0")"
version=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
targets=${*:-linux/amd64 linux/arm64 windows/amd64 windows/arm64 darwin/amd64 darwin/arm64 freebsd/amd64}
rm -rf dist
mkdir -p dist
for target in $targets; do
	os=${target%/*}
	arch=${target#*/}
	name=gobridgesync-$os-$arch
	stage=dist/$name
	rm -rf "$stage"
	mkdir -p "$stage"
	ext=""
	flags="-s -w -X main.version=$version"
	if [ "$os" = windows ]; then
		ext=".exe"
		flags="$flags -H windowsgui"
	fi
	cgo=0
	[ "$os" = darwin ] && [ "$(uname -s)" = Darwin ] && cgo=1
	CGO_ENABLED=$cgo GOOS=$os GOARCH=$arch go build -trimpath -ldflags="$flags" -o "$stage/gobridgesync$ext" .
	case $os in
	linux | freebsd)
		mkdir -p "$stage/assets/icons" "$stage/contrib"
		cp assets/icons/*.png "$stage/assets/icons/"
		cp contrib/install-linux.sh contrib/gobridgesync.desktop "$stage/contrib/"
		tar -czf "dist/$name.tar.gz" -C "$stage" .
		;;
	darwin)
		tar -czf "dist/$name.tar.gz" -C "$stage" .
		;;
	windows)
		cp assets/icons/gobridgesync.ico "$stage/"
		(cd "$stage" && zip -qr "../$name.zip" .)
		;;
	esac
	rm -rf "$stage"
done
(cd dist && sha256sum *.tar.gz *.zip 2>/dev/null >checksums.txt || true)
ls -l dist
