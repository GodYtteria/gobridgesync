package main

import (
	"log"
	"os"
	"path/filepath"
)

func executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}

func ensureAutostart(p Paths) {
	cfg, err := loadAppConfig(p)
	if err != nil {
		log.Printf("read settings: %v", err)
		return
	}
	on := cfg.startAtLogin()
	if err := setAutostart(on); err != nil {
		log.Printf("start at login: %v", err)
		return
	}
	if cfg.Startup == nil {
		cfg.Startup = &on
		if err := cfg.save(p); err != nil {
			log.Printf("save settings: %v", err)
		}
	}
}
