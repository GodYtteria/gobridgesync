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
	_, err := editConfig(p, func(c *AppConfig) error {
		on := c.startAtLogin()
		if err := setAutostart(on); err != nil {
			return err
		}
		if c.Startup == nil {
			c.Startup = &on
		}
		return nil
	})
	if err != nil {
		log.Printf("start at login: %v", err)
	}
}
