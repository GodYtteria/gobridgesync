//go:build !linux

package main

var memAvailable = func() int64 { return 0 }
