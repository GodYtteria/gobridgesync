//go:build !unix && !windows

package main

import "errors"

func freeSpace(dir string) (int64, error) { return 0, errors.New("unsupported") }
