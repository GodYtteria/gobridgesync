package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeleteLocalRemovesFoldersEmptiedByIt(t *testing.T) {
	root := t.TempDir()
	st, _ := loadState(filepath.Join(t.TempDir(), "s.json"))
	s := &Syncer{paths: Paths{Root: root, StateDir: t.TempDir()}, st: st, remote: &Remote{Folders: map[string]string{"": "root", "keep": "K"}}}
	os.MkdirAll(filepath.Join(root, "gone", "deep"), 0o755)
	os.WriteFile(filepath.Join(root, "gone", "deep", "a.txt"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(root, "keep"), 0o755)
	os.WriteFile(filepath.Join(root, "keep", "b.txt"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(root, "busy"), 0o755)
	os.WriteFile(filepath.Join(root, "busy", "c.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, "busy", "mine.txt"), []byte("x"), 0o644)
	for _, p := range []string{"gone/deep/a.txt", "keep/b.txt", "busy/c.txt"} {
		if err := s.deleteLocal(p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "gone")); !os.IsNotExist(err) {
		t.Fatal("folders left empty by a Drive delete must be removed")
	}
	if _, err := os.Stat(filepath.Join(root, "keep")); err != nil {
		t.Fatal("a folder that still exists on Drive must stay")
	}
	if _, err := os.Stat(filepath.Join(root, "busy", "mine.txt")); err != nil {
		t.Fatal("a folder with other files must stay")
	}
}
