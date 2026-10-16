package main

import (
	"context"
	"testing"
	"time"
)

func TestStreamWriteRefusesNamesHiddenFromTheMount(t *testing.T) {
	e := newWEnv(t)
	e.rmu.Lock()
	e.r.Ambiguous = map[string]bool{foldKey("docs/dup.txt"): true}
	e.r.Docs = map[string]RemoteDoc{"docs/Report.gdoc": {ID: "G1"}}
	e.rmu.Unlock()
	before := e.count()
	if _, err := e.w.Create("docs/dup.txt"); err != errStreamExists {
		t.Fatalf("creating a name Drive already has twice must be refused, got %v", err)
	}
	if _, err := e.w.Create("docs/Report.gdoc"); err != errStreamExists {
		t.Fatalf("creating over a Google Docs name must be refused, got %v", err)
	}
	if err := e.w.Mkdir(context.Background(), "docs/dup.txt"); err != errStreamExists {
		t.Fatalf("mkdir over a hidden name must be refused, got %v", err)
	}
	e.addRemote("docs/a.txt", "A1", "x")
	if err := e.w.Rename(context.Background(), "docs/a.txt", "docs/Report.gdoc"); err != errStreamExists {
		t.Fatalf("renaming onto a Google Docs name must be refused, got %v", err)
	}
	if e.count() != before+1 {
		t.Fatalf("no new Drive item expected, before %d after %d", before, e.count())
	}
}

func TestStreamWriteRenameOverOurOwnPendingFileWhileDriveShowsItTwice(t *testing.T) {
	e := newWEnv(t)
	streamUploadDelay = time.Hour
	first, _ := e.w.Create("docs/index")
	first.WriteAt([]byte("v1"), 0)
	e.w.Release(first, true)
	e.rmu.Lock()
	e.r.Ambiguous = map[string]bool{foldKey("docs/index"): true}
	e.rmu.Unlock()
	lock, _ := e.w.Create("docs/index.lock")
	lock.WriteAt([]byte("v2"), 0)
	e.w.Release(lock, true)
	if err := e.w.Rename(context.Background(), "docs/index.lock", "docs/index"); err != nil {
		t.Fatalf("a transient duplicate made by our own replace must not block the next git-style rename: %v", err)
	}
}
