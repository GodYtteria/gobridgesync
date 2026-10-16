package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSessionStoreSlowWriteDoesNotLoseNewerSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	s := loadSessions(path)
	var calls int
	var mu sync.Mutex
	s.write = func(p string, b []byte, perm os.FileMode) error {
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		if first {
			time.Sleep(150 * time.Millisecond)
		}
		return writeFileAtomic(p, b, perm)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.put("first", uploadSession{URI: "u", Created: time.Now()})
	}()
	time.Sleep(30 * time.Millisecond)
	s.put("second", uploadSession{URI: "u", Created: time.Now()})
	wg.Wait()
	got := loadSessions(path).m
	if _, ok := got["first"]; !ok {
		t.Fatalf("first session missing: %v", got)
	}
	if _, ok := got["second"]; !ok {
		t.Fatalf("an older snapshot overwrote the newer one: %v", got)
	}
}

func TestSessionStoreConcurrentPutsAllPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	s := loadSessions(path)
	var wg sync.WaitGroup
	const n = 100
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.put(fmt.Sprintf("k%d", i), uploadSession{URI: "u", Created: time.Now()})
		}()
	}
	wg.Wait()
	if got := len(loadSessions(path).m); got != n {
		t.Fatalf("file holds %d of %d sessions", got, n)
	}
}
