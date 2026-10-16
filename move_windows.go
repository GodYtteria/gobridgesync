//go:build windows

package main

import (
	"errors"
	"syscall"
)

const errorNotSameDevice = syscall.Errno(17)

func isCrossDevice(err error) bool { return errors.Is(err, errorNotSameDevice) }
