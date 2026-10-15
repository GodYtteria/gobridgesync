package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"google.golang.org/api/googleapi"
)

const retryAttempts = 8

var (
	retryBase = time.Second
	retryMax  = 64 * time.Second
)

func retryable(err error) bool {
	var g *googleapi.Error
	if !errors.As(err, &g) {
		return false
	}
	if g.Code == http.StatusTooManyRequests || g.Code >= 500 {
		return true
	}
	if g.Code == http.StatusForbidden {
		for _, e := range g.Errors {
			if e.Reason == "userRateLimitExceeded" || e.Reason == "rateLimitExceeded" {
				return true
			}
		}
	}
	return false
}

func rateLimited(err error) bool {
	var g *googleapi.Error
	if !errors.As(err, &g) {
		return false
	}
	if g.Code == http.StatusTooManyRequests {
		return true
	}
	for _, e := range g.Errors {
		if e.Reason == "userRateLimitExceeded" || e.Reason == "rateLimitExceeded" {
			return true
		}
	}
	return false
}

type pacer struct {
	mu       sync.Mutex
	interval time.Duration
	tokens   float64
	last     time.Time
	min, max time.Duration
	burst    float64
}

func newPacer() *pacer {
	return &pacer{interval: 100 * time.Millisecond, min: 100 * time.Millisecond, max: 2 * time.Second, burst: 100, tokens: 100}
}

var (
	writePacer = newPacer()
	readPacer  = newPacer()
)

func (p *pacer) wait(ctx context.Context) error {
	p.mu.Lock()
	now := time.Now()
	if !p.last.IsZero() {
		p.tokens = min(p.burst, p.tokens+float64(now.Sub(p.last))/float64(p.interval))
	}
	p.last = now
	p.tokens--
	var d time.Duration
	if p.tokens < 0 {
		d = time.Duration(-p.tokens * float64(p.interval))
	}
	p.mu.Unlock()
	if d > 0 {
		select {
		case <-ctx.Done():
			p.mu.Lock()
			p.tokens++
			p.mu.Unlock()
			return ctx.Err()
		case <-time.After(d):
		}
	}
	return nil
}

func (p *pacer) result(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case rateLimited(err):
		p.interval = min(p.interval*2, p.max)
		p.tokens = min(p.tokens, 0)
	case err == nil:
		p.interval = max(p.interval*9/10, p.min)
	}
}

func retryWith(ctx context.Context, p *pacer, fn func() error) error {
	var err error
	for attempt := range retryAttempts {
		if werr := p.wait(ctx); werr != nil {
			return werr
		}
		err = fn()
		p.result(err)
		if err == nil || !retryable(err) {
			return err
		}
		wait := min(retryBase<<attempt, retryMax) + time.Duration(rand.Int64N(int64(retryBase)))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	if rateLimited(err) {
		return fmt.Errorf("%w (Google is limiting this account; GoBridgeSync keeps trying, and each account can upload up to 750 GB a day)", err)
	}
	return err
}

func retry(ctx context.Context, fn func() error) error { return retryWith(ctx, writePacer, fn) }

func retryRead(ctx context.Context, fn func() error) error { return retryWith(ctx, readPacer, fn) }
