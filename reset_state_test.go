package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResetAccountStateRemovesJournalsAndIndexLog(t *testing.T) {
	p := Paths{ConfigDir: t.TempDir(), StateDir: t.TempDir()}
	files := []string{p.syncIDs(), p.backupIDs(), p.index() + ".log", p.index()}
	for _, f := range files {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	keep := filepath.Join(p.StateDir, "keep.txt")
	os.WriteFile(keep, []byte("x"), 0o600)
	if err := resetAccountState(p); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Fatalf("%s survived the reset", filepath.Base(f))
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("unrelated file removed")
	}
}
