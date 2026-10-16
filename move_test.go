//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func crossDeviceRename(string, string) error {
	return &os.LinkError{Op: "rename", Err: syscall.EXDEV}
}

func TestMoveFileFallsBackAcrossDevices(t *testing.T) {
	old := renameFile
	renameFile = crossDeviceRename
	t.Cleanup(func() { renameFile = old })
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")
	os.WriteFile(src, []byte("payload"), 0o644)
	past := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	os.Chtimes(src, past, past)
	if err := moveFile(src, dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "payload" {
		t.Fatalf("copy wrong: %q", b)
	}
	if info, _ := os.Stat(dst); !info.ModTime().Equal(past) {
		t.Fatalf("mtime not preserved: %v", info.ModTime())
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("source not removed")
	}
}

func TestMoveFileKeepsSourceWhenCopyFails(t *testing.T) {
	old := renameFile
	renameFile = crossDeviceRename
	t.Cleanup(func() { renameFile = old })
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")
	os.WriteFile(src, []byte("payload"), 0o644)
	os.WriteFile(dst, []byte("already here"), 0o644)
	if err := moveFile(src, dst); err == nil {
		t.Fatal("overwrote an existing destination")
	}
	if b, _ := os.ReadFile(src); string(b) != "payload" {
		t.Fatal("source lost")
	}
}

func TestMoveFilePassesOtherErrorsThrough(t *testing.T) {
	old := renameFile
	renameFile = func(string, string) error { return os.ErrPermission }
	t.Cleanup(func() { renameFile = old })
	if err := moveFile("a", "b"); err != os.ErrPermission {
		t.Fatalf("got %v", err)
	}
}

func TestDeleteLocalAcrossDevices(t *testing.T) {
	old := renameFile
	renameFile = func(src, dst string) error {
		if filepath.Dir(dst) != filepath.Dir(src) && filepath.Base(src) == "gone.txt" {
			return &os.LinkError{Op: "rename", Err: syscall.EXDEV}
		}
		return old(src, dst)
	}
	t.Cleanup(func() { renameFile = old })
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "gone.txt"), []byte("bye"), 0o644)
	st, _ := loadState(filepath.Join(t.TempDir(), "s.json"))
	st.Files["gone.txt"] = Entry{ID: "i"}
	s := &Syncer{paths: Paths{Root: root, StateDir: t.TempDir()}, st: st}
	if err := s.deleteLocal("gone.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "gone.txt")); !os.IsNotExist(err) {
		t.Fatal("file not removed from the sync folder")
	}
	if _, ok := st.Files["gone.txt"]; ok {
		t.Fatal("state not cleared")
	}
	var found string
	filepath.WalkDir(s.paths.trash(), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			found = p
		}
		return nil
	})
	if b, _ := os.ReadFile(found); string(b) != "bye" {
		t.Fatalf("trash copy missing: %q", found)
	}
}
