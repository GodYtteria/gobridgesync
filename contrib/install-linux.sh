#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
data=${XDG_DATA_HOME:-$HOME/.local/share}
config=${XDG_CONFIG_HOME:-$HOME/.config}

if [ -f go.mod ]; then
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o gobridgesync .
fi
install -Dm755 gobridgesync "$HOME/.local/bin/gobridgesync"
rm -f "$data/icons/hicolor/scalable/apps/gobridgesync.svg"
for s in 16 24 32 48 64 128 256 512; do
	install -Dm644 "assets/icons/gobridgesync-$s.png" "$data/icons/hicolor/${s}x${s}/apps/gobridgesync.png"
done
bin="$HOME/.local/bin/gobridgesync"
mkdir -p "$data/applications"
sed "s|^Exec=gobridgesync|Exec=$bin|" contrib/gobridgesync.desktop >"$data/applications/gobridgesync.desktop"
mkdir -p "$HOME/GoBridgeSync"
bookmarks=${XDG_CONFIG_HOME:-$HOME/.config}/gtk-3.0/bookmarks
if [ -f "$bookmarks" ] && ! grep -q "file://$HOME/GoBridgeSync " "$bookmarks"; then
	echo "file://$HOME/GoBridgeSync GoBridgeSync" >> "$bookmarks"
fi
gtk-update-icon-cache -q "$data/icons/hicolor" 2>/dev/null || true
pkill -u "$(id -u)" -x gobridgesync && sleep 2 || true
nohup "$HOME/.local/bin/gobridgesync" ui -no-browser -tray >/dev/null 2>&1 &
echo "GoBridgeSync installed. Open it from the tray, the app menu, or http://127.0.0.1:47811/"
