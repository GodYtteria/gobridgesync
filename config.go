package main

import (
	"os"
	"path/filepath"
	"runtime"
)

type Paths struct {
	ConfigDir string
	StateDir  string
	Root      string
}

func defaultPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		return Paths{}, err
	}
	data := filepath.Join(cfg, appID(), "state")
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		data = os.Getenv("XDG_DATA_HOME")
		if data == "" {
			data = filepath.Join(home, ".local", "share")
		}
		data = filepath.Join(data, appID())
	}
	return Paths{
		ConfigDir: filepath.Join(cfg, appID()),
		StateDir:  data,
		Root:      filepath.Join(home, syncFolderName()),
	}, nil
}

func (p Paths) credentials() string { return filepath.Join(p.ConfigDir, "credentials.json") }
func (p Paths) token() string       { return filepath.Join(p.ConfigDir, "token.json") }
func (p Paths) scope() string       { return filepath.Join(p.ConfigDir, "scope") }
func (p Paths) state() string {
	if internalEdition() {
		return filepath.Join(p.StateDir, "state.json")
	}
	return filepath.Join(p.StateDir, "folder-sync-state.json")
}
func (p Paths) trash() string { return filepath.Join(p.StateDir, "trash") }
