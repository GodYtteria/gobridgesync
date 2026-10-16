//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func unreadableTree(t *testing.T) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permissions are not enforced for root")
	}
	root := t.TempDir()
	for _, rel := range []string{"ok.txt", "locked/secret.txt", "fine/inner.txt"} {
		full := filepath.Join(root, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte("x"), 0o644)
	}
	locked := filepath.Join(root, "locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	return root
}

func TestScanSkipsUnreadableFolder(t *testing.T) {
	root := unreadableTree(t)
	files, skipped, err := scanLocalSkipping(root, nil)
	if err != nil {
		t.Fatalf("one unreadable folder aborted the scan: %v", err)
	}
	if _, ok := files["ok.txt"]; !ok {
		t.Fatal("readable file missing")
	}
	if _, ok := files["fine/inner.txt"]; !ok {
		t.Fatal("readable folder missing")
	}
	if !slices.Equal(skipped, []string{"locked"}) {
		t.Fatalf("skipped=%v", skipped)
	}
	if !underAny(skipped, "locked/secret.txt") || underAny(skipped, "lockedx/a") || underAny(skipped, "ok.txt") {
		t.Fatal("skipped subtree not recognised by path")
	}
}

func TestLocalCacheReportsUnreadableFolder(t *testing.T) {
	root := unreadableTree(t)
	c := newLocalCache()
	c.setWatching(true)
	now := time.Now()
	_, skipped, err := c.scan(root, nil, nil, now)
	if err != nil || !slices.Equal(skipped, []string{"locked"}) {
		t.Fatalf("full scan: %v %v", skipped, err)
	}
	c.changed(filepath.Join(root, "ok.txt"))
	_, skipped, err = c.scan(root, nil, nil, now.Add(time.Second))
	if err != nil || !slices.Equal(skipped, []string{"locked"}) {
		t.Fatalf("incremental scan lost the skipped folder: %v %v", skipped, err)
	}
	os.Chmod(filepath.Join(root, "locked"), 0o755)
	c.changed(filepath.Join(root, "locked"))
	files, skipped, err := c.scan(root, nil, nil, now.Add(2*time.Second))
	if err != nil || len(skipped) != 0 {
		t.Fatalf("after fixing permissions: %v %v", skipped, err)
	}
	if _, ok := files["locked/secret.txt"]; !ok {
		t.Fatal("folder not rescanned once readable")
	}
}
