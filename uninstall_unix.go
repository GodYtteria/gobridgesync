//go:build unix && !darwin

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func dataHome() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share")
}

func removeShortcuts() []error {
	var errs []error
	data := dataHome()
	files := []string{filepath.Join(data, "applications", appID()+".desktop"), filepath.Join(data, "icons", "hicolor", "scalable", "apps", "gobridgesync.svg")}
	if !otherEditionInstalled(data) {
		for _, s := range []string{"16", "24", "32", "48", "64", "128", "256", "512"} {
			files = append(files, filepath.Join(data, "icons", "hicolor", s+"x"+s, "apps", "gobridgesync.png"))
		}
	}
	for _, f := range files {
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	exec.Command("gtk-update-icon-cache", "-q", filepath.Join(data, "icons", "hicolor")).Run()
	if cfg, err := os.UserConfigDir(); err == nil {
		errs = append(errs, removeBookmark(filepath.Join(cfg, "gtk-3.0", "bookmarks")))
	}
	return errs
}

func removeBookmark(file string) error {
	b, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var keep []string
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if !strings.HasSuffix(line, " "+filepath.Base(defaultPathsRoot())) {
			keep = append(keep, line)
		}
	}
	return writeFileAtomic(file, []byte(strings.Join(keep, "\n")+"\n"), 0o644)
}

func removeProgram() error {
	exe, err := executable()
	if err != nil {
		return err
	}
	if err := os.Remove(exe); err != nil {
		return fmt.Errorf("remove %s: %w", exe, err)
	}
	return nil
}

func otherEditionInstalled(data string) bool {
	other := "gobridgesync"
	if appID() == other {
		other = "gobridgesync-bukluran"
	}
	_, err := os.Stat(filepath.Join(data, "applications", other+".desktop"))
	return err == nil
}

func defaultPathsRoot() string {
	p, err := defaultPaths()
	if err != nil {
		return "GoBridgeSync"
	}
	return p.Root
}
