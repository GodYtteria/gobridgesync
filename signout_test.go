package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSignOutIsAnsweredDuringThePauseBetweenPasses(t *testing.T) {
	r := newRunner(Paths{ConfigDir: t.TempDir(), StateDir: t.TempDir(), Root: t.TempDir()}, false)
	go r.waitGap(context.Background(), time.Hour, &passState{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.SignOut(ctx); errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("sign out must not wait for the pause between passes to end")
	}
}

func TestSignOutStopsRunningPass(t *testing.T) {
	r := newRunner(Paths{ConfigDir: t.TempDir(), StateDir: t.TempDir(), Root: t.TempDir()}, false)
	stopped := make(chan struct{})
	r.passStop = func() { close(stopped) }
	go func() {
		<-stopped
		done := <-r.signout
		done <- nil
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.SignOut(ctx); err != nil {
		t.Fatalf("sign out waited for the whole pass: %v", err)
	}
}
