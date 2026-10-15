package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalCacheRefreshesOnlyChangedPaths(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		full := filepath.Join(root, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(body), 0o644)
	}
	write("a.txt", "a")
	write("d/b.txt", "b")
	write("d/e/c.txt", "c")
	write("node_modules/x.js", "x")
	ex := newExcluder([]string{"node_modules"})
	c := newLocalCache()
	c.setWatching(true)
	now := time.Now()
	files, err := c.scan(root, ex, []string{"node_modules"}, now)
	if err != nil || len(files) != 3 {
		t.Fatalf("first scan: %v %v", files, err)
	}

	write("a.txt", "changed")
	write("d/new.txt", "n")
	os.RemoveAll(filepath.Join(root, "d", "e"))
	write("node_modules/y.js", "y")
	write("untracked.txt", "u")
	for _, p := range []string{"a.txt", "d/new.txt", "d/e", "node_modules/y.js"} {
		c.changed(filepath.Join(root, filepath.FromSlash(p)))
	}
	files, err = c.scan(root, ex, []string{"node_modules"}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if files["a.txt"].Size != 7 {
		t.Errorf("changed file not refreshed: %+v", files["a.txt"])
	}
	if _, ok := files["d/new.txt"]; !ok {
		t.Error("new file missing")
	}
	if _, ok := files["d/e/c.txt"]; ok {
		t.Error("removed folder still listed")
	}
	if _, ok := files["node_modules/y.js"]; ok {
		t.Error("excluded file listed")
	}
	if _, ok := files["untracked.txt"]; ok {
		t.Error("unchanged scan should not have walked the whole tree")
	}

	files, _ = c.scan(root, ex, []string{"node_modules"}, now.Add(localFullScanEvery+time.Minute))
	if _, ok := files["untracked.txt"]; !ok {
		t.Error("periodic full scan missed a file")
	}
	c.setWatching(false)
	write("later.txt", "l")
	files, _ = c.scan(root, ex, []string{"node_modules"}, now.Add(localFullScanEvery+2*time.Minute))
	if _, ok := files["later.txt"]; !ok {
		t.Error("without a watcher every scan must be full")
	}
}
