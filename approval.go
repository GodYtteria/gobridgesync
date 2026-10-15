package main

import (
	"fmt"
	"time"
)

func (r *Runner) AllowDeletes() {
	r.mu.Lock()
	n := r.status.PendingDeletes
	r.allowed, r.allowAt = n, time.Now()
	r.status.PendingDeletes = 0
	r.mu.Unlock()
	if n > 0 {
		r.addRecent(fmt.Sprintf("deleting %d files you allowed", n))
	}
	r.Trigger()
}

func (r *Runner) allowDeletes(n int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	ok := r.allowed > 0 && n <= r.allowed+r.allowed/10
	r.allowed = 0
	return ok
}
