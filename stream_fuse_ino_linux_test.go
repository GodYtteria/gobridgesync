//go:build linux

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"google.golang.org/api/drive/v3"
)

func inoOf(t *testing.T, p string) uint64 {
	t.Helper()
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return info.Sys().(*syscall.Stat_t).Ino
}

func TestStreamMountKeepsTheSameInodeForAFileWhileItIsBeingEdited(t *testing.T) {
	f := newFuseEnv(t)
	file := f.fake.put("E1", "model.bin", "D", "0123456789")
	f.index.note(&drive.File{Id: "E1", Name: "model.bin", Md5Checksum: file.MD5, Size: 10, ModifiedTime: "2026-01-01T00:00:00Z"}, "model.bin", "D")
	f.refresh()
	p := filepath.Join(f.dir, "docs", "model.bin")
	f.wait(func() bool { b, err := os.ReadFile(p); return err == nil && string(b) == "0123456789" })
	before := inoOf(t, p)
	h, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	h.WriteAt([]byte("AB"), 4)
	time.Sleep(streamAttrTimeout + 500*time.Millisecond)
	during := inoOf(t, p)
	h.Close()
	f.wait(func() bool { file, _ := f.named("model.bin"); return string(file.body) == "0123AB6789" })
	time.Sleep(streamAttrTimeout + 500*time.Millisecond)
	after := inoOf(t, p)
	if during != before || after != before {
		t.Fatalf("a file must keep its inode while it is edited, before %d during %d after %d", before, during, after)
	}
}

func TestStreamMountKeepsTheSameInodeForAFolderAcrossLookups(t *testing.T) {
	f := newFuseEnv(t)
	d := filepath.Join(f.dir, "docs", "work")
	if err := os.Mkdir(d, 0o755); err != nil {
		t.Fatal(err)
	}
	before := inoOf(t, d)
	time.Sleep(streamAttrTimeout + 500*time.Millisecond)
	f.refresh()
	time.Sleep(streamAttrTimeout + 500*time.Millisecond)
	if after := inoOf(t, d); after != before {
		t.Fatalf("a folder must keep its inode (cd into it must keep working), before %d after %d", before, after)
	}
	if err := os.WriteFile(filepath.Join(d, "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if after := inoOf(t, d); after != before {
		t.Fatalf("writing inside must not change the folder inode, before %d after %d", before, after)
	}
}
