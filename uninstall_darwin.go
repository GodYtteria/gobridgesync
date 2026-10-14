//go:build darwin

package main

import (
	"fmt"
	"os"
)

func removeShortcuts() []error { return nil }

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
