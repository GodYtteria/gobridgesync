#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
data=${XDG_DATA_HOME:-$HOME/.local/share}
config=${XDG_CONFIG_HOME:-$HOME/.config}

app=gobridgesync
[ -f gobridgesync-bukluran ] && [ ! -f go.mod ] && app=gobridgesync-bukluran
[ -n "${GBS_APP:-}" ] && app=$GBS_APP
if [ "$app" = gobridgesync-bukluran ]; then
	name="GoBridgeSync Bukluran" folder=GoogleDrive label="Google Drive" port=47812
else
	name=GoBridgeSync folder=GoBridgeSync label=GoBridgeSync port=47811
fi

if [ -f go.mod ] && [ "$app" = gobridgesync ]; then
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o gobridgesync .
fi
bin="$HOME/.local/bin/$app"
install -Dm755 "$app" "$bin"
for s in 16 24 32 48 64 128 256 512; do
	install -Dm644 "assets/icons/gobridgesync-$s.png" "$data/icons/hicolor/${s}x${s}/apps/gobridgesync.png"
done
mkdir -p "$data/applications"
sed -e "s|^Exec=gobridgesync|Exec=$bin|" -e "s|^Name=GoBridgeSync$|Name=$name|" contrib/gobridgesync.desktop >"$data/applications/$app.desktop"
mkdir -p "$HOME/$folder"
bookmarks=$config/gtk-3.0/bookmarks
if [ -f "$bookmarks" ] && ! grep -q "file://$HOME/$folder " "$bookmarks"; then
	echo "file://$HOME/$folder $label" >>"$bookmarks"
fi
gtk-update-icon-cache -q "$data/icons/hicolor" 2>/dev/null || true
proc=$(printf %.15s "$app")
pkill -u "$(id -u)" -x "$proc" && sleep 2 || true
nohup "$bin" ui -no-browser -tray >/dev/null 2>&1 &
echo "$name installed. Open it from the tray, the app menu, or http://127.0.0.1:$port/"
