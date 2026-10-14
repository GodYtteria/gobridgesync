#!/bin/sh
set -eu

repo=${GBS_REPO:-GodYtteria/gobridgesync}
base=${GBS_BASE_URL:-https://github.com/$repo/releases/latest/download}

fail() {
	echo "GoBridgeSync install: $*" >&2
	exit 1
}

case $(uname -s) in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) fail "unsupported system $(uname -s); on Windows use install.ps1" ;;
esac
case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) fail "unsupported CPU $(uname -m)" ;;
esac

if command -v curl >/dev/null 2>&1; then
	fetch() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
	fetch() { wget -q "$1" -O "$2"; }
else
	fail "curl or wget is required"
fi
if command -v sha256sum >/dev/null 2>&1; then
	sha() { sha256sum "$1" | cut -d' ' -f1; }
else
	sha() { shasum -a 256 "$1" | cut -d' ' -f1; }
fi

app=gobridgesync
[ "${GBS_EDITION:-}" = bukluran ] && app=gobridgesync-bukluran
name=$app-$os-$arch.tar.gz
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading $name"
fetch "$base/$name" "$tmp/$name" || fail "download failed: $base/$name"
fetch "$base/checksums.txt" "$tmp/checksums.txt" || fail "download failed: $base/checksums.txt"
want=$(awk -v n="$name" '$2 == n || $2 == "*" n { print $1 }' "$tmp/checksums.txt")
[ -n "$want" ] || fail "no checksum for $name"
[ "$(sha "$tmp/$name")" = "$want" ] || fail "checksum mismatch for $name"

mkdir "$tmp/pkg"
tar -xzf "$tmp/$name" -C "$tmp/pkg"

if [ "$os" = linux ]; then
	sh "$tmp/pkg/contrib/install-linux.sh"
	exit 0
fi

bin=$HOME/.local/bin/$app
mkdir -p "$HOME/.local/bin"
pkill -f "$bin ui" 2>/dev/null && sleep 2 || true
install -m 755 "$tmp/pkg/$app" "$bin"
xattr -d com.apple.quarantine "$bin" 2>/dev/null || true
nohup "$bin" ui -tray >/dev/null 2>&1 &
echo "Installed. Open it from the menu bar or the address it shows."
