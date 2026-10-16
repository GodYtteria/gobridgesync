//go:build linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStreamMountGitCommitsLeaveNoConflictCopies(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	oldDelay := streamUploadDelay
	streamUploadDelay = 2 * time.Second
	t.Cleanup(func() { streamUploadDelay = oldDelay })
	f := newFuseEnv(t)
	f.fake.mu.Lock()
	f.fake.delay = 1500 * time.Millisecond
	f.fake.mu.Unlock()
	repo := filepath.Join(f.dir, "docs", "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q", ".")
	for i := 0; i < 4; i++ {
		if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte(strings.Repeat("x", i+1)), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", "f.txt")
		git("commit", "-qm", "c")
		time.Sleep(time.Second)
	}
	time.Sleep(15 * time.Second)
	f.fake.mu.Lock()
	var bad []string
	for _, file := range f.fake.files {
		if !file.Trashed && (strings.Contains(file.Name, "(conflict") || strings.HasSuffix(file.Name, ".lock")) {
			bad = append(bad, file.Name)
		}
	}
	f.fake.mu.Unlock()
	if len(bad) > 0 {
		t.Fatalf("git commits through the mount must not leave conflict copies or lock files on Drive: %v", bad)
	}
	if err := os.RemoveAll(repo); err != nil {
		t.Fatalf("removing the repo through the mount: %v", err)
	}
}
