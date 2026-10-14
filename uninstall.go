package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

func uninstall(p Paths, purge bool) error {
	lf, err := lock(p.StateDir)
	if err != nil {
		return errors.New("GoBridgeSync is running: choose Quit in the tray menu first")
	}
	lf.Close()
	var errs []error
	if err := setAutostart(false); err != nil {
		errs = append(errs, fmt.Errorf("start at login: %w", err))
	}
	errs = append(errs, removeShortcuts()...)
	if purge {
		errs = append(errs, deleteToken(p))
		for _, dir := range []string{p.ConfigDir, p.StateDir} {
			if err := os.RemoveAll(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
		}
	}
	errs = append(errs, removeProgram())
	if err := errors.Join(errs...); err != nil {
		return err
	}
	fmt.Println("GoBridgeSync is uninstalled.")
	if purge {
		fmt.Println("Sign-in and settings were removed.")
	} else {
		fmt.Println("Sign-in and settings were kept. Run with -purge to remove them.")
	}
	fmt.Printf("Your files in %s and in Google Drive were not touched.\n", p.Root)
	return nil
}
