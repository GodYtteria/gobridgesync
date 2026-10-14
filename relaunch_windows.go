//go:build windows

package main

import (
	"os"
	"os/exec"
)

func relaunch() error {
	exe, err := executable()
	if err != nil {
		return err
	}
	return exec.Command(exe, os.Args[1:]...).Start()
}
