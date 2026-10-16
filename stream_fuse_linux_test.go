//go:build linux

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

type fuseEnv struct {
	t       *testing.T
	fake    *wfake
	dir     string
	index   *driveIndex
	refresh func()
}

func newFuseEnv(t *testing.T) *fuseEnv {
	if _, err := exec.LookPath("fusermount3"); err != nil {
		t.Skip("fusermount3 is not installed")
	}
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("no /dev/fuse")
	}
	e := &wenv{}
	_ = e
	fake := &wfake{files: map[string]*wfile{}}
	srvHTTP := newWFakeServer(t, fake)
	srv, _ := drive.NewService(context.Background(), option.WithHTTPClient(srvHTTP.Client()), option.WithEndpoint(srvHTTP.URL+"/"))
	cache, _ := testCache(t, 1<<30)
	index := &driveIndex{RootID: "root", Files: map[string]*drive.File{"D": {Id: "D", Name: "docs", Parents: []string{"root"}, MimeType: folderMime}}}
	var snap atomic.Pointer[Remote]
	refresh := func() {
		r, err := index.snapshot()
		if err != nil {
			t.Fatal(err)
		}
		snap.Store(r.clone())
	}
	refresh()
	writer, err := newStreamWriter(srv, t.TempDir(), cache, snap.Load, index, nil, refresh)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	writer.Start(ctx, 1)
	f := &fuseEnv{t: t, fake: fake, dir: t.TempDir(), index: index, refresh: refresh}
	server, err := mountStream(f.dir, &streamMount{snapshot: snap.Load, srv: srv, cache: cache, writer: writer, uid: uint32(os.Getuid()), gid: uint32(os.Getgid())})
	if err != nil {
		cancel()
		t.Skipf("cannot mount here: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		server.Unmount()
	})
	return f
}

func (f *fuseEnv) wait(cond func() bool) {
	deadline := time.Now().Add(8 * time.Second)
	for !cond() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !cond() {
		f.t.Fatal("timed out")
	}
}

func (f *fuseEnv) named(name string) (wfile, bool) {
	f.fake.mu.Lock()
	defer f.fake.mu.Unlock()
	for _, file := range f.fake.files {
		if file.Name == name && !file.Trashed {
			return *file, true
		}
	}
	return wfile{}, false
}

func TestStreamMountReadsWritesRenamesAndDeletes(t *testing.T) {
	f := newFuseEnv(t)
	docs := filepath.Join(f.dir, "docs")

	a := filepath.Join(docs, "a.txt")
	if err := os.WriteFile(a, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(a); err != nil || string(b) != "hello" {
		t.Fatalf("read right after write: %q %v", b, err)
	}
	h, err := os.OpenFile(a, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	h.WriteString(" more")
	h.Close()
	if b, err := os.ReadFile(a); err != nil || string(b) != "hello more" {
		t.Fatalf("read after append: %q %v", b, err)
	}
	f.wait(func() bool { file, ok := f.named("a.txt"); return ok && string(file.body) == "hello more" })

	if err := os.Rename(a, filepath.Join(docs, "b.txt")); err != nil {
		t.Fatal(err)
	}
	f.wait(func() bool { _, old := f.named("a.txt"); _, nu := f.named("b.txt"); return !old && nu })
	f.wait(func() bool { _, err := os.Stat(filepath.Join(docs, "b.txt")); return err == nil })
	if b, err := os.ReadFile(filepath.Join(docs, "b.txt")); err != nil || string(b) != "hello more" {
		t.Fatalf("read after rename: %q %v", b, err)
	}

	sub := filepath.Join(docs, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sub); err == nil {
		t.Fatal("removing a folder that has a file must fail")
	}
	f.wait(func() bool { _, ok := f.named("x.txt"); return ok })
	if err := os.Remove(filepath.Join(sub, "x.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sub); err != nil {
		t.Fatal(err)
	}
	f.wait(func() bool { _, ok := f.named("x.txt"); _, dir := f.named("sub"); return !ok && !dir })

	if err := os.Remove(filepath.Join(docs, "b.txt")); err != nil {
		t.Fatal(err)
	}
	f.wait(func() bool { _, ok := f.named("b.txt"); return !ok })
	if entries, _ := os.ReadDir(docs); len(entries) != 0 {
		t.Fatalf("the folder should be empty now: %v", entries)
	}
}

func TestStreamMountStreamsAnExistingFileAndEditsItInPlace(t *testing.T) {
	f := newFuseEnv(t)
	file := f.fake.put("E1", "model.bin", "D", "0123456789")
	f.index.note(&drive.File{Id: "E1", Name: "model.bin", Md5Checksum: file.MD5, Size: 10, ModifiedTime: "2026-01-01T00:00:00Z"}, "model.bin", "D")
	f.refresh()
	p := filepath.Join(f.dir, "docs", "model.bin")
	f.wait(func() bool { b, err := os.ReadFile(p); return err == nil && string(b) == "0123456789" })

	h, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	h.WriteAt([]byte("AB"), 4)
	h.Close()
	f.wait(func() bool { file, _ := f.named("model.bin"); return string(file.body) == "0123AB6789" })
	f.fake.mu.Lock()
	n := len(f.fake.files)
	f.fake.mu.Unlock()
	if n != 1 {
		t.Fatalf("an edit must not create another Drive file: %d", n)
	}
}

func TestStreamMountDropsKernelCacheWhenContentChangesButSizeAndTimeDoNot(t *testing.T) {
	f := newFuseEnv(t)
	file := f.fake.put("E1", "same.bin", "D", "AAAAAAAAAA")
	f.index.note(&drive.File{Id: "E1", Name: "same.bin", Md5Checksum: file.MD5, Size: 10, ModifiedTime: "2026-01-01T00:00:00Z"}, "same.bin", "D")
	f.refresh()
	p := filepath.Join(f.dir, "docs", "same.bin")
	f.wait(func() bool { b, err := os.ReadFile(p); return err == nil && string(b) == "AAAAAAAAAA" })
	if b, err := os.ReadFile(p); err != nil || string(b) != "AAAAAAAAAA" {
		t.Fatalf("setup: %q %v", b, err)
	}

	changed := f.fake.put("E1", "same.bin", "D", "BBBBBBBBBB")
	f.index.note(&drive.File{Id: "E1", Name: "same.bin", Md5Checksum: changed.MD5, Size: 10, ModifiedTime: "2026-01-01T00:00:00Z"}, "same.bin", "D")
	f.refresh()
	f.wait(func() bool { b, err := os.ReadFile(p); return err == nil && string(b) == "BBBBBBBBBB" })
}
