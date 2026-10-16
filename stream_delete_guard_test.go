package main

import (
	"context"
	"testing"
	"time"
)

func TestDeleteGuardRefusesPastTheCapAndRecoversAfterTheWindow(t *testing.T) {
	var g deleteGuard
	now := time.Now()
	for i := 0; i < streamDeleteMax; i++ {
		if !g.allow(now) {
			t.Fatalf("delete %d must be allowed", i)
		}
	}
	if g.allow(now) {
		t.Fatal("the delete past the cap must be refused")
	}
	if !g.allow(now.Add(streamDeleteWindow + time.Second)) {
		t.Fatal("deletes must be allowed again once the window has passed")
	}
}

func TestStreamRemoveIsRefusedPastTheDeleteCap(t *testing.T) {
	e := newWEnv(t)
	e.addRemote("docs/a.txt", "A1", "x")
	now := time.Now()
	for i := 0; i < streamDeleteMax; i++ {
		e.w.deletes.allow(now)
	}
	if err := e.w.Remove(context.Background(), "docs/a.txt"); err != errStreamTooManyDeletes {
		t.Fatalf("remove must be refused, got %v", err)
	}
	if e.file("A1").Trashed {
		t.Fatal("a refused delete must leave the file on Drive")
	}
}

func TestStreamFailedTrashDoesNotUseUpTheDeleteBudget(t *testing.T) {
	e := newWEnv(t)
	e.addRemote("docs/a.txt", "A1", "x")
	e.fake.mu.Lock()
	e.fake.failPatch = true
	e.fake.mu.Unlock()
	if err := e.w.Remove(context.Background(), "docs/a.txt"); err == nil {
		t.Fatal("remove must fail when Drive refuses")
	}
	e.w.deletes.mu.Lock()
	used := len(e.w.deletes.times)
	e.w.deletes.mu.Unlock()
	if used != 0 {
		t.Fatalf("a failed delete must not count toward the cap, used=%d", used)
	}
}

func exhaustDeleteGuard(e *wenv) {
	now := time.Now()
	for i := 0; i < streamDeleteMax; i++ {
		e.w.deletes.allow(now)
	}
}

func TestStreamRenameOverIsRefusedToTrashPastTheDeleteCap(t *testing.T) {
	e := newWEnv(t)
	e.addRemote("docs/a.txt", "A1", "one")
	e.addRemote("docs/b.txt", "B1", "two")
	exhaustDeleteGuard(e)
	if err := e.w.Rename(context.Background(), "docs/a.txt", "docs/b.txt"); err != nil {
		t.Fatal(err)
	}
	if e.file("B1").Trashed {
		t.Fatal("a rename over a file must not trash it past the delete cap")
	}
}

func TestStreamRenamePendingEditOverIsRefusedToTrashPastTheDeleteCap(t *testing.T) {
	e := newWEnv(t)
	e.addRemote("docs/a.txt", "A1", "one")
	e.addRemote("docs/b.txt", "B1", "two")
	sf, err := e.w.OpenWrite(context.Background(), "docs/a.txt", false)
	if err != nil {
		t.Fatal(err)
	}
	exhaustDeleteGuard(e)
	if err := e.w.Rename(context.Background(), "docs/a.txt", "docs/b.txt"); err != nil {
		t.Fatal(err)
	}
	e.w.Release(sf, true)
	if e.file("B1").Trashed {
		t.Fatal("a pending rename over a file must not trash it past the delete cap")
	}
}
