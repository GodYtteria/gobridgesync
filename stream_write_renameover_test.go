package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestStreamWriteRenameOverAnUploadingFileMakesNoConflictCopy(t *testing.T) {
	e := newWEnv(t)
	e.addRemote("docs/index", "I1", "v0")
	e.fake.mu.Lock()
	e.fake.delay = 400 * time.Millisecond
	e.fake.mu.Unlock()
	edit, err := e.w.OpenWrite(context.Background(), "docs/index", true)
	if err != nil {
		t.Fatal(err)
	}
	edit.WriteAt([]byte("v1"), 0)
	e.w.Release(edit, true)
	e.wait(func() bool {
		edit.mu.Lock()
		defer edit.mu.Unlock()
		return edit.running
	})
	lock, err := e.w.Create("docs/index.lock")
	if err != nil {
		t.Fatal(err)
	}
	lock.WriteAt([]byte("v2"), 0)
	e.w.Release(lock, true)
	if err := e.w.Rename(context.Background(), "docs/index.lock", "docs/index"); err != nil {
		t.Fatal(err)
	}
	e.wait(func() bool { return string(e.file("I1").body) == "v2" && e.w.Pending() == 0 })
	e.fake.mu.Lock()
	defer e.fake.mu.Unlock()
	for _, f := range e.fake.files {
		if !f.Trashed && (strings.Contains(f.Name, "(conflict") || strings.HasSuffix(f.Name, ".lock")) {
			t.Fatalf("git-style lock and rename over a file still uploading must not leave %q", f.Name)
		}
	}
}
