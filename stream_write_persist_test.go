package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStreamWriteConflictCopyIsAdoptedAfterRestart(t *testing.T) {
	e := newWEnv(t)
	e.addRemote("docs/a.txt", "A1", "original")
	oldSum := e.file("A1").MD5
	e.fake.mu.Lock()
	e.fake.files["A1"].body = []byte("theirs")
	e.fake.files["A1"].MD5 = md5Hex("theirs")
	e.fake.mu.Unlock()
	conflict := "a (conflict 20200101-000000).txt"
	e.fake.mu.Lock()
	e.fake.put("CID", conflict, "D", "mine")
	e.fake.mu.Unlock()
	writeStage(t, e.dir, "123-1", "mine", map[string]any{"path": "docs/a.txt", "baseId": "A1", "baseMd5": oldSum, "conflict": conflict})
	jp := filepath.Join(t.TempDir(), "ids")
	if err := os.WriteFile(jp, []byte("stream:123-1\tCID\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	again := restartWriter(t, e, loadIDJournal(jp))
	e.wait(func() bool { return again.Pending() == 0 })
	if e.count() != 2 || string(e.file("A1").body) != "theirs" || string(e.file("CID").body) != "mine" {
		t.Fatalf("the conflict copy created before the crash must be reused, got %d files", e.count())
	}
}

func TestStreamWritePendingRenameOverIsTrashedAfterRestart(t *testing.T) {
	e := newWEnv(t)
	e.addRemote("docs/b.txt", "B1", "old target")
	writeStage(t, e.dir, "123-1", "fresh", map[string]any{"path": "docs/b.txt", "isNew": true, "replaceId": "B1", "replaceMd5": e.file("B1").MD5})
	again := restartWriter(t, e, nil)
	e.wait(func() bool { return again.Pending() == 0 })
	if !e.file("B1").Trashed {
		t.Fatal("the file replaced by a rename must be moved to the trash after a restart")
	}
	if live := e.remoteByName("b.txt"); live == nil || string(live.body) != "fresh" {
		t.Fatalf("expected the new b.txt to be live, got %+v", live)
	}
}

func TestStreamWriteRecoveredOpenStageKeepsTheRenameTarget(t *testing.T) {
	e := newWEnv(t)
	e.addRemote("docs/b.txt", "B1", "old target")
	writeStage(t, e.dir, "123-2", "half", map[string]any{"path": "docs/b.txt", "isNew": true, "open": true, "replaceId": "B1", "replaceMd5": e.file("B1").MD5})
	again := restartWriter(t, e, nil)
	e.wait(func() bool { return again.Pending() == 0 })
	if e.file("B1").Trashed {
		t.Fatal("a file still open at the crash is saved as a recovered copy; the rename target must stay")
	}
}
