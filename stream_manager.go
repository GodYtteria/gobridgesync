package main

import (
	"context"
	"log"
	"runtime"
	"time"
)

const streamRetry = time.Minute

func (r *Runner) StreamLoop(ctx context.Context) {
	var cancel context.CancelFunc
	var done chan struct{}
	var gb int
	var retryAt time.Time
	stop := func() {
		if cancel != nil {
			cancel()
			<-done
			cancel, done = nil, nil
		}
	}
	defer stop()
	for ctx.Err() == nil {
		cfg, err := loadAppConfig(r.paths)
		if err != nil {
			if !sleepCtx(ctx, 3*time.Second) {
				return
			}
			continue
		}
		want := cfg.Stream && runtime.GOOS == "linux"
		if cancel != nil && (!want || cfg.streamGB() != gb) {
			r.setStream("stopping")
			stop()
			r.setStream("")
		}
		if cancel != nil {
			select {
			case <-done:
				cancel()
				cancel, done = nil, nil
				retryAt = time.Now().Add(streamRetry)
			default:
			}
		}
		switch {
		case want && cancel == nil && time.Now().After(retryAt):
			gb = cfg.streamGB()
			sctx, c := context.WithCancel(ctx)
			ch := make(chan struct{})
			cancel, done = c, ch
			r.setStream("starting")
			go func() {
				defer close(ch)
				err := runStream(sctx, r.paths, defaultStreamDir(), gb, func() { r.setStream("on") })
				switch {
				case sctx.Err() != nil:
				case err != nil:
					log.Printf("streaming folder: %v", err)
					r.setStream("error: " + err.Error())
				default:
					r.setStream("unmounted, retrying")
				}
			}()
		case !want && cfg.Stream && runtime.GOOS != "linux":
			r.setStream("not available on this operating system yet")
		}
		if !sleepCtx(ctx, 3*time.Second) {
			return
		}
	}
}

func (r *Runner) setStream(state string) {
	r.set(func(s *Status) { s.StreamState = state })
}
