//go:build !linux

package main

import (
	"context"
	"errors"
)

var errStreamUnsupported = errors.New("streaming is not available on this operating system yet")

func defaultStreamDir() string { return "" }

func runStream(ctx context.Context, paths Paths, dir string, limitGB int, ready func()) error {
	return errStreamUnsupported
}
