package main

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
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

func retry(ctx context.Context, fn func() error) error {
	var err error
	for attempt := range retryAttempts {
		if err = fn(); err == nil || !retryable(err) {
			return err
		}
		wait := min(retryBase<<attempt, retryMax) + time.Duration(rand.Int64N(int64(retryBase)))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	return err
}
