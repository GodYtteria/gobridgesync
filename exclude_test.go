package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeExcludes(t *testing.T) {
	home, _ := os.UserHomeDir()
	got, err := normalizeExcludes([]string{" *.tmp ", "node_modules", "~/Documents/SD-Backup/", "proj/build/", "*.tmp", ""})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"*.tmp", "node_modules", filepath.Join(home, "Documents", "SD-Backup"), "proj/build"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	for _, bad := range []string{"[", "..", "../x"} {
		if _, err := normalizeExcludes([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestExcluderSkip(t *testing.T) {
	x := newExcluder([]string{"*.tmp", "cache", "/data/Docs/SD-Backup", "proj/build"})
	cases := map[[2]string]bool{
		{"/data/Docs/a.tmp", "a.tmp"}:                       true,
		{"/data/Docs/x/cache", "x/cache"}:                   true,
		{"/data/Docs/SD-Backup", "SD-Backup"}:               true,
		{"/data/Docs/SD-Backup/f.jpg", "SD-Backup/f.jpg"}:   true,
		{"/data/Docs/SD-Backup2/f.jpg", "SD-Backup2/f.jpg"}: false,
		{"/data/Docs/proj/build/o.bin", "proj/build/o.bin"}: true,
		{"/data/Docs/proj/src/main.go", "proj/src/main.go"}: false,
		{"/data/Docs/notes.txt", "notes.txt"}:               false,
	}
	for in, want := range cases {
		if got := x.skip(filepath.FromSlash(in[0]), in[1]); got != want {
			t.Errorf("skip(%q) = %v", in[0], got)
		}
	}
	if (*excluder)(nil).skip("/a", "a") {
		t.Fatal("nil excluder skipped")
	}
}

func TestScanTreeExcludes(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"keep.txt", "a.tmp", "SD-Backup/big.jpg", "sub/cache/x"} {
		p := filepath.Join(root, f)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x"), 0o644)
	}
	files, _, _ := scanTree(root, newExcluder([]string{"*.tmp", "cache", filepath.Join(root, "SD-Backup")}))
	if len(files) != 1 {
		t.Fatalf("got %v", files)
	}
}

func TestSyncIgnoresExcludedPaths(t *testing.T) {
	x := newExcluder([]string{"*.tmp"})
	s := &Syncer{paths: Paths{Root: "/r"}, exclude: x}
	if !s.exclude.skip(s.abs("a/b.tmp"), "a/b.tmp") || s.exclude.skip(s.abs("a/b.txt"), "a/b.txt") {
		t.Fatal("sync exclusion check wrong")
	}
}

func TestDiskBudget(t *testing.T) {
	free, err := freeSpace(t.TempDir())
	if err != nil || free <= 0 {
		t.Fatalf("freeSpace: %d %v", free, err)
	}
	if b := diskBudget(t.TempDir()); b != max(free-diskReserve, 0) && b > free {
		t.Fatalf("budget %d free %d", b, free)
	}
	if diskBudget("/definitely/missing/path") <= 0 {
		t.Fatal("unknown free space should not block downloads")
	}
}
