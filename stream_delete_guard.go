package main

import (
	"log"
	"sync"
	"time"
)

const (
	streamDeleteMax    = massDeleteMax
	streamDeleteWindow = 10 * time.Minute
)

type deleteGuard struct {
	mu     sync.Mutex
	times  []time.Time
	warned time.Time
}

func (g *deleteGuard) refund() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if n := len(g.times); n > 0 {
		g.times = g.times[:n-1]
	}
}

func (g *deleteGuard) allow(now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	keep := g.times[:0]
	for _, t := range g.times {
		if now.Sub(t) < streamDeleteWindow {
			keep = append(keep, t)
		}
	}
	g.times = keep
	if len(g.times) >= streamDeleteMax {
		if now.Sub(g.warned) > time.Minute {
			g.warned = now
			log.Printf("streaming folder: %d files were deleted in the last %s; refusing more until the window passes (restore from the Google Drive trash if this was a mistake)", len(g.times), streamDeleteWindow)
		}
		return false
	}
	g.times = append(g.times, now)
	return true
}
