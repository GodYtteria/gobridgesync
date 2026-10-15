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

func TestRemovePlace(t *testing.T) {
	f := filepath.Join(t.TempDir(), "user-places.xbel")
	keep := " <bookmark href=\"file:///home/x/Music\">\n  <title>Music</title>\n </bookmark>\n"
	gbs := " <bookmark href=\"file:///home/x/GoogleDrive\">\n  <title>Google Drive</title>\n </bookmark>\n"
	os.WriteFile(f, []byte("<xbel>\n"+keep+gbs+"</xbel>\n"), 0o644)
	if err := removePlace(f, "gobridgesync-bukluran", "/home/x/GoogleDrive"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(f); string(b) != "<xbel>\n"+keep+"</xbel>\n" {
		t.Fatalf("got %q", b)
	}
	if err := removePlace(f, "gobridgesync-bukluran", "/home/x/GoogleDrive"); err != nil {
		t.Fatal("second removal should be a no-op")
	}
}

func TestRemovePlaceAfterDolphinRewrite(t *testing.T) {
	f := filepath.Join(t.TempDir(), "user-places.xbel")
	keep := " <bookmark href=\"file:///home/x/Music\">\n  <info><metadata><ID>1700000000/0</ID></metadata></info>\n </bookmark>\n"
	byID := " <bookmark href=\"file:///home/J%C3%B6rg%20X/GoogleDrive\">\n  <info><metadata><ID>gobridgesync-bukluran</ID></metadata></info>\n </bookmark>\n"
	public := " <bookmark href=\"file:///home/x/GoBridgeSync\">\n  <info><metadata><ID>gobridgesync</ID></metadata></info>\n </bookmark>\n"
	os.WriteFile(f, []byte("<xbel>\n"+keep+byID+public+"</xbel>\n"), 0o644)
	if err := removePlace(f, "gobridgesync-bukluran", "/home/Jörg X/GoogleDrive"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(f); string(b) != "<xbel>\n"+keep+public+"</xbel>\n" {
		t.Fatalf("got %q", b)
	}
	os.WriteFile(f, []byte("<xbel>\n"+keep+" <bookmark href=\"file:///home/J%C3%B6rg%20X/GoogleDrive\">\n </bookmark>\n</xbel>\n"), 0o644)
	if err := removePlace(f, "gobridgesync-bukluran", "/home/Jörg X/GoogleDrive"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(f); string(b) != "<xbel>\n"+keep+"</xbel>\n" {
		t.Fatalf("encoded href without ID not removed: %q", b)
	}
}

func TestRemovePlaceEncodedAmpersand(t *testing.T) {
	f := filepath.Join(t.TempDir(), "user-places.xbel")
	os.WriteFile(f, []byte("<xbel>\n <bookmark href=\"file:///home/a&amp;b%20c/GoogleDrive\">\n </bookmark>\n</xbel>\n"), 0o644)
	if err := removePlace(f, "gobridgesync-bukluran", "/home/a&b c/GoogleDrive"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(f); string(b) != "<xbel>\n</xbel>\n" {
		t.Fatalf("got %q", b)
	}
}
