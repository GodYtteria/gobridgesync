package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

func TestCompareConflictKeepsASaveMadeDuringTheDownload(t *testing.T) {
	root := t.TempDir()
	full := filepath.Join(root, "a.txt")
	body := "from drive"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		os.WriteFile(full, []byte("saved again during download"), 0o644)
		w.Write([]byte(body))
	}))
	t.Cleanup(api.Close)
	srv, err := drive.NewService(context.Background(), option.WithHTTPClient(api.Client()), option.WithEndpoint(api.URL+"/"))
	if err != nil {
		t.Fatal(err)
	}
	st, _ := loadState(filepath.Join(t.TempDir(), "s.json"))
	s := &Syncer{srv: srv, paths: Paths{Root: root}, st: st}
	os.WriteFile(full, []byte("local version"), 0o644)
	info, _ := os.Stat(full)
	sum := md5.Sum([]byte(body))
	rf := RemoteFile{ID: "id1", MD5: hex.EncodeToString(sum[:]), Size: int64(len(body))}
	if err := s.apply(context.Background(), Compare, "a.txt", LocalFile{Size: info.Size(), MTime: info.ModTime().UnixNano()}, rf); err != nil {
		t.Fatal(err)
	}
	copies := conflictCopies(t, root)
	found := false
	for _, c := range copies {
		if c == "saved again during download" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a save made while the Drive copy downloads must be kept as a conflict copy, copies: %q", copies)
	}
}
