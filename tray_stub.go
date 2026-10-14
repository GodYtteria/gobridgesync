//go:build !(linux || windows || (darwin && cgo))

package main

import (
	"context"
	"errors"
)

func runTray(ctx context.Context, quit context.CancelFunc, r *Runner, url string) error {
	return errors.New("tray icon is not available in this build")
}
