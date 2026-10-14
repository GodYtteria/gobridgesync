package main

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"
)

func TestLimiterSlowsReads(t *testing.T) {
	l := newLimiter(100 << 10)
	data := bytes.Repeat([]byte("x"), 300<<10)
	start := time.Now()
	n, err := io.Copy(io.Discard, limitReader(context.Background(), bytes.NewReader(data), l))
	elapsed := time.Since(start)
	if err != nil || n != int64(len(data)) {
		t.Fatalf("copy %d %v", n, err)
	}
	if elapsed < 1500*time.Millisecond || elapsed > 4*time.Second {
		t.Fatalf("300 KB at 100 KB/s took %v", elapsed)
	}
	if newLimiter(0) != nil {
		t.Fatal("zero rate should mean unlimited")
	}
	if r := limitReader(context.Background(), bytes.NewReader(data), nil); r == nil {
		t.Fatal("nil limiter should pass through")
	}
}

func TestLimiterCancel(t *testing.T) {
	l := newLimiter(1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	l.wait(ctx, 1)
	if err := l.wait(ctx, 1000); err == nil {
		t.Fatal("expected cancel error")
	}
}
