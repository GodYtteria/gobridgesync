//go:build unix && !darwin

package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func autostartFile() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "autostart", "gobridgesync.desktop"), nil
}

func desktopQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\\\`, `"`, `\\"`, "`", "\\\\`", "$", `\\$`)
	return `"` + r.Replace(s) + `"`
}

func setAutostart(on bool) error {
	file, err := autostartFile()
	if err != nil {
		return err
	}
	if !on {
		if err := os.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	exe, err := executable()
	if err != nil {
		return err
	}
	entry := "[Desktop Entry]\nType=Application\nName=GoBridgeSync\nComment=Back up and sync with Google Drive\n" +
		"Exec=" + desktopQuote(exe) + " ui -no-browser -tray\nIcon=gobridgesync\nTerminal=false\nNoDisplay=true\nX-GNOME-Autostart-enabled=true\n"
	return writeFileAtomic(file, []byte(entry), 0o644)
}
