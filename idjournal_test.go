package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestIDJournalSurvivesRestart(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sync-ids")
	j := loadIDJournal(p)
	if err := j.put("file:a.txt", "id1"); err != nil {
		t.Fatal(err)
	}
	j.put("folder:d", "id2")
	again := loadIDJournal(p)
	if id, ok := again.get("file:a.txt"); !ok || id != "id1" {
		t.Fatalf("lost after restart: %q %v", id, ok)
	}
	r := &Remote{journal: again}
	if id, err := r.newID(context.TODO(), nil, "folder:d"); err != nil || id != "id2" {
		t.Fatalf("newID ignored the journal: %q %v", id, err)
	}
	if err := again.prune(func(k string) bool { return k == "file:a.txt" }); err != nil {
		t.Fatal(err)
	}
	reloaded := loadIDJournal(p)
	if _, ok := reloaded.get("file:a.txt"); ok {
		t.Fatal("resolved key kept")
	}
	if id, ok := reloaded.get("folder:d"); !ok || id != "id2" {
		t.Fatal("unresolved key lost by prune")
	}
	reloaded.put("file:b.txt", "id3")
	if err := reloaded.drop("folder:d"); err != nil {
		t.Fatal(err)
	}
	if id, ok := loadIDJournal(p).get("file:b.txt"); !ok || id != "id3" {
		t.Fatal("put after load not persisted")
	}
	reloaded.prune(func(string) bool { return true })
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("empty journal file not removed")
	}
	var nilJournal *idJournal
	if _, ok := nilJournal.get("x"); ok || nilJournal.put("x", "y") != nil || nilJournal.drop("x") != nil {
		t.Fatal("nil journal must be a no-op")
	}
}
