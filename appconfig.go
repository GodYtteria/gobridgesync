package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type AppConfig struct {
	Folders []string `json:"folders"`
	TwoWay  bool     `json:"twoWay"`
	Paused  bool     `json:"paused"`
	Limit   int      `json:"storageLimit"`
	Every   int      `json:"intervalMinutes"`
	Startup *bool    `json:"startAtLogin,omitempty"`
	Updates *bool    `json:"autoUpdate,omitempty"`
	Exclude []string `json:"exclude"`
	Speed   int      `json:"speedLimitKB"`
	Version int      `json:"configVersion"`
}

const configVersion = 1

var formerBuiltInSkips = []string{".git", "node_modules", ".cache", "__pycache__", ".venv", ".Trash"}

func (c AppConfig) migrate() (AppConfig, bool) {
	if c.Version >= configVersion {
		return c, false
	}
	if len(c.Folders) > 0 {
		seen := map[string]bool{}
		for _, e := range c.Exclude {
			seen[e] = true
		}
		for _, s := range formerBuiltInSkips {
			if !seen[s] {
				c.Exclude = append(c.Exclude, s)
			}
		}
	}
	c.Version = configVersion
	return c, true
}

var speedLimits = map[int]bool{0: true, 512: true, 1024: true, 5120: true, 10240: true}

func (c AppConfig) speed() int {
	if speedLimits[c.Speed] {
		return c.Speed
	}
	return 0
}

func (c AppConfig) limiter() *limiter { return newLimiter(int64(c.speed()) << 10) }

const (
	manualSync          = -1
	instantSync         = -2
	defaultEveryMinutes = 5
)

var syncIntervals = map[int]bool{instantSync: true, manualSync: true, 1: true, 5: true, 15: true, 60: true}

func (c AppConfig) every() int {
	if syncIntervals[c.Every] {
		return c.Every
	}
	return defaultEveryMinutes
}

func (c AppConfig) startAtLogin() bool { return c.Startup == nil || *c.Startup }

func (c AppConfig) autoUpdate() bool { return c.Updates == nil || *c.Updates }

const defaultStorageLimit = 90

var storageLimits = map[int]bool{50: true, 75: true, 90: true, 100: true}

func (c AppConfig) limit() int {
	if storageLimits[c.Limit] {
		return c.Limit
	}
	return defaultStorageLimit
}

func (p Paths) appConfig() string   { return filepath.Join(p.ConfigDir, "config.json") }
func (p Paths) backupState() string { return filepath.Join(p.StateDir, "backup-state.json") }

func loadAppConfig(p Paths) (AppConfig, error) {
	b, err := os.ReadFile(p.appConfig())
	if errors.Is(err, fs.ErrNotExist) {
		return defaultAppConfig(), nil
	}
	if err != nil {
		return AppConfig{}, err
	}
	var c AppConfig
	if err := json.Unmarshal(b, &c); err != nil {
		return AppConfig{}, err
	}
	migrated, _ := c.migrate()
	return migrated, nil
}

func defaultAppConfig() AppConfig {
	return AppConfig{Folders: []string{}, TwoWay: true, Version: configVersion}
}

var suggestedFolders = []string{"Documents", "Pictures", "Desktop", "Videos", "Music", "Downloads"}

func folderSuggestions(home string, chosen []string, syncRoot string) []string {
	out := []string{}
	for _, name := range suggestedFolders {
		dir := filepath.Join(home, name)
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() || within(dir, syncRoot) || within(syncRoot, dir) {
			continue
		}
		overlaps := false
		for _, c := range chosen {
			if within(dir, c) || within(c, dir) {
				overlaps = true
				break
			}
		}
		if !overlaps {
			out = append(out, dir)
		}
	}
	return out
}

func (c AppConfig) encode() ([]byte, error) { return json.MarshalIndent(c, "", "  ") }

func (c AppConfig) save(p Paths) error {
	b, err := c.encode()
	if err != nil {
		return err
	}
	return writeFileAtomic(p.appConfig(), b, 0o600)
}

func expandHome(dir string) (string, error) {
	if dir == "~" || strings.HasPrefix(dir, "~/") || strings.HasPrefix(dir, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, dir[1:]), nil
	}
	return dir, nil
}

func within(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

var configMu sync.Mutex

func editConfig(p Paths, f func(*AppConfig) error) (AppConfig, error) {
	configMu.Lock()
	defer configMu.Unlock()
	cfg, err := loadAppConfig(p)
	if err != nil {
		return cfg, err
	}
	if err := f(&cfg); err != nil {
		return cfg, err
	}
	next, err := cfg.encode()
	if err != nil {
		return cfg, err
	}
	if cur, err := os.ReadFile(p.appConfig()); err == nil && bytes.Equal(cur, next) {
		return cfg, nil
	}
	return cfg, writeFileAtomic(p.appConfig(), next, 0o600)
}

func normalizeFolders(in []string, syncRoot string, known []string) ([]string, error) {
	existing := map[string]bool{}
	for _, k := range known {
		existing[filepath.Clean(k)] = true
	}
	out := []string{}
	for _, raw := range in {
		dir, err := expandHome(strings.TrimSpace(raw))
		if err != nil {
			return nil, err
		}
		if !filepath.IsAbs(dir) {
			return nil, fmt.Errorf("%q: use a full path like ~/Documents", raw)
		}
		dir = filepath.Clean(dir)
		if !existing[dir] {
			info, err := os.Stat(dir)
			if err != nil || !info.IsDir() {
				return nil, fmt.Errorf("%q: folder not found", raw)
			}
		}
		if within(dir, syncRoot) || within(syncRoot, dir) {
			return nil, fmt.Errorf("%q: overlaps the two-way sync folder %s", raw, syncRoot)
		}
		for _, o := range out {
			if within(dir, o) || within(o, dir) {
				return nil, fmt.Errorf("%q overlaps %q", dir, o)
			}
		}
		out = append(out, dir)
	}
	return out, nil
}
