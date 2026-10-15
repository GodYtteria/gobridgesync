package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatchTriggersAfterQuietSave(t *testing.T) {
	p := Paths{ConfigDir: t.TempDir(), StateDir: t.TempDir(), Root: t.TempDir()}
	folder := t.TempDir()
	os.MkdirAll(filepath.Join(folder, "proj", ".git"), 0o755)
	cfg := AppConfig{Folders: []string{folder}, Every: instantSync, Exclude: []string{".git"}, Version: configVersion}
	if err := cfg.save(p); err != nil {
		t.Fatal(err)
	}
	r := newRunner(p, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.WatchLoop(ctx)
	time.Sleep(500 * time.Millisecond)

	os.WriteFile(filepath.Join(folder, "proj", ".git", "index"), []byte("x"), 0o644)
	select {
	case <-r.changed:
		t.Fatal("excluded change triggered a sync")
	case <-time.After(debounce + time.Second):
	}

	os.MkdirAll(filepath.Join(folder, "proj", "src"), 0o755)
	time.Sleep(300 * time.Millisecond)
	os.WriteFile(filepath.Join(folder, "proj", "src", "main.go"), []byte("package main"), 0o644)
	select {
	case <-r.changed:
	case <-time.After(debounce + 3*time.Second):
		t.Fatal("saving a file did not trigger a sync")
	}
}

func TestDebounceDelayIsCapped(t *testing.T) {
	first := time.Now()
	if d := debounceDelay(first, first); d != debounce {
		t.Fatalf("first event: %v", d)
	}
	if d := debounceDelay(first, first.Add(debounceMax-time.Second)); d != time.Second {
		t.Fatalf("near cap: %v", d)
	}
	if d := debounceDelay(first, first.Add(2*debounceMax)); d != 0 {
		t.Fatalf("past cap: %v", d)
	}
}
