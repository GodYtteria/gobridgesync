package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

type adoptDrive struct {
	mu      sync.Mutex
	posts   int
	patches int
	patched string
	getCode int
	getBody string
	create  func(n int) (int, string)
}

func (d *adoptDrive) handler(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodPost:
		d.posts++
		code, body := d.create(d.posts)
		w.WriteHeader(code)
		w.Write([]byte(body))
	case http.MethodPatch:
		d.patches++
		b, _ := io.ReadAll(r.Body)
		d.patched = string(b)
		id := strings.Trim(filepath.Base(r.URL.Path), "/")
		w.Write([]byte(`{"id":"` + id + `","md5Checksum":"updated"}`))
	default:
		w.WriteHeader(d.getCode)
		w.Write([]byte(d.getBody))
	}
}

func adoptSyncer(t *testing.T, d *adoptDrive, local string) (*Syncer, string) {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(d.handler))
	t.Cleanup(api.Close)
	srv, err := drive.NewService(context.Background(), option.WithHTTPClient(api.Client()), option.WithEndpoint(api.URL+"/"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte(local), 0o644); err != nil {
		t.Fatal(err)
	}
	st, _ := loadState(filepath.Join(t.TempDir(), "s.json"))
	journal := loadIDJournal(filepath.Join(t.TempDir(), "sync-ids"))
	rem := &Remote{Folders: map[string]string{"": "root"}, journal: journal, ids: []string{"gen1", "gen2"}}
	return &Syncer{srv: srv, paths: Paths{Root: root}, st: st, remote: rem, journal: journal}, root
}

func TestStaleAdoptionUploadsLocalContent(t *testing.T) {
	d := &adoptDrive{
		getCode: http.StatusOK,
		getBody: `{"id":"gen2","md5Checksum":"older"}`,
		create:  func(int) (int, string) { return http.StatusConflict, `{"error":{"code":409,"message":"exists"}}` },
	}
	s, _ := adoptSyncer(t, d, "local bytes")
	if err := s.upload(context.Background(), "a.txt", nil); err != nil {
		t.Fatal(err)
	}
	if d.patches != 1 || !strings.Contains(d.patched, "local bytes") {
		t.Fatalf("local content not uploaded: patches=%d body=%q", d.patches, d.patched)
	}
	if e := s.st.Files["a.txt"]; e.ID != "gen2" || e.MD5 != "updated" {
		t.Fatalf("state not recorded: %+v", e)
	}
}

func TestAdoptNotFoundRetriesWithFreshID(t *testing.T) {
	sum := md5.Sum([]byte("local bytes"))
	want := hex.EncodeToString(sum[:])
	d := &adoptDrive{
		getCode: http.StatusNotFound,
		getBody: `{"error":{"code":404,"message":"File not found"}}`,
		create: func(n int) (int, string) {
			if n == 1 {
				return http.StatusConflict, `{"error":{"code":409,"message":"exists"}}`
			}
			return http.StatusOK, `{"id":"gen1","md5Checksum":"` + want + `"}`
		},
	}
	s, _ := adoptSyncer(t, d, "local bytes")
	s.remote.journal.put("file:a.txt", "gen2")
	s.remote.idFor = map[string]string{"file:a.txt": "gen2"}
	s.remote.ids = []string{"gen1"}
	if err := s.upload(context.Background(), "a.txt", nil); err != nil {
		t.Fatal(err)
	}
	if d.posts != 2 {
		t.Fatalf("posts=%d", d.posts)
	}
	if e := s.st.Files["a.txt"]; e.ID != "gen1" || e.MD5 != want {
		t.Fatalf("state: %+v", e)
	}
	if id, _ := s.journal.get("file:a.txt"); id != "gen1" {
		t.Fatalf("journal still holds the dead id: %q", id)
	}
}
