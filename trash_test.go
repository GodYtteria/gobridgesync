package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneTrashRemovesOnlyOldEntries(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.Local)
	mk := func(name string) string {
		d := filepath.Join(dir, name, "sub")
		os.MkdirAll(d, 0o700)
		os.WriteFile(filepath.Join(d, "f.txt"), []byte("x"), 0o600)
		return filepath.Join(dir, name)
	}
	old := mk(now.Add(-31 * 24 * time.Hour).Format(trashStamp))
	recent := mk(now.Add(-29 * 24 * time.Hour).Format(trashStamp))
	odd := mk("not-a-timestamp")
	past := now.Add(-40 * 24 * time.Hour)
	os.Chtimes(odd, past, past)
	pruneTrash(dir, now)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("old trash kept")
	}
	if _, err := os.Stat(recent); err != nil {
		t.Fatal("recent trash removed")
	}
	if _, err := os.Stat(odd); !os.IsNotExist(err) {
		t.Fatal("old entry with unparsable name kept")
	}
	pruneTrash(filepath.Join(dir, "missing"), now)
}
