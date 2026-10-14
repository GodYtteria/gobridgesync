package main

import (
	"context"
	"io"
	"sync"
	"time"
)

type limiter struct {
	mu     sync.Mutex
	rate   float64
	tokens float64
	last   time.Time
}

func newLimiter(bytesPerSecond int64) *limiter {
	if bytesPerSecond <= 0 {
		return nil
	}
	return &limiter{rate: float64(bytesPerSecond), tokens: float64(bytesPerSecond), last: time.Now()}
}

func (l *limiter) wait(ctx context.Context, n int) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	now := time.Now()
	l.tokens = min(l.rate, l.tokens+now.Sub(l.last).Seconds()*l.rate)
	l.last = now
	l.tokens -= float64(n)
	var delay time.Duration
	if l.tokens < 0 {
		delay = time.Duration(-l.tokens / l.rate * float64(time.Second))
	}
	l.mu.Unlock()
	if delay == 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(delay):
		return nil
	}
}

type limitedReader struct {
	ctx context.Context
	r   io.Reader
	l   *limiter
}

func (lr *limitedReader) Read(p []byte) (int, error) {
	if lr.l != nil && len(p) > 64<<10 {
		p = p[:64<<10]
	}
	n, err := lr.r.Read(p)
	if n > 0 {
		if werr := lr.l.wait(lr.ctx, n); werr != nil {
			return n, werr
		}
	}
	return n, err
}

func limitReader(ctx context.Context, r io.Reader, l *limiter) io.Reader {
	if l == nil {
		return r
	}
	return &limitedReader{ctx: ctx, r: r, l: l}
}
