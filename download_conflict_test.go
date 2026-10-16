package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

func downloadFixture(t *testing.T, body string) (*Syncer, string, RemoteFile) {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	t.Cleanup(api.Close)
	srv, err := drive.NewService(context.Background(), option.WithHTTPClient(api.Client()), option.WithEndpoint(api.URL+"/"))
	if err != nil {
		t.Fatal(err)
	}
	sum := md5.Sum([]byte(body))
	root := t.TempDir()
	st, _ := loadState(filepath.Join(t.TempDir(), "s.json"))
	s := &Syncer{srv: srv, paths: Paths{Root: root}, st: st}
	return s, root, RemoteFile{ID: "id1", MD5: hex.EncodeToString(sum[:]), Size: int64(len(body))}
}

func conflictCopies(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.Contains(e.Name(), "(conflict ") {
			b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
			out = append(out, string(b))
		}
	}
	return out
}

func TestDownloadKeepsLocalEditMadeAfterScan(t *testing.T) {
	s, root, rf := downloadFixture(t, "from drive")
	full := filepath.Join(root, "a.txt")
	os.WriteFile(full, []byte("old"), 0o644)
	past := time.Now().Add(-time.Hour)
	os.Chtimes(full, past, past)
	info, _ := os.Stat(full)
	seen := scanView{file: LocalFile{Size: info.Size(), MTime: info.ModTime().UnixNano()}, present: true}
	os.WriteFile(full, []byte("edited after the scan"), 0o644)
	errs := s.downloadAll(context.Background(), []pendingDownload{{path: "a.txt", file: rf, seen: seen}})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if b, _ := os.ReadFile(full); string(b) != "from drive" {
		t.Fatalf("download not in place: %q", b)
	}
	if c := conflictCopies(t, root); len(c) != 1 || c[0] != "edited after the scan" {
		t.Fatalf("local edit lost: %q", c)
	}
}

func TestDownloadKeepsFileCreatedAfterScan(t *testing.T) {
	s, root, rf := downloadFixture(t, "from drive")
	full := filepath.Join(root, "new.txt")
	os.WriteFile(full, []byte("created locally"), 0o644)
	errs := s.downloadAll(context.Background(), []pendingDownload{{path: "new.txt", file: rf}})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if b, _ := os.ReadFile(full); string(b) != "from drive" {
		t.Fatalf("download not in place: %q", b)
	}
	if c := conflictCopies(t, root); len(c) != 1 || c[0] != "created locally" {
		t.Fatalf("local file lost: %q", c)
	}
}

func TestDownloadReplacesUnchangedLocalWithoutConflict(t *testing.T) {
	s, root, rf := downloadFixture(t, "from drive")
	full := filepath.Join(root, "a.txt")
	os.WriteFile(full, []byte("old"), 0o644)
	info, _ := os.Stat(full)
	seen := scanView{file: LocalFile{Size: info.Size(), MTime: info.ModTime().UnixNano()}, present: true}
	errs := s.downloadAll(context.Background(), []pendingDownload{{path: "a.txt", file: rf, seen: seen}})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if b, _ := os.ReadFile(full); string(b) != "from drive" {
		t.Fatalf("download not in place: %q", b)
	}
	if c := conflictCopies(t, root); len(c) != 0 {
		t.Fatalf("unneeded conflict copy: %q", c)
	}
}
