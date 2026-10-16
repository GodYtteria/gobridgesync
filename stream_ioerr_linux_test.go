//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"testing"
)

func TestIOErrnoMapsCancelToEINTRAndFailuresToEIO(t *testing.T) {
	if got := ioErrno("reading", "a.bin", 0, fmt.Errorf("load: %w", context.Canceled)); got != syscall.EINTR {
		t.Fatalf("a cancelled read must report EINTR, got %v", got)
	}
	if got := ioErrno("reading", "a.bin", 0, errors.New("drive down")); got != syscall.EIO {
		t.Fatalf("a failed read must report EIO, got %v", got)
	}
}
