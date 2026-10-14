//go:build !unix && !windows

package main

import "errors"

func removeShortcuts() []error { return nil }

func removeProgram() error { return errors.New("remove the program manually on this system") }
