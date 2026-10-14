//go:build darwin

package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

func setAutostart(on bool) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	file := filepath.Join(home, "Library", "LaunchAgents", "com."+appID()+".plist")
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
	var esc bytes.Buffer
	if err := xml.EscapeText(&esc, []byte(exe)); err != nil {
		return err
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.` + appID() + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + esc.String() + `</string>
		<string>ui</string>
		<string>-no-browser</string>
		<string>-tray</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
</dict>
</plist>
`
	return writeFileAtomic(file, []byte(plist), 0o644)
}
