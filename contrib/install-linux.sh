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
places=$data/user-places.xbel
href=$(printf 'file://%s/%s' "$HOME" "$folder" | sed 's/%/%25/g; s/ /%20/g; s/&/\&amp;/g; s/</\&lt;/g; s/>/\&gt;/g; s/"/\&quot;/g')
if [ -f "$places" ] && ! grep -qF "<ID>$app</ID>" "$places" && ! grep -qF "href=\"file://$HOME/$folder\"" "$places" && ! grep -qF "href=\"$href\"" "$places"; then
	GBS_PLACE=$(printf ' <bookmark href="%s">\n  <title>%s</title>\n  <info>\n   <metadata owner="http://freedesktop.org">\n    <bookmark:icon name="folder-cloud"/>\n   </metadata>\n   <metadata owner="http://www.kde.org">\n    <ID>%s</ID>\n    <isSystemItem>false</isSystemItem>\n   </metadata>\n  </info>\n </bookmark>' "$href" "$label" "$app") \
		awk '/<\/xbel>/ && !done { print ENVIRON["GBS_PLACE"]; done = 1 } { print }' "$places" >"$places.tmp" && cat "$places.tmp" >"$places"
	rm -f "$places.tmp"
fi
command -v fusermount3 >/dev/null 2>&1 || echo "Note: install fuse3 (for example: sudo dnf install fuse3, or sudo apt install fuse3) to use the streaming folder."
gtk-update-icon-cache -q "$data/icons/hicolor" 2>/dev/null || true
pkill -u "$(id -u)" -f "$bin ui" && sleep 2 || true
nohup "$bin" ui -no-browser -tray >/dev/null 2>&1 &
echo "$name installed. Open it from the tray, the app menu, or http://127.0.0.1:$port/"
