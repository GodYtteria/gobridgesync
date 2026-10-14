//go:build !windows

package main

import (
	"os"
	"syscall"
)

func relaunch() error {
	exe, err := executable()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, os.Args, os.Environ())
}
