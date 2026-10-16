package main

import (
	"context"
	"testing"
	"time"
)

func TestStreamWriteSetMTimeOnASavedFileUpdatesDrive(t *testing.T) {
	e := newWEnv(t)
	e.addRemote("docs/a.txt", "A1", "x")
	want := time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)
	if err := e.w.SetMTime(context.Background(), "docs/a.txt", want.UnixNano()); err != nil {
		t.Fatal(err)
	}
	got, err := time.Parse(time.RFC3339Nano, e.file("A1").MTime)
	if err != nil || !got.Equal(want) {
		t.Fatalf("Drive must get the new modified time, got %q", e.file("A1").MTime)
	}
}

func TestStreamWriteMTimeSetBeforeUploadIsSent(t *testing.T) {
	e := newWEnv(t)
	sf, _ := e.w.Create("docs/new.txt")
	sf.WriteAt([]byte("data"), 0)
	want := time.Date(2023, 1, 2, 3, 4, 5, 0, time.UTC)
	sf.setMTime(want.UnixNano())
	e.w.Release(sf, true)
	e.wait(func() bool { return e.remoteByName("new.txt") != nil && e.w.Pending() == 0 })
	got, err := time.Parse(time.RFC3339Nano, e.remoteByName("new.txt").MTime)
	if err != nil || !got.Equal(want) {
		t.Fatalf("a modified time set before saving (cp -p, rsync -t) must reach Drive, got %q", e.remoteByName("new.txt").MTime)
	}
}

func TestStreamWriteSameContentRewriteWithExplicitMTimeReachesDrive(t *testing.T) {
	e := newWEnv(t)
	e.addRemote("docs/a.txt", "A1", "x")
	want := time.Date(2023, 1, 2, 3, 4, 5, 0, time.UTC)
	sf, err := e.w.OpenWrite(context.Background(), "docs/a.txt", true)
	if err != nil {
		t.Fatal(err)
	}
	sf.WriteAt([]byte("x"), 0)
	sf.setMTime(want.UnixNano())
	e.w.Release(sf, true)
	e.wait(func() bool {
		got, err := time.Parse(time.RFC3339Nano, e.file("A1").MTime)
		return err == nil && got.Equal(want) && e.w.Pending() == 0
	})
	e.index.mu.Lock()
	known := e.index.Files["A1"]
	e.index.mu.Unlock()
	if known == nil {
		t.Fatal("the index must learn the new modified time")
	}
}
