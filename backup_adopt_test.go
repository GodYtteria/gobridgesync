package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestBackupAdoptNotFoundDropsTheDeadID(t *testing.T) {
	d := &adoptDrive{
		getCode: http.StatusNotFound,
		getBody: `{"error":{"code":404,"message":"File not found"}}`,
		create:  func(int) (int, string) { return http.StatusConflict, `{"error":{"code":409,"message":"exists"}}` },
	}
	s, root := adoptSyncer(t, d, "local bytes")
	s.remote.journal.put("file:a.txt", "gen2")
	s.remote.idFor = map[string]string{"file:a.txt": "gen2"}
	b := &Backup{srv: s.srv, st: s.st, journal: s.journal, remote: s.remote}
	full := filepath.Join(root, "a.txt")
	info, _ := os.Stat(full)
	b.upload(context.Background(), uploadJob{top: root, full: full, rp: "a.txt", lf: LocalFile{Size: info.Size(), MTime: info.ModTime().UnixNano()}})
	if id, ok := s.journal.get("file:a.txt"); ok {
		t.Fatalf("a reserved id that Drive says does not exist must be dropped, still %q", id)
	}
}
