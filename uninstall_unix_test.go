//go:build unix && !darwin

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveBookmark(t *testing.T) {
	f := filepath.Join(t.TempDir(), "bookmarks")
	os.WriteFile(f, []byte("file:///a A\nfile:///home/x/GoogleDrive Google Drive\nfile:///home/x/GoogleDrive2 X\nfile:///b B\n"), 0o644)
	if err := removeBookmark(f, "/home/x/GoogleDrive"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(f); string(b) != "file:///a A\nfile:///home/x/GoogleDrive2 X\nfile:///b B\n" {
		t.Fatalf("got %q", b)
	}
	if err := removeBookmark(filepath.Join(t.TempDir(), "missing"), "/x"); err != nil {
		t.Fatal("missing bookmarks file should be fine")
	}
}
