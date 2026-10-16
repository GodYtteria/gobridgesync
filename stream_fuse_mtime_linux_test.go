//go:build linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestStreamMountChtimesAfterSaveReachesDrive(t *testing.T) {
	f := newFuseEnv(t)
	a := filepath.Join(f.dir, "docs", "t.txt")
	if err := os.WriteFile(a, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.wait(func() bool { file, ok := f.named("t.txt"); return ok && string(file.body) == "hi" })
	time.Sleep(500 * time.Millisecond)
	want := time.Date(2022, 2, 2, 2, 2, 2, 0, time.UTC)
	if err := os.Chtimes(a, want, want); err != nil {
		t.Fatal(err)
	}
	f.wait(func() bool {
		file, ok := f.named("t.txt")
		got, err := time.Parse(time.RFC3339Nano, file.MTime)
		return ok && err == nil && got.Equal(want)
	})
	again := time.Date(2021, 1, 1, 1, 1, 1, 0, time.UTC)
	if out, err := exec.Command("touch", "-d", again.Format(time.RFC3339), a).CombinedOutput(); err != nil {
		t.Fatalf("touch: %v %s", err, out)
	}
	f.wait(func() bool {
		file, ok := f.named("t.txt")
		got, err := time.Parse(time.RFC3339Nano, file.MTime)
		return ok && err == nil && got.Equal(again)
	})
}
