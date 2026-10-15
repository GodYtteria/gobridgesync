//go:build windows

package main

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func removeShortcuts() []error {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return nil
	}
	link := filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", baseName()+".lnk")
	if err := os.Remove(link); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return []error{err}
	}
	return nil
}

func removeProgram() error {
	exe, err := executable()
	if err != nil {
		return err
	}
	dir := filepath.Dir(exe)
	cmd := exec.Command("cmd", "/c", "timeout /t 3 /nobreak >nul & rmdir /s /q \""+dir+"\"")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Start()
}
