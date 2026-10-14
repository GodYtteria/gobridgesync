//go:build !unix && !windows

package main

import "errors"

func setAutostart(on bool) error {
	if on {
		return errors.New("start at login is not supported on this system")
	}
	return nil
}
