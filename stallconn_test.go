package main

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"
)

func TestStallConnTimesOut(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			defer c.Close()
			time.Sleep(time.Second)
		}
	}()
	dial := stallDial((&net.Dialer{}).DialContext, 50*time.Millisecond, 50*time.Millisecond)
	c, err := dial(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	start := time.Now()
	_, err = c.Read(make([]byte, 1))
	if !errors.Is(err, os.ErrDeadlineExceeded) || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("stalled read not cut off: %v after %v", err, time.Since(start))
	}
}
