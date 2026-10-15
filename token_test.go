package main

import (
	"errors"
	"testing"
	"time"
)

func TestKeyringTimeout(t *testing.T) {
	old := keyringTimeout
	keyringTimeout = 20 * time.Millisecond
	defer func() { keyringTimeout = old }()
	block := make(chan struct{})
	defer close(block)
	start := time.Now()
	_, err := withTimeout(func() (string, error) { <-block; return "", nil })
	if !errors.Is(err, errKeyringTimeout) || time.Since(start) > time.Second {
		t.Fatalf("hung keyring not bounded: %v", err)
	}
	v, err := withTimeout(func() (string, error) { return "ok", nil })
	if v != "ok" || err != nil {
		t.Fatal(v, err)
	}
}
